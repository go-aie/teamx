//go:build e2e

package dsh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/agent"
)

// Real managed DSH and real ACP/tool execution, with a controlled upstream.
// This is separate evidence from a real model service probe.
func TestProbeManagedRuntime(t *testing.T) {
	if os.Getenv("TEAMX_PROBE_RUNTIME") != "1" {
		t.Skip("set TEAMX_PROBE_RUNTIME=1 and explicit launcher")
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("TEAMX_DSH_BINARY_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer managed-key" {
			t.Error("bound credential not used")
			w.WriteHeader(401)
			return
		}
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
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		if req.Model != "probe-model" || len(req.Tools) != 1 || req.Tools[0].Function.Name != "teamx_probe" {
			t.Error("probe isolation/model mismatch")
			w.WriteHeader(400)
			return
		}
		n := requests.Add(1)
		delta := map[string]any{"role": "assistant", "content": "TEAMX_TEXT_OK"}
		finish := "stop"
		if n == 2 {
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "probe-call", "type": "function", "function": map[string]any{"name": "teamx_probe", "arguments": "{}"}}}}
			finish = "tool_calls"
		}
		if n == 3 {
			var receipt string
			for _, m := range req.Messages {
				if m.Role == "tool" {
					receipt, _ = m.Content.(string)
				}
			}
			if receipt == "" {
				t.Error("tool result missing")
			}
			delta = map[string]any{"role": "assistant", "content": receipt}
		}
		if n > 3 {
			t.Error("unexpected retry")
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, choice := range []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}, map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
			data, _ := json.Marshal(map[string]any{"id": "test", "object": "chat.completion.chunk", "model": req.Model, "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", data)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_KEY", "wrong-ambient-key")
	t.Setenv("TEAMX_ADMIN_TOKEN", "must-not-inherit")
	result, err := Probe(context.Background(), Options{BinaryPath: os.Getenv("TEAMX_DSH_BINARY_PATH"), BinaryArgs: args, Model: ModelConfig{Protocol: "openai-completions", BaseURL: server.URL + "/v1", ModelID: "probe-model", APIKeyEnv: "OPENAI_API_KEY"}, Credential: &Credential{Value: "managed-key"}})
	if err != nil {
		t.Fatalf("%+v: %v", result, err)
	}
	if !result.Text || !result.ToolRoundTrip || !result.Cleanup || requests.Load() != 3 {
		t.Fatalf("%+v requests=%d", result, requests.Load())
	}
}

// Keep the real retry count and plugin budget; shorten only backoff in this
// controlled-runtime test so exhaustion can be checked within the probe deadline.
type fastProbeRetries struct{ nativeExecution }

func (f fastProbeRetries) Start(ctx context.Context, request agent.ExecutionRequest, failed func(error)) (agent.Execution, error) {
	if len(request.Args) > 0 && request.Args[len(request.Args)-1] == "acp" {
		path := filepath.Join(request.StateDir, "home", "cordis.patch.yml")
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var patches []map[string]any
		if err := json.Unmarshal(body, &patches); err != nil {
			return nil, err
		}
		for _, patch := range patches {
			if patch["id"] == "llm-pi-ai" {
				providers := patch["config"].(map[string]any)["providers"].(map[string]any)
				policy := providers["teamx"].(map[string]any)["retryPolicy"].(map[string]any)
				if policy["maxRetries"] != float64(modelRequestMaxRetries) {
					return nil, errors.New("provider retry count changed")
				}
				policy["backoff"] = map[string]any{"initialDelayMs": 10, "maxDelayMs": 20, "jitterRatio": 0}
			}
		}
		body, err = json.Marshal(patches)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, body, 0600); err != nil {
			return nil, err
		}
	}
	return f.nativeExecution.Start(ctx, request, failed)
}

