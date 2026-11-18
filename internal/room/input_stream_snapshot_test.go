package room

import (
	"encoding/json"
	"reflect"
	"testing"

	inputstream "github.com/go-aie/teamx/internal/inputstreamtest"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestInputCatalogAdaptsCommittedSnapshotsWithoutConsumption(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	roomCall[PostReceipt](t, h.roomHarness, h.post(humanCaller, "scope", "只读核查"))
	for _, key := range []string{"one", "two"} {
		request := h.receive(key)
		request.Item.Source.Reference.RecipientID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: "9"}
		inboxCall[InboxReceipt](t, h, request)
	}
	page := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
	room, err := h.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	before := inboxCall[InboxStatus](t, h, InspectInbox{MemberIdentity: h.id})
	consumed := inboxConsumption{Version: 1, MemberIdentity: h.id}
	admissions := map[uint64]notification.Admission{1: {Verified: true, Decision: notification.AdmissionAllow}, 2: {ReasonCode: "relationship_pending"}}
	records, err := projectStoredInputRecords(h.id, room, page, consumed, admissions, frozenInputCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	owner := inputstream.Member{h.id.RoomID, h.id.RoomInstance, h.id.MembershipID}
	catalog, err := inputstream.NewCatalog(owner, 1, []inputstream.Descriptor{inputstream.GitLabCapability().Descriptor}, records, inputstream.Preferences{})
	if err != nil {
		t.Fatal(err)
	}
	// Both records share a context; missing admission prevents previewing either
	// through the catalog, preserving the existing admission boundary.
	overview, err := catalog.Overview(inputstream.Query{Turn: 1})
	if err != nil || overview.Entries != 3 || overview.Blocked != 1 {
		t.Fatal(overview, err)
	}
	directory, _, err := catalog.Directory()
	if err != nil || len(directory.Streams) != 1 || directory.Streams[0].Kind != "room" {
		t.Fatal(directory, err)
	}
	list, err := catalog.List(inputstream.Query{Turn: 1, State: "blocked"})
	if err != nil || list.Matched != 1 {
		t.Fatal(list, err)
	}
	if _, err := catalog.Preview(inputstream.PreviewRequest{Turn: 1, Ref: list.Streams[0].Ref}); err == nil {
		t.Fatal("preview bypassed admission")
	}
	after := inboxCall[InboxStatus](t, h, InspectInbox{MemberIdentity: h.id})
	if !reflect.DeepEqual(before, after) {
		t.Fatal("catalog changed Inbox state", before, after)
	}
	current, err := h.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(room)
	b, _ := json.Marshal(current)
	if string(a) != string(b) {
		t.Fatal("catalog changed Room state")
	}
	if _, err := projectStoredInputRecords(h.memberID("B", "generation-B"), room, page, consumed, admissions, frozenInputCapabilities); err != ErrIdentity {
		t.Fatal("cross-member snapshot accepted", err)
	}
	admissions[2] = notification.Admission{Verified: true, Decision: notification.AdmissionExcluded}
	records, err = projectStoredInputRecords(h.id, room, page, consumed, admissions, frozenInputCapabilities)
	if err != nil || len(records) != 2 {
		t.Fatal("excluded record returned", records, err)
	}
}
