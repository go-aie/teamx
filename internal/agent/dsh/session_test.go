package dsh

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestDSHProcess is a real stdio subprocess with controllable ACP faults. It
// never invokes a model, uses credentials, or counts as real DSH evidence.
func TestDSHProcess(t *testing.T) {
	if os.Getenv("TEAMX_DSH_TEST") != "1" {
		return
	}
	defer os.Exit(0)
	if os.Args[len(os.Args)-1] == "--version" {
		if os.Getenv("TEAMX_DSH_MODE") == "cli-version-stall" {
			for {
				time.Sleep(time.Hour)
			}
		}
		version := os.Getenv("TEAMX_DSH_TEST_VERSION")
		if version == "" {
			version = "test"
		}
		if os.Getenv("TEAMX_DSH_MODE") == "bad-cli-version" {
			version = "no version available"
		}
		if os.Getenv("TEAMX_DSH_MODE") == "oversized-cli-version" {
			version = strings.Repeat("x", 2048)
		}
		fmt.Println(version)
		return
	}
	if os.Getenv("TEAMX_DSH_TOOL") == "1" {
		writeFixturePID("tool.pid")
		if os.Getenv("TEAMX_DSH_TOOL_SETTLE") == "1" {
			for {
				if _, err := os.Stat("tool.release"); err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			return
		}
		time.Sleep(3 * time.Second)
		_ = os.WriteFile("late.txt", []byte("late"), 0600)
		return
	}
	writeFixturePID("process.pid")
	mode := os.Getenv("TEAMX_DSH_MODE")
	sessionID := fmt.Sprintf("fixture-%d", os.Getpid())
	model := `["teamx","fixture"]`
	if configured := os.Getenv("TEAMX_DSH_TEST_MODEL"); configured != "" {
		model = configured
	}
	var selected [2]string
	_ = json.Unmarshal([]byte(model), &selected)
	config := []any{map[string]any{"id": "model", "currentValue": model, "options": []any{map[string]any{"group": selected[0], "options": []any{map[string]string{"value": model}}}}}}
	if mode == "bad-model" {
		config = nil
	}
	encode := json.NewEncoder(os.Stdout)
	send := func(value any) {
		if err := encode.Encode(value); err != nil {
			os.Exit(2)
		}
	}
	respond := func(id json.RawMessage, result any) {
		send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	update := func(text string) {
		send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": text}}}})
	}
	var promptID json.RawMessage
	var waiting, held string
	var turns int
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 10<<20)
	for scanner.Scan() {
		var f frame
		if json.Unmarshal(scanner.Bytes(), &f) != nil {
			os.Exit(2)
		}
		if f.Method == "" {
			if waiting == "" {
				continue
			}
			if waiting == "unknown" {
				if string(f.ID) != `"reverse\u002did"` || f.Error == nil || f.Error.Code != -32601 {
					os.Exit(4)
				}
			} else if waiting == "permission" {
				if string(f.ID) != `"reverse\u002did"` {
					os.Exit(4)
				}
				update(string(f.Result))
			}
			if held != "permission-hold" {
				respond(promptID, map[string]string{"stopReason": "end_turn"})
			}
			waiting = ""
			continue
		}
		switch f.Method {
		case "initialize":
			if mode == "open-stall" {
				for {
					time.Sleep(time.Hour)
				}
			}
			var params map[string]any
			_ = json.Unmarshal(f.Params, &params)
			if caps, ok := params["clientCapabilities"].(map[string]any); !ok || len(caps) != 0 {
				os.Exit(4)
			}
			version := 1
			if mode == "bad-version" {
				version = 2
			}
			auth := []any{}
			if mode == "auth" {
				auth = append(auth, map[string]string{"id": "login"})
			}
			sessionCaps := map[string]any{"close": map[string]any{}, "resume": map[string]any{}}
			if mode == "no-resume" {
				delete(sessionCaps, "resume")
			}
			if mode == "no-close" {
				sessionCaps = nil
			}
			name, runtimeVersion := "fixture", "test"
			if mode == "no-agent-version" {
				runtimeVersion = ""
			}
			if mode == "changed-agent-name" {
				name = "other-runtime"
			}
			respond(f.ID, map[string]any{"protocolVersion": version, "authMethods": auth, "agentCapabilities": map[string]any{"sessionCapabilities": sessionCaps, "promptCapabilities": map[string]bool{"image": os.Getenv("TEAMX_DSH_IMAGE") == "1"}}, "agentInfo": map[string]string{"name": name, "version": runtimeVersion}})
		case "session/new":
			_ = os.WriteFile("session-new.called", []byte(sessionID), 0600)
			respond(f.ID, map[string]any{"sessionId": sessionID, "configOptions": config})
		case "session/resume":
			_ = os.WriteFile("session-resume.called", []byte(sessionID), 0600)
			var params struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(f.Params, &params)
			if mode == "resume-error" {
				send(map[string]any{"jsonrpc": "2.0", "id": f.ID, "error": map[string]any{"code": -32603, "message": "resume failed"}})
				continue
			}
			sessionID = params.SessionID
			respond(f.ID, map[string]any{"configOptions": config})
		case "session/set_config_option":
			if mode == "reference-failure" {
				_ = os.Mkdir(filepath.Join(os.Getenv("DSH_HOME"), "..", "backend.json"), 0700)
			}
			if mode == "unconfirmed" {
				config = nil
			}
			respond(f.ID, map[string]any{"configOptions": config})
		case "session/prompt":
			_ = os.WriteFile("prompt.json", f.Params, 0600)
			turns++
			var params struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(f.Params, &params)
			text := params.Prompt[0].Text
			promptID = append(json.RawMessage(nil), f.ID...)
			switch text {
			case "live-activity":
				send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]string{"type": "text", "text": "checking files"}}}})
				update("working")
				send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": map[string]any{"sessionUpdate": "tool_call", "toolCallId": "live-tool", "title": "shell", "status": "in_progress", "rawInput": map[string]string{"command": "go test ./..."}}}})
			case "remember":
				_ = os.WriteFile(filepath.Join(os.Getenv("DSH_HOME"), "memory.txt"), []byte("private-memory"), 0600)
				respond(f.ID, map[string]string{"stopReason": "end_turn"})
			case "recall":
				memory, _ := os.ReadFile(filepath.Join(os.Getenv("DSH_HOME"), "memory.txt"))
				update(string(memory) + "|" + os.Getenv("TEAMX_SERVICE_EPOCH") + "|" + sessionID)
				respond(f.ID, map[string]string{"stopReason": "end_turn"})
			case "hold", "ignore-cancel", "detached", "cancel-settle-tool", "cancel-live-tool":
				held = text
				if text == "detached" || text == "cancel-settle-tool" || text == "cancel-live-tool" {
					executable, _ := os.Executable()
					tool := exec.Command(executable, "-test.run=^TestDSHProcess$")
					tool.Env = append(os.Environ(), "TEAMX_DSH_TOOL=1")
					if text == "cancel-settle-tool" {
						tool.Env = append(tool.Env, "TEAMX_DSH_TOOL_SETTLE=1")
					}
					ownProcess(tool)
					if err := tool.Start(); err != nil {
						os.Exit(4)
					}
					go func() { _ = tool.Wait() }()
				}
				update("started")
			case "permission", "permission-hold", "unknown":
				waiting = text
				if text == "permission-hold" {
					held, waiting = text, "permission"
				}
				method := "session/request_permission"
				if text == "unknown" {
					method = "fs/read_text_file"
				}
				send(frame{JSONRPC: "2.0", ID: json.RawMessage(`"reverse\u002did"`), Method: method, Params: mustJSON(PermissionRequest{SessionID: sessionID, Options: []PermissionOption{{OptionID: "allow", Kind: "allow_once"}, {OptionID: "deny", Kind: "reject_once"}}})})
			case "eof":
				os.Exit(3)
			case "bad":
				_, _ = fmt.Fprintln(os.Stdout, "invalid json")
			case "oversize":
				_, _ = fmt.Fprintln(os.Stdout, strings.Repeat("x", 9000))
			case "rpc-error":
				send(map[string]any{"jsonrpc": "2.0", "id": f.ID, "error": map[string]any{"code": -32603, "message": os.Getenv("TEAMX_TEST_KEY")}})
			case "rate-limited":
				send(map[string]any{"jsonrpc": "2.0", "id": f.ID, "error": map[string]any{
					"code": -32603, "message": "Internal error", "data": "turn failed: 429 tpm_exceeded " + os.Getenv("TEAMX_TEST_KEY")}})
			case "model-not-found":
				send(map[string]any{"jsonrpc": "2.0", "id": f.ID, "error": map[string]any{
					"code": -32603, "message": "Internal error", "data": "turn failed: 404 model_not_found " + os.Getenv("TEAMX_TEST_KEY")}})
			case "bad-stop":
				respond(f.ID, map[string]string{"stopReason": "unknown"})
			case "max_tokens", "max_turn_requests", "refusal", "cancelled":
				respond(f.ID, map[string]string{"stopReason": text})
			case "block-next":
				respond(f.ID, map[string]string{"stopReason": "end_turn"})
				for {
					time.Sleep(time.Hour)
				}
			case "output":
				for range 80 {
					update(strings.Repeat("字", 100))
					send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": map[string]string{"sessionUpdate": "tool_call", "toolCallId": "tool", "status": "completed"}}})
				}
				_, _ = fmt.Fprint(os.Stderr, strings.Repeat("diagnostic", 10000))
				respond(f.ID, map[string]string{"stopReason": "end_turn"})
			case "isolation":
				role, _ := os.ReadFile(filepath.Join(os.Getenv("DSH_HOME"), "AGENTS.md"))
				update(string(role) + "|" + os.Getenv("TOOL_ID") + "|" + os.Getenv("DSH_AGENTS_HOME") + "|" + os.Getenv("PATH"))
				respond(f.ID, map[string]string{"stopReason": "end_turn"})
			default:
				update(fmt.Sprintf("%s turn %d", text, turns))
				respond(f.ID, map[string]string{"stopReason": "end_turn"})
			}
		case "session/cancel":
			if held == "ignore-cancel" || held == "detached" {
				continue
			}
			time.Sleep(80 * time.Millisecond)
			update(" late-update")
			reason := "cancelled"
			if mode == "bad-cancel" {
				reason = "invalid"
			}
			respond(promptID, map[string]string{"stopReason": reason})
			held, waiting = "", ""
		case "session/close":
			if mode == "stuck-close" {
				continue
			}
			respond(f.ID, map[string]any{})
		}
	}
	switch mode {
	case "slow-exit":
		time.Sleep(1500 * time.Millisecond)
	case "stuck-exit":
		for {
			time.Sleep(time.Hour)
		}
	case "abnormal-exit":
		os.Exit(3)
	}
}

