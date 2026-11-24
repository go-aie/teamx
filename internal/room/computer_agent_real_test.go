//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/computer/aio"
)

type computerModelRequest struct {
	InvokedTools []string `json:"invoked_tools,omitempty"`
	Agent        string   `json:"agent"`
	Names        []string `json:"tools"`
	Bytes        int      `json:"definition_bytes"`
	Images       int      `json:"image_parts"`
}

func computerImageParts(v any) int {
	n := 0
	switch v := v.(type) {
	case map[string]any:
		if v["type"] == "image_url" || v["type"] == "input_image" {
			n++
		}
		for _, e := range v {
			n += computerImageParts(e)
		}
	case []any:
		for _, e := range v {
			n += computerImageParts(e)
		}
	}
	return n
}
func TestComputerRealAgentSessionCapabilities(t *testing.T) {
	if os.Getenv("TEAMX_COMPUTER_AGENT_REAL") != "1" {
		t.Skip("explicit isolated Computer / DSH / model acceptance")
	}
	output := os.Getenv("TEAMX_COMPUTER_EVIDENCE")
	if output == "" {
		t.Fatal("evidence output required")
	}
	dockerBinary, lookupErr := exec.LookPath("docker")
	if lookupErr != nil {
		t.Fatal(lookupErr)
	}
	visualCode := os.Getenv("TEAMX_COMPUTER_VISUAL_CODE")
	t.Setenv("TEAMX_COMPUTER_VISUAL_CODE", "") // the model process must never inherit the expected answer
	cfg, entry, credential := inputAcceptanceModel(t)
	sourceModel := entry.materialize()
	sourceModel.APIKeyEnv = "TEAMX_COMPUTER_ACCEPTANCE_MODEL_KEY"
	t.Setenv(sourceModel.APIKeyEnv, credential.Value)
	upstream, err := url.Parse(sourceModel.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []computerModelRequest
	defer func() {
		mu.Lock()
		copy := append([]computerModelRequest(nil), requests...)
		mu.Unlock()
		raw, _ := json.MarshalIndent(map[string]any{"requests": copy, "test_failed": t.Failed()}, "", "  ")
		_ = os.WriteFile(filepath.Join(output, "agent-request-checks.json"), raw, 0600)
	}()
	proxy := &httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
		p.Out.URL.Scheme = upstream.Scheme
		p.Out.URL.Host = upstream.Host
		p.Out.Host = upstream.Host
		parts := strings.SplitN(strings.TrimPrefix(p.In.URL.Path, "/"), "/", 2)
		if len(parts) == 2 {
			p.Out.URL.Path = "/" + parts[1]
		}
	}}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if e != nil {
			http.Error(w, "read", 400)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var body struct {
			Tools    []json.RawMessage `json:"tools"`
			Messages any               `json:"messages"`
		}
		_ = json.Unmarshal(raw, &body)
		record := computerModelRequest{Agent: strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")[0], Images: computerImageParts(body.Messages), InvokedTools: computerInvokedTools(body.Messages)}
		for _, tool := range body.Tools {
			record.Bytes += len(tool)
			var d struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			}
			_ = json.Unmarshal(tool, &d)
			record.Names = append(record.Names, d.Function.Name)
		}
		mu.Lock()
		requests = append(requests, record)
		mu.Unlock()
		proxy.ServeHTTP(w, r)
	}))
	defer gateway.Close()
	root := t.TempDir()
	originalPath := os.Getenv("PATH")
	noComputerPath := computerAcceptancePath(t, originalPath)
	t.Setenv("PATH", noComputerPath)
	state := historyRoom(0, "")
	state.InputLog = &roomLogRoot{Version: 1}
	state.Members = append(state.Members, MemberState{AgentID: "B", MembershipID: "generation-B", Runner: state.Members[0].Runner})
	state.Members[1].Runner.Profile = "B"
	store, err := OpenStore(filepath.Join(root, "room"), state.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	access, _, _ := accessFixture(t)
	var mcpMu sync.Mutex
	var mcpCalls []map[string]string
	defer func() {
		mcpMu.Lock()
		raw, _ := json.MarshalIndent(mcpCalls, "", "  ")
		mcpMu.Unlock()
		_ = os.WriteFile(filepath.Join(output, "agent-mcp-calls.json"), raw, 0600)
	}()
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			raw, _ := io.ReadAll(io.LimitReader(r.Body, 128<<10+1))
			r.Body = io.NopCloser(bytes.NewReader(raw))
			var q struct {
				Method string `json:"method"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
			_ = json.Unmarshal(raw, &q)
			mcpMu.Lock()
			mcpCalls = append(mcpCalls, map[string]string{"method": q.Method, "tool": q.Params.Name})
			mcpMu.Unlock()
		}
		access.serveMCP(w, r)
	}))
	defer endpoint.Close()
	profiles := map[string]dshProfile{}
	for _, member := range state.Members {
		model := sourceModel.Clone()
		model.BaseURL = gateway.URL + "/" + member.AgentID + upstream.Path
		role := `This is an isolated read-only Computer acceptance. Read the current Room input first if required. Use the native TeamX Computer tools for the task, and return your answer as private final output; do not post messages, use Bash, host files, delegation, external sites or other tools. If Computer tools are absent, return exactly COMPUTER_DISABLED_OK without trying to access a browser. `
		if member.AgentID == "A" {
			model.InputModalities = []string{"text"}
			role += `When Computer is available, navigate to http://127.0.0.1:18765/?screen=A and read its visible text. Reply with the page heading and account state using the observed text. Use browser_navigate and browser_get_text only (plus input tools).`
		} else {
			role += `When Computer is available, navigate to http://127.0.0.1:18765/visual and take a desktop screenshot using browser_gui_screenshot. Read the eight-digit code in the image and return it exactly. The code is visible only in pixels. Use browser_navigate and browser_gui_screenshot (plus computer_status and input tools); do not use text, markdown, links, DOM, scripts or source-reading tools to obtain the code.`
		}
		workspace := filepath.Join(root, member.AgentID)
		if err = os.Mkdir(workspace, 0700); err != nil {
			t.Fatal(err)
		}
		profiles[member.Runner.Profile] = dshProfile{BinaryPath: cfg.Runtime.DSH.BinaryPath, BinaryArgs: cfg.Runtime.DSH.BinaryArgs, Artifact: cfg.Runtime.DSH.Artifact, Workspace: workspace, Model: model, Instructions: role}
	}
	cli := os.Getenv("TEAMX_COMPUTER_CLI")
	if !filepath.IsAbs(cli) {
		t.Fatal("isolated real TeamX CLI path required")
	}
	if _, err := os.Stat(cli); err != nil {
		t.Fatal(err)
	}
	settings := settingsFromFile(t, filepath.Join(root, "config.toml"), false)
	serviceConfig := serviceConfig{endpoint: endpoint.URL, cliPath: cli, access: access, settings: settings, unifiedInputs: true, dshProfiles: profiles, dshTurnTimeout: 3 * time.Minute, maxTurns: 8}
	svc, err := newService(filepath.Join(root, "room"), state.RoomID, serviceConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := svc.close(ctx); err != nil {
			t.Error(err)
		}
	}()
	c, err := newComputerController(context.Background(), startupConfig{path: settings.path}, settings, svc.info, "isolated-agent-acceptance", access.space.OwnerUserID, func(MemberIdentity) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.writer.Close(context.Background()); _ = unlockDirectory(c.lock); c.cancel() }()
	var providerConnections atomic.Int32
	c.connect = func(_ string, endpoint, key string) (computer.ManagedProvider, error) {
		providerConnections.Add(1)
		return aio.New(endpoint, key)
	}
	keyBytes, err := os.ReadFile(os.Getenv("TEAMX_COMPUTER_TOKEN_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	var saved managedRoomConfig
	saved.Version = managedConfigVersion
	saved.RoomIdentity = state.RoomIdentity
	for _, m := range state.Members {
		saved.Members = append(saved.Members, managedMember{MemberState: m})
	}
	if err = svc.store.save(filepath.Join(svc.store.dir, "room-config.json"), saved, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	x, err := newComputerMembers(c, nil, svc)
	if err != nil {
		t.Fatal(err)
	}
	c.members = x
	settings.computer = c
	defer func() { _ = svc.close(context.Background()); _ = x.host.Close(context.Background()) }()
	apply := func(input computerChangeInput) computerChange {
		t.Helper()
		if _, err := c.save(input); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Minute)
		for time.Now().Before(deadline) {
			c.advance()
			c.mu.Lock()
			op := c.journal.Operations[len(c.journal.Operations)-1]
			c.mu.Unlock()
			if op.Status == "completed" || op.Status == "failed" {
				return op
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("real connection application did not settle")
		return computerChange{}
	}
	connect := computerChangeInput{ServiceInfo: c.service, InstallationID: c.owner.InstallationID, Caller: Caller{Author: Author{Kind: "human", ID: c.owner.OwnerID}}, ID: newComputerID(), ExpectedVersion: c.current.Load().Version, Enabled: true, Provider: "aio", Endpoint: os.Getenv("TEAMX_COMPUTER_ENDPOINT"), MaxScreens: 2, CredentialAction: "replace", Credential: strings.TrimSpace(string(keyBytes))}

	ids := []MemberIdentity{}
	for _, m := range state.Members {
		id := MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: m.AgentID, MembershipID: m.MembershipID}
		ids = append(ids, id)
	}
	post := func(text string) {
		t.Helper()
		_, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: state.RoomIdentity, Caller: Caller{Author: Author{Kind: "human", ID: access.space.OwnerUserID}}, ClientMessageID: newComputerID(), Text: text})
		if err != nil {
			t.Fatal(err)
		}
	}
	waitTurn := func(id MemberIdentity, turn uint64) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		status := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber >= turn })
		if status.Agent.Turn == nil || status.Agent.Turn.Status != "completed" {
			t.Fatalf("%s turn: %+v", id.AgentID, status)
		}
	}
	post("从未启用 Computer。请检查可用工具列表，若没有 Computer 工具，直接私有回复 COMPUTER_DISABLED_OK。")

	for _, id := range ids {
		if _, err := svc.control(context.Background(), apiRequest{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "human", ID: access.space.OwnerUserID}}, AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range ids {
		waitTurn(id, 1)
	}
	if providerConnections.Load() != 0 {
		t.Fatal("never-enabled real Agent constructed Provider")
	}
	mu.Lock()
	plain := append([]computerModelRequest(nil), requests...)
	mu.Unlock()
	for _, r := range plain {
		for _, name := range r.Names {
			if strings.Contains(name, "browser_") || strings.Contains(name, "computer_status") {
				t.Fatal("never-enabled model received Computer schema")
			}
		}
	}

	t.Setenv("PATH", originalPath)
	if op := apply(connect); op.Status != "completed" {
		t.Fatal("initial online connection", op)
	}
	for _, id := range ids {
		applyMemberComputer(t, x, id, true)
	}
	post("请完成各自的只读 Computer 验收任务，私有回复结果，不向外部平台写入。")
	for _, id := range ids {
		waitTurn(id, 2)
	}
	answers := map[string]string{}
	for _, id := range ids {
		var d backendDiagnostic
		if err = readSnapshot(filepath.Join(svc.store.dir, "agents", id.MembershipID, "dsh-turn.json"), &d, maxPrivateFileBytes); err != nil {
			t.Fatal(err)
		}
		answers[id.AgentID] = d.Result.Text
	}
	if !strings.Contains(answers["A"], "TeamX screen A") || !strings.Contains(answers["A"], "anonymous") {
		t.Fatal("real text task did not use page result", answers["A"])
	}
	code := visualCode
	if len(code) != 8 || !strings.Contains(answers["B"], code) {
		t.Fatal("real image task did not read pixels", answers["B"])
	}
	mu.Lock()
	before := len(requests)
	initial := append([]computerModelRequest(nil), requests...)
	mu.Unlock()
	hasImage := false
	observedScreenshotCall := false
	for _, r := range initial {
		if r.Agent == "B" {
			for _, name := range r.InvokedTools {
				if name == "mcp__teamx__browser_gui_screenshot" {
					observedScreenshotCall = true
				}
				if name != "mcp__teamx__computer_status" && name != "mcp__teamx__browser_navigate" && name != "mcp__teamx__browser_gui_screenshot" && name != "mcp__teamx__inbox_read" && name != "mcp__teamx__inbox_list" && name != "mcp__teamx__history_read" && name != "mcp__teamx__history_list" {
					t.Fatal("image acceptance used an alternate answer channel", name)
				}
			}
		}
		if r.Agent == "B" && r.Images > 0 {
			hasImage = true
		}
		if r.Agent == "A" {
			for _, name := range r.Names {
				if strings.Contains(name, "gui_screenshot") || strings.Contains(name, "gui_execute_action") {
					t.Fatal("text model received visual tool", name)
				}
			}
		}
	}
	if !hasImage || !observedScreenshotCall {
		t.Fatal("real model never received screenshot image")
	}
	applyMemberComputer(t, x, ids[0], false)
	disable := connect
	disable.ID = newComputerID()
	disable.Enabled = false
	disable.ExpectedVersion = c.current.Load().Version
	disable.CredentialAction = "keep"
	disable.Credential = ""
	if op := apply(disable); op.Status != "completed" {
		t.Fatal("global disable", op)
	}
	preference, err := x.preference(ids[1], svc.store.dir)
	if err != nil || !preference.ComputerEnabled {
		t.Fatal("global disable lost B preference", err)
	}
	post("电脑已经关闭。请检查可用工具列表，若没有 Computer 工具，直接私有回复 COMPUTER_DISABLED_OK。")
	for _, id := range ids {
		waitTurn(id, 3)
	}
	mu.Lock()
	after := append([]computerModelRequest(nil), requests[before:]...)

	mu.Unlock()
	if len(after) < 2 {
		t.Fatal("missing actual requests after capability removal")
	}
	for _, r := range after {
		for _, name := range r.Names {
			if strings.Contains(name, "browser_") || strings.Contains(name, "computer_status") {
				t.Fatal("DSH retained Computer schema after disable", name)
			}
		}
	}
	for _, id := range ids {
		var d backendDiagnostic
		_ = readSnapshot(filepath.Join(svc.store.dir, "agents", id.MembershipID, "dsh-turn.json"), &d, maxPrivateFileBytes)
		if !strings.Contains(d.Result.Text, "COMPUTER_DISABLED_OK") {
			t.Fatal("disabled member failed ordinary model turn", id.AgentID, d.Result.Text)
		}
	}

	// Stop only the disposable Runtime named by the launcher, then reopen TeamX
	// with its saved disabled configuration while the same model gateway stays up.
	name := os.Getenv("TEAMX_COMPUTER_CONTAINER")
	if !strings.HasPrefix(name, "teamx-computer-connection-") || !strings.HasSuffix(name, "-agent") {
		t.Fatal("disposable Runtime identity required")
	}
	history, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	generations := map[MemberIdentity]string{}
	for _, id := range ids {
		ref, _, err := readSessionReference(filepath.Join(svc.store.dir, "agents", id.MembershipID, "session.json"), id)
		if err != nil {
			t.Fatal(err)
		}
		generations[id] = ref.Directory
	}
	if err = svc.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = x.host.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = c.writer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = unlockDirectory(c.lock); err != nil {
		t.Fatal(err)
	}
	c.cancel()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 60*time.Second)
	stopOutput, stopErr := exec.CommandContext(stopCtx, dockerBinary, "stop", "-t", "45", name).CombinedOutput()
	stopCancel()
	if stopErr != nil {
		t.Fatal("disposable Runtime stop", stopErr, string(stopOutput))
	}
	t.Setenv("PATH", noComputerPath)
	loaded, err := loadStartupConfig(settings.path, true)
	if err != nil {
		t.Fatal(err)
	}
	settings = newSystemSettings(loaded)
	serviceConfig.settings = settings
	svc, err = newService(filepath.Join(root, "room"), state.RoomID, serviceConfig)
	if err != nil {
		t.Fatal(err)
	}
	c, err = newComputerController(context.Background(), loaded, settings, svc.info, "isolated-agent-acceptance", access.space.OwnerUserID, func(MemberIdentity) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	var offlineConnections atomic.Int32
	c.connect = func(string, string, string) (computer.ManagedProvider, error) {
		offlineConnections.Add(1)
		return nil, computer.Fail(computer.Unavailable, "offline acceptance forbids Provider IO")
	}
	x, err = newComputerMembers(c, nil, svc)
	if err != nil {
		t.Fatal(err)
	}
	c.members = x
	settings.computer = c
	x.recover(context.Background())
	c.advance()
	afterRestart, err := svc.store.LoadRoom()
	if err != nil || afterRestart.LatestSeq != history.LatestSeq {
		t.Fatal("restart changed Room history", err)
	}
	if err = svc.resumeMembers(context.Background()); err != nil {
		t.Fatal(err)
	}
	post("电脑容器已经停止。请检查可用工具列表，若没有 Computer 工具，直接私有回复 COMPUTER_DISABLED_OK。")
	for _, id := range ids {
		waitTurn(id, 4)
		ref, _, err := readSessionReference(filepath.Join(svc.store.dir, "agents", id.MembershipID, "session.json"), id)
		if err != nil || ref.Directory != generations[id] {
			t.Fatal("disabled normal restart replaced original generation", err)
		}
	}
	if offlineConnections.Load() != 0 {
		t.Fatal("disabled ordinary Agent constructed Provider", offlineConnections.Load())
	}
	mu.Lock()
	all := append([]computerModelRequest(nil), requests...)
	offlineRequests := append([]computerModelRequest(nil), requests[before:]...)
	mu.Unlock()
	for _, r := range offlineRequests {
		for _, name := range r.Names {
			if strings.Contains(name, "browser_") || strings.Contains(name, "computer_status") {
				t.Fatal("Computer schema returned after disabled restart", name)
			}
		}
	}
	report := map[string]any{"passed": true, "real_dsh": true, "real_model": entry.ModelID, "real_aio": true, "independent_screens": 2, "text_task": true, "image_pixels_read": true, "image_answer_not_in_model_environment": true, "image_task_used_only_native_navigation_screenshot_status_and_input": true, "schema_removed_in_actual_requests": true, "global_disable_preserves_preference": true, "normal_restart_same_generation": true, "ordinary_agent_after_container_stop": true, "never_enabled_real_agent": true, "never_enabled_provider_connections": 0, "optional_computer_binaries_absent_from_path": true, "offline_provider_connections": offlineConnections.Load(), "requests": all, "external_business_writes": 0}
	data, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(output, "agent-model.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func computerInvokedTools(v any) []string {
	var names []string
	switch v := v.(type) {
	case map[string]any:
		if calls, ok := v["tool_calls"].([]any); ok {
			for _, raw := range calls {
				if call, ok := raw.(map[string]any); ok {
					if f, ok := call["function"].(map[string]any); ok {
						if name, ok := f["name"].(string); ok {
							names = append(names, name)
						}
					}
				}
			}
		}
		for key, child := range v {
			if key != "tool_calls" {
				names = append(names, computerInvokedTools(child)...)
			}
		}
	case []any:
		for _, child := range v {
			names = append(names, computerInvokedTools(child)...)
		}
	}
	return names
}

// Preserve the normal Agent toolchain while removing the optional Computer
// executables. Both never-enabled startup and disabled restart run in this PATH.
func computerAcceptancePath(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	seen := map[string]bool{}
	for _, parent := range filepath.SplitList(source) {
		if !filepath.IsAbs(parent) {
			continue
		}
		entries, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if seen[name] || entry.IsDir() || strings.HasPrefix(name, "docker") || name == "teamx-computer" {
				continue
			}
			if err = os.Symlink(filepath.Join(parent, name), filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			seen[name] = true
		}
	}
	original := os.Getenv("PATH")
	t.Setenv("PATH", dir)
	for _, name := range []string{"docker", "teamx-computer"} {
		if _, err := exec.LookPath(name); err == nil {
			t.Fatal("optional executable still discoverable", name)
		}
	}
	t.Setenv("PATH", original)
	return dir
}
