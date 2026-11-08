//go:build e2e

package dsh

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/testutil"
)

// TestRealDSHImage uses synthetic, independently generated pixels, never a
// GitLab notification. The random answer is absent from instructions/prompt.
func TestRealDSHImage(t *testing.T) {
	if os.Getenv("TEAMX_DSH_IMAGE_REAL") != "1" {
		t.Skip("set TEAMX_DSH_IMAGE_REAL=1 for real DSH/model image acceptance")
	}
	output := os.Getenv("TEAMX_DSH_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("TEAMX_DSH_OUTPUT must be a new absolute evidence directory")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"status": "failed", "recordedAt": time.Now().UTC().Format(time.RFC3339), "component": "Go internal/agent/dsh", "scope": "I1 independent image; no GitLab or Room notification input", "recoveryStatus": "not requested; set TEAMX_DSH_IMAGE_RESUME=1 for additional recovery check"}
	defer func() {
		if err := atomicJSON(output, "report.json", report); err != nil {
			t.Error(err)
		}
		t.Logf("image acceptance report: %s", filepath.Join(output, "report.json"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	installation, err := testutil.InstalledDSH(ctx)
	if err != nil {
		t.Fatal(err)
	}
	report["installation"] = installation
	keyEnv := os.Getenv("TEAMX_DSH_API_KEY_ENV")
	if keyEnv == "" {
		keyEnv = "OPENAI_API_KEY"
	}
	if !envName.MatchString(keyEnv) {
		t.Fatal("invalid credential environment name")
	}
	if file := os.Getenv("TEAMX_DSH_ENV_FILE"); file != "" && os.Getenv(keyEnv) == "" {
		credentialCtx, stop := context.WithTimeout(ctx, 15*time.Second)
		defer stop()
		key, err := exec.CommandContext(credentialCtx, "node", "--env-file="+file, "-e", "process.stdout.write(process.env[process.argv[1]] || '')", keyEnv).Output()
		if err != nil {
			t.Fatal("cannot load credential file")
		}
		t.Setenv(keyEnv, string(key))
	}
	baseURL, model := os.Getenv("MODEL_BASE_URL"), os.Getenv("MODEL_ID")
	if baseURL == "" || model == "" {
		t.Fatal("MODEL_BASE_URL and MODEL_ID required for real image acceptance")
	}
	workspace := filepath.Join(output, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	challenge, err := rand.Int(rand.Reader, big.NewInt(100000000))
	if err != nil {
		t.Fatal(err)
	}
	code := fmt.Sprintf("%08d", challenge)
	data := imageChallenge(t, code)
	if err := os.WriteFile(filepath.Join(output, "challenge.png"), data, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	report["image"] = map[string]any{"count": 1, "bytes": len(data), "sha256": hex.EncodeToString(digest[:]), "expectedCode": code, "upperLeft": "red square", "lowerRight": "blue rectangle"}
	opts := Options{BinaryPath: installation.BinaryPath, BinaryArgs: installation.BinaryArgs, Workspace: workspace, StateDir: filepath.Join(output, "state"),
		Instructions: "You are an image acceptance probe. Answer using the supplied image pixels and conversation only. Never use tools, delegate, inspect files or access the network.",
		Model:        ModelConfig{Protocol: "openai-completions", BaseURL: baseURL, ModelID: model, APIKeyEnv: keyEnv, MaxTokens: 8192, InputModalities: []string{"text", "image"}}}
	report["model"] = opts.Model
	if err := acceptRealImage(ctx, opts, report, data, code); err != nil {
		report["error"] = err.Error()
		t.Fatal(err)
	}
	report["status"] = "passed"
}