func mustJSON(v any) json.RawMessage { data, _ := json.Marshal(v); return data }

func writeFixturePID(name string) {
	if os.WriteFile(name+".tmp", []byte(strconv.Itoa(os.Getpid())), 0600) != nil {
		os.Exit(4)
	}
	if os.Rename(name+".tmp", name) != nil {
		os.Exit(4)
	}
}

func fixtureOptions(t *testing.T) Options {
	t.Helper()
	t.Setenv("TEAMX_TEST_KEY", "secret-fixture-key")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	return Options{BinaryPath: executable, BinaryArgs: []string{"-test.run=^TestDSHProcess$", "--"}, Artifact: Artifact{Source: "offline test subprocess", Version: "test"}, Workspace: workspace, StateDir: filepath.Join(dir, "state"), Instructions: "role-A", Model: ModelConfig{Protocol: "openai-completions", BaseURL: "https://example.invalid/v1", ModelID: "fixture", APIKeyEnv: "TEAMX_TEST_KEY"}, ToolEnv: map[string]string{"TEAMX_DSH_TEST": "1", "GORACE": "atexit_sleep_ms=0"}, Limits: Limits{HandshakeTimeout: 2 * time.Second, CancelTimeout: 200 * time.Millisecond, WriteTimeout: 100 * time.Millisecond, CleanupTimeout: 3 * time.Second, FrameBytes: 8192}, inspect: fixtureObserver(workspace)}
}

