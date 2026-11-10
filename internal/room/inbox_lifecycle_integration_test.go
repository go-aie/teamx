//go:build integration || e2e

package room

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestInboxLifecycleSourceAckFailureCrossesExpiryAndRestart(t *testing.T) {
	for _, skip := range []bool{false, true} {
		t.Run(map[bool]string{false: "keep", true: "skip"}[skip], func(t *testing.T) {
			h := newEventHarness(t, gitlab.EventsConfig{})
			svc := eventLiveService(t, h)
			policy := notification.ExpiryPolicy{AfterDays: 3, SkipExpired: skip}
			h.m.config.inboxExpiry = policy
			svc.inboxExpiry = policy
			id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
			inboxRoutingConfigure(t, h, inboxTarget(id))
			seedEventInbox(t, h, 1)
			original := h.m.store.ops.createTemp
			h.m.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("source ack failed") }
			state := h.state()
			if err := h.m.notifications.Deliver(t.Context(), state.Metadata().Key, state.SubscriptionVersion); err == nil {
				t.Fatal("failed ack hidden")
			}
			h.m.store.ops.createTemp = original
			if s := inboxRoutingPreview(t, svc, id); s.SnapshotSeq != 1 {
				t.Fatal(s)
			}
			h.clock.add(4 * 24 * time.Hour)
			root := h.m.store.dir
			if err := h.m.close(); err != nil {
				t.Fatal(err)
			}
			h.m = testManager(t, root, managerTestProfiles(t))
			h.m.config.inboxExpiry = policy
			h.m.notifications.Clock = h.clock.get
			svc = eventLiveService(t, h)
			svc.inboxExpiry = policy
			inboxRoutingDeliver(t, h)
			preview := inboxRoutingPreview(t, svc, id)
			if preview.SnapshotSeq != 1 || len(h.state().Inbox) != 0 {
				t.Fatal("redelivery duplicated or pending lost", preview)
			}
			if skip && (preview.ExpiredThrough != 1 || len(preview.Entries) != 0) {
				t.Fatal("expired handoff retained", preview)
			}
			if !skip && (preview.ExpiredThrough != 0 || len(preview.Entries) != 1) {
				t.Fatal("unconsumed old input removed", preview)
			}
			// Extending the lifetime / turning off skip cannot resurrect a source item.
			h.m.config.inboxExpiry = notification.ExpiryPolicy{AfterDays: 30, SkipExpired: false}
			seedEventInbox(t, h, 1)
			if len(h.state().Inbox) != 0 {
				t.Fatal("old source replayed")
			}
		})
	}
}
func TestInboxLifecycleCapacityReliefResumesDeliveryAndQuery(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	svc := eventLiveService(t, h)
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	inboxRoutingConfigure(t, h, inboxTarget(id))
	limits := DefaultInboxLimits()
	limits.Entries = 1
	addr, err := SpawnAgentInbox(svc.rt, svc.store, id, limits)
	if err != nil {
		t.Fatal(err)
	}
	svc.inboxes[id.MembershipID] = addr
	old := h.event(100, 0)
	old.CreatedAt = h.clock.get().Add(-2 * 24 * time.Hour)
	source := h.state()
	source.ProjectID = 7
	source.Accept([]gitlab.Event{h.event(1, 0)}, h.clock.get())
	item := gitlab.EventsMessage(source, gitlab.EventsInboxEntry{Event: old, Route: notification.Route{Target: inboxTarget(id), Version: source.SubscriptionVersion}, RoomIdentity: source.RoomIdentity, SubscriptionID: source.SubscriptionID, DeliveryID: source.DeliveryID(gitlab.EventsKey(source.Binding, 7, old.ID))})
	if err := svc.receiveInbox(t.Context(), inboxTarget(id), item); err != nil {
		t.Fatal(err)
	}
	seedEventInbox(t, h, 1)
	state := h.state()
	if err := h.m.notifications.Deliver(t.Context(), state.Metadata().Key, state.SubscriptionVersion); !errors.Is(err, notification.ErrCapacity) {
		t.Fatal("missing backpressure", err)
	}
	h.m.mu.Lock()
	state.Status = "capacity"
	state.ErrorCode = "capacity"
	if err := h.m.notifications.SaveLocked(state); err != nil {
		t.Fatal(err)
	}
	h.m.mu.Unlock()
	h.clock.add(2 * 24 * time.Hour)
	inboxRoutingDeliver(t, h)
	preview := inboxRoutingPreview(t, svc, id)
	if preview.ExpiredThrough != 1 || preview.SnapshotSeq != 2 || len(preview.Entries) != 1 || h.state().Status != "running" {
		t.Fatal("capacity did not recover", preview, h.state().Status)
	}
	h.pages.events = []gitlab.Event{h.event(2, int((4*24*time.Hour)/time.Second))}
	h.clock.add(3 * 24 * time.Hour)
	h.newRunner()
	h.prepare()
	h.full()
	if len(h.pages.calls) == 0 || h.state().Status == "capacity" {
		t.Fatal("query did not resume")
	}
	// Independent errors remain effective when an expiration releases space.
	h.m.mu.Lock()
	state = h.stateLockedForLifecycle(t)
	state.Status = "credentials_invalid"
	state.ErrorCode = "credential_unavailable"
	err = h.m.notifications.SaveLocked(state)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	err = h.m.notifications.ExpireTargetLocked(h.room.RoomIdentity, inboxTarget(id))
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if h.state().Status != "credentials_invalid" {
		t.Fatal("expiry resumed invalid credentials")
	}
}
func (h *eventHarness) stateLockedForLifecycle(t *testing.T) gitlab.EventsState {
	s, ok := h.m.eventsSubscriptionLocked(h.m.testEventsKeyLocked(h.room.RoomIdentity))
	if !ok {
		t.Fatal("source missing")
	}
	return s
}

