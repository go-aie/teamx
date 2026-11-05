//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/testutil"
)

// Use the operator's current TOML model and DSH installation in a fresh private
// Session. A loopback observer forwards the actual request to the same gateway;
// it records roles/status only, never headers, prompt bodies or credentials.
func TestRealDSHModelCompatibility(t *testing.T) {
	if os.Getenv("TEAMX_DSH_COMPAT_REAL") != "1" {
		t.Skip("explicit config, credentials and fresh evidence directory required")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "recorded_at": time.Now().UTC(), "scope": "current configured model; role compatibility; no Room mutation or business actions"}
	defer imageRealReport(t, output, report)
	file := os.Getenv("TEAMX_DSH_COMPAT_CONFIG")
	if !filepath.IsAbs(file) {
		t.Fatal("absolute TEAMX_DSH_COMPAT_CONFIG required")
	}
	config, err := loadStartupConfig(file, true)
	if err != nil || config.Runtime == nil {
		t.Fatal("valid current runtime required", err)
	}
	runtime := *config.Runtime
	model := runtime.Model.Clone()
	if model.Protocol != "openai-completions" || model.Compat.SupportsDeveloperRole == nil || *model.Compat.SupportsDeveloperRole || model.ReasoningEffort == "" {
		t.Fatal("explicit system-role compatibility and configured reasoning required")
	}
	imageRealCredential(t, model.APIKeyEnv)
	t.Setenv("TEAMX_DSH_BINARY_PATH", runtime.DSH.BinaryPath)
	args, _ := json.Marshal(runtime.DSH.BinaryArgs)
	if runtime.DSH.BinaryArgs == nil {
		args = []byte("[]")
	}
	t.Setenv("TEAMX_DSH_BINARY_ARGS", string(args))
	installation, err := testutil.InstalledDSH(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	report["installation"], report["configured_model"] = installation, model
	upstream, err := url.Parse(model.BaseURL)
	if err != nil || upstream.Scheme != "https" {
		t.Fatal("HTTPS real gateway required")
	}
	expectedPath := strings.TrimRight(upstream.Path, "/") + "/chat/completions"
	target := &url.URL{Scheme: upstream.Scheme, Host: upstream.Host}
	var mu sync.Mutex
	var requests []map[string]any
	report["requests"] = &requests
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	proxy.ModifyResponse = func(res *http.Response) error {
		mu.Lock()
		if len(requests) > 0 {
			requests[len(requests)-1]["status"] = res.StatusCode
		}
		mu.Unlock()
		return nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != expectedPath {
			http.Error(w, "unexpected request", 400)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, (8<<20)+1))
		r.Body.Close()
		if err != nil || len(data) > 8<<20 {
			http.Error(w, "invalid request", 400)
			return
		}
		var payload struct {
			Model           string `json:"model"`
			ReasoningEffort string `json:"reasoning_effort"`
			Messages        []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if json.Unmarshal(data, &payload) != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		roles := []string{}
		for _, m := range payload.Messages {
			roles = append(roles, m.Role)
		}
		mu.Lock()
		requests = append(requests, map[string]any{"model": payload.Model, "reasoning_effort": payload.ReasoningEffort, "roles": roles})
		mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(data))
		r.ContentLength = int64(len(data))
		r.Host = upstream.Host
		proxy.ServeHTTP(w, r)
	}))
	defer server.Close()
	model.BaseURL = server.URL + strings.TrimRight(upstream.Path, "/")
	workspace := filepath.Join(output, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	opts := dsh.Options{BinaryPath: installation.BinaryPath, BinaryArgs: installation.BinaryArgs, Artifact: runtime.DSH.Artifact, Workspace: workspace, StateDir: filepath.Join(output, "state"), Model: model,
		Instructions: "You are a model compatibility probe. Do not use tools, delegate, inspect files or perform any action. Reply only with the exact requested marker."}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	session, err := dsh.Open(ctx, opts)
	if session != nil {
		defer func() {
			err := session.Close(context.Background())
			report["close_succeeded"] = err == nil
			if err != nil {
				t.Error(err)
			}
		}()
	}
	if err != nil {
		report["error"] = err.Error()
		t.Fatal(err)
	}
	report["supports_images"] = session.SupportsImages()
	result, err := session.Run(ctx, "Reply exactly TEAMX_COMPAT_OK. Do not use tools.")
	report["answer"], report["stop_reason"], report["tool_count"] = result.Text, result.StopReason, len(result.Tools)
	if err != nil {
		report["error"] = err.Error()
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Text) != "TEAMX_COMPAT_OK" || len(result.Tools) != 0 {
		t.Fatal("model did not complete bounded compatibility probe")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 1 {
		t.Fatal("expected one gateway request without replay")
	}
	row := requests[0]
	if row["status"] != http.StatusOK || row["model"] != runtime.Model.ModelID || row["reasoning_effort"] != runtime.Model.ReasoningEffort {
		t.Fatal("actual gateway model or reasoning differs from configured model")
	}
	system := false
	for _, role := range row["roles"].([]string) {
		if role == "developer" {
			t.Fatal("developer role reached real gateway")
		}
		system = system || role == "system"
	}
	if !system {
		t.Fatal("system prompt missing from real request")
	}
	report["status"] = "passed"
}
