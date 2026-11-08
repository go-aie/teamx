//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Invoked by package-acceptance.sh after extracting and verifying the final
// archive. Go is only the external test driver; the service and DSH run with
// a fresh HOME, a restricted PATH and the archived executables.
func TestBundleRealDSH(t *testing.T) {
	if os.Getenv("TEAMX_BUNDLE_REAL") != "1" {
		t.Skip("use scripts/package-acceptance.sh with an explicit model config")
	}
	output, bundle := os.Getenv("TEAMX_DSH_OUTPUT"), os.Getenv("TEAMX_BUNDLE_ROOT")
	if !filepath.IsAbs(output) || !filepath.IsAbs(bundle) {
		t.Fatal("absolute bundle and fresh evidence directory required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	d := &realRoomDriver{
		cliDriver: cliDriver{ctx: ctx, dir: filepath.Join(output, "room"), binary: filepath.Join(bundle, "bin/teamx")},
		output:    output,
		report:    map[string]any{"scenario": "P3-bundle-real-dsh", "status": "failed", "recorded_at": time.Now().UTC(), "fixture": false},
	}
	defer func() {
		if err := d.stopReal(); err != nil {
			d.report["status"], d.report["cleanupError"] = "failed", err.Error()
			t.Error(err)
		}
		data, err := json.MarshalIndent(d.report, "", "  ")
		if err == nil {
			if d.secret != "" {
				data = bytes.ReplaceAll(data, []byte(d.secret), []byte("[REDACTED]"))
			}
			err = os.WriteFile(filepath.Join(output, "report.json"), append(data, '\n'), 0600)
		}
		if err != nil {
			t.Error(err)
		}
	}()
	fail := func(message string) {
		d.report["error"] = message
		t.Fatal(message)
	}
	config, err := loadStartupConfig(os.Getenv("TEAMX_BUNDLE_MODEL_CONFIG"), true)
	if err != nil || config.Runtime == nil {
		fail("valid explicit model config required")
	}
	model := config.Runtime.Model.Clone()
	d.modelOverride = &model
	key := os.Getenv(model.APIKeyEnv)
	if key == "" {
		key = config.Env[model.APIKeyEnv]
	}
	// Load only the selected model credential; never import operator platform
	// sessions, runtime paths, other env values or existing Room data.
	if key != "" {
		t.Setenv(model.APIKeyEnv, key)
	}
	t.Setenv("MODEL_BASE_URL", model.BaseURL)
	t.Setenv("MODEL_ID", model.ModelID)
	t.Setenv("TEAMX_DSH_BINARY_PATH", filepath.Join(bundle, "bin/dsh"))
	t.Setenv("TEAMX_DSH_BINARY_ARGS", "[]")
	home := filepath.Join(output, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		fail("cannot create isolated home")
	}
	// The test harness has already found Go. Nothing spawned by TeamX can
	// discover host Go, Node, Git or platform CLIs through this PATH.
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("PATH", os.Getenv("TEAMX_BUNDLE_TEST_PATH"))
	t.Setenv("SHELL", "/bin/sh")
	d.env = []string{"HOME=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"PATH=" + os.Getenv("PATH"), "SHELL=/bin/sh", "LANG=C.UTF-8", "TMPDIR=" + output}
	t.Chdir(output)
	d.report["configured_model"], d.report["host_tools_on_path"] = model, false
	if err := d.accept(); err != nil {
		fail(err.Error())
	}
	d.report["status"] = "passed"
}
