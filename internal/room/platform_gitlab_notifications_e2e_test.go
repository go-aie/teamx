//go:build e2e

package room

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestNotificationReceptionBrowser(t *testing.T) {
	if os.Getenv("TEAMX_RECEPTION_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	verifyTodoAccount(t, m, platformTestSave(t, m, platformTestRequest(m)), 101)
	room := managerCreate(t, m, "reception-browser")
	eventLiveService(t, &eventHarness{m: m, room: room})
	server := httptest.NewServer(m)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/notification_reception_browser.cjs", server.URL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
