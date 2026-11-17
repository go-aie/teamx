package room

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func (s *uiUpdateSubscription) take() ([]uiUpdate, bool) {
	var updates []uiUpdate
	for {
		u, resync, ok := s.next()
		if !ok || resync {
			return updates, resync
		}
		updates = append(updates, u)
	}
}

func updateSub(t *testing.T, h *uiUpdateHub, session int, operator string, info ServiceInfo) *uiUpdateSubscription {
	t.Helper()
	s, err := h.subscribe(uiUpdateSession{hash: [32]byte{byte(session)}}, operator, info)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	return s
}

func TestUIUpdatesGlobalVersionsBridgeSubscriptionGap(t *testing.T) {
	hub := newUIUpdateHub()
	first := updateSub(t, hub, 1, "alice", ServiceInfo{})
	if len(first.globalVersions) != 3 || first.globalVersions["catalog"] != "0" {
		t.Fatal(first.globalVersions)
	}
	first.close()
	// Global installations advance even while no stream is registered.
	hub.mark(uiUpdate{Topic: "catalog", Operator: "alice"})
	hub.mark(uiUpdate{Topic: "platforms"})
	info := ServiceInfo{RoomIdentity: RoomIdentity{"room", "instance"}, ServiceEpoch: "runtime"}
	hub.mark(roomUIUpdate("messages", info))
	second := updateSub(t, hub, 1, "alice", info)
	if second.globalVersions["catalog"] != "1" || second.globalVersions["platforms"] != "1" || second.globalVersions["settings"] != "0" || len(second.globalVersions) != 3 {
		t.Fatal(second.globalVersions)
	}
	// A change after registration remains queued; it does not rewrite hello's
	// baseline while the network writer marshals or flushes that snapshot.
	hub.mark(uiUpdate{Topic: "settings"})
	if second.globalVersions["settings"] != "0" || first.globalVersions["catalog"] != "0" {
		t.Fatal("registration snapshot mutated")
	}
	updates, resync := second.take()
	if resync || len(updates) != 1 || updates[0].Topic != "settings" {
		t.Fatal(updates, resync)
	}
}

func TestUIUpdatesExpiredGlobalObservationsCannotBeReused(t *testing.T) {
	hub := newUIUpdateHub()
	// Retain only a fixed-topic deadline, without retaining a subscriber timer.
	hub.atEarliest(uiUpdate{Topic: "platforms"}, time.Now().Add(-time.Second))
	sub := updateSub(t, hub, 1, "alice", ServiceInfo{})
	if _, reusable := sub.globalVersions["platforms"]; reusable || len(sub.deadlines) != 0 {
		t.Fatal("expired observation advertised as reusable", sub.globalVersions)
	}
	if len(sub.globalVersions) != 2 {
		t.Fatal(sub.globalVersions)
	}
	hub.mark(uiUpdate{Topic: "platforms"})
	hub.atEarliest(uiUpdate{Topic: "platforms"}, time.Now().Add(time.Hour))
	next := updateSub(t, hub, 2, "alice", ServiceInfo{})
	if next.globalVersions["platforms"] != "1" {
		t.Fatal(next.globalVersions)
	}
}

func TestUIUpdatesDeadlinesScopedBoundedAndCancelled(t *testing.T) {
	hub := newUIUpdateHub()
	info := ServiceInfo{RoomIdentity: RoomIdentity{"room", "instance"}, ServiceEpoch: "runtime"}
	u := roomUIUpdate("policy", info)
	u.AgentID, u.MembershipID = "agent", "membership"
	hub.at(u, time.Now().Add(time.Hour)) // No subscribers: no retained timer.
	sub := updateSub(t, hub, 1, "alice", info)
	otherInfo := info
	otherInfo.ServiceEpoch = "old"
	other := updateSub(t, hub, 2, "alice", otherInfo)
	global := updateSub(t, hub, 3, "alice", ServiceInfo{})
	hub.at(u, time.Now().Add(30*time.Millisecond))
	// A later read of another operation sharing a resource must not postpone
	// its earliest outstanding expiry.
	hub.atEarliest(u, time.Now().Add(time.Hour))
	sourceTopics(t, sub, "policy")
	assertSourceQuiet(t, other)
	assertSourceQuiet(t, global)
	hub.mu.Lock()
	if len(sub.deadlines) != 0 {
		t.Fatal("expired timer retained")
	}
	hub.mu.Unlock()
	hub.at(u, time.Now().Add(time.Hour))
	hub.at(u, time.Time{}) // A read with no remaining deadline cancels the wake.
	hub.mu.Lock()
	if len(sub.deadlines) != 0 {
		t.Fatal("cancelled timer retained")
	}
	hub.mu.Unlock()
	// Replacing one policy resource's deadline cancels its earlier timer.
	hub.at(u, time.Now().Add(20*time.Millisecond))
	hub.at(u, time.Now().Add(time.Hour))
	time.Sleep(40 * time.Millisecond)
	assertSourceQuiet(t, sub)
	for i := range uiUpdateMaxKeys + 1 {
		key := u
		key.MembershipID = fmt.Sprint(i)
		hub.at(key, time.Now().Add(time.Hour))
	}
	if _, resync := sub.take(); !resync {
		t.Fatal("deadline overflow must request reconciliation")
	}
	hub.mu.Lock()
	if len(sub.deadlines) > uiUpdateMaxKeys {
		t.Fatal("unbounded deadline storage")
	}
	hub.mu.Unlock()
	sub.close()
	hub.close()
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if len(sub.deadlines) != 0 || len(hub.subs) != 0 {
		t.Fatal("close retained deadlines")
	}
}

