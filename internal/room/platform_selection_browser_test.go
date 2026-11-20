//go:build e2e

package room

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestPlatformSelectionConversationBrowser(t *testing.T) {
	requirePlatformSelectionBrowser(t)
	t.Setenv("USER", humanCaller.ID)
	h, member := platformMemberHarness(t)
	platformSelectionConversation(t, h, member, "inbox", runPlatformSelectionBrowser)
}

func TestDefaultAssistantPlatformOnboardingBrowser(t *testing.T) {
	requirePlatformSelectionBrowser(t)
	defaultAssistantPlatformOnboarding(t, "inbox", runPlatformSelectionBrowser)
}

func requirePlatformSelectionBrowser(t *testing.T) {
	t.Helper()
	if os.Getenv("TEAMX_SELECTION_BROWSER") != "1" {
		t.Skip("set TEAMX_SELECTION_BROWSER=1 for local Chromium selection acceptance")
	}
}

func runPlatformSelectionBrowser(t *testing.T, endpoint, accountID, groupID string, h *larkHarness) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 70*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/platform_selection_browser.cjs", endpoint, accountID, groupID, h.c.ID, h.room.RoomInstance)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Chromium selection: %v\n%s", err, out)
	}
	t.Log(string(out))
}
