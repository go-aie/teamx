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
	m, svc, selection, req, _ := scionRepositorySelectionFixture(t)
	accountID, _ := newOpaqueID()
	account := selection
	account.OperationID = accountID
	account.Input = &platformSelectionInput{Provider: "scion", Kind: "account"}
	account.Draft = &platformSelectionDraft{PlatformID: selection.Input.PlatformID, PlatformVersion: selection.Input.PlatformVersion}
	started := selectionCall(t, m, "start", account, false)
	submit := selectionQuery(account, true)
	submit.Version = started.Version
	selectionCall(t, m, "submit", submit, true)
	feedback, err := svc.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: selection.Caller, ClientMessageID: "scion-account-ready", Text: "Scion 账号已确认，接下来选择仓库。"})
	if err != nil {
		t.Fatal(err)
	}
	selectionCall(t, m, "start", selection, false)
	server := browserTestServer(t, m)
	defer server.Close()
	input, _ := json.Marshal(map[string]any{"room_id": req.RoomID, "room_instance": req.RoomInstance, "operation_id": selection.OperationID, "platform_id": req.PlatformID, "account_operation_id": accountID, "source_seq": selection.SourceSeq, "feedback_seq": feedback.Seq})
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
