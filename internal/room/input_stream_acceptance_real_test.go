//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/inputstream"
)

func inputAcceptanceModel(t *testing.T) (startupConfig, modelEntry, *dsh.Credential) {
	t.Helper()
	path := os.Getenv("TEAMX_INPUT_COMPARISON_CONFIG")
	cfg, err := loadStartupConfig(path, true)
	if err != nil || cfg.Runtime == nil || cfg.Models == nil {
		t.Fatal("acceptance requires a configured DSH launcher and default model", err)
	}
	entry, err := cfg.Models.resolve(modelSelection{Mode: "default"})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := newModelCredentials(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := credentials.get(entry.CredentialRef)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, entry, credential
}

func TestInputSelectionBrowserRealDSH(t *testing.T) {
	if os.Getenv("TEAMX_INPUT_SELECTION_REAL") != "1" {
		t.Skip("opt-in Chromium and real DSH selection acceptance")
	}
	cfg, entry, credential := inputAcceptanceModel(t)
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "transport": "HTTP MCP", "external_writes": 0, "model": entry.materialize().ModelID}
	defer imageRealReport(t, out, report)
	workspace := filepath.Join(out, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(workspace, "teamx")
	if b, err := exec.CommandContext(t.Context(), "go", "build", "-o", cli, "../../cmd/teamx").CombinedOutput(); err != nil {
		t.Fatal(err, string(b))
	}
	testGitLabSelectionBrowser(t, func(t *testing.T, server *httptest.Server, m *roomManager, reader *inputSession) {
		id, svc := reader.storage.id, reader.service
		access := m.config.access
		initial, _ := issueInboxCredential(t, access, svc, id, false)
		inputToken, revokeInput, err := access.issueInputBinding(reader)
		if err != nil {
			t.Fatal(err)
		}
		defer revokeInput()
		execution, revokeExec, err := access.issueExecution(executionBinding{ServiceInfo: svc.info, Caller: agentCallerIdentity(id)})
		if err != nil {
			t.Fatal(err)
		}
		defer revokeExec()
		dir := filepath.Join(t.TempDir(), "runtime")
		instructions := `Read the Room stream using inbox_read and locate the user_selection event and its result_ref. Then run "$TEAMX_CLI" platforms selection status --operation-id ID. ID is the final path component of result_ref, after "platforms/selection/". This command already returns JSON. Reply privately with the exact selected project path, numeric project ID, and result version from the status. Do not run other shell commands, acknowledge, alter any setting, write to any platform, or read other streams.`
		opts := dsh.Options{BinaryPath: cfg.Runtime.DSH.BinaryPath, BinaryArgs: cfg.Runtime.DSH.BinaryArgs,
			Artifact: cfg.Runtime.DSH.Artifact, Workspace: workspace, StateDir: dir, Instructions: instructions + "\n" + unifiedAgentGuide(),
			Model: entry.materialize(), Credential: credential, InputBridge: true, ExecutionToken: execution,
			MCPServers: []dsh.MCPHTTPServer{mcpHTTPConfiguration(server.URL, initial)}, Limits: dsh.Limits{TurnTimeout: 2 * time.Minute},
			ToolEnv: map[string]string{"TEAMX_CLI": cli, "TEAMX_ENDPOINT": server.URL, "TEAMX_ROOM_ID": id.RoomID,
				"TEAMX_ROOM_INSTANCE": id.RoomInstance, "TEAMX_SERVICE_EPOCH": svc.info.ServiceEpoch,
				"TEAMX_AGENT_ID": id.AgentID, "TEAMX_MEMBERSHIP_ID": id.MembershipID, "TEAMX_INPUT_CONTRACT": inputstream.Contract}}
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		session, err := dsh.Open(ctx, opts)
		if session != nil {
			defer func() {
				closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				if err := session.Close(closeCtx); err != nil {
					t.Error(err)
				}
			}()
		}
		if err != nil {
			t.Fatal(err)
		}
		bindingPath := filepath.Join(dir, "input-execution.json")
		if err := os.WriteFile(bindingPath, mustContractJSON(map[string]string{"token": inputToken, "endpoint": server.URL}), 0600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(bindingPath)
		catalog, err := reader.catalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		directory, _, err := catalog.Directory()
		if err != nil {
			t.Fatal(err)
		}
		reader.rememberDirectory(directory)
		prompt := "Input directory (JSON):\n" + string(mustContractJSON(directory))
		start := time.Now()
		result, runErr := session.Run(ctx, prompt)
		report["elapsed_seconds"], report["instructions"], report["prompt"] = time.Since(start).Seconds(), instructions, prompt
		report["answer"], report["tools"] = result.Text, result.Tools
		if runErr != nil {
			t.Fatal(runErr)
		}
		read, status := false, false
		for _, tool := range result.Tools {
			read = read || strings.Contains(tool.Output, "\"event_type\":\"user_selection\"") ||
				strings.Contains(tool.Output, "platforms/selection/")
			status = status || strings.Contains(tool.Input, "platforms selection status") && strings.Contains(tool.Output, "other/project")
		}
		if !read || !status || !strings.Contains(result.Text, "other/project") {
			t.Fatal("the Agent did not formally read and query the selected project")
		}
		state, err := reader.storage.repository.State(reader.execution)
		if err != nil || state.Used != 0 || reader.storage.repository.Barrier(reader.execution) != nil {
			t.Fatal("selection recovery resolved unrelated platform input or left an unconfirmed result", err)
		}
		report["agent_read_and_status"] = true
		report["checkpoints"] = state.Attempts
	})
	report["status"] = "passed"
}

// Use the shipped server entry point and the configured model. The old layout
// and settled generation are created locally before copying into the deployment.
func TestInputUpgradeNormalRestartRealDSH(t *testing.T) {
	if os.Getenv("TEAMX_INPUT_UPGRADE_REAL") != "1" {
		t.Skip("opt-in normal startup and restart with a real model")
	}
	cfg, entry, credential := inputAcceptanceModel(t)
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "transport": "HTTP MCP", "external_writes": 0, "model": entry.materialize().ModelID}
	defer imageRealReport(t, out, report)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	const key = "TEAMX_INPUT_UPGRADE_MODEL_KEY"
	t.Setenv(key, credential.Value)
	model := entry.materialize()
	model.APIKeyEnv = key
	workspace := filepath.Join(out, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	profile := dshProfile{BinaryPath: cfg.Runtime.DSH.BinaryPath, BinaryArgs: cfg.Runtime.DSH.BinaryArgs,
		Artifact: cfg.Runtime.DSH.Artifact, Workspace: workspace, Model: model,
		Instructions: "Read the pending Room stream with inbox_read before answering every request. Use only the Inbox MCP tools. Never run shell commands, use platform tools, send messages or make external writes. Answer privately."}
	const savedScope = "READ_ONLY_SCOPE_7"
	legacy := historyRoom(3, "")
	legacy.Members[0].AgentID = "reviewer"
	legacy.Members[0].ReadThrough = 2
	legacy.Messages[0].Author = Author{Kind: "agent", ID: "reviewer"}
	legacy.Messages[0].AuthorMembershipID = legacy.Members[0].MembershipID
	legacy.Messages[0].Text = "Which scope should I review?"
	legacy.Messages[1].ReplyTo = 1
	legacy.Messages[1].Text = "The confirmed scope is " + savedScope + ". Read only; never ask the same question again."
	legacy.Messages[2].Text = "Continue with my existing answer. Use inbox_read to read this new Room message, then reply privately with UPGRADE_ROOM_MARKER followed by the scope in my earlier answer."
	h := newRoomHarnessWithTimeout(t, legacy, nil, 2*time.Minute)
	id := h.memberID("reviewer", legacy.Members[0].MembershipID)
	old, err := (dshBackend{store: h.store, profile: profile}).OpenSession(ctx, adapterOptions(h.store, id))
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1,
		LastOfferedUnreadSeq: 2, Turn: &TurnRecord{Number: 1, Status: "completed", OfferedUnreadSeq: 2, SnapshotSeq: 2}}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.saveScheduling(memberScheduling{Version: schedulingVersion, MemberIdentity: id, Enabled: true, Used: 1}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "room")
	if err := os.CopyFS(dir, os.DirFS(h.store.dir)); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(out, "teamx")
	if b, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "../../cmd/teamx").CombinedOutput(); err != nil {
		t.Fatal(err, string(b))
	}
	profilePath := filepath.Join(out, "dsh-profiles.json")
	if err := os.WriteFile(profilePath, mustContractJSON(map[string]dshProfile{"fixture-ignore": profile}), 0600); err != nil {
		t.Fatal(err)
	}
	d := &realRoomDriver{cliDriver: cliDriver{ctx: ctx, binary: binary, dir: dir}, output: out,
		report: report, profilePath: profilePath, secret: credential.Value, sessionMaxTurns: 1}
	report["apiKeyEnv"] = key
	defer func() {
		if err := d.stopReal(); err != nil {
			report["status"] = "failed"
			t.Error(err)
		}
	}()
	must := func(err error) {
		t.Helper()
		if err != nil {
			report["error"] = err.Error()
			t.Fatal(err)
		}
	}
	oldBackendPath := filepath.Join(dir, "agents", id.MembershipID, "session.json.dsh", "backend.json")
	oldBackend, err := os.ReadFile(oldBackendPath)
	must(err)
	check := func(turn, through uint64) string {
		t.Helper()
		list, err := d.waitReal(turn, "completed")
		must(err)
		member, err := findMember(list, id.AgentID)
		must(err)
		if member.ReadThrough != through || member.UnreadCount != 0 || member.Execution.SessionGeneration == "" {
			t.Fatalf("turn %d did not confirm the pending Room input: read through %d, unread %d", turn, member.ReadThrough, member.UnreadCount)
		}
		return member.Execution.SessionGeneration
	}
	must(d.startRealRoom(1, id.RoomID))
	first := check(2, 3)
	var recovered dshDiagnostic
	must(readSnapshot(filepath.Join(dir, "agents", id.MembershipID, "dsh-turn.json"), &recovered, maxPrivateFileBytes))
	if !strings.Contains(recovered.Result.Text, "UPGRADE_ROOM_MARKER") || !strings.Contains(recovered.Result.Text, savedScope) {
		t.Fatal("the migrated Session did not continue with the saved human answer")
	}
	report["saved_answer_used"] = true
	report["visible_recovery_answer"] = recovered.Result.Text
	report["recovery_tool_calls"] = recovered.Result.Tools
	progress := readUpgradeProgress(t, dir)
	if progress.Phase != "completed" || first == "session.json.dsh" {
		t.Fatal("upgrade or tool contract rotation was not completed", progress.Phase, first)
	}
	after, err := os.ReadFile(oldBackendPath)
	must(err)
	if !bytes.Equal(oldBackend, after) {
		t.Fatal("original settled generation changed")
	}
	report["migration_and_contract_rotation"] = true
	must(d.stopReal())
	must(d.startRealRoom(2, id.RoomID))
	list, err := d.members()
	must(err)
	member, err := findMember(list, id.AgentID)
	must(err)
	afterRestart := readUpgradeProgress(t, dir)
	if member.Execution.Agent.TurnNumber != 2 || member.ReadThrough != 3 || member.Execution.SessionGeneration != first ||
		afterRestart.ID != progress.ID || afterRestart.Phase != "completed" || d.info.RoomIdentity != id.RoomIdentity {
		t.Fatal("restart changed the settled turn, cursor, generation, migration ID or Room identity")
	}
	must(d.command(nil, "send", "--text", "Read this new Room input with inbox_read and answer privately with RESTART_ROOM_MARKER.", "--id", "after-restart"))
	if second := check(3, 4); second == first {
		t.Fatal("session turn limit did not rotate the generation")
	}
	log, err := os.ReadFile(filepath.Join(out, "serve-1.log"))
	must(err)
	if !strings.Contains(string(log), "输入格式升级完成") {
		t.Fatal("normal startup did not log the upgrade")
	}
	report["restart_preserved_input_state"] = true
	report["session_rotation_after_restart"] = true
	report["room_read_through"] = []uint64{2, 3, 3, 4}
	report["status"] = "passed"
}
