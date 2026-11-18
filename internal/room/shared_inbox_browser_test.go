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

func TestSharedInboxBrowserConfigurationAndPreview(t *testing.T) {
	if os.Getenv("TEAMX_SHARED_INBOX_BROWSER") != "1" {
		t.Skip("explicit Chromium acceptance opt-in")
	}
	t.Setenv("USER", humanCaller.ID)
	h, _, source, _ := sharedEventFixtureAt(t, t.TempDir())
	if err := h.m.notifications.Deliver(t.Context(), source.Metadata().Key, source.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	server := browserTestServer(t, h.m)
	room, _ := json.Marshal(h.room.RoomIdentity)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/shared_inbox_browser.cjs", server.URL, string(room))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err, string(output))
	}
	t.Log(string(output))
}
