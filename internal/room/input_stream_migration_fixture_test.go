package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func saveMigrationFixture(t *testing.T, store *Store, path string, data any) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.save(path, data, inputstream.MaxStateBytes); err != nil {
		t.Fatal(path, err)
	}
}

func TestInputMigrationFixturePreservesDeliveryAndRoomFacts(t *testing.T) {
	old := newInboxHarnessWithTimeout(t, InboxLimits{}, 30*time.Second)
	id := old.id
	for _, text := range []string{"Original operation request", "Reply retained across migration"} {
		roomCall[PostReceipt](t, old.roomHarness, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: text, Text: text})
	}
	unread := roomCall[ReadPage](t, old.roomHarness, ReadMessages{RoomIdentity: id.RoomIdentity, Caller: agentCaller(id.AgentID, id.MembershipID), Mode: ReadUnread, Limit: 1})
	roomCall[ReadConfirmation](t, old.roomHarness, old.roomHarness.confirm(agentCaller(id.AgentID, id.MembershipID), unread.ReadReceipt))

	gitlab := func(key string, withAttachment bool) notification.Item {
		got := old.receive(key).Item
		got.Route = notification.Route{Version: 1, Target: notification.Target{Mode: "inbox", AgentID: id.AgentID, MembershipID: id.MembershipID}}
		got.Source.Reference.RecipientID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: "9"}
		if withAttachment {
			got.Text += " ![saved](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/diagram.png)"
			got.Source.ProjectID = 123
			got.Source.URL = "https://gitlab.example.com/group/project/-/merge_requests/42"
			got.Content = &notification.Content{Structured: true, Text: got.Text, Attachments: []notification.Attachment{{Type: "image", Reference: "diagram.png", Status: "not_read"}}}
		}
		return got
	}
	inboxCall[InboxReceipt](t, old, ReceiveInbox{MemberIdentity: id, Item: gitlab("expired", false)})
	inboxCall[InboxReceipt](t, old, ReceiveInbox{MemberIdentity: id, Item: gitlab("confirmed", true)})
	receiveTestTaskEvent(t, old, taskEventFixture("consumed-event"))
	excluded := storedLark(id, "excluded", "oc_fixture", "saved conversation", "omt_fixture", "excluded by old admission")
	inboxCall[InboxReceipt](t, old, ReceiveInbox{MemberIdentity: id, Item: excluded})
	inboxCall[InboxReceipt](t, old, ReceiveInbox{MemberIdentity: id, Item: gitlab("unconfirmed", false)})
	receiveTestTaskEvent(t, old, taskEventFixture("pending-event"))

	inboxRunning(t, old, 1, 3)
	first := inboxCall[InboxBatchPage](t, old, ReadInboxBatch{MemberIdentity: id, TurnNumber: 1})
	links := linkInboxPage(t, old, first)
	if len(links) != 2 || links[1].ObjectRef == "" || confirmInboxPage(t, old, first).ConsumedThrough != 3 {
		t.Fatal("legacy confirmed fixture invalid", links)
	}
	agent, err := old.store.LoadAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	agent.Turn.Status = "completed"
	if err := old.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	if got := inboxCall[InboxStatus](t, old, ReclaimInbox{id, 1}); got.ExpiredThrough != 1 {
		t.Fatal(got)
	}
	agent.TurnNumber = 2
	agent.LastOfferedInboxSeq, agent.LastOfferedInboxThrough = 6, 3
	agent.Turn = &TurnRecord{Number: 2, Status: "running", InboxStart: 3, InboxSnapshotSeq: 6}
	if err := old.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	scheduling := memberScheduling{Version: formatVersion, MemberIdentity: id, Enabled: false, Used: 2, InboxContinueCount: 1,
		InboxRecovery: &inboxRecovery{Start: 3, SnapshotSeq: 6, ConsumedThrough: 3, Since: time.Now().UTC()}}
	if err := old.store.saveScheduling(scheduling); err != nil {
		t.Fatal(err)
	}
	outstanding := inboxCall[InboxBatchPage](t, old, ReadInboxBatch{MemberIdentity: id, TurnNumber: 2})
	consumptionFile := filepath.Join(old.store.dir, "inboxes", id.MembershipID, "consumption.json")
	var consumed inboxConsumption
	if err := readSnapshot(consumptionFile, &consumed, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	// Install the saved sparse exclusion in the legacy authority and verify
	// the original reader can reopen it alongside the unconfirmed batch.
	consumed.Skipped = []uint64{4}
	saveMigrationFixture(t, old.store, consumptionFile, consumed)
	old.reload(t, true)
	page := inboxCall[InboxPage](t, old, PreviewInbox{MemberIdentity: id})
	if consumed.Through != 3 || outstanding.Batch.PageThrough != 6 || len(page.Entries) != 5 {
		t.Fatal("legacy cutoff or pending batch", consumed, outstanding.Batch, page.Entries)
	}
	if _, err := old.store.inboxEntryLocked(id, 1); !errors.Is(err, ErrInboxExpired) {
		t.Fatal("legacy body was not reclaimed", err)
	}

	roomState, err := old.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(old.store.dir)); err != nil {
		t.Fatal(err)
	}
	svc, err := startService(dir, id.RoomID, serviceConfig{unifiedInputs: true, endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}, func(s *service, room RoomSnapshot) error {
		return s.loadMembers(room)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := svc.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	target := &roomHarness{id: id.RoomIdentity, addr: svc.room, store: svc.store, ctx: old.ctx}
	p, err := svc.inputStorage(id)
	if err != nil {
		t.Fatal(err)
	}
	converted, err := svc.store.LoadAgent(id)
	if err != nil || converted.Turn == nil || converted.Turn.Status != "interrupted" || converted.TurnNumber != 2 ||
		converted.LastOfferedInboxSeq != 0 || converted.Turn.InboxSnapshotSeq != 0 || converted.InputStateRef == "" {
		t.Fatal("old execution was not closed", converted, err)
	}
	intent, err := svc.store.loadScheduling(id)
	if err != nil || intent.Enabled || intent.Used != 2 || intent.InboxContinueCount != 1 || intent.InboxRecovery != nil {
		t.Fatal("pause intent or recovery migration changed", intent, err)
	}
	if _, has, err := p.repository.CurrentState(); err != nil || has {
		t.Fatal("old page should remain closed", has, err)
	}
	pending, err := p.repository.PendingLocations(nil)
	if err != nil || len(pending) != 1 || pending[0].Seq != 5 {
		t.Fatal("platform pending set changed", pending, err)
	}
	usage, err := p.repository.InspectUsage()
	if err != nil || usage.Records != 3 {
		t.Fatal(usage, err)
	}
	for _, orig := range links {
		link, entry, err := target.store.readFollowupSourceContent(id, 0, orig.SourceRef)
		if err != nil || link.ObjectRef != orig.ObjectRef || link.TargetRef != orig.TargetRef || link.Location == nil ||
			link.Location.Seq != orig.Seq || (orig.Seq == 1) != (entry == nil) {
			t.Fatal("source/target linkage changed", orig.Seq, link, entry, err)
		}
		unitRef := referenceDigest("unit_", orig.ObjectRef)
		previousUnit, err := old.store.readFollowup(id, orig.ObjectRef, unitRef)
		if err != nil {
			t.Fatal(err)
		}
		savedUnit, err := target.store.readFollowup(id, orig.ObjectRef, unitRef)
		if err != nil || !reflect.DeepEqual(previousUnit, savedUnit) {
			t.Fatal("business unit identity or facts changed", savedUnit, err)
		}
		if entry != nil {
			original := page.Entries[0].Item
			original.CreatedAt = original.CreatedAt.UTC()
			before := findNotificationImages(original.Text, original.Source)
			after := findNotificationImages(entry.Item.Text, entry.Item.Source)
			if !reflect.DeepEqual(entry.Item, original) || len(before.Images) != 1 || !before.Images[0].Supported || !reflect.DeepEqual(before, after) {
				t.Fatal("retained attachment evidence or locator changed", before, after)
			}
		}
	}
	disk, err := target.store.LoadRoom()
	if err != nil || len(disk.Messages) != 4 || !reflect.DeepEqual(disk.Messages[:2], roomState.Messages) ||
		disk.Members[0].ReadThrough != roomState.Members[0].ReadThrough || len(disk.Messages[2].Event.Targets) != 0 ||
		!reflect.DeepEqual(disk.Messages[3].Event.Targets, []string{id.MembershipID}) || disk.Messages[3].Event.ResultRef != page.Entries[4].TaskEvent.ResultRef {
		t.Fatal("Room or event mapping changed", disk, err)
	}
	b := agentCaller("B", "generation-B")
	other := roomCall[ReadPage](t, target, ReadMessages{RoomIdentity: target.id, Caller: b, Mode: ReadUnread, Peek: true})
	if len(other.Messages) != 2 {
		t.Fatal("event leaked to the other member", other.Messages)
	}

	var progress inputUpgradeProgress
	if err := readSnapshot(filepath.Join(dir, inputUpgradeJournal), &progress, inputUpgradeLimit); err != nil || progress.Phase != "completed" {
		t.Fatal(progress, err)
	}
	backup := filepath.Join(dir, inputUpgradeDirectory, progress.ID, "backup")
	source, err := os.ReadFile(consumptionFile)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := os.ReadFile(filepath.Join(backup, "room", "inboxes", id.MembershipID, "consumption.json"))
	if err != nil || !bytes.Equal(previous, source) {
		t.Fatal("backup lost the original pending batch", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "inboxes", id.MembershipID, "inbox.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy Inbox header remains live", err)
	}
	if err := os.RemoveAll(filepath.Join(dir, inputUpgradeDirectory)); err != nil {
		t.Fatal(err)
	}

	converted.TurnNumber = 3
	converted.Turn = &TurnRecord{Number: 3, Status: "running", OfferedUnreadSeq: converted.LastOfferedUnreadSeq, SnapshotSeq: disk.LatestSeq}
	if err := target.store.SaveAgent(converted); err != nil {
		t.Fatal(err)
	}
	session := startPlatformReader(t, target, p, svc, 3)
	stream, err := p.repository.StreamAt(pending[0])
	if err != nil {
		t.Fatal(err)
	}
	body := readInputStream(t, session, "first", stream, 1, true)
	if len(body.Entries) != 1 || body.Remaining != 0 || p.repository.Barrier(session.execution) != nil {
		t.Fatal("first formal delivery after startup upgrade", body)
	}
	roomBody := readInputStream(t, session, "room-after-upgrade", roomStreamRef(id), 10, true)
	encoded, err := json.Marshal(roomBody.Entries)
	if err != nil || len(roomBody.Entries) != 2 || !strings.Contains(string(encoded), "Reply retained across migration") ||
		!strings.Contains(string(encoded), page.Entries[4].TaskEvent.ResultRef) || target.list(t).Members[0].ReadThrough != disk.LatestSeq {
		t.Fatal("expected just the original pending message and one targeted event", roomBody, err)
	}
	if retry, err := p.Receive(gitlab("unconfirmed", false), time.Now()); err != nil || retry.Seq != 5 || retry.Partition != pending[0].Partition {
		t.Fatal("retained DeliveryID did not deduplicate", retry, err)
	}
	if added, err := p.Receive(gitlab("new", false), time.Now()); err != nil || added.Seq != 6 {
		t.Fatal("new input did not advance the imported partition high water", added, err)
	}

}
