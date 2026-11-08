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

// Two production DSH processes use real CLI tools. Explicitly enabled because
// this spends model budget; ordinary tests never read credentials or use models.
func TestRealDSHCollaboration(t *testing.T) {
	if os.Getenv("TEAMX_DSH_COLLAB_REAL") != "1" {
		t.Skip("set TEAMX_DSH_COLLAB_REAL=1 for P2-03 real acceptance")
	}
	output := os.Getenv("TEAMX_DSH_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("TEAMX_DSH_OUTPUT must be a new absolute directory")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	d := &realRoomDriver{cliDriver: cliDriver{ctx: ctx, dir: filepath.Join(output, "room")}, output: output,
		report: map[string]any{"scenario": "P2-03 / DA2-01", "status": "failed", "recordedAt": time.Now().UTC().Format(time.RFC3339), "platform": runtime.GOOS + "/" + runtime.GOARCH, "recovery": "normal original-session resume; no crash takeover, replacement or rotation"}}
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
		t.Logf("P2-03 report: %s", filepath.Join(output, "report.json"))
	}()
	if err := d.collaborate(); err != nil {
		d.report["error"] = err.Error()
		t.Fatal(err)
	}
	d.report["status"] = "passed"
}
