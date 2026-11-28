//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestScionRepositorySelectionBrowser(t *testing.T) {
	if os.Getenv("TEAMX_SELECTION_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	t.Setenv("USER", humanCaller.ID)
	m, _, selection, req, _ := scionRepositorySelectionFixture(t)
	selectionCall(t, m, "start", selection, false)
	server := browserTestServer(t, m)
	defer server.Close()
	input, _ := json.Marshal(map[string]any{"room_id": req.RoomID, "room_instance": req.RoomInstance, "operation_id": selection.OperationID, "platform_id": req.PlatformID})
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/scion_repository_picker_browser.cjs", server.URL, string(input), os.Getenv("TEAMX_SCION_PICKER_OUTPUT"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	} else {
		t.Log(string(out))
	}
	m.mu.Lock()
	done, err := m.readPlatformSelectionLocked(selection.OperationID)
	m.mu.Unlock()
	if err != nil || done.Status != "completed" || done.Draft.Repository == nil || done.Draft.Repository.Name != "Owner/repo" {
		t.Fatal(done, err)
	}
}
