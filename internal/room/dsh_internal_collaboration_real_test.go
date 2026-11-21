//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Opt-in, isolated Room: native DSH children and real model, production host,
// CLI and publication. The independent Room reviewer is present but never run.
func TestDSHInternalCollaborationReal(t *testing.T) {
	if os.Getenv("TEAMX_INTERNAL_COLLAB_REAL") != "1" {
		t.Skip("set TEAMX_INTERNAL_COLLAB_REAL=1, TEAMX_INPUT_COMPARISON_CONFIG and fresh TEAMX_DSH_OUTPUT")
	}
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Fatal("zstd is required to inspect native child Session evidence", err)
	}
	cfg, err := loadStartupConfig(os.Getenv("TEAMX_INPUT_COMPARISON_CONFIG"), true)
	if err != nil || cfg.Runtime == nil || cfg.Models == nil {
		t.Fatal("configured runtime and default model required", err)
	}
	entry, err := cfg.Models.resolve(modelSelection{Mode: "default"})
	if err != nil {
		t.Fatal(err)
	}
	// Read existing immutable credentials; do not initialize the operator's store.
	credentials := &modelCredentials{dir: filepath.Join(filepath.Dir(cfg.path), "model-credentials")}
	credential, err := credentials.get(entry.CredentialRef)
	if err != nil {
		t.Fatal(err)
	}
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "model": entry.ModelID, "external_platform_writes": 0, "scope": "native internal investigation and review; isolated production Room"}
	defer imageRealReport(t, out, report)
	workspace := filepath.Join(out, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"observations.txt": "batch-alpha: 17\nbatch-beta: 25\n",
		"calc.go":          "package sample\n\n// Mean returns the arithmetic mean.\nfunc Mean(a, b int) int { return a + b / 2 }\n",
	} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name, task string
		required   []string
	}{
		{"investigation", "请用运行时内部助手调查 observations.txt 中两批的总数；启动后给这个内部助手发一条补充消息：同时算出两批相差多少。等它回传后，你自己核对文件和计算，再在这里汇总总数与差值。这只是内部调查，不分派给其他 Room 成员。", []string{"42", "8"}},
		{"review", "请用运行时内部助手对 calc.go 做一次内部 review；你核对它的结论后，在这里说明问题、用 Mean(2, 4) 演示实际值与期望值，并说明这是否已构成 Room 中 reviewer 成员的独立评审。本次只审查，不修改文件。", []string{"4", "3", "reviewer"}},
	}
	state := historyRoom(1, cases[0].task)
	state.InputLog = &roomLogRoot{Version: 1}
	state.Members = append(state.Members, MemberState{AgentID: "reviewer", MembershipID: "generation-reviewer", Runner: RunnerConfig{Kind: "dsh", Profile: "reviewer"}})
	dir := filepath.Join(out, "room")
	store, err := OpenStore(dir, state.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	audit := &policyRealAudit{Calls: map[string]int{}}
	access, _, _ := accessFixture(t)
	access.disabled, access.operator = true, humanCaller.ID
	var svc *service
	server := httptest.NewUnstartedServer(access.wrap(audit.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { svc.ServeHTTP(w, r) }))))
	endpoint := "http://" + server.Listener.Addr().String()
	access.internal, access.public = endpoint, endpoint
	server.Start()
	defer server.Close()
	profile := dshProfile{BinaryPath: cfg.Runtime.DSH.BinaryPath, BinaryArgs: cfg.Runtime.DSH.BinaryArgs, Artifact: cfg.Runtime.DSH.Artifact,
		Workspace: workspace, Model: entry.materialize(), Instructions: assistantInstructions}
	svc, err = startService(dir, state.RoomID, serviceConfig{unifiedInputs: true, access: access, endpoint: endpoint, cliPath: productionFixtureCLI(t), maxTurns: 8,
		dshTurnTimeout: 4 * time.Minute, dshProfiles: map[string]dshProfile{"fixture-ignore": profile, "reviewer": {Instructions: "Independent Room reviewer; review only when requested."}}}, func(s *service, state RoomSnapshot) error {
		if err := s.loadMembers(state); err != nil {
			return err
		}
		s.memberFactory = func(id MemberIdentity, runner RunnerConfig, restored *AgentSnapshot) actorx.Actor {
			m := s.newMember(id, runner, restored).(*MemberAgent)
			b := m.Backend.(dshBackend)
			b.open = func(ctx context.Context, opts dsh.Options) (dshComponent, error) {
				if !strings.Contains(opts.Instructions, `You are member "A"`) {
					return nil, errors.New("unexpected executing membership")
				}
				opts.Credential = credential
				return dsh.Open(ctx, opts)
			}
			m.Backend = policyRealBackend{b, audit}
			return m
		}
		return s.attachMembers(state)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		err := svc.close(ctx)
		report["closed_cleanly"] = err == nil
		if err != nil {
			t.Error(err)
		}
	}()
	defer func() { report["audit"] = audit; room, _ := svc.store.LoadRoom(); report["messages"] = room.Messages }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	id := MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}
	if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "enable"); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	report["scenarios"] = &rows
	var since uint64
	for i, scenario := range cases {
		t.Log("real collaboration:", scenario.name)
		if i > 0 {
			if _, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: state.RoomIdentity, Caller: humanCaller, ClientMessageID: fmt.Sprintf("internal-%d", i), Text: scenario.task}); err != nil {
				t.Fatal(err)
			}
		}
		status := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && (s.Agent.TurnNumber >= uint64(i+1) || s.Paused) })
		if status.Error != "" || status.Agent.Turn == nil || status.Agent.Turn.Status != "completed" {
			t.Fatal("real internal collaboration failed", status.Error)
		}
		room, err := svc.store.LoadRoom()
		if err != nil {
			t.Fatal(err)
		}
		var replies []RoomMessage
		for _, msg := range room.Messages {
			if msg.Seq > since && msg.Author.Kind == "agent" {
				if msg.Author.ID != "A" {
					t.Fatal("internal helper or reviewer published as another member")
				}
				replies = append(replies, msg)
			}
		}
		row := map[string]any{"scenario": scenario.name, "replies": replies}
		rows = append(rows, row)
		if len(replies) == 0 {
			t.Fatal("owner did not publish the result")
		}
		var text string
		for _, msg := range replies {
			text += msg.Text + "\n"
		}
		for _, required := range scenario.required {
			if !strings.Contains(text, required) {
				t.Fatal("missing verified result", required)
			}
		}
		if i == 1 && !strings.Contains(text, "不构成") && !strings.Contains(text, "不能替代") && !strings.Contains(text, "不代表") && !strings.Contains(text, "不等于") {
			t.Fatal("internal review was not distinguished from independent Room review")
		}
		audit.mu.Lock()
		if len(audit.Turns) != i+1 {
			audit.mu.Unlock()
			t.Fatal("missing ACP evidence")
		}
		result := audit.Turns[i].Result
		audit.mu.Unlock()
		raw := string(mustContractJSON(result))
		if !strings.Contains(raw, "subagent") {
			t.Fatal("no native internal delegation observed")
		}
		if i == 0 && !strings.Contains(raw, "send_message") {
			t.Fatal("no internal communication observed")
		}
		row["status"] = "passed"
		since = room.LatestSeq
	}
	members, err := svc.members(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members.Members {
		if member.AgentID == "reviewer" && member.Execution != nil && member.Execution.Agent.TurnNumber != 0 {
			t.Fatal("Room reviewer unexpectedly executed")
		}
	}
	content, err := os.ReadFile(filepath.Join(workspace, "calc.go"))
	if err != nil || string(content) != "package sample\n\n// Mean returns the arithmetic mean.\nfunc Mean(a, b int) int { return a + b / 2 }\n" {
		t.Fatal("read-only review changed source", err)
	}
	report["native_sessions"] = inspectInternalCollaborationSessions(t, dir)
	report["status"] = "passed"
}

