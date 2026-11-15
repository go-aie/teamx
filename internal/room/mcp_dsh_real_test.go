//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/testutil"
)

// Actual installed DSH, ACP and MCP client; deterministic local model gateway.
// This proves tool discovery/execution, not real-model or image understanding.
func TestRealDSHMCPHTTPDiscovery(t *testing.T) {
	if os.Getenv("TEAMX_MCP_DSH_REAL") != "1" {
		t.Skip("set TEAMX_MCP_DSH_REAL=1, explicit DSH launcher and fresh TEAMX_DSH_OUTPUT")
	}
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "recorded_at": time.Now().UTC(), "scope": "real installed DSH ACP HTTP MCP discovery/call; controlled local model gateway", "external_business_writes": 0, "real_model": false, "images": false}
	defer imageRealReport(t, out, report)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	installation, err := testutil.InstalledDSH(ctx)
	if err != nil {
		t.Fatal(err)
	}
	report["installation"] = installation
	inbox := newInboxHarnessWithTimeout(t, InboxLimits{}, 2*time.Minute)
	initReferenceTest(t, inbox)
	receiveReference(t, inbox, "probe", referenceTestSource("probe"))
	inboxRunning(t, inbox, 1, 1)
	a, _, _ := accessFixture(t)
	var confirmations atomic.Int32
	addMCPOutputProbe(a.mcp, inbox, nil, func(err error) {
		if err != nil {
			t.Error(err)
		}
		confirmations.Add(1)
	})
	token, revoke := issueMCPProbeCredential(t, a, inbox)
	defer func() { revoke() }()
	var mu sync.Mutex
	var transportVersions []string
	accessHandler := a.wrap(http.NotFoundHandler())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if version := r.Header.Get("MCP-Protocol-Version"); version != "" {
			mu.Lock()
			transportVersions = append(transportVersions, version)
			mu.Unlock()
		}
		accessHandler.ServeHTTP(w, r)
	}))
	defer server.Close()
	a.internal, a.public = server.URL, server.URL
	var discovered []string
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "invalid local model request", 400)
			return
		}
		found := ""
		for _, tool := range req.Tools {
			if tool.Function.Name == "mcp__teamx__output_probe" {
				found = tool.Function.Name
			}
		}
		if found == "" {
			t.Error("ACP-injected MCP tool absent from real DSH model tools")
			http.Error(w, "missing MCP tool", 400)
			return
		}
		mu.Lock()
		discovered = append(discovered, found)
		mu.Unlock()
		n := calls.Add(1)
		delta := map[string]any{"role": "assistant", "content": "MCP_HTTP_OK"}
		finish := "stop"
		if n%2 == 1 {
			turn := 1
			if n > 2 {
				turn = 2
			}
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("mcp-call-%d", n), "type": "function", "function": map[string]any{"name": found, "arguments": fmt.Sprintf(`{"turn_number":%d}`, turn)}}}}
			finish = "tool_calls"
		} else {
			visible := false
			for _, message := range req.Messages {
				data, _ := json.Marshal(message.Content)
				if message.Role == "tool" && strings.Contains(string(data), "source body probe") {
					visible = true
				}
			}
			if !visible {
				t.Error("MCP result missing from model input")
				http.Error(w, "missing tool result", 400)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, choice := range []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}, map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
			data, _ := json.Marshal(map[string]any{"id": "local-model", "object": "chat.completion.chunk", "model": req.Model, "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer model.Close()
	workspace := filepath.Join(out, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	opts := dsh.Options{BinaryPath: installation.BinaryPath, BinaryArgs: installation.BinaryArgs, Workspace: workspace, StateDir: filepath.Join(out, "state"),
		Instructions: "This isolated acceptance calls only the discovered TeamX MCP output_probe. Do not use shell, files, delegation or external services.",
		Model:        dsh.ModelConfig{Protocol: "openai-completions", BaseURL: model.URL + "/v1", ModelID: "mcp-acceptance", APIKeyEnv: "OPENAI_API_KEY", Compat: dsh.ModelCompat{SupportsDeveloperRole: new(false)}},
		Credential:   &dsh.Credential{Value: "local-only-model-key"}, MCPServers: []dsh.MCPHTTPServer{mcpHTTPConfiguration(server.URL, token)}}
	for _, resume := range []bool{false, true} {
		open := dsh.Open
		if resume {
			inbox.store.finishMCPTurn(inbox.id, 1)
			receiveReference(t, inbox, "probe-resume", referenceTestSource("probe-resume"))
			inboxRunning(t, inbox, 2, 2)
			revoke()
			token, revoke = issueMCPProbeCredential(t, a, inbox)
			opts.MCPServers = []dsh.MCPHTTPServer{mcpHTTPConfiguration(server.URL, token)}
			open = dsh.Resume
		}
		session, err := open(ctx, opts)
		if err != nil {
			if session != nil {
				_ = session.Close(context.Background())
			}
			t.Fatal("real DSH ACP open", err)
		}
		result, runErr := session.Run(ctx, "Call the discovered output_probe with turn_number 1 and report its result.")
		closeErr := session.Close(context.Background())
		if runErr != nil || closeErr != nil || !strings.Contains(result.Text, "MCP_HTTP_OK") {
			t.Fatal("real DSH MCP round trip", runErr, closeErr)
		}
	}
	mu.Lock()
	report["discovered_tools"] = append([]string(nil), discovered...)
	report["transport_protocol_headers"] = append([]string(nil), transportVersions...)
	mu.Unlock()
	report["server_protocol"] = mcpProtocolVersion
	report["model_requests"], report["confirmations"] = calls.Load(), confirmations.Load()
	report["new_and_resume"] = true
	if calls.Load() != 4 || confirmations.Load() != 2 {
		t.Fatal("discovery/call count mismatch", calls.Load(), confirmations.Load())
	}
	if got := inboxCall[InboxStatus](t, inbox, InspectInbox{MemberIdentity: inbox.id}); got.ConsumedThrough != 2 {
		t.Fatal("MCP response not consumed", got)
	}
	report["status"] = "passed"
}
