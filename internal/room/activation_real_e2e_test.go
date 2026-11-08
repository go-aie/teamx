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

// Production binary + production Room → MemberAgent → DSH. The small custom
// example bounds model spend and makes private memory and isolation observable.
func TestRealDSHManagedRooms(t *testing.T) {
	if *managementRealOutput != "" {
		t.Setenv("TEAMX_DSH_MANAGEMENT_REAL", "1")
		t.Setenv("TEAMX_DSH_OUTPUT", *managementRealOutput)
	}
	if *managementEnvFile != "" {
		t.Setenv("TEAMX_DSH_ENV_FILE", *managementEnvFile)
	}
	if *managementBaseURL != "" {
		t.Setenv("MODEL_BASE_URL", *managementBaseURL)
	}
	if os.Getenv("TEAMX_DSH_MANAGEMENT_REAL") != "1" {
		t.Skip("set TEAMX_DSH_MANAGEMENT_REAL=1 for real managed Room acceptance")
	}
	output := os.Getenv("TEAMX_DSH_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("TEAMX_DSH_OUTPUT must be a new absolute evidence directory")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	d := &realRoomDriver{cliDriver: cliDriver{ctx: ctx, dir: filepath.Join(output, "rooms-root")}, output: output, report: map[string]any{"scenario": "multi-room-runtime-step-4", "status": "failed", "recordedAt": time.Now().UTC().Format(time.RFC3339)}}
	defer func() {
		if err := d.stopReal(); err != nil {
			d.report["cleanupError"] = err.Error()
			d.report["status"] = "failed"
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
		t.Log("managed rooms report:", filepath.Join(output, "report.json"))
	}()
	if err := d.acceptManagedRooms(t); err != nil {
		d.report["error"] = err.Error()
		t.Fatal(err)
	}
	d.report["status"] = "passed"
}
