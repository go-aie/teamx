//go:build e2e && linux

package dsh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Real pinned DSH and its built-in tools, controlled model responses, synthetic
// files only. Run through scripts/space_isolation/run.py, never in a daily home.
func TestLinuxSpaceIsolationRoutes(t *testing.T) {
	if os.Getenv("TEAMX_ISOLATION_REAL") != "1" {
		t.Skip("use scripts/space_isolation/run.py in a disposable Linux container")
	}
	if os.Getenv("ISOLATION_RESTART_ONLY") == "1" {
		acceptLinuxIsolationContainerResume(t)
		return
	}
	if os.Getuid() != 0 || os.Getenv("HOME") == "/lab/a/home" {
		t.Fatal("trusted disposable test supervisor required")
	}
	if _, err := os.Stat("/.dockerenv"); err != nil {
		t.Fatal("disposable Docker environment required")
	}
	if entries, err := os.ReadDir("/lab"); err != nil || len(entries) != 0 {
		t.Fatal("empty isolated /lab required", err)
	}
	report := map[string]any{"status": "failed", "real_dsh": true, "real_model": false, "production_backend": false}
	defer func() {
		if t.Failed() {
			report["status"] = "failed"
		}
		if err := atomicJSON("/lab", "report.json", report); err != nil {
			t.Error(err)
		}
	}()
	makeDir := func(path string, uid int) {
		t.Helper()
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(path, uid, uid); err != nil {
			t.Fatal(err)
		}
	}
	for i, area := range []string{"a", "b", "c", "control", "service"} {
		base := "/lab/" + area
		if err := os.MkdirAll(base, 0755); err != nil {
			t.Fatal(err)
		}
		uid := 10001 + i
		makeDir(base+"/private", uid)
		if err := os.WriteFile(base+"/private/secret", []byte("SYNTHETIC_"+strings.ToUpper(area)), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(base+"/private/secret", uid, uid); err != nil {
			t.Fatal(err)
		}
	}
	for _, area := range []string{"a", "b"} {
		uid := 10001
		if area == "b" {
			uid = 10002
		}
		for _, part := range []string{"workspace", "home", "tmp", "state"} {
			makeDir("/lab/"+area+"/"+part, uid)
		}
	}
	if err := os.Symlink("/lab/b/private", "/lab/a/workspace/escape"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"glab", "lark-config", "lark-data"} {
		makeDir("/lab/b/private/"+path, 10002)
	}
	if err := os.WriteFile("/lab/b/private/glab/config.yml", []byte("hosts:\n  gitlab.example.invalid:\n    token: SYNTHETIC_B\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Populate the pinned CLI's own encrypted Linux store, without a real app
	// or login. config init may fail its optional network probe; the persisted
	// store and independent auth status are the required evidence.
	larkEnv := append(os.Environ(), "HOME=/lab/b/home", "LARKSUITE_CLI_CONFIG_DIR=/lab/b/private/lark-config", "LARKSUITE_CLI_DATA_DIR=/lab/b/private/lark-data", "LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1", "LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1")
	initCtx, stopInit := context.WithTimeout(t.Context(), 15*time.Second)
	initCommand := exec.CommandContext(initCtx, "lark-cli", "config", "init", "--app-id", "cli_isolation_probe", "--name", "isolation", "--app-secret-stdin")
	initCommand.Env = larkEnv
	initCommand.Stdin = strings.NewReader("SYNTHETIC_LARK_SECRET\n")
	_ = initCommand.Run()
	stopInit()
	master, err := os.ReadFile("/lab/b/private/lark-data/lark-cli/master.key")
	if err != nil || len(master) != 32 {
		t.Fatal("official synthetic master key missing", err)
	}
	ciphertext, err := os.ReadFile("/lab/b/private/lark-data/lark-cli/appsecret_cli_isolation_probe.enc")
	if err != nil || len(ciphertext) == 0 || strings.Contains(string(ciphertext), "SYNTHETIC_LARK_SECRET") {
		t.Fatal("official encrypted synthetic secret missing", err)
	}
	statusCommand := exec.Command("lark-cli", "auth", "status", "--json")
	statusCommand.Env = larkEnv
	status, err := statusCommand.Output()
	if err != nil || !strings.Contains(string(status), "cli_isolation_probe") {
		t.Fatal("official synthetic credential not reloadable", err)
	}
	if err := filepath.WalkDir("/lab/b/private", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(path, 10002, 10002)
	}); err != nil {
		t.Fatal(err)
	}
	report["official_lark_encrypted_store_reloaded"] = true

	// The supervisor deliberately offers an unauthenticated diagnostic canary:
	// distinct UIDs alone cannot protect loopback services. The selected worker
	// boundary exposes only the authenticated model endpoint, never this canary.
	var mu sync.Mutex
	var toolName string
	var toolArgs map[string]any
	var issued bool
	var observed string
	var offered []string
	callSequence := 0
	uidCanary := true
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		canaryEnabled := uidCanary
		mu.Unlock()
		if r.URL.Path == "/uid-canary" && canaryEnabled {
			fmt.Fprint(w, "SYNTHETIC_CONTROL")
			return
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer scoped-model-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			Tools []struct {
				Function struct {
					Name       string          `json:"name"`
					Parameters json.RawMessage `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		delta := map[string]any{"role": "assistant", "content": "ISOLATION_DONE"}
		finish := "stop"
		if !issued && toolName != "" {
			found := false
			for _, tool := range req.Tools {
				offered = append(offered, tool.Function.Name)
				if tool.Function.Name == toolName {
					found = true
					report["tool_schema_"+toolName] = tool.Function.Parameters
				}
			}
			if !found {
				t.Errorf("real DSH tool %s missing; offered %v", toolName, offered)
				w.WriteHeader(400)
				return
			}
			args, _ := json.Marshal(toolArgs)
			callSequence++
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("isolation-call-%d", callSequence), "type": "function", "function": map[string]any{"name": toolName, "arguments": string(args)}}}}
			finish, issued = "tool_calls", true
		} else {
			for i := len(req.Messages) - 1; i >= 0; i-- {
				if req.Messages[i].Role == "tool" {
					observed = string(req.Messages[i].Content)
					break
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, choice := range []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}, map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
			data, _ := json.Marshal(map[string]any{"id": "isolation", "object": "chat.completion.chunk", "model": "isolation", "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	server.Listener.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	options := func(area string, uid int) Options { return linuxIsolationOptions(server.URL, area, uid) }
	closeSession := func(s *Session) error {
		closeCtx, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		return s.Close(closeCtx)
	}
	run := func(s *Session, name string, args map[string]any) string {
		t.Helper()
		mu.Lock()
		toolName, toolArgs, issued, observed = name, args, false, ""
		mu.Unlock()
		result, err := s.Run(ctx, "Execute the configured isolation case.")
		if err != nil {
			t.Fatalf("tool %s: %v; result %+v", name, err, result)
		}
		mu.Lock()
		defer mu.Unlock()
		if !issued || observed == "" || len(result.Tools) == 0 {
			t.Fatalf("tool round trip absent: %s %+v", name, result)
		}
		return observed
	}
	opts := options("a", 10001)
	opts.BinaryArgs = []string{"/opt/experiment/launch.py"} // Stock DSH comparison first.
	opts.StateDir = "/lab/a/baseline-state"
	s, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s == nil {
			return
		}
		if err := closeSession(s); !CleanupSettled(err) {
			t.Error(err)
		}
	}()
	report["agent_info"] = s.reference.AgentInfo
	report["dsh_cli_version"] = s.reference.Artifact.Version
	if kernel, err := exec.Command("uname", "-sr").Output(); err == nil {
		report["kernel"] = strings.TrimSpace(string(kernel))
	}
	// Same UID baseline: two logical spaces with 0700/0600 have no read boundary.
	if err := os.Chown("/lab/b/private", 10001, 10001); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown("/lab/b/private/secret", 10001, 10001); err != nil {
		t.Fatal(err)
	}
	baseline := run(s, "read", map[string]any{"file_path": "/lab/b/private/secret"})
	if !strings.Contains(baseline, "SYNTHETIC_B") {
		t.Fatalf("baseline read not reproduced: %s", baseline)
	}
	report["same_uid_workspace_write_leaks_read"] = true
	if err := os.Chown("/lab/b/private", 10002, 10002); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown("/lab/b/private/secret", 10002, 10002); err != nil {
		t.Fatal(err)
	}
	_ = run(s, "write", map[string]any{"file_path": opts.Workspace + "/own.txt", "content": "OWN_WRITE"})
	if data, err := os.ReadFile(opts.Workspace + "/own.txt"); err != nil || string(data) != "OWN_WRITE" {
		t.Fatal("own file write failed", err)
	}
	foreignWrite := run(s, "write", map[string]any{"file_path": "/lab/b/private/secret", "content": "UNAUTHORIZED"})
	if !strings.Contains(strings.ToLower(foreignWrite), "denied") && !strings.Contains(strings.ToLower(foreignWrite), "permission") && !strings.Contains(strings.ToLower(foreignWrite), "cancel") {
		t.Fatalf("foreign write not explicitly rejected: %s", foreignWrite)
	}
	report["file_tool_own_write_and_foreign_write_denied"] = true
	for _, area := range []string{"b", "c", "control", "service"} {
		value := run(s, "read", map[string]any{"file_path": "/lab/" + area + "/private/secret"})
		if strings.Contains(value, "SYNTHETIC_") || (!strings.Contains(strings.ToLower(value), "permission") && !strings.Contains(value, "EACCES")) {
			t.Fatalf("file read boundary %s: %s", area, value)
		}
	}
	report["file_tool_private_roots_denied"] = true
	stockShell := run(s, "bash", map[string]any{"command": "printf stock-shell", "description": "Check stock nested sandbox availability"})
	report["stock_workspace_write_shell_available"] = strings.Contains(stockShell, "stock-shell")
	report["stock_workspace_write_shell_refuses_without_backend"] = strings.Contains(stockShell, "no sandbox backend is usable")
	if err := closeSession(s); err != nil {
		t.Fatal(err)
	}
	opts = options("a", 10001)
	s, err = Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	report["candidate_dsh_policy"] = "explicit danger-full-access inside outer container; ask unchanged"
	for _, area := range []string{"b", "c", "control", "service"} {
		value := run(s, "read", map[string]any{"file_path": "/lab/" + area + "/private/secret"})
		if strings.Contains(value, "SYNTHETIC_") || (!strings.Contains(strings.ToLower(value), "permission") && !strings.Contains(value, "EACCES")) {
			t.Fatalf("outer file read boundary %s: %s", area, value)
		}
	}
	_ = run(s, "write", map[string]any{"file_path": opts.Workspace + "/outer-own.txt", "content": "OUTER_OWN_WRITE"})
	if data, err := os.ReadFile(opts.Workspace + "/outer-own.txt"); err != nil || string(data) != "OUTER_OWN_WRITE" {
		t.Fatal("outer own write failed", err)
	}
	_ = run(s, "write", map[string]any{"file_path": "/lab/b/private/secret", "content": "UNAUTHORIZED"})
	report["outer_file_tools_exercised"] = true
	canary := run(s, "bash", map[string]any{"command": "curl -fsS " + server.URL + "/uid-canary", "description": "Show distinct UID alone does not protect local services"})
	if !strings.Contains(canary, "SYNTHETIC_CONTROL") {
		t.Fatalf("UID network counterexample not demonstrated: %s", canary)
	}
	report["distinct_uid_alone_reaches_loopback_control"] = true
	mu.Lock()
	uidCanary = false // Selected route has no unauthenticated control endpoint.
	mu.Unlock()
	// The real CLI credential context and child process probes execute in bash.
	_ = run(s, "bash", map[string]any{"command": "python3 /opt/experiment/probe.py " + server.URL, "description": "Synthetic isolation boundary probes"})
	var checks map[string]bool
	data, err := os.ReadFile(opts.Workspace + "/probe.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &checks); err != nil {
		t.Fatal(err)
	}
	report["checks"] = checks
	for name, passed := range checks {
		if !passed {
			t.Errorf("boundary check failed: %s", name)
		}
	}
	if len(checks) < 20 {
		t.Fatal("incomplete independent probe receipt")
	}
	for _, area := range []string{"b", "c", "control", "service"} {
		value, err := os.ReadFile("/lab/" + area + "/private/secret")
		if err != nil || string(value) != "SYNTHETIC_"+strings.ToUpper(area) {
			t.Fatal("foreign original changed", area, err)
		}
	}
	if t.Failed() {
		return
	}
	other, err := Open(ctx, options("b", 10002))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeSession(other); err != nil {
			t.Error(err)
		}
	}()
	_ = run(other, "bash", map[string]any{"command": "printf before > independent.txt", "description": "Independent second space"})
	otherCLI := run(other, "bash", map[string]any{"command": "GLAB_CONFIG_DIR=/lab/b/private/glab glab config get token --host gitlab.example.invalid; LARKSUITE_CLI_CONFIG_DIR=/lab/b/private/lark-config LARKSUITE_CLI_DATA_DIR=/lab/b/private/lark-data lark-cli auth status --json", "description": "Read only this space's synthetic CLI contexts"})
	if !strings.Contains(otherCLI, "SYNTHETIC_B") || !strings.Contains(otherCLI, "cli_isolation_probe") {
		t.Fatalf("own CLI context failed: %s", otherCLI)
	}
	report["own_glab_and_lark_contexts_work"] = true
	// Actual original normal Close -> Resume, preserving cwd/state/identity.
	original := s.id
	if err := closeSession(s); err != nil {
		t.Fatal(err)
	}
	changedPath := opts
	changedPath.Workspace = "/lab/b/workspace"
	if err := CheckResumeConfig(changedPath); !errors.Is(err, ErrRecoveryUnsupported) {
		t.Fatal("changed cwd did not fail closed", err)
	}
	report["changed_cwd_resume_rejected"] = true
	s, err = Resume(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if s.id != original {
		t.Fatal("original Session identity changed")
	}
	_ = run(s, "bash", map[string]any{"command": "printf resumed > resumed.txt", "description": "Verify same cwd after normal resume"})
	if data, err := os.ReadFile(opts.Workspace + "/resumed.txt"); err != nil || string(data) != "resumed" {
		t.Fatal("resumed cwd receipt missing", err)
	}
	report["normal_resume_same_session_and_cwd"] = true

	// Cancel after an independently observed real shell side effect.
	mu.Lock()
	toolName, toolArgs, issued = "bash", map[string]any{"command": "echo $$ > cancel.pid; printf ready > cancel.ready; sleep 30; printf late > cancel.late", "description": "Cancellation with delayed side effect"}, false
	mu.Unlock()
	turnCtx, stopTurn := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := s.Run(turnCtx, "Run cancellation case."); done <- err }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(opts.Workspace + "/cancel.ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			stopTurn()
			t.Fatal("cancel tool did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stopTurn()
	if err := <-done; !errors.Is(err, context.Canceled) || errors.Is(err, ErrUncertain) {
		t.Fatal("cancellation did not settle", err)
	}
	if _, err := os.Stat(opts.Workspace + "/cancel.late"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("late cancellation effect")
	}
	pidData, err := os.ReadFile(opts.Workspace + "/cancel.pid")
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err := fmt.Sscan(string(pidData), &pid); err != nil || pid <= 1 {
		t.Fatal("invalid shell PID receipt")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatal("cancelled shell remains alive", err)
	}
	report["cancelled_shell_reaped"] = true
	_ = run(s, "bash", map[string]any{"command": "printf continued > after-cancel", "description": "Same Session after cancellation"})
	if err := closeSession(s); err != nil {
		t.Fatal(err)
	}
	// An actual killed ACP process must retain fail-closed recovery semantics.
	s, err = Resume(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.process.(*process).cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := closeSession(s); !CleanupSettled(err) {
		t.Fatal("crash cleanup not settled", err)
	}
	report["crash_cleanup_settled"] = true
	if err := CheckResume(opts.StateDir); !errors.Is(err, ErrRecoveryUnsupported) {
		t.Fatal("crashed Session did not preserve fail-closed recovery", err)
	}
	report["crash_resume_rejected"] = true
	_ = run(other, "bash", map[string]any{"command": "printf after > independent.txt", "description": "Second space survives first ACP crash"})
	if data, err := os.ReadFile("/lab/b/workspace/independent.txt"); err != nil || string(data) != "after" {
		t.Fatal("other space did not remain usable", err)
	}
	report["other_space_survives_cancel_and_crash"] = true
	if err := closeSession(other); err != nil {
		t.Fatal(err)
	}
	// A fresh state after crash is explicit, while the old evidence is retained.
	fresh := opts
	fresh.StateDir = "/lab/a/fresh-state"
	makeDir(fresh.StateDir, 10001)
	freshSession, err := Open(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	_ = run(freshSession, "bash", map[string]any{"command": "printf recovered > recovered.txt", "description": "Explicit fresh Session after crash"})
	if err := closeSession(freshSession); err != nil {
		t.Fatal(err)
	}
	report["fresh_after_crash_original_evidence_retained"] = freshSession.id != original
	if err := atomicJSON("/lab", "restart-expected.json", map[string]string{"session_id": freshSession.id}); err != nil {
		t.Fatal(err)
	}
	versions := map[string]string{}
	for _, binary := range []string{"node", "glab", "lark-cli"} {
		cmd := exec.Command(binary, "--version")
		cmd.Env = append(os.Environ(), "HOME=/lab/a/home", "GLAB_CONFIG_DIR=/lab/a/home/glab-version")
		output, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		versions[binary] = strings.TrimSpace(string(output))
	}
	report["versions"], report["status"] = versions, "passed"
}

func linuxIsolationOptions(endpoint, area string, uid int) Options {
	return Options{BinaryPath: "/usr/bin/python3", BinaryArgs: []string{"/opt/experiment/launch.py", "--outer-isolation"}, Workspace: "/lab/" + area + "/workspace", StateDir: "/lab/" + area + "/state", Instructions: "Disposable deterministic isolation experiment. Execute requested tools only.",
		Model: ModelConfig{Protocol: "openai-completions", BaseURL: endpoint + "/v1", ModelID: "isolation", APIKeyEnv: "OPENAI_API_KEY"}, Credential: &Credential{Value: "scoped-model-key"},
		Environment: []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/lab/" + area + "/home", "TMPDIR=/lab/" + area + "/tmp", "ISOLATION_PEER_IP=" + os.Getenv("ISOLATION_PEER_IP"), fmt.Sprintf("ISOLATION_UID=%d", uid)},
		Limits:      Limits{HandshakeTimeout: 60 * time.Second, TurnTimeout: 45 * time.Second},
	}
}

func acceptLinuxIsolationContainerResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer scoped-model-key" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, choice := range []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "RESTARTED"}, "finish_reason": nil}, map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}} {
			data, _ := json.Marshal(map[string]any{"id": "restart", "object": "chat.completion.chunk", "model": "isolation", "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	opts := linuxIsolationOptions(server.URL, "a", 10001)
	opts.StateDir = "/lab/a/fresh-state"
	var expected map[string]string
	data, err := os.ReadFile("/lab/restart-expected.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	s, err := Resume(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	if s.id != expected["session_id"] {
		t.Fatal("container replacement changed SessionID")
	}
	result, err := s.Run(ctx, "Verify original Session after container replacement.")
	if err != nil || !strings.Contains(result.Text, "RESTARTED") {
		t.Fatal("resumed ACP request failed", err)
	}
	if data, err := os.ReadFile(opts.Workspace + "/recovered.txt"); err != nil || string(data) != "recovered" {
		t.Fatal("persistent cwd content missing", err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON("/lab", "container-restart.json", map[string]any{"status": "passed", "same_session_id": true, "same_cwd": true, "stable_endpoint": true}); err != nil {
		t.Fatal(err)
	}
}
