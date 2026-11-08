//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

func TestLarkNotificationRecoversConsumedBindingFailureLocally(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	member := h.members[0]
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	if err := h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 1,
		Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.m)
	defer server.Close()
	cli := contractCLI(t, server.URL, h.svc, id)
	// Reproduce the result saved by the previous resolver, including successful
	// Inbox consumption. No live state or real connection is changed by this test.
	h.m.mu.Lock()
	resolver := h.m.referenceResolvers["lark"]
	h.m.referenceResolvers["lark"] = func(context.Context, MemberIdentity, platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		return platform.TargetResolution{}, platform.AccessContext{}, ErrRequest
	}
	h.m.mu.Unlock()
	var page agentReferenceInboxPage
	if err := cli([]string{"inbox", "read"}, &page); err != nil || len(page.Entries) != 1 || len(page.Followups) != 0 || page.Entries[0].Source.ReasonCode != "binding_unavailable" {
		t.Fatal("did not reproduce old saved failure", page, err)
	}
	h.m.mu.Lock()
	h.m.referenceResolvers["lark"] = resolver
	h.m.mu.Unlock()
	if err := os.Remove(filepath.Join(h.dir, "lark-cli")); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(h.svc.store.dir, "inboxes", id.MembershipID)
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	original, consumption := read(inboxEntryName(1)), read("consumption.json")
	source := page.Entries[0].Source
	var resolved agentSourceView
	for i := 0; i < 2; i++ {
		if err := cli([]string{"matters", "resolve", "--seq", "1", "--source", source.SourceRef, "--turn", "1"}, &resolved); err != nil ||
			resolved.ObjectRef == "" || resolved.SourceRef != source.SourceRef || resolved.Seq != 1 || resolved.Resolution.EvidenceSource != lark.NotificationEvidence {
			t.Fatal("could not recover locally", resolved, err)
		}
	}
	if !bytes.Equal(original, read(inboxEntryName(1))) || !bytes.Equal(consumption, read("consumption.json")) {
		t.Fatal("local resolution changed original evidence or consumption")
	}
	units, err := h.svc.store.listFollowups(id, followupQuery{Limit: 25})
	if err != nil || len(units.Items) != 1 || units.Items[0].ObjectRef != resolved.ObjectRef {
		t.Fatal("recovery duplicated followups", units, err)
	}
	link, err := h.svc.store.readFollowupSource(id, 1, source.SourceRef)
	if err != nil || link.Access != (platform.AccessContext{}) || link.DeliveryID != resolved.DeliveryID || link.LastAttempt != nil {
		t.Fatal("snapshot borrowed live access", link, err)
	}
	// Resume through the actual Store decoder; snapshot evidence must survive a
	// restart without contacting the removed executable or requiring a binding.
	storeDir := h.svc.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(storeDir, id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reopened, err := store.readFollowupSource(id, 1, source.SourceRef)
	if err != nil || !reflect.DeepEqual(link, reopened) {
		t.Fatal("saved notification reference did not survive restart", reopened, err)
	}
}

func TestLarkNotificationResolutionIsBoundToSavedEnvelope(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	member := h.members[0]
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	h.svc.store.mu.Lock()
	entry, err := h.svc.store.inboxEntryLocked(id, 1)
	h.svc.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	resolution := lark.ResolveNotification(entry.Item.Source, time.Now().UTC())
	if err := validateFollowupResolution(entry, resolution, platform.AccessContext{}); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*platform.TargetResolution){
		"message":      func(r *platform.TargetResolution) { r.Target.Message.Value = "om_other" },
		"root":         func(r *platform.TargetResolution) { r.Target.Object.ID.Value = "om_other" },
		"chat":         func(r *platform.TargetResolution) { r.Target.Containers[0].ID.Value = "oc_other" },
		"tenant":       func(r *platform.TargetResolution) { r.Target.Tenant = r.Account.Tenant },
		"account":      func(r *platform.TargetResolution) { r.Account.ID.Value = "ou_other" },
		"evidence":     func(r *platform.TargetResolution) { r.EvidenceSource = "unknown" },
		"remote_claim": func(r *platform.TargetResolution) { r.EvidenceSource = "" },
	} {
		t.Run(name, func(t *testing.T) {
			var changed platform.TargetResolution
			b, _ := json.Marshal(resolution)
			if err := json.Unmarshal(b, &changed); err != nil {
				t.Fatal(err)
			}
			mutate(&changed)
			if err := validateFollowupResolution(entry, changed, platform.AccessContext{}); err == nil {
				t.Fatal("accepted identity not established by saved envelope")
			}
		})
	}
}
