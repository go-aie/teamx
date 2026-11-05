package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type inboxHarness struct {
	*roomHarness
	id   MemberIdentity
	addr actorx.Addr
}

func newInboxHarness(t *testing.T, limits InboxLimits, options ...actorx.Option) *inboxHarness {
	t.Helper()
	room := historyRoom(0, "")
	room.Members = append(room.Members, MemberState{AgentID: "B", MembershipID: "generation-B", Runner: room.Members[0].Runner})
	r := newRoomHarness(t, room, nil, options...)
	id := r.memberID("A", "generation-A")
	addr, err := SpawnAgentInbox(r.rt, r.store, id, limits)
	if err != nil {
		t.Fatal(err)
	}
	return &inboxHarness{roomHarness: r, id: id, addr: addr}
}

func inboxCall[R any](t *testing.T, h *inboxHarness, msg any) R {
	t.Helper()
	v, err := h.addr.Call[R](h.ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func inboxError[R any](t *testing.T, h *inboxHarness, msg any, want error) {
	t.Helper()
	if _, err := h.addr.Call[R](h.ctx, msg); !errors.Is(err, want) {
		t.Fatalf("%T: want %v, got %v", msg, want, err)
	}
}

func (h *inboxHarness) receive(key string) ReceiveInbox {
	return ReceiveInbox{MemberIdentity: h.id, Item: notification.Item{
		Key: key, DeliveryID: "delivery-" + key, CreatedAt: time.Date(2026, 11, 4, 1, 2, 3, 0, time.UTC), Order: key, Text: "source body " + key,
		Source: platform.SourceRef{Provider: "gitlab", Channel: "events", Instance: "https://gitlab.example.com", SubscriptionID: strings.Repeat("a", 32), ItemKey: key,
			Reference: platform.NotificationReference{Version: 1, ProjectID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "123"},
				Object: platform.ObjectReference{Type: "MergeRequest", ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "9001"}, IID: platform.ExternalID{Namespace: "gitlab:rest:iid", Value: "42"}},
				NoteID: platform.ExternalID{Namespace: "gitlab:graphql:gid", Value: "gid://gitlab/Note/99"}}},
	}}
}

func (h *inboxHarness) stop(t *testing.T) {
	t.Helper()
	if err := h.addr.Stop(nil); err != nil {
		t.Fatal(err)
	}
	if exit, err := h.addr.Wait(h.ctx); err != nil || exit.Reason != nil {
		t.Fatal(exit, err)
	}
}

func (h *inboxHarness) reload(t *testing.T, reopen bool) {
	t.Helper()
	h.stop(t)
	if reopen {
		// Also settle the unrelated Room actor before replacing its Store.
		if err := h.roomHarness.addr.Stop(nil); err != nil {
			t.Fatal(err)
		}
		if _, err := h.roomHarness.addr.Wait(h.ctx); err != nil {
			t.Fatal(err)
		}
		dir := h.store.dir
		if err := h.store.Close(); err != nil {
			t.Fatal(err)
		}
		var err error
		h.store, err = OpenStore(dir, h.id.RoomID)
		if err != nil {
			t.Fatal(err)
		}
	}
	var err error
	h.addr, err = SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAgentInboxDurableReceiptAndRecovery(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	request := h.receive("first")
	request.Item.Text = "source <p> & body"
	first := inboxCall[InboxReceipt](t, h, request)
	if first.MemberIdentity != h.id || first.Seq != 1 {
		t.Fatal(first)
	}
	// Delivery committed but the source has not acknowledged its pending item.
	h.reload(t, true)
	if again := inboxCall[InboxReceipt](t, h, request); again != first {
		t.Fatal("redelivery after lost source ack changed receipt", first, again)
	}
	page := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
	if len(page.Entries) != 1 || !reflect.DeepEqual(page.Entries[0].Item, request.Item) {
		t.Fatal("source evidence lost or duplicate created", page)
	}
	changed := request
	changed.Item.Text = "changed evidence"
	inboxError[InboxReceipt](t, h, changed, ErrConflict)
	changed = request
	changed.Item.Source.URL = "https://gitlab.example.com/g/p/-/merge_requests/42"
	inboxError[InboxReceipt](t, h, changed, ErrConflict)
	changed = request
	changed.Item.Source.Reference.Object.IID.Value = "9001"
	inboxError[InboxReceipt](t, h, changed, ErrConflict)
	status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if status.LatestSeq != 1 || status.Bytes <= 0 || status.WriteError != "" {
		t.Fatal(status)
	}
	info, err := os.Stat(filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, inboxEntryName(1)))
	if err != nil || info.Size() != status.Bytes {
		t.Fatal("byte accounting differs from durable encoding", info, status, err)
	}
}