func TestInboxLifecycleSameWorkerResumesCapacityPausedQuery(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	svc := eventLiveService(t, h)
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	inboxRoutingConfigure(t, h, inboxTarget(id))
	limits := DefaultInboxLimits()
	limits.Entries = 1
	addr, err := SpawnAgentInbox(svc.rt, svc.store, id, limits)
	if err != nil {
		t.Fatal(err)
	}
	svc.inboxes[id.MembershipID] = addr
	source := h.state()
	source.ProjectID = 7
	old := h.event(100, 0)
	old.CreatedAt = h.clock.get().Add(-2 * 24 * time.Hour)
	item := gitlab.EventsMessage(source, gitlab.EventsInboxEntry{Event: old, Route: notification.Route{Target: inboxTarget(id), Version: source.SubscriptionVersion}, RoomIdentity: source.RoomIdentity, SubscriptionID: source.SubscriptionID, DeliveryID: source.DeliveryID(gitlab.EventsKey(source.Binding, 7, old.ID))})
	if err := svc.receiveInbox(t.Context(), inboxTarget(id), item); err != nil {
		t.Fatal(err)
	}
	seedEventInbox(t, h, 1)
	var calls atomic.Int64
	h.m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		return eventFetchFunc(func(context.Context, gitlab.EventQuery) (gitlab.EventBatch, error) {
			calls.Add(1)
			return gitlab.EventBatch{Version: 1, Events: []gitlab.Event{h.event(1, 0), h.event(2, 1)}, Complete: true}, nil
		}), 7, nil
	}
	h.m.mu.Lock()
	h.m.notifications.Limits.Inbox = 1
	h.m.mu.Unlock()
	if err := h.m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	wait := func(done func() bool) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			if done() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("worker did not reach expected state", h.state().Status, calls.Load())
	}
	wait(func() bool { return h.state().DeliveryErrorCode == "inbox_capacity" })
	if calls.Load() != 0 {
		t.Fatal("full target continued growing source backlog")
	}
	before := calls.Load()
	h.clock.add(2 * 24 * time.Hour)
	wait(func() bool { return calls.Load() > before && h.state().Status != "capacity" })
	preview := inboxRoutingPreview(t, svc, id)
	if preview.ExpiredThrough != 1 || preview.SnapshotSeq != 2 || len(preview.Entries) != 1 {
		t.Fatal(preview)
	}
}

func TestInboxLifecycleExpiringBlockedSourceDoesNotStrandQuery(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	svc := eventLiveService(t, h)
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	inboxRoutingConfigure(t, h, inboxTarget(id))
	seedEventInbox(t, h, 1)
	h.m.mu.Lock()
	state, _ := h.m.eventsSubscriptionLocked(h.m.testEventsKeyLocked(h.room.RoomIdentity))
	state.DeliveryErrorCode = "inbox_capacity"
	err := h.m.notifications.SaveLocked(state)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.clock.add(4 * 24 * time.Hour)
	inboxRoutingDeliver(t, h)
	state = h.state()
	if len(state.Inbox) != 0 || state.DeliveryErrorCode != "" {
		t.Fatal("expired backlog left a permanent delivery/query block", state.DeliveryErrorCode)
	}
	h.pages.events = []gitlab.Event{h.event(2, int((4*24*time.Hour)/time.Second))}
	h.newRunner()
	h.prepare()
	h.full()
	if len(h.state().Inbox) != 1 {
		t.Fatal("new valid input was stranded after expiry")
	}
}

func TestInboxLifecycleFrozenRoutesProtectReceiptsAndRoomHistory(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	svc := eventLiveService(t, h)
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	inboxRoutingConfigure(t, h, inboxTarget(id))
	seedEventInbox(t, h, 1)
	inboxRoutingConfigure(t, h, notification.Target{})
	seedEventInbox(t, h, 2)
	h.m.mu.Lock()
	pending := h.m.notifications.PendingDeliveriesLocked(h.room.RoomIdentity, inboxTarget(id))
	h.m.mu.Unlock()
	if len(pending) != 1 {
		t.Fatal("current subscription metadata hid a frozen unconfirmed Inbox route", pending)
	}
	h.clock.add(4 * 24 * time.Hour)
	h.m.mu.Lock()
	err := h.m.notifications.ExpireTargetLocked(h.room.RoomIdentity, inboxTarget(id))
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	state := h.state()
	if len(state.Inbox) != 1 {
		t.Fatal("frozen Inbox route did not expire independently")
	}
	for _, entry := range state.Inbox {
		if entry.Route.Target.Inbox() || entry.Event.ID != 2 {
			t.Fatal("Room history was expired by Inbox policy", entry)
		}
	}
}