// Inspect the instructions actually loaded by each native child, not a model's
// claim of inheritance. This decoder is test-only and follows DSH v4 logs.
func inspectInternalCollaborationSessions(t *testing.T, dir string) []map[string]any {
	t.Helper()
	type call struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}
	type nativeSession struct {
		ID, Parent string
		Calls      []call
		Guidance   bool
	}
	var sessions []nativeSession
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() != "session.v4.jsonl.zstd" {
			return err
		}
		data, err := exec.CommandContext(t.Context(), "zstd", "-dc", path).Output()
		if err != nil {
			return err
		}
		var session nativeSession
		requested := false
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var row struct {
				Type   string          `json:"type"`
				ID     string          `json:"id"`
				Parent string          `json:"parentSession"`
				Data   json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				return err
			}
			switch row.Type {
			case "session":
				session.ID, session.Parent = row.ID, row.Parent
			case "request/header":
				requested = true
			case "user/message":
				var message struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				}
				if err := json.Unmarshal(row.Data, &message); err != nil {
					return err
				}
				for _, block := range message.Content {
					if !requested && strings.Contains(block.Text, strings.TrimSpace(unifiedAgentGuide())) &&
						strings.Contains(block.Text, `You are member "A" in Room "design-review" (instance "room-1", membership "generation-A")`) {
						session.Guidance = true
					}
				}
			case "tool/call":
				var c call
				if err := json.Unmarshal(row.Data, &c); err != nil {
					return err
				}
				session.Calls = append(session.Calls, c)
			}
		}
		sessions = append(sessions, session)
		return nil
	})
	if err != nil {
		t.Fatal("cannot inspect native Sessions", err)
	}
	ids := map[string]bool{}
	children, ownerMessages, childMessages := 0, 0, 0
	for _, session := range sessions {
		ids[session.ID] = true
		if session.Parent != "" {
			children++
		}
	}
	var evidence []map[string]any
	for _, session := range sessions {
		if !session.Guidance {
			t.Fatal("native Agent lacks effective member identity and operation constraints", session.ID)
		}
		var names []string
		for _, c := range session.Calls {
			names = append(names, c.Name)
			if session.Parent != "" && strings.Contains(c.Arguments, "TEAMX_CLI") {
				t.Fatal("internal helper used TeamX publication in a read-only subtask")
			}
			if c.Name == "send_message" {
				var args struct {
					AgentID string `json:"agent_id"`
				}
				if json.Unmarshal([]byte(c.Arguments), &args) != nil || !ids[args.AgentID] {
					t.Fatal("internal message did not address a native Agent ID")
				}
				if session.Parent == "" {
					ownerMessages++
				} else {
					if args.AgentID != session.Parent {
						t.Fatal("child did not return to its owning Agent")
					}
					childMessages++
				}
			}
		}
		evidence = append(evidence, map[string]any{"id": session.ID, "parent": session.Parent, "effective_member_guidance": session.Guidance, "tools": names})
	}
	if children < 2 || ownerMessages == 0 || childMessages < 2 {
		t.Fatal("missing native children or bidirectional internal communication")
	}
	return evidence
}
