//go:build e2e && linux

package srt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-aie/teamx/agent"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

type tracedExecution struct {
	agent.ExecutionFactory
	mu          sync.Mutex
	diagnostics []string
}
type tracedHandle struct {
	agent.Execution
	owner *tracedExecution
}

func executionErrorDetails(err error) any {
	if err == nil {
		return nil
	}
	data, _ := json.Marshal(err)
	if len(data) > 2 {
		return json.RawMessage(data)
	}
	if e, ok := err.(interface{ Unwrap() []error }); ok {
		var children []any
		for _, child := range e.Unwrap() {
			children = append(children, executionErrorDetails(child))
		}
		return children
	}
	if e, ok := err.(interface{ Unwrap() error }); ok && e.Unwrap() != nil {
		return executionErrorDetails(e.Unwrap())
	}
	return map[string]any{"error": err.Error(), "wire": json.RawMessage(data)}
}

func (f *tracedExecution) Start(ctx context.Context, r agent.ExecutionRequest, failed func(error)) (agent.Execution, error) {
	p, err := f.ExecutionFactory.Start(ctx, r, failed)
	if p == nil {
		return nil, err
	}
	return &tracedHandle{Execution: p, owner: f}, err
}
func (p *tracedHandle) Stop(grace time.Duration) {
	p.Execution.Stop(grace)
	text, _ := p.Execution.Diagnostic()
	p.owner.mu.Lock()
	p.owner.diagnostics = append(p.owner.diagnostics, text)
	p.owner.mu.Unlock()
}

// Signal only a verified process from this test's exact private DSH home.
func stopFixtureProcess(t *testing.T, base, state, role string) {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		prefix := "/proc/" + entry.Name()
		exe, err := os.Readlink(prefix + "/exe")
		if err != nil {
			process.Release()
			continue
		}
		node := exe == base+"/node/bin/node"
		if !node && exe != base+"/tools/usr/bin/socat" {
			process.Release()
			continue
		}
		env, err := os.ReadFile(prefix + "/environ")
		if err != nil || !bytes.Contains(env, []byte("DSH_HOME="+state+"/home\x00")) {
			process.Release()
			continue
		}
		argv, _ := os.ReadFile(prefix + "/cmdline")
		match := role == "relay" && !node || role == "wrapper" && node && bytes.Contains(argv, []byte("--input-type=module\x00-e\x00")) || role == "dsh" && node && bytes.Contains(argv, []byte("/dsh/lib/bin.js\x00"))
		if match {
			err = process.Signal(syscall.SIGKILL)
			process.Release()
			if err != nil {
				t.Fatal(err)
			}
			return
		}
		process.Release()
	}
	t.Fatal("owned fixture process unavailable", role)
}

