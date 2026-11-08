package room

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"
)

var humanCaller = Caller{Author: Author{Kind: "human", ID: "user"}}

type roomHarness struct {
	store     *Store
	rt        *actorx.Runtime
	addr      actorx.Addr
	scheduler actorx.Addr
	target    actorx.Addr
	id        RoomIdentity
	ctx       context.Context
}

type notificationSink struct{}

func (notificationSink) Bindings() []actorx.Binding {
	return []actorx.Binding{actorx.HandleMessage(func(actorx.Context, struct{}) error { return nil })}
}

func emptyRoom() RoomSnapshot {
	return RoomSnapshot{Version: formatVersion, RoomIdentity: RoomIdentity{RoomID: "design-review", RoomInstance: "room-1"}, Messages: []RoomMessage{}, Members: []MemberState{}}
}

func historyRoom(n int, text string) RoomSnapshot {
	s := emptyRoom()
	for i := range n {
		s.Messages = append(s.Messages, RoomMessage{Seq: uint64(i + 1), ClientMessageID: fmt.Sprintf("seed-%d", i), Author: humanCaller.Author, Text: text, CreatedAt: time.Date(2026, 10, 31, 1, 0, i, 0, time.UTC)})
	}
	s.LatestSeq, s.Revision = uint64(n), uint64(n)
	s.Members = []MemberState{{AgentID: "A", MembershipID: "generation-A", Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}}}
	return s
}