func TestProbeManagedRuntimeRetries(t *testing.T) {
	if os.Getenv("TEAMX_PROBE_RUNTIME") != "1" {
		t.Skip("explicit managed runtime required")
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("TEAMX_DSH_BINARY_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name                       string
		failStep, failures, status int
		wantCode                   string
	}{
		{"limited-before-tool-recovers", 1, 3, 429, ""},
		{"limited-after-tool-recovers", 2, 3, 429, ""},
		{"limited-exhausts-retries", 1, modelRequestMaxRetries + 1, 429, "rate_limited"},
		{"authentication-fails-without-retry", 1, 1, 401, "authentication"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests, successes, failures atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer managed-key" {
					t.Error("bound credential not used")
					w.WriteHeader(401)
					return
				}
				var req struct {
					Model    string `json:"model"`
					Messages []struct {
						Role    string `json:"role"`
						Content any    `json:"content"`
					} `json:"messages"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					w.WriteHeader(400)
					return
				}
				step := int(successes.Load())
				if step == test.failStep && int(failures.Load()) < test.failures {
					failures.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(test.status)
					if test.status == 401 {
						fmt.Fprint(w, `{"error":{"code":"invalid_api_key","message":"private-provider-detail","type":"authentication_error"}}`)
					} else {
						fmt.Fprint(w, `{"error":{"code":"capacity_exceeded","message":"tpm_exceeded private-provider-detail","type":"rate_limit_error"}}`)
					}
					return
				}
				successes.Add(1)
				delta := map[string]any{"role": "assistant", "content": "TEAMX_TEXT_OK"}
				finish := "stop"
				if step == 1 {
					delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "probe-call", "type": "function", "function": map[string]any{"name": "teamx_probe", "arguments": "{}"}}}}
					finish = "tool_calls"
				} else if step == 2 {
					receipt := ""
					for _, message := range req.Messages {
						if message.Role == "tool" {
							receipt, _ = message.Content.(string)
						}
					}
					if receipt == "" {
						t.Error("tool result missing")
					}
					delta = map[string]any{"role": "assistant", "content": receipt}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, choice := range []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}, map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
					body, _ := json.Marshal(map[string]any{"id": "retry-test", "object": "chat.completion.chunk", "model": req.Model, "choices": []any{choice}})
					fmt.Fprintf(w, "data: %s\n\n", body)
				}
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			result, err := Probe(ctx, Options{Execution: fastProbeRetries{}, BinaryPath: os.Getenv("TEAMX_DSH_BINARY_PATH"), BinaryArgs: args,
				Model: ModelConfig{Protocol: "openai-completions", BaseURL: server.URL + "/v1", ModelID: "probe-model", APIKeyEnv: "OPENAI_API_KEY"}, Credential: &Credential{Value: "managed-key"}})
			wantSuccesses := 3
			if test.wantCode != "" {
				wantSuccesses = test.failStep
				var probe *ProbeError
				if !errors.As(err, &probe) || probe.Stage != "tool" || probe.Code != test.wantCode {
					t.Fatalf("original upstream classification lost: %v", err)
				}
			} else if err != nil || !result.ToolRoundTrip {
				t.Fatalf("transient failure did not recover: %+v %v", result, err)
			}
			if !result.Text || !result.Cleanup || int(successes.Load()) != wantSuccesses || int(requests.Load()) != wantSuccesses+test.failures {
				t.Fatalf("unexpected replay, retry count or cleanup: %+v requests=%d successes=%d", result, requests.Load(), successes.Load())
			}
		})
	}
}

func TestManagedRuntimeCredentialIsolation(t *testing.T) {
	if os.Getenv("TEAMX_PROBE_RUNTIME") != "1" {
		t.Skip("explicit managed runtime required")
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("TEAMX_DSH_BINARY_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		if r.Header.Get("Authorization") != "Bearer bound-"+req.Model {
			t.Error("model/key crossed")
			w.WriteHeader(401)
			return
		}
		for _, message := range req.Messages {
			if message.Role == "developer" {
				t.Error("explicit system compatibility lost")
			}
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"id": "isolation", "object": "chat.completion.chunk", "model": req.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "KEY_OK"}, "finish_reason": nil}}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		chunk, _ = json.Marshal(map[string]any{"id": "isolation", "object": "chat.completion.chunk", "model": req.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}})
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer server.Close()
	t.Setenv("OPENAI_API_KEY", "stale-host-key")
	options := make([]Options, 2)
	for i, model := range []string{"A", "B"} {
		root := t.TempDir()
		workspace := filepath.Join(root, "workspace")
		if err := os.Mkdir(workspace, 0700); err != nil {
			t.Fatal(err)
		}
		options[i] = Options{BinaryPath: os.Getenv("TEAMX_DSH_BINARY_PATH"), BinaryArgs: args, Model: ModelConfig{Protocol: "openai-completions", BaseURL: server.URL + "/v1", ModelID: model, APIKeyEnv: "OPENAI_API_KEY", Compat: ModelCompat{SupportsDeveloperRole: new(false)}}, Credential: &Credential{Ref: "version-" + model, Value: "bound-" + model}, Workspace: workspace, StateDir: filepath.Join(root, "state"), Instructions: "Reply only with the requested marker."}
	}
	for _, resume := range []bool{false, true} {
		if resume {
			t.Setenv("OPENAI_API_KEY", "")
		}
		done := make(chan error, 2)
		for _, opts := range options {
			go func() {
				open := Open
				if resume {
					open = Resume
				}
				session, err := open(context.Background(), opts)
				if err == nil {
					result, runErr := session.Run(context.Background(), "Reply KEY_OK, no tools.")
					if runErr != nil {
						err = runErr
					} else if result.Text != "KEY_OK" {
						err = errors.New("marker missing")
					}
				}
				if session != nil {
					err = errors.Join(err, session.Close(context.Background()))
				}
				done <- err
			}()
		}
		for range 2 {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	}
	if requests.Load() != 4 {
		t.Fatal("unexpected retries or missing requests", requests.Load())
	}
}
