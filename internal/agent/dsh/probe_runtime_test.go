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