func TestAgentInboxIsolationAndSingleWriter(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	if _, err := SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{}); !errors.Is(err, ErrBusy) {
		t.Fatal("two writers admitted", err)
	}
	bID := h.memberID("B", "generation-B")
	bAddr, err := SpawnAgentInbox(h.rt, h.store, bID, InboxLimits{})
	if err != nil {
		t.Fatal(err)
	}
	b := &inboxHarness{roomHarness: h.roomHarness, id: bID, addr: bAddr}
	request := h.receive("same-source")
	inboxCall[InboxReceipt](t, h, request)
	if page := inboxCall[InboxPage](t, b, PreviewInbox{MemberIdentity: b.id}); len(page.Entries) != 0 {
		t.Fatal("shared member progress", page)
	}
	inboxError[InboxReceipt](t, b, request, ErrIdentity)
	inboxError[InboxPage](t, b, PreviewInbox{MemberIdentity: h.id}, ErrIdentity)
	request.MemberIdentity = b.id
	if receipt := inboxCall[InboxReceipt](t, b, request); receipt.Seq != 1 {
		t.Fatal("receipt shared across members", receipt)
	}
	otherState := historyRoom(0, "")
	otherState.RoomIdentity = RoomIdentity{RoomID: "another-room", RoomInstance: "another-instance"}
	otherRoom := newRoomHarness(t, otherState, nil)
	other := &inboxHarness{roomHarness: otherRoom, id: otherRoom.memberID("A", "generation-A")}
	other.addr, err = SpawnAgentInbox(other.rt, other.store, other.id, InboxLimits{})
	if err != nil {
		t.Fatal(err)
	}
	request.MemberIdentity = other.id
	if receipt := inboxCall[InboxReceipt](t, other, request); receipt.Seq != 1 {
		t.Fatal("receipt shared across stores", receipt)
	}
	wrong := h.id
	wrong.RoomInstance = "different-instance"
	inboxError[InboxPage](t, h, PreviewInbox{MemberIdentity: wrong}, ErrIdentity)
	wrong.RoomID = "different-room"
	inboxError[InboxReceipt](t, h, ReceiveInbox{MemberIdentity: wrong, Item: request.Item}, ErrIdentity)
	// No integration chat messages or member cursors were changed.
	state, err := h.store.LoadRoom()
	if err != nil || state.LatestSeq != 0 || state.Members[0].ReadThrough != 0 || state.Members[1].ReadThrough != 0 {
		t.Fatal(state, err)
	}
	// A same-name replacement receives an independent, empty inbox.
	h.stop(t)
	state.Members[0].MembershipID = "replacement-A"
	if err := h.store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	if _, err := SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{}); !errors.Is(err, ErrIdentity) {
		t.Fatal("removed membership restored", err)
	}
	h.id.MembershipID = "replacement-A"
	h.addr, err = SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if page := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id}); len(page.Entries) != 0 {
		t.Fatal("replacement inherited history", page)
	}
}

