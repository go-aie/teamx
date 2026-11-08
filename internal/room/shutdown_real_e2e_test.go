//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Opt-in because this starts the production CLI/DSH and spends model budget.
func TestRealDSHShutdownRecovery(t *testing.T) {
	if os.Getenv("TEAMX_DSH_SHUTDOWN_REAL") != "1" {
		t.Skip("set TEAMX_DSH_SHUTDOWN_REAL=1 for running-tool Ctrl-C/restart acceptance")
	}
	output := os.Getenv("TEAMX_DSH_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("TEAMX_DSH_OUTPUT must be a new absolute directory")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	d := &realRoomDriver{cliDriver: cliDriver{ctx: ctx, dir: filepath.Join(output, "room")}, output: output,
		report: map[string]any{"scenario": "Ctrl-C during a real running tool, then original-session resume",
			"status": "failed", "recordedAt": time.Now().UTC().Format(time.RFC3339), "platform": runtime.GOOS + "/" + runtime.GOARCH}}
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
		t.Logf("shutdown acceptance report: %s", filepath.Join(output, "report.json"))
	}()
	if err := d.shutdownRecovery(); err != nil {
		d.report["error"] = err.Error()
		t.Fatal(err)
	}
	d.report["status"] = "passed"
}