func newRoomHarness(t *testing.T, state RoomSnapshot, configure func(*Room, *Store), options ...actorx.Option) *roomHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	store, err := OpenStore(t.TempDir(), state.RoomID)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	h := &roomHarness{store: store, id: state.RoomIdentity, ctx: ctx}
	t.Cleanup(func() {
		defer cancel()
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if h.rt != nil {
			if err := h.rt.Close(closeCtx); err != nil {
				t.Error(err)
			}
		}
		if err := h.store.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	r, err := NewRoom(store)
	if err != nil {
		t.Fatal(err)
	}
	if configure != nil {
		configure(r, store)
	}
	h.rt, err = actorx.NewRuntime(ctx, options...)
	if err != nil {
		t.Fatal(err)
	}
	h.target, err = h.rt.Spawn(notificationSink{})
	if err != nil {
		t.Fatal(err)
	}
	h.scheduler, err = h.rt.Spawn(&turnScheduler{RoomIdentity: h.id})
	if err != nil {
		t.Fatal(err)
	}
	h.addr, err = h.rt.Spawn(r)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func roomCall[R any](t *testing.T, h *roomHarness, msg any) R {
	t.Helper()
	v, err := h.addr.Call[R](h.ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func roomError[R any](t *testing.T, h *roomHarness, msg any, want error) {
	t.Helper()
	if _, err := h.addr.Call[R](h.ctx, msg); !errors.Is(err, want) {
		t.Fatalf("%T: want %v, got %v", msg, want, err)
	}
}

func (h *roomHarness) memberID(agent, generation string) MemberIdentity {
	return MemberIdentity{RoomIdentity: h.id, AgentID: agent, MembershipID: generation}
}

func agentCaller(agent, generation string) Caller {
	return Caller{Author: Author{Kind: "agent", ID: agent}, MembershipID: generation}
}

func (h *roomHarness) post(c Caller, id, text string) PostMessage {
	return PostMessage{RoomIdentity: h.id, Caller: c, ClientMessageID: id, Text: text}
}

func (h *roomHarness) confirm(c Caller, receipt string) ConfirmRead {
	return ConfirmRead{MemberIdentity: h.memberID(c.ID, c.MembershipID), ReadReceipt: receipt}
}

func (h *roomHarness) list(t *testing.T) MemberList {
	t.Helper()
	return roomCall[MemberList](t, h, ListMembers{h.id})
}

func (h *roomHarness) idle(t *testing.T) MemberList {
	t.Helper()
	for {
		v := h.list(t)
		if !v.Committing {
			return v
		}
	}
}

func (h *roomHarness) reload(t *testing.T, reopen bool) {
	t.Helper()
	if err := h.addr.Stop(nil); err != nil {
		t.Fatal(err)
	}
	exit, err := h.addr.Wait(h.ctx)
	if err != nil || exit.Reason != nil {
		t.Fatalf("Room exit: %+v, %v", exit, err)
	}
	if reopen {
		dir := h.store.dir
		if err := h.store.Close(); err != nil {
			t.Fatal(err)
		}
		h.store, err = OpenStore(dir, h.id.RoomID)
		if err != nil {
			t.Fatal(err)
		}
	}
	r, err := NewRoom(h.store)
	if err != nil {
		t.Fatal(err)
	}
	h.addr, err = h.rt.Spawn(r)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRoomMembersPostsAndUnread(t *testing.T) {
	h := newRoomHarness(t, emptyRoom(), nil)
	roomCall[PostReceipt](t, h, h.post(humanCaller, "before", "before joining"))
	a, b := agentCaller("A", "generation-A"), agentCaller("B", "generation-B")
	add := AddMember{MemberIdentity: h.memberID(a.ID, a.MembershipID), Target: h.target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}}
	joined := roomCall[MemberInfo](t, h, add)
	if joined.ReadThrough != 1 || joined.UnreadCount != 0 || !joined.Attached {
		t.Fatalf("join baseline: %+v", joined)
	}
	before := h.list(t).Revision
	roomCall[MemberInfo](t, h, add)
	if h.list(t).Revision != before {
		t.Fatal("idempotent join changed revision")
	}
	conflict := add
	conflict.MembershipID = "other-generation"
	roomError[MemberInfo](t, h, conflict, ErrConflict)
	otherTarget, err := h.rt.Spawn(notificationSink{})
	if err != nil {
		t.Fatal(err)
	}
	conflict = add
	conflict.Target = otherTarget
	roomError[MemberInfo](t, h, conflict, ErrConflict)
	roomCall[MemberInfo](t, h, AddMember{MemberIdentity: h.memberID(b.ID, b.MembershipID), Target: h.target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}})
	p2 := h.post(humanCaller, "post-2", "one")
	if got := roomCall[PostReceipt](t, h, p2); got.Seq != 2 {
		t.Fatal(got)
	}
	roomCall[PostReceipt](t, h, h.post(humanCaller, "post-3", "two"))
	roomCall[PostReceipt](t, h, h.post(a, "post-4", "A speaking"))
	roomCall[PostReceipt](t, h, h.post(Caller{Author: Author{Kind: "human", ID: "A"}}, "post-5", "human named A"))
	status := h.list(t)
	if status.Members[0].UnreadCount != 3 || status.Members[0].ReadThrough != 1 || status.Members[0].NewestUnreadSeq != 5 || status.Members[1].UnreadCount != 4 {
		t.Fatalf("self/unread: %+v", status)
	}
	before = status.Revision
	if got := roomCall[PostReceipt](t, h, p2); got.Seq != 2 {
		t.Fatal("duplicate appended", got)
	}
	if h.list(t).Revision != before {
		t.Fatal("duplicate changed revision")
	}
	conflictingPost := p2
	conflictingPost.Text = "different"
	roomError[PostReceipt](t, h, conflictingPost, ErrConflict)
	conflictingPost = p2
	conflictingPost.ReplyTo = 1
	roomError[PostReceipt](t, h, conflictingPost, ErrConflict)
	roomCall[PostReceipt](t, h, h.post(humanCaller, "different-id", "one"))
	preview := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadLatest, Latest: 1})
	if len(preview.Messages) != 1 || preview.Messages[0].Seq != 6 || preview.ReadReceipt != "" {
		t.Fatal(preview)
	}
	all := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadAll})
	if len(all.Messages) != 6 || h.list(t).Members[0].ReadThrough != 1 {
		t.Fatal("preview moved cursor")
	}
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread, Limit: 1})
	if len(page.Messages) != 1 || page.Messages[0].Seq != 2 || page.FromExclusive != 1 || page.ScannedThrough != 2 || page.ReadReceipt == "" || page.HasMore {
		t.Fatalf("consume page: %+v", page)
	}
	if h.list(t).Members[0].ReadThrough != 1 {
		t.Fatal("unconfirmed page moved cursor")
	}
	roomError[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread}, ErrBusy)
	roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread, Peek: true})
	roomCall[PostReceipt](t, h, h.post(humanCaller, "later", "after page snapshot"))
	confirmed := roomCall[ReadConfirmation](t, h, h.confirm(a, page.ReadReceipt))
	if confirmed.ReadThrough != 2 || confirmed.UnreadCount != 4 {
		t.Fatal(confirmed)
	}
	if retry := roomCall[ReadConfirmation](t, h, h.confirm(a, page.ReadReceipt)); retry != confirmed {
		t.Fatal("confirmation not idempotent")
	}
	if h.list(t).Revision != confirmed.Revision {
		t.Fatal("duplicate confirmation committed again")
	}
	page = roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread, Limit: maxMessages})
	if len(page.Messages) != 4 || page.ScannedThrough != 7 {
		t.Fatal(page)
	}
	for _, m := range page.Messages {
		if m.Author == a.Author {
			t.Fatal("unread included self")
		}
	}
	roomCall[ReadConfirmation](t, h, h.confirm(a, page.ReadReceipt))
	status = h.list(t)
	if status.Members[0].UnreadCount != 0 || status.Members[0].ReadThrough != 7 || status.Members[1].UnreadCount != 6 || status.Members[1].ReadThrough != 1 {
		t.Fatalf("independent cursors: %+v", status)
	}
	roomCall[MemberInfo](t, h, RemoveMember{h.memberID(a.ID, a.MembershipID)})
	roomCall[MemberInfo](t, h, AddMember{MemberIdentity: h.memberID(a.ID, "new-A"), Target: h.target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}})
	roomError[PostReceipt](t, h, h.post(a, "old-task", "stale"), ErrIdentity)
	roomError[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadAll}, ErrIdentity)
	roomError[ReadConfirmation](t, h, h.confirm(a, page.ReadReceipt), ErrIdentity)
	roomError[MemberInfo](t, h, RemoveMember{h.memberID(a.ID, a.MembershipID)}, ErrIdentity)
	status = h.list(t)
	if status.Members[1].MembershipID != "new-A" || status.Members[1].ReadThrough != 7 || status.Members[1].UnreadCount != 0 {
		t.Fatal(status)
	}
	wrongRoom := h.id
	wrongRoom.RoomInstance = "other-instance"
	roomError[MemberList](t, h, ListMembers{wrongRoom}, ErrIdentity)
	all = roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadAll})
	if len(all.Messages) != 7 || all.Messages[3].Author != a.Author {
		t.Fatal("removal lost history")
	}
}

func TestRoomPaginationAndFixedSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name            string
		n, limit, first int
		text            string
	}{
		{"message bound", 205, 150, 100, "body"},
		{"byte bound", 130, 70, 64, strings.Repeat("x", maxTextBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newRoomHarness(t, historyRoom(tc.n, tc.text), nil)
			a := agentCaller("A", "generation-A")
			page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadAll})
			if len(page.Messages) != tc.first || !page.HasMore || page.SnapshotSeq != uint64(tc.n) {
				t.Fatal(page)
			}
			page.Messages[0].Text = "caller changed returned slice"
			roomCall[PostReceipt](t, h, h.post(humanCaller, "live", "new arrival"))
			all := []RoomMessage{}
			// Re-read and traverse only the original fixed snapshot.
			snapshot := page.SnapshotSeq
			after := uint64(0)
			for {
				p := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadRange, After: &after, SnapshotSeq: &snapshot})
				all = append(all, p.Messages...)
				after = p.NextAfter
				if !p.HasMore {
					break
				}
			}
			if len(all) != tc.n || all[0].Text != tc.text || all[len(all)-1].Seq != snapshot {
				t.Fatal("preview lost fixed snapshot or aliased state")
			}
			consumed := 0
			remaining := tc.limit
			after = 0
			for {
				p := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread, Limit: remaining, After: &after, SnapshotSeq: &snapshot})
				if len(p.Messages) > maxPageMessages {
					t.Fatal("page count exceeded")
				}
				bytes := 0
				for _, m := range p.Messages {
					bytes += len(m.Text)
				}
				if bytes > maxPageBytes {
					t.Fatal("page bytes exceeded")
				}
				consumed += len(p.Messages)
				roomCall[ReadConfirmation](t, h, h.confirm(a, p.ReadReceipt))
				after, remaining = p.NextAfter, p.Remaining
				if !p.HasMore {
					break
				}
			}
			if consumed != tc.limit || after != uint64(tc.limit) {
				t.Fatalf("limit across pages: %d, %d", consumed, after)
			}
			status := h.list(t)
			if status.Members[0].UnreadCount != tc.n+1-tc.limit {
				t.Fatal("new arrival consumed by old snapshot", status)
			}
			latest := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadLatest, Latest: 1, SnapshotSeq: &snapshot})
			if len(latest.Messages) != 1 || latest.Messages[0].Seq != snapshot || latest.ReadReceipt != "" || h.list(t).Members[0].ReadThrough != after {
				t.Fatal("latest preview changed progress")
			}
			zero := uint64(0)
			empty := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadAll, SnapshotSeq: &zero})
			if len(empty.Messages) != 0 || empty.SnapshotSeq != 0 || empty.HasMore {
				t.Fatal(empty)
			}
			future := uint64(tc.n + 2)
			roomError[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadAll, SnapshotSeq: &future}, ErrRequest)
			stale := after - 1
			roomError[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread, After: &stale}, ErrReadExpired)
		})
	}
}

