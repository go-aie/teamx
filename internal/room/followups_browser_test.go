//go:build e2e && (darwin || linux)

package room

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Serves the actual management app with an isolated local Store. A browser
// operator checks navigation, receipts, Room reply and policy correction; the
// hold helper independently verifies persisted reply_to and revocation.
func TestReferenceFollowupsBrowser(t *testing.T) {
	if os.Getenv("TEAMX_REFERENCE_BROWSER") != "1" {
		t.Skip("explicit browser fixture opt-in required")
	}
	output := os.Getenv("TEAMX_REFERENCE_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("new absolute evidence directory required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"scenario": "R6-browser", "status": "failed", "remote_writes": 0, "model": "not involved", "recorded_at": time.Now().UTC()}
	defer func() {
		if err := os.WriteFile(filepath.Join(output, "report.json"), mustContractJSON(report), 0600); err != nil {
			t.Error(err)
		}
	}()
	m := testManager(t, filepath.Join(output, "rooms-root"), managerTestProfiles(t))
	r := managerCreate(t, m, "r6-browser")
	svc := eventLiveService(t, &eventHarness{m: m, room: r})
	adapter := &referenceContractAdapter{}
	adapter.install(m, svc)
	m.referenceResolvers["gitlab"] = referenceTestResolver
	state, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: r.RoomIdentity, AgentID: state.Members[0].AgentID, MembershipID: state.Members[0].MembershipID}
	source := referenceTestSource("browser-object")
	contractDeliver(t, m, svc, id, source, "R6 对象来源：本地浏览器夹具，不是实时平台通知。")
	source.ItemKey = "browser-discussion"
	source.Reference.DiscussionID.Namespace, source.Reference.DiscussionID.Value = "gitlab:rest:discussion", "browser-thread"
	source.Reference.NoteID.Namespace, source.Reference.NoteID.Value = "gitlab:rest:id", "12345"
	contractDeliver(t, m, svc, id, source, "R6 讨论来源：浏览器验收夹具。")
	if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 2, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 2}}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(m)
	defer server.Close()
	cli := contractCLI(t, server.URL, svc, id)
	var page referenceInboxPage
	if err := cli([]string{"inbox", "read"}, &page); err != nil || len(page.Followups) != 1 {
		t.Fatal(err)
	}
	u, err := svc.store.ensureDiscussionFollowup(id, 2, 1, page.Sources[1].TargetRef)
	if err != nil {
		t.Fatal(err)
	}
	// Native identities here are fixture evidence, not a real GitLab query.
	referenceBrowserHold(t, output, server.URL, id, svc, u, report)
	if _, err := svc.store.readFollowup(id, u.ObjectRef, u.Ref); err != nil {
		t.Fatal(err)
	}
	report["status"] = "passed"
}
