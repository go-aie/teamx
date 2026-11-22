//go:build e2e && (darwin || linux)

package dsh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/testutil"
)

// Docker, discovery, independent assertions and cleanup belong to the companion
// scripts/agent_sandbox_acceptance.py runner. This entry only exercises the real
// Go ACP backend. It is excluded from normal tests and requires explicit opt-in.
func TestRealDSHSandboxBrowser(t *testing.T) {
	if os.Getenv("TEAMX_SANDBOX_REAL") != "1" {
		t.Skip("use scripts/agent_sandbox_acceptance.py for isolated AIO acceptance")
	}
	var cfg struct {
		Output          string                        `json:"output"`
		Endpoint        string                        `json:"endpoint"`
		Model           ModelConfig                   `json:"model"`
		Prompts         []struct{ Name, Text string } `json:"prompts"`
		IntervalSeconds int                           `json:"interval_seconds"`
	}
	data, err := os.ReadFile(os.Getenv("TEAMX_SANDBOX_RUN_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(cfg.Output) || len(cfg.Prompts) == 0 {
		t.Fatal("absolute output and prompts required")
	}
	report := map[string]any{"status": "failed", "usage": "unavailable: ACP TurnResult has no token usage", "visual": "not verified by ACP text or screenshot generation alone"}
	defer func() {
		if err := atomicJSON(cfg.Output, "dsh-report.json", report); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Minute)
	defer cancel()
	// The host runner requests cancellation without killing the Go owner of
	// the DSH process tree. ACP cancellation and Close retain their normal gates.
	if _, err := os.Stat(filepath.Join(cfg.Output, "cancel")); err == nil {
		t.Fatal("acceptance cancelled before DSH startup")
	}
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(cfg.Output, "cancel")); err == nil {
					cancel()
					return
				}
			}
		}
	}()
	installation, err := testutil.InstalledDSH(ctx)
	if err != nil {
		t.Fatal(err)
	}
	report["installation"], report["model"] = installation, cfg.Model
	launchers := map[string]string{}
	for _, arg := range installation.BinaryArgs {
		if filepath.IsAbs(arg) && (strings.HasSuffix(arg, ".js") || strings.HasSuffix(arg, ".mjs")) {
			data, err := os.ReadFile(arg)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			launchers[arg] = hex.EncodeToString(sum[:])
		}
	}
	report["launcher_sha256"] = launchers
	workspace := filepath.Join(cfg.Output, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		BinaryPath: installation.BinaryPath, BinaryArgs: installation.BinaryArgs,
		Workspace: workspace, StateDir: filepath.Join(cfg.Output, "state"), Model: cfg.Model,
		Credential:   &Credential{Ref: "sandbox-acceptance", Value: os.Getenv("TEAMX_SANDBOX_MODEL_KEY")},
		MCPServers:   []MCPHTTPServer{{Type: "http", Name: "sandbox", URL: cfg.Endpoint, Headers: []MCPHeader{{Name: "Authorization", Value: "Bearer " + os.Getenv("TEAMX_SANDBOX_KEY")}}}},
		Instructions: "You are a browser acceptance agent in a disposable AIO environment. Use only sandbox MCP browser tools, never local shell, files, network clients, subagents or delegation. Read actual page content, never guess results. DSH runs on the HOST; browser downloads and paths are REMOTE container files, never read them with local tools. Do not inspect test harness files, environment or credentials. Report failures explicitly. Screenshots alone do not prove vision. Stop after the requested task.",
		Limits:       Limits{HandshakeTimeout: 90 * time.Second, TurnTimeout: 4 * time.Minute},
	}
	s, err := Open(ctx, opts)
	if s != nil {
		defer func() {
			closeCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			err := s.Close(closeCtx)
			report["local_cleanup_settled"] = CleanupSettled(err)
			if err != nil {
				report["local_cleanup_error"] = err.Error()
				report["status"] = "failed"
				t.Error(err)
			}
		}()
	}
	if err != nil {
		report["error"] = err.Error()
		t.Fatal(err)
	}
	report["session_id"] = s.id
	turns := []map[string]any{}
	report["turns"] = turns
	for i, prompt := range cfg.Prompts {
		if i > 0 && cfg.IntervalSeconds > 0 {
			t.Logf("cooldown before %s: %ds", prompt.Name, cfg.IntervalSeconds)
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Duration(cfg.IntervalSeconds) * time.Second):
			}
		}
		t.Logf("starting %s", prompt.Name)
		started := time.Now()
		result, err := s.Run(ctx, prompt.Text)
		turn := map[string]any{"name": prompt.Name, "seconds": time.Since(started).Seconds(), "result": result}
		if err != nil {
			turn["error"] = err.Error()
		}
		turns = append(turns, turn)
		report["turns"] = turns
		if saveErr := atomicJSON(cfg.Output, "dsh-report.json", report); saveErr != nil {
			t.Fatal(saveErr)
		}
		ids := map[string]bool{}
		for _, tool := range result.Tools {
			ids[tool.ID] = true
			if tool.Title != "" && tool.Input != "" && !strings.Contains(tool.Title, "sandbox") && !strings.Contains(tool.Title, "browser") {
				turn["unexpected_tool"] = tool.Title
				t.Fatal("non-browser tool used")
			}
		}
		turn["tool_calls"] = len(ids)
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) == 0 {
			t.Fatal(errors.New("browser task completed without observed tool calls"))
		}
		if result.StopReason != "end_turn" {
			t.Fatal(fmt.Errorf("unexpected stop reason %s", result.StopReason))
		}
		if strings.HasPrefix(prompt.Name, "trial-") {
			// Independent capture before the next task changes the browser page.
			req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimSuffix(cfg.Endpoint, "/mcp")+"/v1/browser/page/screenshot", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+os.Getenv("TEAMX_SANDBOX_KEY"))
			response, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<20))
			response.Body.Close()
			if readErr != nil || response.StatusCode != 200 {
				t.Fatal("independent screenshot failed", readErr)
			}
			if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cfg.Output, prompt.Name+".png"), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	report["status"] = "passed"
}