func TestRoomReceiptExpiryAndRecovery(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC).Unix())
	h := newRoomHarness(t, historyRoom(3, "body"), func(r *Room, _ *Store) { r.now = func() time.Time { return time.Unix(clock.Load(), 0) } })
	a := agentCaller("A", "generation-A")
	request := ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread, Limit: 1}
	p1 := roomCall[ReadPage](t, h, request)
	clock.Add(int64(readReceiptTTL / time.Second))
	roomError[ReadConfirmation](t, h, h.confirm(a, p1.ReadReceipt), ErrReadExpired)
	p2 := roomCall[ReadPage](t, h, request)
	if p1.ReadReceipt == p2.ReadReceipt || p1.ScannedThrough != p2.ScannedThrough {
		t.Fatal("expired page was not reissued")
	}
	roomError[ReadConfirmation](t, h, h.confirm(a, p1.ReadReceipt), ErrReadExpired)
	roomCall[ReadConfirmation](t, h, h.confirm(a, p2.ReadReceipt))
	p3 := roomCall[ReadPage](t, h, request)
	roomError[ReadConfirmation](t, h, h.confirm(a, p2.ReadReceipt), ErrReadExpired)
	roomCall[ReadConfirmation](t, h, h.confirm(a, p3.ReadReceipt))
	pending := roomCall[ReadPage](t, h, request)
	before := h.list(t)
	h.reload(t, true)
	after := h.list(t)
	if after.Members[0].ReadThrough != 2 || after.Members[0].UnreadCount != 1 || after.Members[0].Attached || before.Revision != after.Revision {
		t.Fatalf("persisted cursor: %+v", after)
	}
	roomError[ReadConfirmation](t, h, h.confirm(a, pending.ReadReceipt), ErrReadExpired)
	roomError[ReadConfirmation](t, h, h.confirm(a, p3.ReadReceipt), ErrReadExpired)
	newPage := roomCall[ReadPage](t, h, request)
	if newPage.Messages[0].Seq != 3 {
		t.Fatal("pending page was persisted or skipped")
	}
	roomCall[ReadConfirmation](t, h, h.confirm(a, newPage.ReadReceipt))
	// The original message key survives reconstruction of the Room Actor.
	retry := h.post(humanCaller, "seed-0", "body")
	if got := roomCall[PostReceipt](t, h, retry); got.Seq != 1 {
		t.Fatal("dedup lost on reload", got)
	}
	conflict := retry
	conflict.Text = "conflict after recovery"
	roomError[PostReceipt](t, h, conflict, ErrConflict)
	if h.list(t).LatestSeq != 3 {
		t.Fatal("reload retry appended")
	}
}

func TestRoomOwnMessagesAreScanned(t *testing.T) {
	state := historyRoom(3, "own message")
	a := agentCaller("A", "generation-A")
	for i := range state.Messages {
		state.Messages[i].Author = a.Author
	}
	h := newRoomHarness(t, state, nil)
	p := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread})
	if len(p.Messages) != 0 || p.ScannedThrough != 3 || p.ReadReceipt == "" || p.UnreadCount != 0 {
		t.Fatal(p)
	}
	roomCall[ReadConfirmation](t, h, h.confirm(a, p.ReadReceipt))
	empty := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread})
	if empty.ReadReceipt != "" || empty.ScannedThrough != 3 {
		t.Fatal(empty)
	}
}

func TestRoomCommitBusyAndCallerCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	state := historyRoom(1, "committed")
	state.Members = append(state.Members, MemberState{AgentID: "B", MembershipID: "generation-B", Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}})
	h := newRoomHarness(t, state, func(_ *Room, s *Store) {
		real := s.ops.syncDir
		s.ops.syncDir = func(dir string) error { once.Do(func() { close(entered); <-release }); return real(dir) }
	})
	defer onceRelease(release)()
	a := agentCaller("A", "generation-A")
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread})
	request := h.post(humanCaller, "pending", "candidate")
	callerCtx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := h.addr.Call[PostReceipt](callerCtx, request); done <- err }()
	<-entered // Replacement happened, directory sync and reply are still blocked.
	status := h.list(t)
	if !status.Committing || status.LatestSeq != 1 {
		t.Fatal("candidate exposed before commit", status)
	}
	preview := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadAll})
	if len(preview.Messages) != 1 {
		t.Fatal("read exposed uncommitted message")
	}
	b := agentCaller("B", "generation-B")
	bPage := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: b, Mode: ReadUnread})
	if bPage.SnapshotSeq != 1 || len(bPage.Messages) != 1 {
		t.Fatal("unread read exposed candidate", bPage)
	}
	select {
	case err := <-done:
		t.Fatalf("premature commit receipt: %v", err)
	default:
	}
	roomError[PostReceipt](t, h, h.post(humanCaller, "other", "busy"), ErrBusy)
	roomError[MemberInfo](t, h, AddMember{MemberIdentity: h.memberID("B", "generation-B"), Target: h.target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}}, ErrBusy)
	roomError[MemberInfo](t, h, RemoveMember{h.memberID(a.ID, a.MembershipID)}, ErrBusy)
	roomError[ReadConfirmation](t, h, h.confirm(a, page.ReadReceipt), ErrBusy)
	roomError[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread}, ErrBusy)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	onceRelease(release)()
	status = h.idle(t)
	if status.LatestSeq != 2 {
		t.Fatal("caller cancellation rolled back commit", status)
	}
	if got := roomCall[PostReceipt](t, h, request); got.Seq != 2 {
		t.Fatal("retry duplicated canceled call", got)
	}
	roomCall[ReadConfirmation](t, h, h.confirm(a, page.ReadReceipt))
	roomCall[ReadConfirmation](t, h, h.confirm(b, bPage.ReadReceipt))
	if h.list(t).Members[0].UnreadCount != 1 {
		t.Fatal("old page consumed concurrent arrival")
	}
}

func TestRoomConfirmInFlightPinsReceipt(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC).Unix())
	h := newRoomHarness(t, historyRoom(2, "unread"), func(r *Room, s *Store) {
		r.now = func() time.Time { return time.Unix(clock.Load(), 0) }
		real := s.ops.syncDir
		s.ops.syncDir = func(dir string) error { once.Do(func() { close(entered); <-release }); return real(dir) }
	})
	defer onceRelease(release)()
	a := agentCaller("A", "generation-A")
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread})
	done := make(chan error, 1)
	go func() { _, err := h.addr.Call[ReadConfirmation](h.ctx, h.confirm(a, page.ReadReceipt)); done <- err }()
	<-entered
	clock.Add(int64(readReceiptTTL / time.Second))
	roomError[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread}, ErrBusy)
	preview := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread, Peek: true})
	if preview.FromExclusive != 0 || len(preview.Messages) != 2 {
		t.Fatal("uncommitted cursor exposed", preview)
	}
	onceRelease(release)()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if h.list(t).Members[0].ReadThrough != 2 {
		t.Fatal("accepted confirmation lost its receipt")
	}
}