func TestUIUpdatesStatusObservationSkipsUnsubscribedRooms(t *testing.T) {
	hub := newUIUpdateHub()
	info := ServiceInfo{RoomIdentity: RoomIdentity{"room", "instance"}, ServiceEpoch: "runtime"}
	member := &MemberAgent{Observe: func() { t.Fatal("unsubscribed observer invoked") }, ObserveEnabled: func() bool { return hub.observesRoom(info) }}
	member.observeStatus()
	if member.observedStatus != nil {
		t.Fatal("unsubscribed status snapshot built")
	}
	global := updateSub(t, hub, 1, "alice", ServiceInfo{})
	member.observeStatus()
	if hub.observesRoom(info) {
		t.Fatal("global subscription enabled private status observation")
	}
	selected := updateSub(t, hub, 2, "alice", info)
	if !hub.observesRoom(info) {
		t.Fatal("selected Room not observed")
	}
	selected.close()
	global.close()
	if hub.listeners.Load() != 0 {
		t.Fatal("subscription counter leaked")
	}
}

func TestUIUpdatesScopeAndBounds(t *testing.T) {
	info := ServiceInfo{RoomIdentity: RoomIdentity{"room", "instance"}, ServiceEpoch: "runtime"}
	h := newUIUpdateHub()
	selected := updateSub(t, h, 1, "alice", info)
	global := updateSub(t, h, 2, "alice", ServiceInfo{})
	otherOperator := updateSub(t, h, 3, "bob", info)
	stale := info
	stale.ServiceEpoch = "old"
	oldRuntime := updateSub(t, h, 4, "alice", stale)
	different := info
	different.RoomInstance = "recreated"
	otherInstance := updateSub(t, h, 6, "alice", different)
	different = info
	different.RoomID = "other-room"
	otherRoom := updateSub(t, h, 7, "alice", different)
	u := roomUIUpdate("messages", info)
	for range 10000 {
		h.mark(u)
	}
	if got, resync := selected.take(); resync || len(got) != 1 || got[0] != u {
		t.Fatal(got, resync)
	}
	for _, s := range []*uiUpdateSubscription{global, oldRuntime, otherInstance, otherRoom} {
		if got, _ := s.take(); len(got) != 0 {
			t.Fatal("room notification escaped scope", got)
		}
	}
	otherOperator.take() // shared Room messages are readable by either allowed operator
	u.Operator = "alice"
	h.mark(u)
	if got, _ := otherOperator.take(); len(got) != 0 {
		t.Fatal("operator notification leaked", got)
	}
	selected.take()
	h.mark(uiUpdate{Topic: "catalog"})
	for _, s := range []*uiUpdateSubscription{selected, global, otherOperator, oldRuntime} {
		if got, _ := s.take(); len(got) != 1 || got[0].RoomID != "" {
			t.Fatal("missing global notification", got)
		}
	}
	otherInstance.take()
	otherRoom.take()
	for i := 0; i < 65; i++ {
		u := roomUIUpdate("activity", info)
		u.AgentID = "agent"
		u.MembershipID = fmt.Sprint(i)
		h.mark(u)
	}
	if got, resync := selected.take(); !resync || len(got) != 0 {
		t.Fatal("overflow must replace backlog with resync", got, resync)
	}
	h.mark(roomUIUpdate("messages", info))
	if got, resync := selected.take(); resync || len(got) != 1 {
		t.Fatal("queue did not recover", got, resync)
	}
	// The byte budget can overflow before the resource-count budget.
	long := info
	long.RoomID = strings.Repeat("r", 128)
	long.RoomInstance = strings.Repeat("i", 128)
	large := updateSub(t, h, 5, "alice", long)
	for i := 0; i < 40; i++ {
		u := roomUIUpdate("activity", long)
		u.AgentID = strings.Repeat("a", 128)
		u.MembershipID = fmt.Sprintf("%0128d", i)
		h.mark(u)
	}
	if _, resync := large.take(); !resync {
		t.Fatal("byte budget was not enforced")
	}
	bad := roomUIUpdate("messages", info)
	bad.RoomID = strings.Repeat("x", 257)
	h.mark(bad)
	h.mark(uiUpdate{Topic: "unknown"})
	if got, _ := large.take(); len(got) != 0 {
		t.Fatal("invalid key accepted")
	}
	h.close()
	if _, err := h.subscribe(uiUpdateSession{}, "alice", info); !errors.Is(err, ErrClosing) {
		t.Fatal(err)
	}
}

