//go:build e2e && linux

package dsh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Test-only route probe, not a production execution adapter. Synthetic model
// responses drive the pinned DSH's actual tools. No platform account is used.
func TestLinuxSRTDSHCompatibility(t *testing.T) {
	if os.Getenv("TEAMX_SRT_EXPERIMENT") != "1" {
		t.Skip("use scripts/srt_isolation/native-run.py in an approved disposable directory")
	}
	if os.Getuid() == 0 {
		t.Fatal("the trusted test supervisor must also run without root")
	}
	var cfg struct{ Lab, Root, Node, SRT, DSH, Bwrap, Socat, RG string }
	data, err := os.ReadFile(os.Getenv("TEAMX_SRT_CONFIG"))
	if err != nil || json.Unmarshal(data, &cfg) != nil || cfg.Lab != "/mnt/lab" {
		t.Fatal("explicit isolated native configuration required", err)
	}
	if entries, err := os.ReadDir(cfg.Lab); err != nil || len(entries) != 0 {
		t.Fatal("new empty acceptance directory required", err)
	}
	report := map[string]any{"status": "failed", "real_dsh": true, "real_model": false,
		"production_backend": false, "srt_route_accepted": false, "requested_inner_policy": "workspace-write; approval ask",
		"internal_model_transport": "exact host:port allowlist; NO_PROXY cleared before Node; NODE_USE_ENV_PROXY=1",
		"unverified":               []string{"authenticated_mcp_cli", "network_bypasses", "cancel_resume_faults", "real_model"}}
	defer func() {
		if err := atomicJSON(cfg.Lab, "report.json", report); err != nil {
			t.Error(err)
		}
	}()
	mkdir := func(dir string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, area := range []string{"a", "b", "c", "control", "service", "host-home"} {
		mkdir(filepath.Join(cfg.Lab, area, "private"))
		if err := os.WriteFile(filepath.Join(cfg.Lab, area, "private/secret"), []byte("SYNTHETIC_"+area), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, area := range []string{"a", "b"} {
		for _, part := range []string{"workspace", "home", "tmp", "backend"} {
			mkdir(filepath.Join(cfg.Lab, area, part))
		}
	}
	mkdir(filepath.Join(cfg.Lab, "policies"))
	if err := os.Symlink(filepath.Join(cfg.Lab, "b/private"), filepath.Join(cfg.Lab, "a/workspace/escape")); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var name, observed string
	var args map[string]any
	issued, sequence := false, 0
	modelRequests, rejectedRequests := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/transport-probe" && r.Header.Get("Authorization") == "Bearer synthetic-user1-space-a-member-a-gen1" {
			fmt.Fprint(w, "SYNTHETIC_PROXY_OK")
			return
		}
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-user1-space-a-member-a-gen1" {
			mu.Lock()
			rejectedRequests++
			mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		modelRequests++
		mu.Unlock()
		var request struct {
			Tools    []struct{ Function struct{ Name string } }
			Messages []struct {
				Role    string
				Content json.RawMessage
			}
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		delta := map[string]any{"role": "assistant", "content": "SRT_DONE"}
		finish := "stop"
		if !issued && name != "" {
			found := false
			for _, tool := range request.Tools {
				found = found || tool.Function.Name == name
			}
			if !found {
				w.WriteHeader(400)
				return
			}
			encoded, _ := json.Marshal(args)
			sequence++
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0,
				"id": fmt.Sprintf("srt-%d", sequence), "type": "function", "function": map[string]any{"name": name, "arguments": string(encoded)}}}}
			issued, finish = true, "tool_calls"
		} else {
			for i := len(request.Messages) - 1; i >= 0; i-- {
				if request.Messages[i].Role == "tool" {
					observed = string(request.Messages[i].Content)
					break
				}
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, choice := range []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil},
			map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
			chunk, _ := json.Marshal(map[string]any{"id": "srt", "object": "chat.completion.chunk", "model": "srt", "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	state := filepath.Join(cfg.Lab, "a/backend")
	policy := map[string]any{
		"network": map[string]any{"allowedDomains": []string{strings.TrimPrefix(server.URL, "http://")},
			"deniedDomains": []string{}, "strictAllowlist": true, "allowAllUnixSockets": false, "allowLocalBinding": false},
		"filesystem": map[string]any{"denyRead": []string{"/"},
			"allowRead": []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/dev", "/proc", "/etc/ssl", "/etc/alternatives", "/etc/ld.so.cache", "/etc/hosts", "/etc/resolv.conf", "/etc/protocols", "/etc/services", "/etc/nsswitch.conf",
				cfg.SRT, cfg.DSH, cfg.Root + "/node", cfg.Root + "/tools", cfg.Lab + "/a/workspace", cfg.Lab + "/a/home", cfg.Lab + "/a/tmp", state + "/home", state + "/agents"},
			"allowWrite": []string{cfg.Lab + "/a/workspace", cfg.Lab + "/a/home", cfg.Lab + "/a/tmp", state + "/home", state + "/agents"},
			"denyWrite":  []string{state + "/home/AGENTS.md", state + "/home/cordis.patch.yml"}},
		"bwrapPath": cfg.Bwrap, "socatPath": cfg.Socat, "ripgrep": map[string]any{"command": cfg.RG},
		"enableWeakerNestedSandbox": false, "enableWeakerNetworkIsolation": false, "allowNestedUserNamespaces": false,
	}
	if err := atomicJSON(filepath.Join(cfg.Lab, "policies"), "a.json", policy); err != nil {
		t.Fatal(err)
	}
	report["srt_policy"] = policy
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	opts := Options{BinaryPath: "/usr/bin/python3", BinaryArgs: []string{cfg.Root + "/dsh-launch.py", "--policy", cfg.Lab + "/policies/a.json"},
		Workspace: cfg.Lab + "/a/workspace", StateDir: state, Instructions: "Execute the configured synthetic route probe only.",
		Model:      ModelConfig{Protocol: "openai-completions", BaseURL: server.URL + "/v1", ModelID: "srt", APIKeyEnv: "OPENAI_API_KEY"},
		Credential: &Credential{Ref: "synthetic-member-a-gen1", Value: "synthetic-user1-space-a-member-a-gen1"},
		Environment: []string{"PATH=" + filepath.Dir(cfg.Node) + ":" + filepath.Dir(cfg.Bwrap) + ":/usr/bin:/bin", "HOME=" + cfg.Lab + "/a/home", "TMPDIR=" + cfg.Lab + "/a/tmp",
			"NODE_USE_ENV_PROXY=1", "TEAMX_SRT_CONFIG=" + os.Getenv("TEAMX_SRT_CONFIG")},
		Permission: func(_ context.Context, req PermissionRequest) (string, error) {
			for _, option := range req.Options {
				if option.Kind == "allow_once" {
					return option.OptionID, nil
				}
			}
			return "", fmt.Errorf("synthetic probe requires allow_once")
		}, Limits: Limits{HandshakeTimeout: 45 * time.Second, TurnTimeout: 30 * time.Second}}
	s, err := Open(ctx, opts)
	if s != nil {
		defer func() {
			closeCtx, stop := context.WithTimeout(context.Background(), 25*time.Second)
			defer stop()
			err := s.Close(closeCtx)
			s.process.(*process).mu.Lock()
			report["dsh_stderr"] = s.redact(s.process.(*process).stderrText.String())
			s.process.(*process).mu.Unlock()
			mu.Lock()
			report["authenticated_model_requests"] = modelRequests
			report["rejected_model_requests"] = rejectedRequests
			mu.Unlock()
			report["cleanup_settled"] = CleanupSettled(err)
			if err != nil {
				report["abnormal_resume_rejected"] = errors.Is(CheckResume(opts.StateDir), ErrRecoveryUnsupported)
			}
			if err != nil {
				report["close_error"] = err.Error()
				t.Error(err)
			}
		}()
	}
	if err != nil {
		report["failure_class"], report["error"] = "dsh_acp_start_failed", err.Error()
		t.Fatal(err)
	}
	report["agent_info"], report["session_id"], report["dsh_cli_version"] = s.reference.AgentInfo, s.id, s.reference.Artifact.Version
	probeJS := `(async()=>{const out={};try{const r=await fetch(process.argv[1],{headers:{Authorization:'Bearer synthetic-user1-space-a-member-a-gen1'},signal:AbortSignal.timeout(4000)});out.status=r.status;out.body=await r.text()}catch(e){out.error=e.message;out.cause=e.cause?.message;out.code=e.cause?.code}try{const p=await import(process.argv[2]);const dispose=await p.installProxyFromEnvironment({get:n=>process.env[n]===undefined?undefined:{value:process.env[n]}},()=>{});out.dsh_loopback_proxied=p.proxyRouteFor(process.argv[1]).proxied;await dispose()}catch(e){out.policy_error=e.message}console.log(JSON.stringify(out))})()`
	probeOpts := opts
	probeOpts.BinaryPath = cfg.Node
	proxyModule := cfg.DSH + "/node_modules/@deepseek-ai/dsh-http-proxy/lib/index.js"
	proxyHash, err := executableDigest(proxyModule)
	if err != nil {
		t.Fatal(err)
	}
	report["dsh_proxy_module_sha256"] = proxyHash
	probeOpts.BinaryArgs = []string{cfg.SRT + "/node_modules/@anthropic-ai/sandbox-runtime/dist/cli.js", "--settings", cfg.Lab + "/policies/a.json", "--", "/usr/bin/env", "NO_PROXY=", "no_proxy=", "NODE_USE_ENV_PROXY=1", cfg.Node, "-e", probeJS, server.URL + "/transport-probe", proxyModule}
	probeOpts.inspect = processTable
	probe, probeErr := startProcess(probeOpts, func(error) {})
	if probeErr != nil {
		t.Fatal(probeErr)
	}
	probeText := make(chan []byte, 1)
	go func() { b, _ := io.ReadAll(io.LimitReader(probe.out, 8192)); probeText <- b }()
	probeStopped := make(chan struct{})
	go func() { probe.stop(12 * time.Second); close(probeStopped) }()
	select {
	case <-probeStopped:
		report["node_proxy_probe_cleanup_settled"] = true
	case <-time.After(20 * time.Second):
		report["node_proxy_probe_cleanup_settled"] = false
		t.Fatal("transport probe remains owned; stop further execution")
	}
	report["node_proxy_probe"] = s.redact(string(<-probeText))
	run := func(tool string, parameters map[string]any) string {
		t.Helper()
		mu.Lock()
		name, args, observed, issued = tool, parameters, "", false
		mu.Unlock()
		result, err := s.Run(ctx, "Run the configured synthetic probe.")
		if err != nil {
			var rpc *rpcError
			if errors.As(err, &rpc) {
				report["private_acp_error"] = map[string]string{"message": s.redact(rpc.Message), "data": s.redact(string(rpc.Data))}
			}
			report["failure_class"], report["error"] = "model_or_tool_transport_failed", err.Error()
			if text, ok := report["node_proxy_probe"].(string); ok && strings.Contains(text, "SYNTHETIC_PROXY_OK") && strings.Contains(text, `"dsh_loopback_proxied":false`) {
				report["failure_class"] = "dsh_loopback_proxy_incompatible"
			}
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if !issued || observed == "" || len(result.Tools) == 0 {
			t.Fatal("missing real DSH tool round trip")
		}
		return observed
	}
	_ = run("write", map[string]any{"file_path": opts.Workspace + "/own.txt", "content": "OWN_WRITE"})
	if b, err := os.ReadFile(opts.Workspace + "/own.txt"); err != nil || string(b) != "OWN_WRITE" {
		t.Fatal("own write failed", err)
	}
	report["file_tool_own_write"] = true
	for _, area := range []string{"b", "c", "control", "service", "host-home"} {
		value := run("read", map[string]any{"file_path": cfg.Lab + "/" + area + "/private/secret"})
		if strings.Contains(value, "SYNTHETIC_") || (!strings.Contains(value, "EACCES") && !strings.Contains(strings.ToLower(value), "permission") && !strings.Contains(value, "ENOENT")) {
			report["failure_class"], report["tool_result"] = "file_boundary_failed", value
			t.Fatal("foreign synthetic private file was not explicitly denied", area)
		}
	}
	report["file_tool_private_reads_denied"] = true
	value := run("bash", map[string]any{"command": "printf ready > shell.ready", "description": "Verify default inner sandbox availability"})
	report["stock_shell_result"] = value
	if b, err := os.ReadFile(opts.Workspace + "/shell.ready"); err != nil || string(b) != "ready" {
		report["failure_class"] = "nested_dsh_sandbox_unavailable"
		t.Fatal("stock workspace-write shell did not execute; boundary failure is not task denial")
	}
	report["stock_workspace_write_shell"] = true
	report["status"] = "compatibility_passed"
}