func TestAgentInboxPreviewSnapshotAndBounds(t *testing.T) {
	limits := DefaultInboxLimits()
	limits.PageSize, limits.EntryBytes, limits.PageBytes = 2, 4096, 4096
	h := newInboxHarness(t, limits)
	for i := range 3 {
		request := h.receive(fmt.Sprint(i))
		request.Item.Text = strings.Repeat("x", 2100)
		inboxCall[InboxReceipt](t, h, request)
	}
	page := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
	if len(page.Entries) != 1 || page.SnapshotSeq != 3 || page.NextAfter != 1 || !page.HasMore {
		t.Fatal("page byte bound", page)
	}
	inboxCall[InboxReceipt](t, h, h.receive("new"))
	for page.HasMore {
		page = inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id, After: page.NextAfter, SnapshotSeq: &page.SnapshotSeq})
	}
	if page.NextAfter != 3 || page.SnapshotSeq != 3 {
		t.Fatal("new delivery leaked into frozen snapshot", page)
	}
	// Interrupted and repeated preview creates no receipt/cursor to confirm.
	again := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
	if again.Entries[0].Seq != 1 || again.SnapshotSeq != 4 {
		t.Fatal(again)
	}
	empty := uint64(0)
	if p := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id, SnapshotSeq: &empty}); len(p.Entries) != 0 || p.HasMore {
		t.Fatal(p)
	}
	inboxError[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id, Limit: 3}, ErrRequest)
	inboxError[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id, After: 5}, ErrRequest)
	future := uint64(5)
	inboxError[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id, SnapshotSeq: &future}, ErrRequest)
	h.reload(t, false)
	if p := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id}); p.Entries[0].Seq != 1 {
		t.Fatal("preview consumed on rebuild", p)
	}
}

func TestAgentInboxCapacityAndValidation(t *testing.T) {
	for _, mode := range []string{"entries", "bytes", "entry_bytes"} {
		t.Run(mode, func(t *testing.T) {
			limits := DefaultInboxLimits()
			limits.EntryBytes, limits.PageBytes = 4096, 4096
			switch mode {
			case "entries":
				limits.Entries = 1
			case "bytes":
				limits.Bytes = 4096
			}
			h := newInboxHarness(t, limits)
			request := h.receive("one")
			request.Item.Text = strings.Repeat("x", 2100)
			inboxCall[InboxReceipt](t, h, request)
			next := h.receive("two")
			next.Item.Text = request.Item.Text
			if mode == "entry_bytes" {
				next.Item.Text = strings.Repeat("x", 4096)
			}
			inboxError[InboxReceipt](t, h, next, ErrCapacity)
			h.reload(t, false)
			if r := inboxCall[InboxReceipt](t, h, request); r.Seq != 1 {
				t.Fatal("capacity evicted receipt", r)
			}
			inboxError[InboxReceipt](t, h, next, ErrCapacity)
		})
	}
	h := newInboxHarness(t, InboxLimits{})
	for _, change := range []func(*ReceiveInbox){
		func(r *ReceiveInbox) { r.Item.Source.Reference.Object.ID.Namespace = "" },
		func(r *ReceiveInbox) { r.Item.Source.Reference.Version = 2 },
		func(r *ReceiveInbox) { r.Item.Key = "mismatched-source" },
		func(r *ReceiveInbox) { r.Item.CreatedAt = time.Time{} },
		func(r *ReceiveInbox) { r.Item.Text = strings.Repeat("x", maxTextBytes+1) },
		func(r *ReceiveInbox) { r.Item.Order = string([]byte{0xff}) },
	} {
		request := h.receive("invalid")
		change(&request)
		inboxError[InboxReceipt](t, h, request, ErrRequest)
	}
}

func TestAgentInboxCommitBoundaryAndStop(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer onceRelease(release)()
	real := h.store.ops.syncDir
	h.store.ops.syncDir = func(dir string) error {
		once.Do(func() { close(entered); <-release })
		return real(dir)
	}
	request := h.receive("stopped")
	done := make(chan error, 1)
	go func() { _, err := h.addr.Call[InboxReceipt](h.ctx, request); done <- err }()
	<-entered // Rename completed, directory fsync is still pending.
	select {
	case err := <-done:
		t.Fatal("success before durable commit", err)
	default:
	}
	status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if status.LatestSeq != 0 || !status.Committing {
		t.Fatal("uncommitted entry visible", status)
	}
	inboxError[InboxReceipt](t, h, h.receive("conflict"), ErrBusy)
	if err := h.addr.Stop(nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, actorx.ErrStopped) {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { _, err := h.addr.Wait(h.ctx); waited <- err }()
	select {
	case err := <-waited:
		t.Fatal("Wait failed to join file operation", err)
	default:
	}
	if _, err := SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{}); !errors.Is(err, ErrBusy) {
		t.Fatal("new writer admitted before old task settled", err)
	}
	onceRelease(release)()
	if err := <-waited; err != nil {
		t.Fatal(err)
	}
	h.reload(t, false)
	if receipt := inboxCall[InboxReceipt](t, h, request); receipt.Seq != 1 {
		t.Fatal("lost or duplicated interrupted commit", receipt)
	}
}