func TestUIUpdatesConnectionLimitsAndConcurrentLifecycle(t *testing.T) {
	h := newUIUpdateHub()
	for i := 0; i < 8; i++ {
		updateSub(t, h, 1, "alice", ServiceInfo{})
	}
	if _, err := h.subscribe(uiUpdateSession{hash: [32]byte{1}}, "alice", ServiceInfo{}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	for i := 8; i < 64; i++ {
		updateSub(t, h, i, "alice", ServiceInfo{})
	}
	if _, err := h.subscribe(uiUpdateSession{hash: [32]byte{100}}, "alice", ServiceInfo{}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	h.close()
	h = newUIUpdateHub()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for range 100 {
				s, err := h.subscribe(uiUpdateSession{}, "alice", ServiceInfo{})
				if err != nil {
					continue
				}
				h.mark(uiUpdate{Topic: "catalog"})
				s.take()
				s.close()
			}
		})
	}
	wg.Wait()
	h.close()
}

func TestUIUpdatesObserveOnlyInstalledMessageState(t *testing.T) {
	for _, failure := range []error{nil, errors.New("disk failed"), ErrCommitUnknown} {
		name := "success"
		if failure != nil {
			name = failure.Error()
		}
		t.Run(name, func(t *testing.T) {
			hub := newUIUpdateHub()
			info := ServiceInfo{RoomIdentity: emptyRoom().RoomIdentity, ServiceEpoch: "runtime"}
			sub := updateSub(t, hub, 1, "alice", info)
			h := newRoomHarness(t, emptyRoom(), func(r *Room, _ *Store) {
				r.observeMessages = func() { hub.mark(roomUIUpdate("messages", info)); hub.mark(uiUpdate{Topic: "catalog"}) }
				if failure != nil {
					r.save = func(RoomSnapshot) error { return failure }
				}
			})
			msg := PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: "once", Text: "private message body"}
			_, err := h.addr.Call[PostReceipt](h.ctx, msg)
			if !errors.Is(err, failure) {
				t.Fatal(err)
			}
			updates, resync := sub.take()
			if failure != nil {
				if len(updates) != 0 || resync {
					t.Fatal("failed commit published installed state", updates)
				}
				page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadAll})
				if len(page.Messages) != 0 {
					t.Fatal("failed candidate installed")
				}
				return
			}
			if len(updates) != 2 || resync {
				t.Fatal(updates, resync)
			}
			roomCall[PostReceipt](t, h, msg) // retry of an accepted send does not install again
			if got, _ := sub.take(); len(got) != 0 {
				t.Fatal("duplicate send emitted a new state change", got)
			}
			roomCall[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 1})
			if got, _ := sub.take(); len(got) != 2 {
				t.Fatal("clear did not invalidate messages/catalog", got)
			}
		})
	}
}

func TestUIUpdatesOptionalObserverPreservesCommits(t *testing.T) {
	for _, observe := range []bool{false, true} {
		h := newRoomHarness(t, emptyRoom(), func(r *Room, _ *Store) {
			if observe {
				hub := newUIUpdateHub()
				info := ServiceInfo{RoomIdentity: r.state.RoomIdentity, ServiceEpoch: "runtime"}
				r.observeMessages = func() { hub.mark(roomUIUpdate("messages", info)) }
			}
		})
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		receipt, err := h.addr.Call[PostReceipt](ctx, PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: "once", Text: "same"})
		cancel()
		if err != nil || receipt.Seq != 1 {
			t.Fatal(receipt, err)
		}
		state, err := h.store.LoadRoom()
		if err != nil || state.LatestSeq != 1 || state.Revision != 1 || state.Messages[0].Text != "same" {
			t.Fatal(state, err)
		}
	}
}
