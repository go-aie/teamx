//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Real Chromium and TeamX server; remote metadata and model continuation use
// separate evidence. This validates the browser's fixed form contract.
func TestGitLabProjectSelectionConversationBrowser(t *testing.T) {
	if os.Getenv("TEAMX_CONNECT_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	testGitLabSelectionBrowser(t, nil)
}

func testGitLabSelectionBrowser(t *testing.T, realRead func(*testing.T, *httptest.Server, *roomManager, *inputSession)) {
	t.Helper()
	t.Setenv("USER", humanCaller.ID)
	m, svc, member, settings, req := gitlabProjectSelectionFixtureFor(t, func(t *testing.T) (*roomManager, *service, MemberState, platformConnectRequest) {
		return connectFixtureWithSource(t, true, "请在 GitLab 选择用于只读检查的项目。不要评论、合并或更改通知。")
	})
	server := browserTestServer(t, m)
	defer server.Close()
	m.config.endpoint = server.URL
	svc.endpoint, svc.cliPath = server.URL, os.Args[0]
	initial, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.loadMembers(initial); err != nil {
		t.Fatal(err)
	}
	if err := svc.attachMembers(initial); err != nil {
		t.Fatal(err)
	}
	if got := gitlabMemberApply(t, m, "notifications", settings); got.Status != "completed" {
		t.Fatal("could not seed member notification settings")
	}
	id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	gitlab := storedGitLab(id, "gitlab-partition", "events", "7", "42", "GitLab partition evidence")
	m.mu.Lock()
	account, _, err := m.resolvePlatformAccountLocked(id.RoomIdentity, settings.Input.PlatformID, "gitlab")
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	gitlab.Source.Instance, gitlab.Source.Reference.RecipientID = account.Identity.Instance, account.Identity.ID
	lark := storedLark(id, "lark-partition", "oc_fixture", "Fixture group", "omt_fixture", "Feishu partition evidence")
	lark.Content.Conversation.Type = "group"
	for _, item := range []notification.Item{gitlab, lark} {
		if err := svc.receiveInbox(t.Context(), inboxTarget(id), item); err != nil {
			t.Fatal(err)
		}
	}
	mode := "inbox"
	req.Notify = &mode
	selectionCall(t, m, "start", req, false)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	browser := exec.CommandContext(ctx, "node", "testdata/platform_selection_gitlab_browser.cjs", server.URL, req.OperationID, req.RoomInstance, id.MembershipID)
	if out, err := browser.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	} else {
		t.Log(string(out))
	}
	receipt := selectionCall(t, m, "status", selectionQuery(req, false), false)
	if receipt.Status != "completed" || receipt.Project == nil || receipt.Project.ID != 8 || receipt.Project.Path != "other/project" {
		t.Fatal(receipt)
	}
	state, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	var eventSeq uint64
	for _, msg := range state.Messages {
		if msg.Event != nil && msg.Event.OperationID == req.OperationID {
			found++
			eventSeq = msg.Seq
			if msg.Event.Kind != "user_selection" || len(msg.Event.Targets) != 1 ||
				msg.Event.Targets[0] != member.MembershipID || msg.Event.SourceSeq != req.SourceSeq {
				t.Fatal("selection event did not preserve its source and recipient")
			}
		}
	}
	if found != 1 {
		t.Fatalf("expected exactly one Room event for the selection, found %d", found)
	}
	wrong := selectionQuery(req, false)
	other := state.Members[1]
	wrong.Caller = agentCaller(other.AgentID, other.MembershipID)
	if w := managerHTTP(m, "/v1/platforms/selection/status", wrong); w.Code != http.StatusForbidden {
		t.Fatal("another member could inspect the private operation", w.Code)
	}
	if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, InputStateRef: "input-state.json",
		TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: state.LatestSeq}}); err != nil {
		t.Fatal(err)
	}
	storage, err := svc.inputStorage(id)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := newInputSession(t.Context(), svc, storage, "browser-selection", 1, false, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	gitlabSnapshot, err := storage.repository.Snapshot("gitlab")
	if err != nil {
		t.Fatal(err)
	}
	gitlabStream, err := storage.repository.StreamAt(inputstream.Location{Partition: gitlabSnapshot.Partition, Seq: 1})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := reader.catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	directory, _, err := catalog.Directory()
	if err != nil {
		t.Fatal(err)
	}
	highAttention := false
	for _, stream := range directory.Streams {
		highAttention = highAttention || stream.Ref == gitlabStream && stream.Attention == "high"
	}
	if !highAttention {
		t.Fatal("live attention change did not reach the next Agent directory")
	}
	if realRead != nil {
		realRead(t, server, m, reader)
	} else {
		page := readInputStream(t, reader, "selection-result", roomStreamRef(id), 20, true)
		b, err := json.Marshal(page.Entries)
		if err != nil {
			t.Fatal(err)
		}
		var entries []inputRoomBody
		if err := json.Unmarshal(b, &entries); err != nil {
			t.Fatal(err)
		}
		found = 0
		for _, entry := range entries {
			if entry.Event != nil && entry.Event.Result == "platforms/selection/"+req.OperationID && entry.Seq == eventSeq {
				found++
			}
		}
		if found != 1 {
			t.Fatal("the original agent did not read the exact selection result")
		}
	}
	status := selectionCall(t, m, "status", selectionQuery(req, false), false)
	if status.Continuation.State != "consumed" || status.Project == nil || status.Project.ID != receipt.Project.ID {
		t.Fatal("selection status did not reflect the agent read", status.Continuation)
	}
	ack := selectionQuery(req, false)
	ack.ResultVersion = status.ResultVersion
	if acked := selectionCall(t, m, "acknowledge", ack, false); acked.Continuation.State != "handled" {
		t.Fatal("the acknowledged version was not handled", acked.Continuation)
	}
	server.Close()
	root := m.store.dir
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openRoomManager(root, defaultMaxTurns, &inputUpgradeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	reopened.config.unifiedInputs = true
	restarted := eventLiveService(t, &eventHarness{m: reopened, room: roomSummary{RoomIdentity: id.RoomIdentity}})
	reopened.deliverConnectNotifications(t.Context())
	saved, err := reopened.readSelectionNotificationLocked(status.platformSelection)
	if err != nil || saved.HandledVersion != status.ResultVersion || saved.RoomReceipt == nil || saved.RoomReceipt.Seq != eventSeq {
		t.Fatal("selection continuation did not survive restart", err)
	}
	after, err := restarted.store.LoadRoom()
	if err != nil || after.LatestSeq != state.LatestSeq {
		t.Fatal("restart changed the Room event sequence", err)
	}
}