func TestAgentInboxWriteFailures(t *testing.T) {
	for _, boundary := range []string{"before_rename", "rename_unknown", "fsync_unknown"} {
		t.Run(boundary, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			boom := errors.New("injected disk failure")
			real := h.store.ops
			switch boundary {
			case "before_rename":
				h.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, boom }
			case "rename_unknown":
				h.store.ops.rename = func(from, to string) error {
					if err := real.rename(from, to); err != nil {
						return err
					}
					return boom
				}
			case "fsync_unknown":
				h.store.ops.syncDir = func(string) error { return boom }
			}
			request := h.receive("failure")
			want := error(ErrCommitUnknown)
			if boundary == "before_rename" {
				want = boom
			}
			inboxError[InboxReceipt](t, h, request, want)
			status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
			if status.LatestSeq != 0 || status.Committing {
				t.Fatal("failed write installed state", status)
			}
			if boundary == "before_rename" {
				h.store.ops = real
				inboxCall[InboxReceipt](t, h, request)
			} else {
				if status.WriteError == "" {
					t.Fatal("uncertain write invisible")
				}
				inboxError[InboxReceipt](t, h, request, ErrCommitUnknown)
				if _, err := SpawnAgentInbox(h.rt, h.store, h.memberID("B", "generation-B"), InboxLimits{}); !errors.Is(err, ErrCommitUnknown) {
					t.Fatal("uncertain Store allowed another writer", err)
				}
				h.reload(t, true)
				if receipt := inboxCall[InboxReceipt](t, h, request); receipt.Seq != 1 {
					t.Fatal("uncertain commit duplicated", receipt)
				}
			}
		})
	}
}

func TestAgentInboxCorruptionIsNotReset(t *testing.T) {
	for _, corruption := range []string{"gap", "wrong_owner", "missing_header"} {
		t.Run(corruption, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			inboxCall[InboxReceipt](t, h, h.receive("one"))
			inboxCall[InboxReceipt](t, h, h.receive("two"))
			h.stop(t)
			dir := filepath.Join(h.store.dir, "inboxes", h.id.MembershipID)
			want := error(ErrInvalid)
			switch corruption {
			case "gap":
				if err := os.Remove(filepath.Join(dir, inboxEntryName(1))); err != nil {
					t.Fatal(err)
				}
			case "wrong_owner":
				var entry InboxEntry
				path := filepath.Join(dir, inboxEntryName(1))
				if err := readSnapshot(path, &entry, maxPrivateFileBytes); err != nil {
					t.Fatal(err)
				}
				entry.RoomInstance = "other-instance"
				if err := h.store.save(path, entry, maxPrivateFileBytes); err != nil {
					t.Fatal(err)
				}
				want = ErrIdentity
			case "missing_header":
				if err := os.Remove(filepath.Join(dir, "inbox.json")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{}); !errors.Is(err, want) {
				t.Fatal("corrupted inbox reset or accepted", err)
			}
		})
	}
}