func TestLinuxDSHExecution(t *testing.T) {
	base := os.Getenv("TEAMX_EXECUTION_RUNTIME_ROOT")
	if base == "" {
		t.Skip("explicit disposable Linux runtime root required")
	}
	if !strings.HasPrefix(base, "/root/luop/teamx/agent-runtime-") || os.Geteuid() == 0 {
		t.Fatal("approved directory and non-root core required")
	}
	root := t.TempDir()
	report := map[string]any{"status": "failed", "real_dsh": true, "real_model": false, "uid": os.Geteuid(), "root": root}
	defer func() {
		data, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(os.Getenv("TEAMX_EXECUTION_REPORT"), data, 0600); err != nil {
			t.Error(err)
		}
	}()
	for _, name := range []string{"core", "a/workspace", "a/state", "b/private", "c/private"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"b/workspace", "b/state"} {
		if err := os.MkdirAll(root+"/"+name, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"core", "b/private", "c/private"} {
		if err := os.WriteFile(filepath.Join(root, name, "secret"), []byte("SYNTHETIC_PRIVATE"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	var tool string
	var arguments map[string]any
	var observed string
	issued := false
	requests := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-model-key" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/transport" {
			fmt.Fprint(w, "PROXY_OK")
			return
		}
		var input struct {
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
			Tools []struct{ Function struct{ Name string } }
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		requests++
		delta := map[string]any{"role": "assistant", "content": "SRT_DONE"}
		finish := "stop"
		if tool != "" && !issued {
			found := false
			for _, row := range input.Tools {
				found = found || row.Function.Name == tool
			}
			if !found {
				w.WriteHeader(400)
				return
			}
			args, _ := json.Marshal(arguments)
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("srt-%d", requests), "type": "function", "function": map[string]any{"name": tool, "arguments": string(args)}}}}
			issued = true
			finish = "tool_calls"
		} else {
			for _, message := range input.Messages {
				if message.Role == "tool" {
					observed = string(message.Content)
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, choice := range []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}, map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
			chunk, _ := json.Marshal(map[string]any{"id": "srt", "object": "chat.completion.chunk", "model": "fixture", "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer model.Close()
	pin := func(path string) Artifact {
		hash, err := DigestPath(path)
		if err != nil {
			t.Fatal(err)
		}
		return Artifact{Path: path, SHA256: hash}
	}
	config := Config{Node: pin(base + "/node/bin/node"), Bwrap: pin(base + "/tools/usr/bin/bwrap"), Socat: pin(base + "/tools/usr/bin/socat"), Ripgrep: pin(base + "/tools/usr/bin/rg"), Runtime: pin(base + "/srt"), ReadOnly: []Artifact{pin(base + "/dsh")}, Endpoints: []string{model.URL}, PrivateRoots: []string{root + "/core", root + "/b/private", root + "/c/private"}}
	factory := New(config)
	trace := &tracedExecution{ExecutionFactory: factory.native}
	factory.native = trace
	defer func() { trace.mu.Lock(); defer trace.mu.Unlock(); report["execution_stderr"] = trace.diagnostics }()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	if err := factory.Check(ctx); err != nil {
		t.Fatal("prerequisite", err)
	}
	report["prerequisite"] = true
	probeState := root + "/node-probe"
	for _, name := range []string{"home", "agents"} {
		if err := os.MkdirAll(probeState+"/"+name, 0700); err != nil {
			t.Fatal(err)
		}
	}
	target, _ := factory.RouteEndpoint(model.URL + "/transport")
	js := `const p=await import(process.argv[2]);const dispose=await p.installProxyFromEnvironment({get:n=>process.env[n]===undefined?undefined:{value:process.env[n]}},()=>{});const out={proxy_set:Boolean(process.env.HTTP_PROXY),proxied:p.proxyRouteFor(new URL(process.argv[1])).proxied};try{const r=await fetch(process.argv[1],{headers:{Authorization:'Bearer synthetic-model-key'},signal:AbortSignal.timeout(5000)});out.status=r.status;out.body=await r.text()}catch(e){out.error=e.message;out.cause=e.cause?.message;out.code=e.cause?.code}console.log(JSON.stringify(out));await dispose();`
	probe, err := factory.ForSpace("user", "a").Start(ctx, agent.ExecutionRequest{BinaryPath: config.Node.Path, Args: []string{"--input-type=module", "-e", js, target, base + "/dsh/node_modules/@deepseek-ai/dsh-http-proxy/lib/index.js"}, Workspace: root + "/a/workspace", StateDir: probeState, Binding: agent.ExecutionBinding{UserID: "user", SpaceID: "a", Probe: true}, Endpoints: []string{model.URL}}, func(error) {})
	if probe == nil {
		t.Fatal("node proxy probe", err)
	}
	probe.Stdin().Close()
	text, readErr := io.ReadAll(probe.Stdout())
	probe.Stop(time.Second)
	report["proxy_probe"] = string(text)
	if readErr != nil {
		report["proxy_probe_read_error"] = readErr.Error()
	}
	if err := probe.Result(); err != nil {
		report["proxy_probe_exit"] = err.Error()
	}
	opts := dsh.Options{Execution: factory.ForSpace("user", "a"), ExecutionBinding: agent.ExecutionBinding{UserID: "user", SpaceID: "a", Generation: "state", MemberIdentity: agent.MemberIdentity{RoomIdentity: agent.RoomIdentity{RoomID: "room", RoomInstance: "instance"}, AgentID: "agent", MembershipID: "member"}}, BinaryPath: config.Node.Path, BinaryArgs: []string{base + "/dsh/node_modules/@deepseek-ai/dsh/lib/bin.js"}, Artifact: dsh.Artifact{Source: "npm:@deepseek-ai/dsh", Version: "0.2.2-rc.1"}, Workspace: root + "/a/workspace", StateDir: root + "/a/state", Instructions: "Run the requested synthetic test only.", Model: dsh.ModelConfig{Protocol: "openai-completions", BaseURL: model.URL + "/v1", ModelID: "fixture", APIKeyEnv: "OPENAI_API_KEY"}, Credential: &dsh.Credential{Ref: "synthetic", Value: "synthetic-model-key"}, Environment: []string{}, Permission: func(_ context.Context, r dsh.PermissionRequest) (string, error) {
		for _, o := range r.Options {
			if o.Kind == "allow_once" {
				return o.OptionID, nil
			}
		}
		return "", fmt.Errorf("allow_once required")
	}, Limits: dsh.Limits{HandshakeTimeout: 45 * time.Second, TurnTimeout: 40 * time.Second}}
	s, err := dsh.Open(ctx, opts)
	if s != nil {
		defer func() {
			closeCtx, stop := context.WithTimeout(context.Background(), 25*time.Second)
			defer stop()
			closeErr := s.Close(closeCtx)
			report["cleanup_settled"] = dsh.CleanupSettled(closeErr)
			if closeErr != nil {
				report["cleanup_error"] = closeErr.Error()
				t.Error(closeErr)
			}
		}()
	}
	if err != nil {
		t.Fatal("open", err)
	}
	defer func() { text, _ := s.Diagnostics(); report["stderr"] = text }()
	if result, err := s.Run(ctx, "Reply SRT_DONE without tools."); err != nil || !strings.Contains(result.Text, "SRT_DONE") {
		if err != nil {
			report["error_details"] = executionErrorDetails(err)
		}
		mu.Lock()
		report["model_requests"] = requests
		mu.Unlock()
		t.Fatal("ordinary model round", result, err)
	}
	report["model_proxy_round"] = true
	run := func(name string, args map[string]any) string {
		t.Helper()
		mu.Lock()
		tool, arguments, issued, observed = name, args, false, ""
		mu.Unlock()
		if _, err := s.Run(ctx, "Execute the configured synthetic tool."); err != nil {
			t.Fatal(name, err)
		}
		mu.Lock()
		defer mu.Unlock()
		return observed
	}
	run("write", map[string]any{"file_path": opts.Workspace + "/own.txt", "content": "OWN"})
	if data, err := os.ReadFile(opts.Workspace + "/own.txt"); err != nil || string(data) != "OWN" {
		t.Fatal("own file write", err)
	}
	report["own_write"] = true
	for _, area := range []string{"core", "b/private", "c/private"} {
		value := run("read", map[string]any{"file_path": root + "/" + area + "/secret"})
		if strings.Contains(value, "SYNTHETIC_PRIVATE") || (!strings.Contains(value, "EACCES") && !strings.Contains(value, "ENOENT") && !strings.Contains(strings.ToLower(value), "permission") && !strings.Contains(strings.ToLower(value), "not found")) {
			t.Fatal("private read boundary", area, value)
		}
	}
	report["private_reads_denied"] = true
	result := run("bash", map[string]any{"command": "printf ready > shell.ready", "description": "Check the selected inner sandbox"})
	report["shell_result"] = result
	if data, err := os.ReadFile(opts.Workspace + "/shell.ready"); err != nil || string(data) != "ready" {
		t.Fatal("inner sandbox shell unavailable", err)
	}
	report["shell"] = true
	value := run("bash", map[string]any{"command": "if cat " + root + "/b/private/secret; then printf BREACH; else printf SHELL_DENIED; fi", "description": "Verify outer read boundary for the real Shell"})
	if !strings.Contains(value, "SHELL_DENIED") || strings.Contains(value, "SYNTHETIC_PRIVATE") || strings.Contains(value, "BREACH") {
		t.Fatal("shell crossed private boundary", value)
	}
	report["shell_private_read_denied"] = true
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dsh.CheckResumeConfig(opts); err != nil {
		t.Fatal("resume config", err)
	}
	resumed, err := dsh.Resume(ctx, opts)
	if err != nil {
		t.Fatal("resume", err)
	}
	defer resumed.Close(context.Background())
	report["resume"] = true
	bopts := opts
	bopts.Execution = factory.ForSpace("user", "b")
	bopts.ExecutionBinding.SpaceID = "b"
	bopts.ExecutionBinding.AgentID = "agent-b"
	bopts.ExecutionBinding.MembershipID = "member-b"
	bopts.StateDir = root + "/b/state"
	bopts.Workspace = root + "/b/workspace"
	parallel, err := dsh.Open(ctx, bopts)
	if err != nil {
		t.Fatal("parallel space open", err)
	}
	defer parallel.Close(context.Background())
	mu.Lock()
	tool = "bash"
	arguments = map[string]any{"command": "printf armed > cancel.ready; sleep 4; printf late > cancel.late", "description": "Synthetic cancellation test"}
	issued = false
	mu.Unlock()
	turnCtx, stopTurn := context.WithCancel(ctx)
	turnDone := make(chan error, 1)
	go func() { _, err := resumed.Run(turnCtx, "Run the configured cancellation probe."); turnDone <- err }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, err := os.Stat(opts.Workspace + "/cancel.ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			stopTurn()
			t.Fatal("real Shell was not armed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stopTurn()
	select {
	case err := <-turnDone:
		report["cancel_error"] = fmt.Sprint(err)
	case <-time.After(20 * time.Second):
		t.Fatal("cancellation did not return")
	}
	if err := resumed.Close(ctx); !dsh.CleanupSettled(err) {
		t.Fatal("cancellation lost ownership", err)
	}
	mu.Lock()
	tool = ""
	issued = false
	mu.Unlock()
	if result, err := parallel.Run(ctx, "Reply SRT_DONE without tools."); err != nil || !strings.Contains(result.Text, "SRT_DONE") {
		t.Fatal("A cancellation broke B", err)
	}
	stopFixtureProcess(t, base, bopts.StateDir, "dsh")
	time.Sleep(300 * time.Millisecond)
	if _, err := parallel.Run(ctx, "This failed execution must not accept work."); err == nil {
		t.Fatal("DSH exit accepted another turn")
	}
	if err := parallel.Close(ctx); !dsh.CleanupSettled(err) {
		t.Fatal("DSH exit cleanup pending", err)
	}
	if err := dsh.CheckResumeConfig(bopts); err == nil {
		t.Fatal("abnormal DSH exit allowed resume")
	}
	report["dsh_fault_settled_and_resume_denied"] = true
	for _, role := range []string{"wrapper", "relay"} {
		faultOpts := opts
		faultOpts.Execution = factory.ForSpace("user", role)
		faultOpts.ExecutionBinding.SpaceID = role
		faultOpts.ExecutionBinding.AgentID = role
		faultOpts.ExecutionBinding.MembershipID = role
		faultOpts.Workspace = root + "/" + role + "/workspace"
		faultOpts.StateDir = root + "/" + role + "/state"
		if err := os.MkdirAll(faultOpts.Workspace, 0700); err != nil {
			t.Fatal(err)
		}
		fault, err := dsh.Open(ctx, faultOpts)
		if err != nil {
			t.Fatal("fault fixture", role, err)
		}
		defer fault.Close(context.Background())
		stopFixtureProcess(t, base, faultOpts.StateDir, role)
		time.Sleep(300 * time.Millisecond)
		if _, err := fault.Run(ctx, "This failed execution must not accept work."); err == nil {
			t.Fatal("infrastructure fault accepted work", role)
		}
		if err := fault.Close(ctx); !dsh.CleanupSettled(err) {
			t.Fatal("fault cleanup pending", role, err)
		}
		if err := dsh.CheckResumeConfig(faultOpts); err == nil {
			t.Fatal("abnormal infrastructure exit allowed resume", role)
		}
		report[role+"_fault_settled_and_resume_denied"] = true
	}
	time.Sleep(4500 * time.Millisecond)
	if _, err := os.Stat(opts.Workspace + "/cancel.late"); !os.IsNotExist(err) {
		t.Fatal("late side effect after settled cancellation", err)
	}
	report["cancel_no_late_write"] = true
	report["parallel_space_survived"] = true
	report["status"] = "passed"
}