// A cleanup release can also run on a failure path without blocking shutdown.
func onceRelease(ch chan struct{}) func() {
	return func() {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

func TestRoomCommitFailures(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("uncertain=%v", uncertain), func(t *testing.T) {
			fault := errors.New("controlled commit failure")
			var once sync.Once
			h := newRoomHarness(t, historyRoom(1, "committed"), func(_ *Room, s *Store) {
				if uncertain {
					real := s.ops.syncDir
					s.ops.syncDir = func(dir string) error {
						var err error
						once.Do(func() { err = fault })
						if err != nil {
							return err
						}
						return real(dir)
					}
				} else {
					real := s.ops.createTemp
					s.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
						var err error
						once.Do(func() { err = fault })
						if err != nil {
							return nil, err
						}
						return real(dir, pattern)
					}
				}
			})
			request := h.post(humanCaller, "retry-key", "new body")
			got, err := h.addr.Call[PostReceipt](h.ctx, request)
			if !errors.Is(err, fault) || got.Seq != 0 {
				t.Fatalf("failed commit returned success: %+v, %v", got, err)
			}
			status := h.list(t)
			if status.Committing || status.LatestSeq != 1 || (status.WriteError != "") != uncertain {
				t.Fatal(status)
			}
			preview := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadAll})
			if len(preview.Messages) != 1 {
				t.Fatal("failure installed candidate")
			}
			if uncertain {
				if !errors.Is(err, ErrCommitUnknown) {
					t.Fatal(err)
				}
				roomError[PostReceipt](t, h, request, ErrCommitUnknown)
				roomError[MemberInfo](t, h, RemoveMember{h.memberID("A", "generation-A")}, ErrCommitUnknown)
				h.reload(t, true)
				if h.list(t).LatestSeq != 2 {
					t.Fatal("recovery did not load replaced file")
				}
			}
			if receipt := roomCall[PostReceipt](t, h, request); receipt.Seq != 2 {
				t.Fatal(receipt)
			}
			persisted, err := h.store.LoadRoom()
			if err != nil || persisted.LatestSeq != 2 || len(persisted.Messages) != 2 {
				t.Fatal(persisted, err)
			}
		})
	}
}

func TestRoomTaskAdmissionFailure(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h := newRoomHarness(t, emptyRoom(), func(r *Room, _ *Store) {
		r.now = func() time.Time { once.Do(func() { close(entered); <-release }); return time.Now() }
	}, actorx.WithMailboxCapacity(2))
	defer onceRelease(release)()
	done := make(chan error, 1)
	request := h.post(humanCaller, "admission", "body")
	go func() { _, err := h.addr.Call[PostReceipt](h.ctx, request); done <- err }()
	<-entered // Hold handler before StartTask: maintenance reserves one slot and the queued item fills the other.
	if err := h.addr.Send(h.ctx, ListMembers{h.id}); err != nil {
		t.Fatal(err)
	}
	onceRelease(release)()
	if err := <-done; !errors.Is(err, actorx.ErrFull) {
		t.Fatalf("task admission: %v", err)
	}
	status := h.list(t)
	if status.Committing || status.LatestSeq != 0 {
		t.Fatal("admission failure left ghost state", status)
	}
	roomCall[PostReceipt](t, h, request)
}

func TestRoomStopWaitsForCommitAndReloads(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h := newRoomHarness(t, emptyRoom(), func(_ *Room, s *Store) {
		real := s.ops.syncDir
		s.ops.syncDir = func(dir string) error { once.Do(func() { close(entered); <-release }); return real(dir) }
	})
	defer onceRelease(release)()
	request := h.post(humanCaller, "interrupted-call", "durable despite stop")
	done := make(chan error, 1)
	go func() { _, err := h.addr.Call[PostReceipt](h.ctx, request); done <- err }()
	<-entered
	if err := h.addr.Stop(nil); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, actorx.ErrStopped) {
		t.Fatalf("stopped caller: %v", err)
	}
	waited := make(chan error, 1)
	go func() { _, err := h.addr.Wait(h.ctx); waited <- err }()
	select {
	case err := <-waited:
		t.Fatalf("Wait preceded owned commit: %v", err)
	default:
	}
	onceRelease(release)()
	if err := <-waited; err != nil {
		t.Fatal(err)
	}
	h.reload(t, true)
	if got := roomCall[PostReceipt](t, h, request); got.Seq != 1 || h.list(t).LatestSeq != 1 {
		t.Fatal("stop recovery duplicated or lost post", got)
	}
}

