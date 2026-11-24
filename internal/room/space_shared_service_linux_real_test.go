//go:build e2e && linux

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/agent"
	"github.com/go-aie/teamx/internal/agent/srt"
	"github.com/pelletier/go-toml/v2"
)

// This is the incremental shared-core scenario. Isolation/fault matrices stay
// in their existing tests. An explicit Linux model source and pinned manifest
// are required; no ambient home, platform credentials or external writes.
func TestLinuxSharedServiceArchiveWithModelCallbacks(t *testing.T) {
	source := os.Getenv("TEAMX_SHARED_SERVICE_CONFIG")
	if source == "" {
		t.Skip("explicit Linux SRT/model config, CLI and fresh output required")
	}
	output, cli := os.Getenv("TEAMX_SHARED_SERVICE_OUTPUT"), os.Getenv("TEAMX_SHARED_SERVICE_CLI")
	if os.Geteuid() == 0 || !filepath.IsAbs(source) || !filepath.IsAbs(output) || !filepath.IsAbs(cli) {
		t.Fatal("non-root Linux and absolute explicit paths required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"status": "failed", "uid": os.Geteuid(), "real_dsh": true, "real_model": true, "external_platform_writes": false, "multi_user_enabled": false}
	defer func() {
		if t.Failed() {
			report["status"] = "failed"
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(output, "report.json"), data, 0600)
		}
		if err != nil {
			t.Error(err)
		}
	}()
	original, err := loadStartupConfig(source, true)
	if err != nil || original.Runtime == nil || original.Models == nil || original.Deployment.Execution.Kind != "srt" || original.Server.Listen == nil {
		t.Fatal("managed Linux model, stable listener and SRT manifest required", err)
	}
	entry, err := original.Models.resolve(modelSelection{Mode: "default"})
	if err != nil || !strings.HasPrefix(entry.BaseURL, "https://") {
		t.Fatal("explicit HTTPS external model required", err)
	}
	report["model_id"] = entry.ModelID
	manifest, err := srt.ReadConfig(original.Deployment.Execution.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.PrivateRoots = append(manifest.PrivateRoots, output)
	manifestPath := filepath.Join(output, "srt.json")
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(output, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	data, err = toml.Marshal(map[string]any{
		"server":  map[string]any{"listen": *original.Server.Listen},
		"access":  map[string]any{"login_enabled": true},
		"runtime": map[string]any{"dsh": original.Runtime.DSH}, "models": modelCatalog{SchemaVersion: 1, Version: 1},
		"deployment": map[string]any{"execution": map[string]any{"kind": "srt", "manifest": manifestPath}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	// Keep the public multi-user gate closed. The private composition seam
	// selects the same restricted tools without inventing an authentication bypass.
	selected := personalDeployment(func(dir, id string, c serviceConfig) (*service, error) {
		c.cliPath = cli
		return newConfiguredService(dir, id, c)
	})
	selected.factories.hostTools = func(string) (hostToolPolicy, error) { return deploymentHostTools("multi-user") }
	done, ready := make(chan error, 1), make(chan serverTestReady, 1)
	exited := false
	go func() {
		done <- runConfiguredServer(ctx, []string{"--home", home}, io.Discard, io.Discard, serverOptions{
			deployment: &selected, platformCatalog: defaultPlatformCatalog(),
			ready: func(v any) error {
				data, err := json.Marshal(v)
				var value serverTestReady
				if err == nil {
					err = json.Unmarshal(data, &value)
				}
				if err == nil {
					ready <- value
				}
				return err
			},
		})
	}()
	stop := sync.OnceFunc(func() {
		cancel()
		if exited {
			return
		}
		select {
		case err := <-done:
			if err != nil {
				t.Error("shared service shutdown", err)
			}
		case <-time.After(40 * time.Second):
			t.Error("shared service settlement timed out")
		}
	})
	defer stop()
	var info serverTestReady
	select {
	case info = <-ready:
	case err := <-done:
		exited = true
		cancel()
		t.Fatal("shared service startup", err)
	case <-ctx.Done():
		t.Fatal("shared service startup deadline")
	}
	t.Log("authenticated shared service ready")
	// SRT prerequisites have passed before any source model credential is read.
	credential, err := (&modelCredentials{dir: filepath.Join(filepath.Dir(source), "model-credentials")}).get(entry.CredentialRef)
	if err != nil {
		t.Fatal(err)
	}
	client := serverTestCLI(t, home, info.Endpoint)
	s := selected.spaces
	aID := s.directory.DefaultSpaceID
	bID, err := newOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	var record SpaceRecord
	if err := client.request(ctx, "/v1/spaces", spaceChangeInput{RequestID: bID, Name: "Acceptance B"}, &record); err != nil {
		t.Fatal(err)
	}
	waitReady := func(id string) *SpaceRuntime {
		t.Helper()
		deadline := time.Now().Add(100 * time.Second)
		for time.Now().Before(deadline) && ctx.Err() == nil {
			s.mu.Lock()
			entry := s.entries[id]
			s.mu.Unlock()
			if entry != nil {
				entry.mu.RLock()
				runtime, err := entry.runtime, entry.err
				entry.mu.RUnlock()
				if err != nil {
					t.Fatal("Space startup", err)
				}
				if runtime != nil && runtime.State() == "ready" {
					return runtime
				}
				child := client
				child.endpoint += spaceRoute(id)
				var setup struct {
					Operation *setupOperation `json:"operation"`
				}
				if err := child.request(ctx, "/v1/setup", nil, &setup); err == nil && setup.Operation != nil && setup.Operation.Status == "failed" {
					t.Fatal("model validation", setup.Operation.Code, setup.Operation.Message)
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("Space readiness deadline", id)
		return nil
	}
	spaces := map[string]*SpaceRuntime{}
	for _, id := range []string{aID, bID} {
		waitSpaceState(t, s, id, "setup_required")
		child := client
		child.endpoint += spaceRoute(id)
		var operation setupOperation
		if err := child.request(ctx, "/v1/setup/model", setupModelInput{RequestID: "shared-model-" + id, ExpectedVersion: 1, ModelParameters: entry.ModelParameters.clone(), APIKey: credential.Value}, &operation); err != nil {
			t.Fatal("model setup", err)
		}
		// Model validation has an existing service-wide bounded slot. Wait for
		// its durable completion before configuring the next Space.
		spaces[id] = waitReady(id)
		t.Log("model setup completed", id)
	}
	a, b := spaces[aID], spaces[bID]
	if a == b || a.manager == b.manager || a.config.executionResources != b.config.executionResources || !a.config.hostTools.restricted || !b.config.hostTools.restricted || !a.config.access.privateLark || !b.config.access.privateLark {
		t.Fatal("shared service did not bind independent restricted Spaces and shared admission")
	}
	run := func(r *SpaceRuntime, phase string) (*service, MemberIdentity) {
		t.Helper()
		svc := waitSpaceAssistant(t, r)
		snapshot, err := svc.store.LoadRoom()
		if err != nil || len(snapshot.Members) != 1 {
			t.Fatal("default assistant identity", err)
		}
		member := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: snapshot.Members[0].AgentID, MembershipID: snapshot.Members[0].MembershipID}
		marker := "TEAMX_SHARED_" + r.SpaceID + "_" + phase
		child := client
		child.endpoint += spaceRoute(r.SpaceID)
		var posted PostReceipt
		first := phase == "A_FIRST" || phase == "B_FIRST"
		readInstructions := "调用 MCP inbox_read 读取本轮输入；若已知当前会话的 stream_ref，直接复用，否则先 inbox_list。"
		if first {
			readInstructions = "先调用 MCP inbox_list 和 inbox_read 读取本轮输入。"
		}
		prompt := fmt.Sprintf("这是无平台副作用的共享服务验收。%s再用 bash 执行产品 CLI：\"$TEAMX_CLI\" send --text %s --id %s --json。只发送一次这个本地回执，完成后结束回合。不要调用外部平台、浏览器或其他网络，不要修改文件。", readInstructions, marker, marker)
		if err := serverTestRequest(ctx, child, "/v1/send", apiRequest{ServiceInfo: svc.info, Caller: serverTestCaller(t, home), ClientMessageID: "human-" + marker, Text: prompt}, &posted); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(100 * time.Second)
		for {
			snapshot, err = svc.store.LoadRoom()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, message := range snapshot.Messages {
				found = found || message.Author.Kind == "agent" && message.Text == marker
			}
			state, err := svc.store.LoadAgent(member)
			if err != nil {
				t.Fatal(err)
			}
			if found && state.Turn != nil && state.Turn.Status == "completed" {
				var diagnostic backendDiagnostic
				if err := readSnapshot(filepath.Join(svc.store.dir, "agents", member.MembershipID, "dsh-turn.json"), &diagnostic, maxPrivateFileBytes); err != nil {
					t.Fatal(err)
				}
				list, read, cliSend := false, false, false
				for _, tool := range diagnostic.Result.Tools {
					if tool.Status != "completed" {
						continue
					}
					list = list || strings.Contains(tool.Title, "inbox_list")
					read = read || strings.Contains(tool.Title, "inbox_read")
					cliSend = cliSend || strings.Contains(tool.Input, "TEAMX_CLI") && strings.Contains(tool.Input, marker)
				}
				if diagnostic.Error != "" || first && !list || !read || !cliSend {
					t.Fatalf("callback evidence incomplete: list=%v read=%v cli=%v", list, read, cliSend)
				}
				report[phase] = map[string]any{"space": r.SpaceContext, "member": member, "mcp_list": list, "mcp_read": read, "cli_send": cliSend}
				t.Log("model and callbacks completed", phase)
				return svc, member
			}
			if ctx.Err() != nil || time.Now().After(deadline) || state.Turn != nil && state.Turn.Status == "failed" {
				t.Fatal("model callback receipt did not complete", state.Turn)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	firstA, memberA := run(a, "A_FIRST")
	firstB, memberB := run(b, "B_FIRST")
	// The existing delegation owner supplies this disposable credential. Its
	// revocation is checked through HTTP after archive/restore, never logged.
	token, revoke, err := a.config.access.issueExecution(executionBinding{ServiceInfo: firstA.info, Caller: agentCaller(memberA.AgentID, memberA.MembershipID)})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	record = spaceRecordForTest(t, s, aID)
	if err := client.request(ctx, "/v1/spaces/"+aID+"/archive", spaceChangeInput{ExpectedVersion: record.Version}, &record); err != nil {
		t.Fatal(err)
	}
	archived := waitSpaceArchived(t, s, aID)
	if a.State() != "stopped" {
		t.Fatal("archive completed before original owner settled")
	}
	backend := func(svc *service, member MemberIdentity) (sessionReference, string) {
		t.Helper()
		path := filepath.Join(svc.store.dir, "agents", member.MembershipID, "session.json")
		ref, _, err := readSessionReference(path, member)
		if err != nil {
			t.Fatal(err)
		}
		var saved struct {
			SessionID string                   `json:"sessionId"`
			Stopped   bool                     `json:"stopped"`
			Execution *agent.ExecutionIdentity `json:"execution"`
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(path), ref.Directory, "backend.json"))
		if err == nil {
			err = json.Unmarshal(data, &saved)
		}
		if err != nil || !saved.Stopped || saved.SessionID == "" || saved.Execution == nil || saved.Execution.Kind != "srt" {
			t.Fatal("missing settled SRT backend receipt", err)
		}
		return ref, saved.SessionID
	}
	originalRef, originalSession := backend(firstA, memberA)
	_, _ = run(b, "B_WHILE_A_ARCHIVED")
	if err := client.request(ctx, "/v1/spaces/"+aID+"/restore", spaceChangeInput{ExpectedVersion: archived.Version}, &record); err != nil {
		t.Fatal(err)
	}
	fresh := waitReady(aID)
	if fresh == a || fresh.manager == a.manager || fresh.RuntimeEpoch == a.RuntimeEpoch || fresh.OwnerUserID != a.OwnerUserID || fresh.SpaceID != a.SpaceID || fresh.config.endpoint != a.config.endpoint {
		t.Fatal("restore reused owners or changed persistent identity/scope")
	}
	secondA, restoredMember := run(fresh, "A_RESTORED")
	if restoredMember != memberA || secondA.info.ServiceEpoch == firstA.info.ServiceEpoch {
		t.Fatal("restore changed member identity or reused service epoch")
	}
	child := client
	child.endpoint += spaceRoute(aID)
	var rejected PostReceipt
	if err := serverTestRequest(ctx, child, "/v1/send", apiRequest{ServiceInfo: firstA.info, Caller: serverTestCaller(t, home), ClientMessageID: "stale-service", Text: "must reject"}, &rejected); err == nil || !strings.HasPrefix(err.Error(), "identity:") {
		t.Fatal("old service epoch did not receive an identity rejection", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, child.endpoint+"/v1/rooms", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	// A live delegation would return 403 for this human-only route. Require
	// authentication failure so that route permissions cannot mask non-revocation.
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("old delegation survived restore", response.StatusCode)
	}
	lastB, currentMemberB := run(b, "B_AFTER_A_RESTORED")
	if lastB != firstB || currentMemberB != memberB || b.State() != "ready" {
		t.Fatal("A lifecycle replaced B")
	}
	stop()
	resources := b.config.executionResources
	resources.mu.Lock()
	remaining := resources.sessions
	resources.mu.Unlock()
	if remaining != 0 {
		t.Fatal("settled service retained Session quota", remaining)
	}
	sessions := map[string]string{}
	for _, target := range []struct {
		svc    *service
		member MemberIdentity
	}{{secondA, memberA}, {lastB, memberB}} {
		ref, session := backend(target.svc, target.member)
		if target.member == memberA && (ref.Directory != originalRef.Directory || session != originalSession) {
			t.Fatal("archive/restore replaced the original Session")
		}
		sessions[target.member.MembershipID] = session
	}
	if sessions[memberA.MembershipID] == sessions[memberB.MembershipID] {
		t.Fatal("Spaces shared a DSH Session")
	}
	report["sessions"], report["old_epoch_rejected"], report["old_token_rejected"], report["settled"] = sessions, true, true, true
	report["status"] = "passed"
}
