//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
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
	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/mark3labs/mcp-go/mcp"
)

type upgradeObservedDSH struct {
	dshComponent
	prompt chan []dsh.ContentBlock
}

func (c upgradeObservedDSH) RunPrompt(ctx context.Context, blocks []dsh.ContentBlock) (dsh.TurnResult, error) {
	c.prompt <- blocks
	return c.dshComponent.RunPrompt(ctx, blocks)
}

func TestInputUpgradeRotatesSettledDSHContract(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for the offline ACP fixture")
	}
	node, err = filepath.EvalSymlinks(node)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs("../../scripts/fixtures/background-acp.mjs")
	if err != nil {
		t.Fatal(err)
	}
	h := newRoomHarnessWithTimeout(t, historyRoom(1, "saved conversation before upgrading"), nil, 30*time.Second)
	id := h.memberID("A", "generation-A")
	fixtureDir := t.TempDir()
	log, release := filepath.Join(fixtureDir, "acp.jsonl"), filepath.Join(fixtureDir, "release")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	binary := filepath.Join(fixtureDir, "fixture-dsh")
	wrapper := "#!/bin/sh\nif [ \"${1:-}\" = --version ]; then echo 1.0.0; exit 0; fi\nexec " + quote(node) + " " + quote(fixture) + " \"$@\"\n"
	if err := os.WriteFile(binary, []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEAMX_UPGRADE_FIXTURE_KEY", "offline-only")
	profile := dshProfile{BinaryPath: binary, Workspace: fixtureDir,
		Model:   dsh.ModelConfig{Protocol: "openai-completions", BaseURL: "http://127.0.0.1:1/v1", ModelID: "background-fixture", APIKeyEnv: "TEAMX_UPGRADE_FIXTURE_KEY"},
		ToolEnv: map[string]string{"TEAMX_BACKGROUND_LOG": log, "TEAMX_BACKGROUND_RELEASE": release},
	}
	oldBackend := dshBackend{store: h.store, profile: profile}
	oldSession, err := oldBackend.OpenSession(h.ctx, adapterOptions(h.store, id))
	if err != nil {
		t.Fatal(err)
	}
	if err := oldSession.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	agent := AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: 1,
		Turn: &TurnRecord{Number: 1, Status: "completed", OfferedUnreadSeq: 1, SnapshotSeq: 1}}
	if err := h.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	if err := h.store.saveScheduling(memberScheduling{Version: formatVersion, MemberIdentity: id, Enabled: true, Used: 1}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(h.store.dir)); err != nil {
		t.Fatal(err)
	}
	oldBackendPath := filepath.Join(dir, "agents", id.MembershipID, "session.json.dsh", "backend.json")
	oldEvidence, err := os.ReadFile(oldBackendPath)
	if err != nil {
		t.Fatal(err)
	}
	accessConfig, _ := testAccessConfig(t, t.TempDir())
	access, err := newServerAccess(accessConfig, "http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	mcpServer := httptest.NewServer(http.HandlerFunc(access.serveMCP))
	defer mcpServer.Close()
	options := make(chan dsh.Options, 2)
	prompts := make(chan []dsh.ContentBlock, 2)
	config := serviceConfig{unifiedInputs: true, endpoint: mcpServer.URL, cliPath: os.Args[0], access: access,
		dshProfiles: map[string]dshProfile{"fixture-ignore": profile}}
	svc, err := startService(dir, id.RoomID, config, func(s *service, state RoomSnapshot) error {
		factory := s.memberFactory
		s.memberFactory = func(id MemberIdentity, runner RunnerConfig, restored *AgentSnapshot) actorx.Actor {
			member := factory(id, runner, restored).(*MemberAgent)
			b := member.Backend.(dshBackend)
			b.open = func(ctx context.Context, o dsh.Options) (dshComponent, error) {
				options <- o
				component, err := dsh.Open(ctx, o)
				if err != nil {
					return nil, err
				}
				return upgradeObservedDSH{component, prompts}, nil
			}
			member.Backend = b
			return member
		}
		if err := s.loadMembers(state); err != nil {
			return err
		}
		return s.attachMembers(state)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.WriteFile(release, nil, 0600)
		closeService(t, svc)
	}()
	recovered := svc.recovered[0]
	if recovered.backendResume || !recovered.recovery || !strings.Contains(recovered.blocked, "requires settled Session rotation") {
		t.Fatal("changed tool contract did not require generation replacement", recovered)
	}
	if err := svc.resumeMembers(h.ctx); err != nil {
		t.Fatal(err)
	}
	var opts dsh.Options
	select {
	case opts = <-options:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	if opts.StateDir == filepath.Dir(oldBackendPath) || !opts.InputBridge || len(opts.MCPServers) != 1 ||
		!strings.Contains(opts.Instructions, inputstream.Contract) || strings.Contains(opts.Instructions, "turn_number=current_turn") {
		t.Fatal("recovered Session received wrong tool configuration", opts.StateDir)
	}
	select {
	case prompt := <-prompts:
		if len(prompt) < 2 || !strings.Contains(prompt[0].Text, "Input directory") || !strings.Contains(prompt[1].Text, "saved conversation before upgrading") {
			t.Fatal("missing migrated recovery context", prompt)
		}
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	verifyMigratedSessionTools(t, svc, id, opts, mcpServer.Client())
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	status := waitMember(t, h.ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool {
		return s.Agent.TurnNumber >= 2 && s.Agent.Turn != nil && s.Agent.Turn.Status == "completed"
	})
	if status.Agent.TurnNumber != 2 {
		t.Fatal("old turn was replayed", status.Agent)
	}
	after, err := os.ReadFile(oldBackendPath)
	if err != nil || !bytes.Equal(oldEvidence, after) {
		t.Fatal("old Session settlement was changed", err)
	}
}

func verifyMigratedSessionTools(t *testing.T, svc *service, id MemberIdentity, opts dsh.Options, client *http.Client) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), "POST", opts.MCPServers[0].URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	for _, h := range opts.MCPServers[0].Headers {
		request.Header.Set(h.Name, h.Value)
	}
	resp, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var listed struct {
		Result struct{ Tools []mcp.Tool }
	}
	if err := json.Unmarshal(body, &listed); err != nil || resp.StatusCode != http.StatusOK || len(listed.Result.Tools) != 2 ||
		listed.Result.Tools[0].Name != "inbox_list" || listed.Result.Tools[1].Name != "inbox_read" {
		t.Fatal("unexpected MCP tools/list for rotated Session", resp.StatusCode, string(body), err)
	}
	read := listed.Result.Tools[1].InputSchema
	if len(read.Properties) != 2 || read.Properties["stream_ref"] == nil || read.Properties["limit"] == nil ||
		len(read.Required) != 1 || read.Required[0] != "stream_ref" {
		t.Fatal("unexpected inbox_read schema", read)
	}
	ref := roomStreamRef(id)
	var execution struct {
		Token    string `json:"token"`
		Endpoint string `json:"endpoint"`
	}
	if err := readSnapshot(filepath.Join(opts.StateDir, "input-execution.json"), &execution, maxPrivateFileBytes); err != nil || execution.Token == "" {
		t.Fatal("missing migrated turn binding", err)
	}
	result := simpleMCPRequest(t, svc.access, execution.Token, "list-current", "inbox_list", map[string]any{}, httptest.NewRecorder())
	if result == nil || result.IsError {
		t.Fatal("the migrated execution could not discover inputs")
	}
	for _, key := range []string{"turn_number", "read_id", "cursor"} {
		out := httptest.NewRecorder()
		result := simpleMCPRequest(t, svc.access, execution.Token, "reject-"+key, "inbox_read",
			map[string]any{"stream_ref": ref, key: "legacy"}, out)
		if result != nil && !result.IsError || !strings.Contains(out.Body.String(), "unknown field") || !strings.Contains(out.Body.String(), key) {
			t.Fatalf("MCP inbox_read did not reject legacy field %s: %s", key, out.Body.String())
		}
	}
	binding, err := json.Marshal(map[string]string{"token": execution.Token, "call": "framework-cli"})
	if err != nil {
		t.Fatal(err)
	}
	env := func(key string) string {
		switch key {
		case "TEAMX_EXECUTION_TOKEN":
			return opts.ExecutionToken
		case "DSH_TEAMX_INPUT_BINDING":
			return string(binding)
		default:
			return opts.ToolEnv[key]
		}
	}
	for _, flag := range []string{"--turn-number", "--read-id", "--cursor"} {
		err := runCLIWithInput(t.Context(), []string{"inbox", "read", "--stream", ref, flag, "legacy"}, strings.NewReader(""), io.Discard, io.Discard, env)
		if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("CLI did not reject %s at the parser: %v", flag, err)
		}
	}
}
