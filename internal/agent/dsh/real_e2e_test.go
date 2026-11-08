//go:build e2e

package dsh

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/testutil"
)

// Explicitly enabled acceptance of the installed DSH through the Go component.
// The default suite never uses a real model or reads a credential file.
func TestRealDSHSession(t *testing.T) {
	if os.Getenv("TEAMX_DSH_REAL") != "1" {
		t.Skip("set TEAMX_DSH_REAL=1 for installed DSH/model acceptance")
	}
	output := os.Getenv("TEAMX_DSH_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("TEAMX_DSH_OUTPUT must be a new absolute evidence directory")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"status": "failed", "recordedAt": time.Now().UTC().Format(time.RFC3339), "platform": runtime.GOOS + "/" + runtime.GOARCH, "component": "Go internal/agent/dsh", "permission": "workspace-write + ask; extra permission requests cancelled", "resume": "not exercised by this scenario"}
	defer func() {
		if err := atomicJSON(output, "report.json", report); err != nil {
			t.Errorf("report: %v", err)
		}
		t.Logf("acceptance report: %s", filepath.Join(output, "report.json"))
	}()
	installation, err := testutil.InstalledDSH(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	report["installation"] = installation
	credentialCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	keyEnv := os.Getenv("TEAMX_DSH_API_KEY_ENV")
	if keyEnv == "" {
		keyEnv = "OPENAI_API_KEY"
	}
	if !envName.MatchString(keyEnv) {
		t.Fatal("invalid credential environment name")
	}
	if file := os.Getenv("TEAMX_DSH_ENV_FILE"); file != "" && os.Getenv(keyEnv) == "" {
		// Use Node's dotenv parser, capture only the chosen secret in memory.
		key, err := exec.CommandContext(credentialCtx, "node", "--env-file="+file, "-e", "process.stdout.write(process.env[process.argv[1]] || '')", keyEnv).Output()
		if err != nil {
			t.Fatal("cannot load credential file")
		}
		t.Setenv(keyEnv, string(key))
	}
	baseURL := os.Getenv("MODEL_BASE_URL")
	if baseURL == "" {
		t.Fatal("MODEL_BASE_URL is required for explicit real acceptance")
	}
	model := os.Getenv("MODEL_ID")
	if model == "" {
		t.Fatal("MODEL_ID is required for explicit real acceptance")
	}
	workspace := filepath.Join(output, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	role, memory, challenge := randomToken(), randomToken(), randomToken()
	if err := os.WriteFile(filepath.Join(workspace, "challenge.txt"), []byte(challenge+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{
		BinaryPath: installation.BinaryPath, BinaryArgs: installation.BinaryArgs,
		Workspace: workspace, StateDir: filepath.Join(output, "state"),
		Instructions: "You are the TeamX Session acceptance probe. Your role token is " + role + ". Never delegate. Use only the requested local file commands.",
		Model:        ModelConfig{Protocol: "openai-completions", BaseURL: baseURL, ModelID: model, APIKeyEnv: keyEnv},
	}
	report["baseURL"], report["model"], report["apiKeyEnv"] = baseURL, model, keyEnv
	if err := acceptRealSession(opts, report, memory, role, challenge); err != nil {
		report["error"] = err.Error()
		t.Fatal(err)
	}
	report["status"] = "passed"
}
