//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/testutil"
	"github.com/pelletier/go-toml/v2"
)

// Explicit real-provider acceptance. Reads existing credentials without changing
// the operator's environment/config and writes only to a new isolated home.
func TestModelValidationConfiguredService(t *testing.T) {
	source := os.Getenv("TEAMX_MODEL_PROBE_REAL_CONFIG")
	if source == "" {
		t.Skip("explicit real model config required")
	}
	output := os.Getenv("TEAMX_MODEL_PROBE_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("new absolute TEAMX_MODEL_PROBE_OUTPUT required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"status": "failed", "scope": "real provider text/fixed tool/cleanup; isolated config/credential restart", "at": time.Now().UTC()}
	defer func() {
		data, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(filepath.Join(output, "report.json"), data, 0600); err != nil {
			t.Error(err)
		}
	}()
	legacy, err := loadStartupConfig(source, true)
	if err != nil || legacy.Runtime == nil {
		t.Fatal("valid legacy runtime required", err)
	}
	installation, err := testutil.InstalledDSH(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	key := legacyModelCredential(legacy.Runtime.Model.APIKeyEnv, legacy.Env)
	if _, exported := os.LookupEnv(legacy.Runtime.Model.APIKeyEnv); !exported && key == "" && os.Getenv("TEAMX_MODEL_PROBE_ENV_FILE") != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		value, err := exec.CommandContext(ctx, installation.BinaryPath, "--env-file="+os.Getenv("TEAMX_MODEL_PROBE_ENV_FILE"), "-e", "process.stdout.write(process.env[process.argv[1]] || '')", legacy.Runtime.Model.APIKeyEnv).Output()
		if err != nil {
			t.Fatal("cannot read selected API key")
		}
		key = string(value)
	}
	if key == "" {
		t.Fatal("configured API key unavailable")
	}
	model := legacy.Runtime.Model.Clone()
	configPath := filepath.Join(output, "config.toml")
	config := startupConfig{path: configPath}
	config.Runtime = &teamRuntime{}
	config.Runtime.DSH.BinaryPath = installation.BinaryPath
	config.Runtime.DSH.BinaryArgs = installation.BinaryArgs
	// Save a launcher-only source; never copy the existing env table.
	startup, err := toml.Marshal(map[string]any{"server": map[string]any{"time_zone": "UTC"}, "runtime": map[string]any{"dsh": config.Runtime.DSH}, "models": modelCatalog{SchemaVersion: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, startup, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadStartupConfig(configPath, true)
	if err != nil {
		t.Fatal(err)
	}
	service, err := newModelService(loaded, newSystemSettings(loaded), output)
	if err != nil {
		t.Fatal(err)
	}
	entry := modelEntry{ID: "daily", Name: "Configured model", ModelParameters: parametersFromLegacyModel(model)}
	saved, err := service.save(context.Background(), modelSave{Entry: entry, APIKey: key, ExpectedVersion: 0})
	if err != nil {
		report["error"] = err.Error()
		t.Fatal(err)
	}
	reloaded, err := loadStartupConfig(configPath, true)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := newModelService(reloaded, newSystemSettings(reloaded), output)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := restarted.credentials.get(saved.Entries[0].CredentialRef)
	if err != nil {
		t.Fatal(err)
	}
	opts := dsh.Options{BinaryPath: restarted.launcher.BinaryPath, BinaryArgs: restarted.launcher.BinaryArgs, Model: saved.Entries[0].materialize(), Credential: credential, Workspace: filepath.Join(output, "workspace"), StateDir: filepath.Join(output, "session"), Instructions: "Reply only to the connectivity request. Never call tools or perform any action."}
	if err := os.Mkdir(opts.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session, err := dsh.Open(ctx, opts)
	if session != nil {
		defer session.Close(context.Background())
	}
	if err != nil {
		report["error"] = err.Error()
		t.Fatal(err)
	}
	result, err := session.Run(ctx, "Reply exactly TEAMX_RESTART_OK. Do not call tools.")
	if err != nil {
		report["error"] = err.Error()
		t.Fatal(err)
	}
	if result.Text != "TEAMX_RESTART_OK" || len(result.Tools) != 0 {
		t.Fatal("restart marker/tool mismatch")
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	report["status"] = "passed"
	report["model_id"] = model.ModelID
	report["installation"] = installation
	report["catalog_version"] = saved.Version
	report["restart_credential_used"] = true
	report["text_tool_cleanup"] = true
}