func TestAgentInboxCancelledCallDoesNotCancelCommit(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer onceRelease(release)()
	real := h.store.ops.syncDir
	h.store.ops.syncDir = func(dir string) error { once.Do(func() { close(entered); <-release }); return real(dir) }
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	done := make(chan error, 1)
	request := h.receive("cancelled")
	go func() { _, err := h.addr.Call[InboxReceipt](ctx, request); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	onceRelease(release)()
	h.reload(t, false) // Stop/Wait joins the possibly discarded completion.
	if receipt := inboxCall[InboxReceipt](t, h, request); receipt.Seq != 1 {
		t.Fatal("cancelled caller lost committed delivery", receipt)
	}
}

func TestAgentInboxEnqueueIsNotDurableReceipt(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer onceRelease(release)()
	real := h.store.ops.createTemp
	h.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		once.Do(func() { close(entered); <-release })
		return real(dir, pattern)
	}
	request := h.receive("queued")
	if err := h.addr.Send(h.ctx, request); err != nil {
		t.Fatal(err)
	}
	<-entered
	if status := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); status.LatestSeq != 0 || !status.Committing {
		t.Fatal("mailbox admission counted as a durable receipt", status)
	}
	if _, err := os.Stat(filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, inboxEntryName(1))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("test did not stop before record persistence", err)
	}
	onceRelease(release)()
	h.reload(t, false)
	if receipt := inboxCall[InboxReceipt](t, h, request); receipt.Seq != 1 {
		t.Fatal(receipt)
	}
}

func TestAgentInboxTaskAdmissionFailure(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{}, actorx.WithTaskLimit(1))
	// Occupy the inbox's only task slot with a read waiting for Store access.
	h.store.mu.Lock()
	locked := true
	defer func() {
		if locked {
			h.store.mu.Unlock()
		}
	}()
	done := make(chan error, 1)
	go func() { _, err := h.addr.Call[InboxPage](h.ctx, PreviewInbox{MemberIdentity: h.id}); done <- err }()
	for !inboxCall[InboxStatus](t, h, InspectInbox{h.id}).Previewing {
		// Actor inspection is independent of the blocked disk read.
	}
	request := h.receive("admission")
	inboxError[InboxReceipt](t, h, request, actorx.ErrFull)
	status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if status.Committing || status.LatestSeq != 0 {
		t.Fatal("failed task admission installed state", status)
	}
	h.store.mu.Unlock()
	locked = false
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	inboxCall[InboxReceipt](t, h, request)
}

func TestAgentInboxInterruptedPreviewDoesNotConsume(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	inboxCall[InboxReceipt](t, h, h.receive("unread"))
	h.store.mu.Lock()
	locked := true
	defer func() {
		if locked {
			h.store.mu.Unlock()
		}
	}()
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := h.addr.Call[InboxPage](ctx, PreviewInbox{MemberIdentity: h.id}); done <- err }()
	for !inboxCall[InboxStatus](t, h, InspectInbox{h.id}).Previewing {
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	h.store.mu.Unlock()
	locked = false
	h.reload(t, false)
	if page := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id}); len(page.Entries) != 1 || page.Entries[0].Seq != 1 {
		t.Fatal("interrupted preview consumed delivery", page)
	}
}

func TestAgentInboxMissingRoomIsNotReinitialized(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "inboxes"), 0700); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenStore(dir, "missing-room"); !errors.Is(err, ErrInvalid) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("orphaned inbox allowed a new Room", err)
	}
}

func TestIntegrationRedeliveryRequiresExactSourceEvidence(t *testing.T) {
	h := newRoomHarness(t, emptyRoom(), nil)
	source := platform.SourceRef{Provider: "gitlab", Channel: "events", Instance: "https://gitlab.example.com", SubscriptionID: strings.Repeat("a", 32), ItemKey: "event-1"}
	request := postIntegrationMessage{RoomIdentity: h.id, DeliveryID: "existing-delivery", Text: "original body", Source: source}
	_ = roomCall[PostReceipt](t, h, request)
	request.Source.Reference = platform.NotificationReference{Version: 1, Object: platform.ObjectReference{Type: "MergeRequest", ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "9001"}}}
	roomError[PostReceipt](t, h, request, ErrConflict)
	request.Source.ItemKey = "another-event"
	roomError[PostReceipt](t, h, request, ErrConflict)
	request.DeliveryID = "new-delivery"
	roomCall[PostReceipt](t, h, request)
	request.Source.Reference.Object.ID.Value = "different-object"
	roomError[PostReceipt](t, h, request, ErrConflict)
}