func TestRoomValidationAndCapacity(t *testing.T) {
	h := newRoomHarness(t, emptyRoom(), nil)
	invalid := h.post(humanCaller, "invalid", strings.Repeat("x", maxTextBytes+1))
	roomError[PostReceipt](t, h, invalid, ErrRequest)
	invalid.Text, invalid.ReplyTo = "body", 1
	roomError[PostReceipt](t, h, invalid, ErrRequest)
	roomError[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadUnread}, ErrRequest)
	roomError[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadLatest}, ErrRequest)
	for i := range maxMembers {
		roomCall[MemberInfo](t, h, AddMember{MemberIdentity: h.memberID(fmt.Sprintf("agent-%d", i), fmt.Sprintf("membership-%d", i)), Target: h.target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}})
	}
	roomError[MemberInfo](t, h, AddMember{MemberIdentity: h.memberID("excess", "excess"), Target: h.target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}}, ErrCapacity)
	state := historyRoom(maxMessages, "body")
	full := newRoomHarness(t, state, nil)
	roomError[PostReceipt](t, full, full.post(humanCaller, "overflow", "body"), ErrCapacity)
	// A retry remains valid even when history cannot accept a new message.
	roomCall[PostReceipt](t, full, full.post(humanCaller, "seed-0", "body"))
	if got, err := full.store.LoadRoom(); err != nil || !reflect.DeepEqual(got, state) {
		t.Fatal("capacity check changed history", err)
	}
}

func TestRoomFailedReadConfirmationKeepsCursorAndReceipt(t *testing.T) {
	fault := errors.New("controlled cursor write failure")
	var once sync.Once
	h := newRoomHarness(t, historyRoom(2, "unread"), func(_ *Room, s *Store) {
		real := s.ops.createTemp
		s.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
			var err error
			once.Do(func() { err = fault })
			if err != nil {
				return nil, err
			}
			return real(dir, pattern)
		}
	})
	a := agentCaller("A", "generation-A")
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread})
	confirmation := h.confirm(a, page.ReadReceipt)
	got, err := h.addr.Call[ReadConfirmation](h.ctx, confirmation)
	if !errors.Is(err, fault) || got.ReadThrough != 0 {
		t.Fatalf("failed cursor commit: %+v, %v", got, err)
	}
	status := h.list(t)
	if status.Members[0].ReadThrough != 0 || status.Members[0].UnreadCount != 2 {
		t.Fatal("failed confirmation installed cursor", status)
	}
	persisted, err := h.store.LoadRoom()
	if err != nil || persisted.Members[0].ReadThrough != 0 {
		t.Fatal("failed confirmation changed disk", err)
	}
	roomError[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread}, ErrBusy)
	confirmed := roomCall[ReadConfirmation](t, h, confirmation)
	if confirmed.ReadThrough != 2 || confirmed.UnreadCount != 0 {
		t.Fatal(confirmed)
	}
	h.reload(t, true)
	if h.list(t).Members[0].ReadThrough != 2 {
		t.Fatal("confirmed cursor not durable")
	}
}

func (h *roomHarness) spawn(member actorx.Actor) (actorx.Addr, error) {
	return spawnScheduledMember(h.rt, member, h.scheduler)
}

func TestRoomHistoryByteCapacityPreservesReadableHistory(t *testing.T) {
	state := historyRoom(maxHistoryBytes/maxTextBytes, strings.Repeat("x", maxTextBytes))
	r := &Room{state: state}
	post := PostMessage{RoomIdentity: state.RoomIdentity, Caller: humanCaller, ClientMessageID: "exceeds-by-one", Text: "x"}
	if err := r.Post(nil, post, actorx.Reply[PostReceipt]{}); !errors.Is(err, ErrCapacity) || r.committing {
		t.Fatal("byte capacity did not reject before committing", err)
	}
	page, err := r.Read(nil, ReadMessages{RoomIdentity: state.RoomIdentity, Caller: humanCaller, Mode: ReadAll})
	if err != nil || page.SnapshotSeq != state.LatestSeq || len(page.Messages) != maxPageBytes/maxTextBytes || !page.HasMore ||
		!reflect.DeepEqual(r.state, state) {
		t.Fatal("capacity rejection changed history or preview", page, err)
	}
}
