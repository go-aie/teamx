//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	computerdocker "github.com/go-aie/teamx/internal/computer/docker"
)

func applyManagedBrowser(t *testing.T, c *computerController, endpoint string, enabled bool) {
	t.Helper()
	progress, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c.advance()
			if c.members != nil {
				c.members.advance()
			}
			select {
			case <-progress.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	defer func() { stop(); <-done }()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	value := "false"
	if enabled {
		value = "true"
	}
	cmd := exec.CommandContext(ctx, "node", "testdata/computer_settings_real.cjs", endpoint, value)
	cmd.Env = os.Environ()
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal("actual settings UI", err, string(output))
	}
	t.Log(string(output))
	if c.current.Load().Enabled != enabled {
		t.Fatal("settings UI did not apply global intent")
	}
}
func managedAcceptanceSite(t *testing.T, c *computerController) {
	t.Helper()
	c.mu.Lock()
	d := *c.journal.Docker
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for _, args := range [][]string{{"cp", "testdata/computer_view_site.py", d.Container + ":/opt/site.py"}, {"exec", "-d", d.Container, "python3", "/opt/site.py"}} {
		cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", d.Host}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
}
func managedAcceptanceStatus(w http.ResponseWriter, r *http.Request, c *computerController) {
	c.mu.Lock()
	d := *c.journal.Docker
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "--host", d.Host, "exec", d.Container, "python3", "-c", "import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:18765/status',timeout=5).read().decode())")
	raw, err := cmd.Output()
	if err != nil {
		http.Error(w, "fixture unavailable", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}
func cleanupManagedAcceptance(t *testing.T, c *computerController) {
	t.Helper()
	c.cancel()
	c.finishDockerWorker()
	c.mu.Lock()
	if c.journal.Docker == nil {
		c.mu.Unlock()
		return
	}
	d := *c.journal.Docker
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	if err := computerdocker.New().Stop(ctx, &d); err != nil {
		t.Error("disposable stop", err)
		return
	}
	removed, volumesRemoved := false, false
	for index, args := range [][]string{{"rm", d.Name}, {"volume", "rm", d.Name + "-state", d.Name + "-files"}} {
		if b, err := exec.CommandContext(ctx, "docker", append([]string{"--host", d.Host}, args...)...).CombinedOutput(); err != nil {
			t.Error("disposable cleanup", err, string(b))
		} else if index == 0 {
			removed = true
		} else {
			volumesRemoved = true
		}
	}
	if dir := os.Getenv("TEAMX_COMPUTER_VIEW_EVIDENCE"); dir != "" {
		raw, _ := json.MarshalIndent(map[string]any{"container": d.Name, "stopped": d.Stopped, "removed": removed, "volumes_removed": volumesRemoved, "test_failed": t.Failed()}, "", "  ")
		_ = os.WriteFile(filepath.Join(dir, "managed-cleanup.json"), raw, 0600)
	}
}

// Verify actual new Session snapshots after the same global application has
// finished. Real model request inspection remains the combined-model gate.
func assertManagedMemberCapabilitiesOff(t *testing.T, x *computerMembers, ids ...MemberIdentity) {
	t.Helper()
	for _, id := range ids {
		dir := x.memberDirectory(id)
		opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(dir, "agents", id.MembershipID, "session.json")}
		ref, exists, err := readSessionReference(opts.StatePath, id)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			continue
		}
		cap, err := loadComputerCapability(opts, ref.Directory)
		if err != nil && err != io.EOF {
			t.Fatal(err)
		}
		if cap.Enabled {
			t.Fatal("disabled global left Computer capability in current Session")
		}
	}
}

func computerNativeResultError(raw json.RawMessage) bool {
	var result struct {
		IsError bool `json:"isError"`
	}
	return json.Unmarshal(raw, &result) != nil || result.IsError
}
