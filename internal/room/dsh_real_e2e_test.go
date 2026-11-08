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

// This is an operator-enabled acceptance, using the compiled Room CLI and the
// production DSH backend. The ordinary suite never loads credentials or a model.
func TestRealDSHRoom(t *testing.T) {
	if os.Getenv("TEAMX_DSH_ROOM_REAL") != "1" {
		t.Skip("set TEAMX_DSH_ROOM_REAL=1 for real Room/DSH acceptance")
	}
	output := os.Getenv("TEAMX_DSH_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("TEAMX_DSH_OUTPUT must be a new absolute evidence directory")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	d := &realRoomDriver{cliDriver: cliDriver{ctx: ctx, dir: filepath.Join(output, "room")},
		output: output, report: map[string]any{
			"scenario": "DA1-04", "status": "failed", "recordedAt": time.Now().UTC().Format(time.RFC3339),
			"platform":   runtime.GOOS + "/" + runtime.GOARCH,
			"permission": "workspace-write + ask; extra approval cancelled",
			"recovery":   "normal shutdown permits lazy original-session resume; crash takeover unsupported",
		}}
	// Cleanup and evidence also run on setup/readiness/model failures.
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
		t.Logf("acceptance report: %s", filepath.Join(output, "report.json"))
	}()
	if err := d.accept(); err != nil {
		d.report["error"] = err.Error()
		t.Fatal(err)
	}
	d.report["status"] = "passed"
}
