package room

import (
	"context"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestNotificationUpdateSummaryExcludesInternalProgress(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	h.full()
	before := h.state()
	now := h.clock.get()
	diagnostic := before.Clone()
	diagnostic.LastCheck = now.Add(time.Second)
	diagnostic.Metrics.Calls++
	diagnostic.Metrics.DurationMS++
	diagnostic.Metrics.ResponseBytes++
	diagnostic.ExpiredBefore = now
	diagnostic.Observed["internal"] = gitlab.EventsObservation{}
	diagnostic.Scan = &gitlab.EventsScan{Pages: 2, Bytes: 512}
	if summarizeNotification(before, now) != summarizeNotification(diagnostic, now) {
		t.Fatal("diagnostics changed business summary")
	}
	for _, mutate := range []func(*gitlab.EventsState){
		func(s *gitlab.EventsState) { s.Status = "credentials_invalid" },
		func(s *gitlab.EventsState) { s.ErrorCode = "query_failed" },
		func(s *gitlab.EventsState) {
			s.Gap = &gitlab.EventsGap{Start: now, End: now.Add(time.Second), Reason: "gap"}
		},
		func(s *gitlab.EventsState) { s.DeliveryErrorCode = "target_removed" },
		func(s *gitlab.EventsState) { s.SubscriptionVersion++ },
		func(s *gitlab.EventsState) { s.Config.Enabled = false },
		func(s *gitlab.EventsState) { s.Retired = true },
		func(s *gitlab.EventsState) { s.Inbox["pending"] = gitlab.EventsInboxEntry{} },
	} {
		next := before.Clone()
		mutate(&next)
		if summarizeNotification(before, now) == summarizeNotification(next, now) {
			t.Fatal("lost summary change", next.RuntimeState)
		}
	}
	// Metadata is deliberately identical for runtime/collection errors and gaps.
	failed := before.Clone()
	failed.ErrorCode = "query_failed"
	if failed.Metadata() != before.Metadata() {
		t.Fatal("fixture metadata differs")
	}
	l := lark.State{RuntimeState: notification.RuntimeState{Status: "running"}, Search: &lark.SearchProgress{Branches: []lark.SearchBranch{{Kind: "private"}}}}
	changed := l
	search := *l.Search
	search.Branches = append([]lark.SearchBranch(nil), l.Search.Branches...)
	changed.Search = &search
	changed.Search.Metrics.Pages++
	changed.Search.Branches[0].Completed++
	if summarizeNotification(l, now) != summarizeNotification(changed, now) {
		t.Fatal("search progress became summary")
	}
	changed.Search.Branches[0].ErrorCode = "permission_denied"
	if summarizeNotification(l, now) == summarizeNotification(changed, now) {
		t.Fatal("lost branch error")
	}
}

func TestNotificationUpdatesCheckpointFanoutAndSummaryPriority(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	svc := eventLiveService(t, h)
	h.full()
	a := updateSub(t, h.m.updates, 1, humanCaller.ID, svc.info)
	b := updateSub(t, h.m.updates, 2, humanCaller.ID, svc.info)
	global := updateSub(t, h.m.updates, 3, humanCaller.ID, ServiceInfo{})
	for range 5 {
		h.clock.add(time.Minute)
		h.step()
	}
	for _, sub := range []*uiUpdateSubscription{a, b} {
		updates, resync := sub.take()
		if resync || len(updates) != 1 || updates[0].Topic != "notifications" || updates[0].Kind != "diagnostics" {
			t.Fatal(updates, resync)
		}
	}
	assertSourceQuiet(t, global)
	// Summary survives later checkpoints, separately in both pages' queues.
	h.m.mu.Lock()
	s, _ := h.m.eventsSubscriptionLocked(h.m.testEventsKeyLocked(h.room.RoomIdentity))
	s.ErrorCode = "query_failed"
	err := h.m.notifications.SaveLocked(s)
	s.LastCheck = s.LastCheck.Add(time.Second)
	if err == nil {
		err = h.m.notifications.SaveLocked(s)
	}
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []*uiUpdateSubscription{a, b} {
		updates, _ := sub.take()
		if len(updates) != 1 || updates[0].Kind != "summary" {
			t.Fatal(updates)
		}
	}
	assertSourceQuiet(t, global)
}

// A state that cannot be inspected proves the no-observer fast path runs
// before even Metadata, without changing the common store's commit contract.
type unobservableNotification struct{ notification.State }

func TestNotificationUpdatesSkipUnobservedClassification(t *testing.T) {
	m := &roomManager{updates: newUIUpdateHub()}
	m.observeNotificationChange(unobservableNotification{}, unobservableNotification{})
	// A global catalog listener is not a notification-summary consumer.
	svc := &service{info: ServiceInfo{RoomIdentity: emptyRoom().RoomIdentity, ServiceEpoch: "runtime"}, updates: m.updates}
	m.runtimes = map[RoomIdentity]*roomRuntime{svc.info.RoomIdentity: {service: svc, status: "ready"}}
	updateSub(t, m.updates, 1, "operator", ServiceInfo{})
	s := gitlab.EventsState{RoomIdentity: svc.info.RoomIdentity}
	// m.notifications is intentionally nil: classification would dereference it.
	m.observeNotificationChange(s, s)
}

func TestMemberObservationDoesNotInvalidateDirectory(t *testing.T) {
	svc, _, _, _, _, _ := updateHTTPService(t, false)
	sub := updateSub(t, svc.updates, 1, humanCaller.ID, svc.info)
	svc.invalidate("members", MemberIdentity{})
	updates, _ := sub.take()
	if len(updates) != 1 || updates[0].Topic != "members" {
		t.Fatal(updates)
	}
	// A real Room attach installs member observation without a directory change.
	h := newRoomHarness(t, historyRoom(1, "body"), func(r *Room, _ *Store) {
		r.observeMembers = func() { svc.invalidate("members", MemberIdentity{}) }
		r.observeCatalog = func() { svc.updates.mark(uiUpdate{Topic: "catalog"}) }
	})
	_, err := h.addr.Call[MemberInfo](context.Background(), AttachMember{MemberIdentity: h.memberID("A", "generation-A"), Target: h.addr})
	if err != nil {
		t.Fatal(err)
	}
	updates, _ = sub.take()
	if len(updates) != 1 || updates[0].Topic != "members" {
		t.Fatal(updates)
	}
	caller := agentCaller("A", "generation-A")
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: caller, Mode: ReadUnread})
	roomCall[ReadConfirmation](t, h, h.confirm(caller, page.ReadReceipt))
	updates, _ = sub.take()
	if len(updates) != 1 || updates[0].Topic != "members" {
		t.Fatal("receipt changed directory", updates)
	}
	id := h.memberID("B", "generation-B")
	roomCall[MemberInfo](t, h, AddMember{MemberIdentity: id, Runner: historyRoom(0, "").Members[0].Runner, Target: h.addr})
	sourceTopics(t, sub, "members", "catalog")
	roomCall[MemberInfo](t, h, RemoveMember{MemberIdentity: id})
	sourceTopics(t, sub, "members", "catalog")
}