// This observer is restricted to the PIDs explicitly created by our fixture.
// It exercises real signalling/reaping, but does not prove /bin/ps works on
// the host. Production always uses the platform process table instead.
func fixtureObserver(workspace string) func() (map[int]processIdentity, error) {
	return func() (map[int]processIdentity, error) {
		table := map[int]processIdentity{}
		root := 0
		for _, name := range []string{"process.pid", "tool.pid"} {
			data, err := os.ReadFile(filepath.Join(workspace, name))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil {
				return nil, err
			}
			if name == "process.pid" {
				root = pid
			}
			if err := signalPID(pid, 0); errors.Is(err, syscall.ESRCH) {
				continue
			} else if err != nil {
				return nil, err
			}
			parent := root
			if name == "process.pid" {
				parent = os.Getpid()
			}
			table[pid] = processIdentity{pid: pid, parent: parent, group: pid, started: "explicit-test-child-" + string(data)}
		}
		return table, nil
	}
}

func openFixture(t *testing.T, opts Options) *Session {
	t.Helper()
	s, err := Open(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			// Fault tests already assert the Run failure. A disabled session
			// must now report unavailable recovery when cleanup finishes too.
			s.mu.Lock()
			disabled := s.disabled != nil
			s.mu.Unlock()
			if !disabled || !errors.Is(err, ErrRecoveryUnsupported) || errors.Is(err, ErrCleanupPending) {
				t.Errorf("cleanup: %v", err)
			}
		}
	})
	return s
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSequentialTurnsAndState(t *testing.T) {
	opts := fixtureOptions(t)
	s := openFixture(t, opts)
	for i := 1; i <= 2; i++ {
		result, err := s.Run(context.Background(), "hello")
		if err != nil || result.StopReason != "end_turn" || result.Text != fmt.Sprintf("hello turn %d", i) {
			t.Fatalf("%+v %v", result, err)
		}
	}
	if _, err := Open(context.Background(), opts); !errors.Is(err, ErrLocked) {
		t.Fatalf("lock: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "again"); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(opts.StateDir, "backend.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ref reference
	if json.Unmarshal(data, &ref) != nil || ref.Version != 1 || ref.SessionID != s.id || ref.Artifact.Version != "test" || ref.BinarySHA256 == "" {
		t.Fatalf("reference: %s", data)
	}
	if strings.Contains(string(data), os.Getenv("TEAMX_TEST_KEY")) {
		t.Fatal("credential in reference")
	}
	if _, err := Open(context.Background(), opts); !errors.Is(err, ErrRecoveryUnsupported) {
		t.Fatal(err)
	}
}

func TestCancellationRetainsSlotAndAllowsReuse(t *testing.T) {
	s := openFixture(t, fixtureOptions(t))
	ctx, cancel := context.WithCancel(context.Background())
	type outcome struct {
		result TurnResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() { result, err := s.Run(ctx, "hold"); done <- outcome{result, err} }()
	waitFor(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.active != nil && s.active.result.Text == "started"
	})
	cancel()
	if _, err := s.Run(context.Background(), "overlap"); !errors.Is(err, ErrBusy) {
		t.Fatalf("cancel released slot: %v", err)
	}
	got := <-done
	if !errors.Is(got.err, context.Canceled) || got.result.StopReason != "cancelled" || !strings.Contains(got.result.Text, "late-update") {
		t.Fatalf("%+v", got)
	}
	if _, err := s.Run(context.Background(), "after"); err != nil {
		t.Fatal(err)
	}
}

func TestStopReasonsAndFaults(t *testing.T) {
	for _, input := range []string{"max_tokens", "max_turn_requests", "refusal", "cancelled", "eof", "bad", "oversize", "rpc-error", "bad-stop"} {
		t.Run(input, func(t *testing.T) {
			s := openFixture(t, fixtureOptions(t))
			result, err := s.Run(context.Background(), input)
			if err == nil {
				t.Fatal("fault reported success")
			}
			if strings.Contains(err.Error(), "secret-fixture-key") {
				t.Fatal("key in error")
			}
			switch input {
			case "max_tokens", "max_turn_requests", "refusal", "cancelled":
				if result.StopReason != input {
					t.Fatalf("lost stop reason: %+v", result)
				}
				if _, err := s.Run(context.Background(), "next"); err != nil {
					t.Fatal(err)
				}
			default:
				if !errors.Is(err, ErrUncertain) {
					t.Fatalf("not uncertain: %v", err)
				}
				if _, err := s.Run(context.Background(), "next"); !errors.Is(err, ErrUnavailable) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestOpenFailuresCleanResources(t *testing.T) {
	for _, mode := range []string{"bad-version", "auth", "bad-model", "unconfirmed", "reference-failure", "open-stall", "no-agent-version"} {
		t.Run(mode, func(t *testing.T) {
			opts := fixtureOptions(t)
			opts.ToolEnv["TEAMX_DSH_MODE"] = mode
			if mode == "open-stall" {
				opts.Limits.HandshakeTimeout = 80 * time.Millisecond
			}
			if s, err := Open(context.Background(), opts); err == nil || s != nil {
				t.Fatalf("Open: %v %v", s, err)
			}
			lock, err := lockDirectory(opts.StateDir)
			if err != nil {
				t.Fatalf("cleanup did not release lock: %v", err)
			}
			_ = lock.Close()
			if _, err := Open(context.Background(), opts); !errors.Is(err, ErrRecoveryUnsupported) {
				t.Fatalf("startup state overwritten: %v", err)
			}
		})
	}
}

func TestPermissionPolicyAndUnknownRequests(t *testing.T) {
	for _, policy := range []string{"nil", "allow", "invalid", "mutate", "error", "timeout", "unknown"} {
		t.Run(policy, func(t *testing.T) {
			opts := fixtureOptions(t)
			opts.Limits.PermissionTimeout = 30 * time.Millisecond
			if policy != "nil" && policy != "unknown" {
				opts.Permission = func(ctx context.Context, req PermissionRequest) (string, error) {
					if len(req.Options) != 2 {
						return "", errors.New("options missing")
					}
					switch policy {
					case "allow":
						return "allow", nil
					case "invalid":
						return "invented", nil
					case "mutate":
						req.Options[0].OptionID = "invented"
						return "invented", nil
					case "error":
						return "allow", errors.New("denied")
					default:
						<-ctx.Done()
						return "allow", nil
					}
				}
			}
			s := openFixture(t, opts)
			input := "permission"
			if policy == "unknown" {
				input = "unknown"
			}
			result, err := s.Run(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if policy == "allow" {
				if !strings.Contains(result.Text, `"optionId":"allow"`) {
					t.Fatal(result.Text)
				}
			} else if policy != "unknown" && !strings.Contains(result.Text, `"outcome":"cancelled"`) {
				t.Fatal(result.Text)
			}
		})
	}
}

func TestUncooperativePermissionRetainsCleanupLock(t *testing.T) {
	opts := fixtureOptions(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	opts.Permission = func(context.Context, PermissionRequest) (string, error) {
		once.Do(func() { close(entered) })
		<-release
		return "allow", nil
	}
	opts.Limits.PermissionTimeout = 20 * time.Millisecond
	s := openFixture(t, opts)
	done := make(chan error, 1)
	go func() { _, err := s.Run(context.Background(), "permission"); done <- err }()
	<-entered
	if err := <-done; !errors.Is(err, ErrUncertain) {
		t.Fatalf("hung policy: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrCleanupPending) {
		t.Fatalf("premature close: %v", err)
	}
	if _, err := Open(context.Background(), opts); !errors.Is(err, ErrLocked) {
		t.Fatalf("premature unlock: %v", err)
	}
	close(release)
	if err := s.Close(context.Background()); !errors.Is(err, ErrRecoveryUnsupported) || errors.Is(err, ErrCleanupPending) {
		t.Fatal("permission failure disappeared during cleanup", err)
	}
}

func TestCancelTimeoutReapsDetachedTool(t *testing.T) {
	opts := fixtureOptions(t)
	s := openFixture(t, opts)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Run(ctx, "detached"); done <- err }()
	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(opts.Workspace, "tool.pid")); return err == nil })
	cancel()
	if err := <-done; !errors.Is(err, ErrUncertain) {
		t.Fatalf("cancel failure: %v", err)
	}
	if err := s.Close(context.Background()); !errors.Is(err, ErrRecoveryUnsupported) || errors.Is(err, ErrCleanupPending) {
		t.Fatal("cancel failure disappeared during cleanup", err)
	}
	data, err := os.ReadFile(filepath.Join(opts.Workspace, "tool.pid"))
	if err != nil {
		t.Fatal(err)
	}
	if table, err := opts.inspect(); err != nil {
		t.Fatal(err)
	} else {
		for pid := range table {
			if fmt.Sprint(pid) == strings.TrimSpace(string(data)) {
				t.Fatal("detached tool survived")
			}
		}
	}
	if _, err := os.Stat(filepath.Join(opts.Workspace, "late.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("tool reached late effect")
	}
}

func TestTurnTimeoutCancelsAndSettlesPrompt(t *testing.T) {
	opts := fixtureOptions(t)
	opts.Limits.TurnTimeout = 100 * time.Millisecond
	s := openFixture(t, opts)
	result, err := s.Run(context.Background(), "hold")
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrCancelled) || result.StopReason != "cancelled" {
		t.Fatalf("turn deadline did not settle via cancellation: %+v %v", result, err)
	}
	if _, active := s.Activity(); active {
		t.Fatal("timed out prompt still active")
	}
	if _, err := s.Run(context.Background(), "after-timeout"); err != nil {
		t.Fatal("settled timeout left component unavailable", err)
	}
}

func TestBlockedWriteDisablesSession(t *testing.T) {
	opts := fixtureOptions(t)
	opts.Limits.FrameBytes = 1 << 20
	opts.Limits.TurnTimeout = 500 * time.Millisecond
	s := openFixture(t, opts)
	if _, err := s.Run(context.Background(), "block-next"); err != nil {
		t.Fatal(err)
	}
	_, err := s.Run(context.Background(), strings.Repeat("x", 512<<10))
	if !errors.Is(err, ErrUncertain) {
		t.Fatalf("blocked/partial write: %v", err)
	}
}

func TestBoundsAndIsolation(t *testing.T) {
	opts := fixtureOptions(t)
	opts.Limits.TextBytes = 100
	s := openFixture(t, opts)
	result, err := s.Run(context.Background(), "output")
	if err != nil || len(result.Text) > 100 || !result.TextTruncated || !result.ToolsTruncated || len(result.Tools) != 64 {
		t.Fatalf("bounds: %+v %v", result, err)
	}
	waitFor(t, func() bool { _, truncated := s.Diagnostics(); return truncated })
	if text, _ := s.Diagnostics(); len(text) > 64<<10 {
		t.Fatal("stderr unbounded")
	}
	if _, err := s.Run(context.Background(), strings.Repeat("x", (1<<20)+1)); err == nil {
		t.Fatal("oversized input accepted")
	}
	for _, role := range []string{"role-one", "role-two"} {
		opts := fixtureOptions(t)
		opts.Instructions = role
		opts.ToolEnv["TOOL_ID"], opts.ToolEnv["PATH"] = role, "/custom/"+role
		session := openFixture(t, opts)
		result, err := session.Run(context.Background(), "isolation")
		if err != nil || !strings.Contains(result.Text, role+"|"+role) || !strings.Contains(result.Text, "/custom/"+role+string(os.PathListSeparator)) || !strings.Contains(result.Text, session.opts.StateDir) {
			t.Fatalf("isolation: %+v %v", result, err)
		}
		roleData, _ := os.ReadFile(filepath.Join(session.opts.StateDir, "home", "AGENTS.md"))
		if string(roleData) != role {
			t.Fatal("role changed")
		}
	}
}

func TestActivityAvailableBeforePromptTerminates(t *testing.T) {
	s := openFixture(t, fixtureOptions(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Run(ctx, "live-activity"); done <- err }()
	waitFor(t, func() bool {
		result, active := s.Activity()
		return active && result.Text == "working" && result.Thought == "checking files" && len(result.Tools) == 1 && result.Tools[0].Input != ""
	})
	select {
	case err := <-done:
		t.Fatalf("prompt ended before observation: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("observing activity blocked cancellation")
	}
	if _, active := s.Activity(); active {
		t.Fatal("settled prompt still active")
	}
}

func TestCloseDuringTurnAndWithoutCapability(t *testing.T) {
	for _, mode := range []string{"", "no-close"} {
		t.Run(mode, func(t *testing.T) {
			opts := fixtureOptions(t)
			opts.ToolEnv["TEAMX_DSH_MODE"] = mode
			s := openFixture(t, opts)
			done := make(chan error, 1)
			go func() { _, err := s.Run(context.Background(), "hold"); done <- err }()
			waitFor(t, func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				return s.active != nil && s.active.result.Text == "started"
			})
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			defer cancel()
			if err := s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("close wait: %v", err)
			}
			if err := <-done; !errors.Is(err, ErrCancelled) {
				t.Fatal(err)
			}
			if err := s.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := CheckResume(opts.StateDir); err != nil {
				t.Fatalf("settled cancellation lost normal shutdown evidence: %v", err)
			}
		})
	}
}

func TestConfigurationValidation(t *testing.T) {
	for _, mutation := range []func(*Options){
		func(o *Options) { o.BinaryPath = "relative" },
		func(o *Options) { o.Workspace = filepath.Join(o.Workspace, "missing") },
		func(o *Options) { o.Model.BaseURL = "https://user:secret@example.com" },
		func(o *Options) { o.Model.Protocol = "wrong" },
		func(o *Options) { o.Model.ReasoningEffort = "wrong" },
		func(o *Options) { o.Model.Protocol = "deepseek-messages"; o.Model.ReasoningEffort = "medium" },
		func(o *Options) { o.Model.ContextWindow = -1 },
		func(o *Options) { o.Model.MaxTokens = -1 },
		func(o *Options) { o.Model.MaxTokens = 65537 },
		func(o *Options) { o.Model.ContextWindow = 1024 },
		func(o *Options) { o.Model.Protocol = "deepseek-messages"; o.Model.ContextWindow = 65536 },
		func(o *Options) { o.Model.APIKeyEnv = "MISSING_TEAMX_CREDENTIAL" },
		func(o *Options) { o.ToolEnv["DSH_HOME"] = "/tmp/wrong" },
		func(o *Options) { o.ToolEnv["TEAMX_TEST_KEY"] = "wrong" },
		func(o *Options) { o.Limits.FrameBytes = 9 << 20 },
	} {
		opts := fixtureOptions(t)
		mutation(&opts)
		if s, err := Open(context.Background(), opts); err == nil || s != nil {
			t.Fatalf("bad config accepted: %v", s)
		}
		if _, err := os.Stat(opts.StateDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid config created state")
		}
	}
}
