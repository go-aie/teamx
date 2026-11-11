//go:build integration || e2e

package room

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Real Chromium and TeamX server; remote metadata and model continuation use
// separate evidence. This validates the browser's fixed form contract.
func TestGitLabProjectSelectionConversationBrowser(t *testing.T) {
	if os.Getenv("TEAMX_CONNECT_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	t.Setenv("USER", humanCaller.ID)
	m, _, _, _, req := gitlabProjectSelectionFixture(t)
	server := httptest.NewServer(m)
	defer server.Close()
	m.config.endpoint = server.URL
	mode := "inbox"
	req.Notify = &mode
	selectionCall(t, m, "start", req, false)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	browser := exec.CommandContext(ctx, "node", "testdata/platform_selection_gitlab_browser.cjs", server.URL, req.OperationID, req.RoomInstance)
	if out, err := browser.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	} else {
		t.Log(string(out))
	}
	receipt := selectionCall(t, m, "status", selectionQuery(req, false), false)
	if receipt.Status != "completed" || receipt.Project == nil || receipt.Project.ID != 8 || receipt.Project.Path != "other/project" {
		t.Fatal(receipt)
	}
}
