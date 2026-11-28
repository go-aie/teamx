//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/scion"
)

type scionReadAuditWriter struct {
	http.ResponseWriter
	bytes.Buffer
}

func (w *scionReadAuditWriter) Write(b []byte) (int, error) {
	w.Buffer.Write(b)
	return w.ResponseWriter.Write(b)
}
func (w *scionReadAuditWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Isolated real model/DSH/browser/native read-only acceptance. The wrapper
// forwards only version, identity, repository and open Issue list reads.
func TestScionMemberRealConversation(t *testing.T) {
	if os.Getenv("TEAMX_SCION_MEMBER_REAL") != "1" {
		t.Skip("explicit real model, copied credentials and fresh output required")
	}
	out := os.Getenv("TEAMX_SCION_MEMBER_OUTPUT")
	if !filepath.IsAbs(out) {
		t.Fatal("fresh absolute output required")
	}
	if e := os.Mkdir(out, 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("USER", humanCaller.ID)
	report := map[string]any{"status": "incomplete", "business_writes": 0, "production_subscription": false, "recorded_at": time.Now().UTC()}
	defer imageRealReport(t, out, report)
	credentials, e := os.ReadFile(os.Getenv("TEAMX_SCION_TEST_CREDENTIALS"))
	if e != nil {
		t.Fatal("copied credentials unavailable")
	}
	native := os.Getenv("TEAMX_SCION_BIN")
	if !filepath.IsAbs(native) {
		t.Fatal("absolute native CLI required")
	}
	workspace := filepath.Join(out, "workspace")
	if e = os.Mkdir(workspace, 0700); e != nil {
		t.Fatal(e)
	}
	profile := settingsRealProfile(t, os.Getenv("TEAMX_SETTINGS_CONFIG"), workspace)
	report["model"] = profile.Model.ModelID
	// JSON string literals here are Python source, never interpolated shell code.
	encodedNative, _ := json.Marshal(native)
	encodedTrace, _ := json.Marshal(filepath.Join(out, "native-reads.jsonl"))
	wrapper := filepath.Join(out, "scn-readonly")
	script := fmt.Sprintf(`#!/usr/bin/env python3
import os,sys,json
args=sys.argv[1:]
allowed=args==['--version'] or (args[:2]==['--json','whoami'] and len(args) in (2,3)) or (len(args)==4 and args[:3]==['--json','repo','info']) or (len(args)>=5 and args[:2]==['--json','--repo'] and args[3:5]==['issue','ls']) or (len(args)==6 and args[:2]==['--json','--repo'] and args[3:5]==['repo','info'])
with open(%s,'a') as f:f.write(json.dumps({'args':args,'allowed':allowed})+'\n')
if not allowed:sys.exit(2)
os.execv(%s,[%s]+args)
`, encodedTrace, encodedNative, encodedNative)
	if e = os.WriteFile(wrapper, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	m := testManager(t, filepath.Join(out, "rooms"), managerTestProfiles(t))
	m.config.unifiedInputs = true
	m.toolCatalog = nil
	m.defaultRuntime = testTeamDefinition(t).Runtime
	ensureAssistant(t, m)
	room := assistantSummary(t, m)
	_, snap := readManagedRoom(t, m, room)
	member := snap.Members[0]
	requests := &settingsRequestAudit{}
	audit := &policyRealAudit{Calls: map[string]int{}}
	successfulToolReads := 0
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tools/run" {
			m.ServeHTTP(w, r)
			return
		}
		captured := &scionReadAuditWriter{ResponseWriter: w}
		m.ServeHTTP(captured, r)
		var stdout bytes.Buffer
		ok := false
		decoder := json.NewDecoder(bytes.NewReader(captured.Bytes()))
		for {
			var frame toolFrame
			if decoder.Decode(&frame) != nil {
				break
			}
			if frame.Stream == "stdout" {
				stdout.Write(frame.Data)
			}
			if frame.Stream == "exit" && frame.Code == 0 {
				ok = true
			}
		}
		var result struct {
			Schema string `json:"schema"`
		}
		if ok && json.Unmarshal(stdout.Bytes(), &result) == nil && (result.Schema == "scion.cli.repo/v1" || result.Schema == "scion.cli.issue-list/v1") {
			audit.mu.Lock()
			successfulToolReads++
			audit.mu.Unlock()
		}
	})
	server := browserTestServer(t, m, requests.serve(audit.serve(capture)))
	defer server.Close()
	m.config.endpoint = server.URL
	svc, e := startService(m.roomDir(room.RoomID), room.RoomID, serviceConfig{endpoint: server.URL, cliPath: productionFixtureCLI(t), maxTurns: 16, persistentScheduling: true, unifiedInputs: true, updates: m.updates, access: m.config.access, dshTurnTimeout: 3 * time.Minute, dshProfiles: map[string]dshProfile{member.Runner.Profile: profile}}, func(s *service, state RoomSnapshot) error {
		if e := s.loadMembers(state); e != nil {
			return e
		}
		return s.attachMembers(state)
	})
	if e != nil {
		t.Fatal(e)
	}
	defer closeService(t, svc)
	m.bindPlatformReferences(svc)
	m.mu.Lock()
	m.runtimes[room.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	m.catalogChangedLocked()
	m.mu.Unlock()
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	report["member"] = id
	defer func() {
		report["requests"] = requests.snapshot()
		s, _ := svc.store.LoadRoom()
		report["messages"] = s.Messages
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	turn := uint64(0)
	wait := func() {
		t.Helper()
		s := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber > turn })
		if s.Error != "" || s.Agent.Turn == nil || s.Agent.Turn.Status != "completed" {
			t.Fatal("real model turn", s.Error, s.Agent.Turn)
		}
		turn = s.Agent.TurnNumber
	}
	post := func(text string) PostReceipt {
		t.Helper()
		key, _ := newOpaqueID()
		p, e := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: room.RoomIdentity, Caller: humanCaller, ClientMessageID: key, Text: text})
		if e != nil {
			t.Fatal(e)
		}
		if turn == 0 {
			if _, e = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); e != nil {
				t.Fatal(e)
			}
		}
		wait()
		return p
	}
	browser := func(mode, operation, platformID string) {
		t.Helper()
		input, _ := json.Marshal(map[string]any{"room_id": room.RoomID, "room_instance": room.RoomInstance, "mode": mode, "operation_id": operation, "platform_id": platformID})
		cmd := exec.CommandContext(ctx, "node", "testdata/scion_member_browser.cjs", server.URL, string(input), out)
		b, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatal("browser", e, string(b))
		}
		t.Log(string(b))
	}
	// Missing account guidance must not reserve configuration or ask for PAT in chat.
	post("给你开启 Scion 工具和通知，关注 OpenCSG/scion 最近一天的新 Issue。只做本地配置和远端只读，不创建或修改任何远端内容。")
	m.mu.Lock()
	ops, e := m.platformMemberOperationsLocked()
	m.mu.Unlock()
	if e != nil || len(ops) != 0 {
		t.Fatal("missing account changed settings", e)
	}
	browser("login", "", "")
	p := &platform.CLIProfile{Version: 1, Brand: "scion", Profile: strings.Repeat("c", 32), Executable: wrapper, CLIVersion: scion.CLIVersion}
	dir := filepath.Join(m.store.dir, "scion-profiles", p.Profile)
	if os.MkdirAll(dir, 0700) != nil || os.WriteFile(filepath.Join(dir, "credentials.json"), credentials, 0600) != nil {
		t.Fatal("credential copy failed")
	}
	c, e := m.updatePlatform(managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "Scion", Provider: "scion", URL: "https://scion.opencsg.com", CLIProfile: p}, false)
	if e != nil {
		t.Fatal(e)
	}
	c = verifyScion(t, m, c)
	source := post("Scion 已登录。请先显示已有 Scion 账号选择卡片；我选好后，给你开启工具，并关注 OpenCSG/scion 最近一天的新 Issue。工具与通知都配置好后，实际只读查询一次该仓库信息。仍不做任何远端写入。")
	m.mu.Lock()
	selections, e := m.platformSelectionsLocked()
	m.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	pick := ""
	for _, s := range selections {
		if s.Source.Seq == source.Seq && s.Input.Provider == "scion" && s.Input.Kind == "account" && s.Status == "waiting" {
			pick = s.ID
		}
	}
	if pick == "" {
		t.Fatal("real model did not create Scion account card")
	}
	browser("select", pick, c.ID)
	wait()
	req := platformMemberRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id), Input: &platformMemberInput{PlatformID: c.ID, PlatformVersion: c.Version}}
	m.mu.Lock()
	view, e := m.platformMemberViewLocked(ctx, req)
	m.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	if !view.ToolEnabled || view.Scion == nil || !view.Scion.Enabled || view.Scion.Repository != "OpenCSG/scion" || view.Baseline.IsZero() {
		t.Fatal("conversation did not configure both", view)
	}
	report["configured"] = view
	audit.mu.Lock()
	toolRead := successfulToolReads > 0
	report["successful_native_tool_reads"] = successfulToolReads
	audit.mu.Unlock()
	if !toolRead {
		t.Fatal("no real member tool call after enable")
	}
	if e = m.notifications.Start(); e != nil {
		t.Fatal(e)
	}
	defer m.notifications.Stop(context.Background())
	deadline := time.Now().Add(50 * time.Second)
	var state scion.State
	for time.Now().Before(deadline) {
		m.mu.Lock()
		s, err := m.notifications.CurrentLocked(room.RoomIdentity, scion.WorkitemsSourceID, c.ID, inboxTarget(id))
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if s != nil {
			state = s.(scion.State)
			if !state.Discovery.LastFull.IsZero() && len(state.Observed) > 0 && len(state.Inbox) == 0 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(state.Observed) == 0 || len(state.Inbox) != 0 {
		t.Fatal("real worker did not deliver", state.Status, state.ErrorCode)
	}
	browser("configured", pick, c.ID)
	post("请正式读取并确认你收到的一条 Scion Issue 通知，简要告诉我编号和标题。只读，勿评论或修改远端。")
	storage, e := svc.inputStorage(id)
	if e != nil {
		t.Fatal(e)
	}
	inputState, exists, e := storage.repository.CurrentState()
	if e != nil || !exists {
		t.Fatal("missing formal input state", e)
	}
	if inputState.Used == 0 {
		t.Fatal("formal input not confirmed")
	}
	// Persisted input consumption, not a model claim, is the acceptance evidence.
	b, _ := json.Marshal(inputState)
	report["input_state"] = json.RawMessage(b)
	report["observed"] = len(state.Observed)
	report["real_model"] = true
	report["browser"] = true
	report["native_cli"] = scion.CLIVersion
	confirmedScion := false
	for _, a := range inputState.Attempts {
		if a.Phase == "settled" {
			for _, location := range a.Entries {
				if location.Partition == inputstream.PrivatePartitionRef(inputstream.Member{RoomID: id.RoomID, RoomInstance: id.RoomInstance, MembershipID: id.MembershipID}, "scion") {
					confirmedScion = true
				}
			}
		}
	}
	if !confirmedScion {
		t.Fatal("no settled Scion input page", inputState)
	}
	report["status"] = "passed"
}
