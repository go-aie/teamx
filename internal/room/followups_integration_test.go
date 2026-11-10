//go:build integration || e2e

package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestReferenceFollowupsManagedBindingAssembly(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, connection, members := cliToolFixture(t, "exit 99\n") // native source IDs must not launch the process
	member := members[0]
	bindCLITool(t, m, room.RoomIdentity, member, connection, "gitlab", 0)
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	if err := svc.store.initReferenceFollowups(id); err != nil {
		t.Fatal(err)
	}
	h := &inboxHarness{id: id}
	q := h.receive("native")
	q.Item.Source = referenceTestSource("native")
	q.Item.Source.Instance = "https://gitlab.invalid"
	q.Item.Source.Reference.RecipientID.Value = "101"
	if err := svc.receiveInbox(context.Background(), inboxTarget(id), q.Item); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 1, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	req := apiRequest{ServiceInfo: svc.info, Caller: agentCaller(id.AgentID, id.MembershipID), TurnNumber: 1, Limit: 25}
	page, err := m.readReferenceInbox(context.Background(), req)
	if err != nil || len(page.Followups) != 1 || page.Sources[0].Status != platform.ResolutionResolved {
		t.Fatal(page, err)
	}
	m.mu.Lock()
	pending := len(m.platformCalls)
	m.mu.Unlock()
	if pending != 0 {
		t.Fatal("reference process ownership leaked")
	}
	req.ServiceEpoch = "stale"
	if _, err := m.readReferenceInbox(context.Background(), req); !errors.Is(err, ErrIdentity) {
		t.Fatal("stale service accepted", err)
	}
}

func TestReferenceFollowupsHundredNotificationsRecovery(t *testing.T) {
	// This recovery fixture performs hundreds of durable writes and full source
	// scans; it has no ten-second latency contract, especially under -race.
	h := newInboxHarnessWithTimeout(t, InboxLimits{}, time.Minute)
	initReferenceTest(t, h)
	for i := 1; i <= 100; i++ {
		key := fmt.Sprint(i)
		source := referenceTestSource(key)
		if i%2 == 0 {
			source.Channel = "events"
		} // independent source receipts, one native MR
		receiveReference(t, h, key, source)
	}
	svc := referenceTestService(h)
	calls := 0
	resolve := func(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		calls++
		return referenceTestResolver(ctx, id, source)
	}
	var objectRef, unitRef string
	for batch := 0; batch < 4; batch++ {
		turn := uint64(batch + 1)
		referenceRunning(t, h, turn, uint64(batch*25), uint64((batch+1)*25))
		page, err := svc.readReferenceInbox(h.ctx, h.id, turn, 25, resolve)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Entries) != 25 || len(page.Sources) != 25 || len(page.Followups) != 1 {
			t.Fatal("not deduplicated", page)
		}
		if batch == 0 {
			objectRef, unitRef = page.Followups[0].ObjectRef, page.Followups[0].Ref
			before := calls
			again, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, resolve)
			if err != nil || calls != before || !reflect.DeepEqual(page, again) {
				t.Fatal("reread was not stable", calls, err)
			}
			if _, err := h.store.updateFollowup(h.id, followupUpdate{ObjectRef: objectRef, UnitRef: unitRef, Revision: 1, TurnNumber: 1, Status: "waiting_user", Summary: "排期待确认", Next: "等待用户回复"}); err != nil {
				t.Fatal(err)
			}
		}
		if page.Followups[0].ObjectRef != objectRef {
			t.Fatal("split object identity")
		}
		inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: turn, Receipt: page.Batch.Receipt})
	}
	page, err := h.store.listFollowups(h.id, followupQuery{Limit: 25})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].Status != "waiting_user" {
		t.Fatal(page, err)
	}
	sources, err := h.store.listFollowupSources(h.id, objectRef, 0, 7)
	if err != nil || sources.Total != 100 || len(sources.Items) != 7 || sources.Complete {
		t.Fatal(sources, err)
	}
	if _, err := os.Stat(filepath.Join(h.store.dir, "agents", h.id.MembershipID, "matters")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy matters written", err)
	}
	original, err := os.ReadFile(filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, inboxEntryName(1)))
	if err != nil {
		t.Fatal(err)
	}
	h.reload(t, true)
	u, err := h.store.readFollowup(h.id, objectRef, unitRef)
	if err != nil || u.Revision != 2 || u.Status != "waiting_user" {
		t.Fatal(u, err)
	}
	status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if status.ConsumedThrough != 100 || status.LatestSeq != 100 {
		t.Fatal(status)
	}
	if receipt := receiveReference(t, h, "1", referenceTestSource("1")); receipt.Seq != 1 {
		t.Fatal("delivery dedup changed", receipt)
	}
	after, err := os.ReadFile(filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, inboxEntryName(1)))
	if err != nil || string(original) != string(after) {
		t.Fatal("original delivery changed", err)
	}
	referenceRunning(t, h, 5, 75, 100)
	svc = referenceTestService(h)
	if _, err := svc.resolveFollowupEvent(h.ctx, h.id, 1, 5, false, func(context.Context, MemberIdentity, platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		t.Fatal("recovery unexpectedly re-resolved")
		return platform.TargetResolution{}, platform.AccessContext{}, nil
	}); err != nil {
		t.Fatal(err)
	}
}
