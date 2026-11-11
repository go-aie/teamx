package room

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Actual Chromium and TeamX HTTP with temporary data and a controlled backend.
// No production directory, model or external platform is used.
func TestDirectConversationBrowser(t *testing.T) {
	directConversationBrowser(t, false)
}

func TestDefaultAssistantRestorationBrowser(t *testing.T) {
	directConversationBrowser(t, true)
}

func directConversationBrowser(t *testing.T, restore bool) {
	t.Helper()
	if os.Getenv("TEAMX_DM_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	t.Setenv("USER", humanCaller.ID)
	var m *roomManager
	if restore {
		m, _, _ = deletedAssistantFixture(t)
		m.factory = controlledActivationFactory(nil, nil)
	} else {
		m = directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
		ensureAssistant(t, m)
		assistant := assistantSummary(t, m)
		m.teams = map[string]teamDefinition{"sample": testTeamDefinition(t)}
		team := createTeamRoom(t, m, "browser-team")
		m.factory = controlledActivationFactory(nil, nil)
		openRoomOK(t, m, assistant, "browser-open-assistant")
		openRoomOK(t, m, team, "browser-open-team")
	}
	server := httptest.NewServer(m)
	defer server.Close()
	m.config.endpoint = server.URL
	if restore {
		if err := m.restoreRuntimes(); err != nil {
			t.Fatal(err)
		}
		awaitRecovery(t, m)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := []string{"testdata/direct_conversation_browser.cjs", server.URL}
	if restore {
		args = append(args, "restore")
	}
	cmd := exec.CommandContext(ctx, "node", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Chromium: %v\n%s", err, output)
	}
	t.Log(string(output))
}