func TestNotificationUpdateKindsCoalescePerSubscriber(t *testing.T) {
	for _, kinds := range [][]string{{"diagnostics", "summary", "diagnostics"}, {"summary", "diagnostics"}, {"", "summary", "diagnostics"}, {"summary", "", "diagnostics"}} {
		hub := newUIUpdateHub()
		info := ServiceInfo{RoomIdentity: emptyRoom().RoomIdentity, ServiceEpoch: "runtime"}
		a := updateSub(t, hub, 1, "operator", info)
		b := updateSub(t, hub, 2, "operator", info)
		for _, kind := range kinds {
			u := roomUIUpdate("notifications", info)
			u.Kind = kind
			hub.mark(u)
		}
		for _, sub := range []*uiUpdateSubscription{a, b} {
			updates, resync := sub.take()
			if resync || len(updates) != 1 || updates[0].Kind == "diagnostics" {
				t.Fatal(updates, resync)
			}
			if sub.bytes != 0 {
				t.Fatal("queue accounting", sub.bytes)
			}
		}
	}
	for _, topic := range []string{"notifications", "members"} {
		u := roomUIUpdate(topic, ServiceInfo{RoomIdentity: emptyRoom().RoomIdentity, ServiceEpoch: "runtime"})
		u.Kind = "anything"
		if u.valid() {
			t.Fatal("invalid kind accepted")
		}
	}
}
