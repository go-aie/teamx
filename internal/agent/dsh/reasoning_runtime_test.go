//go:build e2e

package dsh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Exercise the installed DSH/ACP and actual request serialization against a
// controlled local upstream. This does not validate any external provider.
func TestReasoningManagedRuntime(t *testing.T) {
	if os.Getenv("TEAMX_PROBE_RUNTIME") != "1" {
		t.Skip("explicit managed runtime required")
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("TEAMX_DSH_BINARY_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var efforts []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ReasoningEffort string `json:"reasoning_effort"`
			Model           string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		efforts = append(efforts, request.ReasoningEffort)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		for _, choice := range []any{
			map[string]any{"index": 0, "delta": map[string]any{"role": "assistant", "content": "REASONING_OK"}, "finish_reason": nil},
			map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"},
		} {
			chunk, _ := json.Marshal(map[string]any{"id": "reasoning", "object": "chat.completion.chunk", "model": request.Model, "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, effort := range []string{"low", "high"} {
		root := t.TempDir()
		workspace := filepath.Join(root, "workspace")
		if err := os.Mkdir(workspace, 0700); err != nil {
			t.Fatal(err)
		}
		opts := Options{BinaryPath: os.Getenv("TEAMX_DSH_BINARY_PATH"), BinaryArgs: args, Workspace: workspace, StateDir: filepath.Join(root, "state"),
			Model:      ModelConfig{Protocol: "openai-completions", BaseURL: server.URL + "/v1", ModelID: "reasoning-probe", APIKeyEnv: "OPENAI_API_KEY", ReasoningEffort: effort},
			Credential: &Credential{Ref: "local-reasoning-probe", Value: "dummy-local-key"}}
		for _, open := range []func(context.Context, Options) (*Session, error){Open, Resume} {
			session, err := open(ctx, opts)
			if err != nil {
				t.Fatal(err)
			}
			result, runErr := session.Run(ctx, "Reply REASONING_OK without tools.")
			closeErr := session.Close(ctx)
			if runErr != nil || closeErr != nil || result.Text != "REASONING_OK" {
				t.Fatal(result.Text, runErr, closeErr)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(efforts) != 4 || efforts[0] != "low" || efforts[1] != "low" || efforts[2] != "high" || efforts[3] != "high" {
		t.Fatal("reasoning was not isolated and restored", efforts)
	}
}
