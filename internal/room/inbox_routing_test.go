package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func inboxRoutingMember(t *testing.T, svc *service, name, membership string) MemberIdentity {
	t.Helper()
	id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: name, MembershipID: membership}
	addr, err := svc.rt.Spawn(notificationSink{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.room.Call[MemberInfo](context.Background(), AddMember{MemberIdentity: id, Runner: RunnerConfig{Kind: "dsh", Profile: "runtime"}, Target: addr}); err != nil {
		t.Fatal(err)
	}
	svc.setTarget(membership, addr)
	return id
}
func inboxRoutingPreview(t *testing.T, svc *service, id MemberIdentity) InboxPage {
	t.Helper()
	w := httptest.NewRecorder()
	body, _ := json.Marshal(apiRequest{ServiceInfo: svc.info, Caller: humanCaller, AgentID: id.AgentID, MembershipID: id.MembershipID, Limit: 1})
	svc.ServeHTTP(w, httptest.NewRequest("POST", "/v1/member/inbox", strings.NewReader(string(body))))
	var page InboxPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return page
}
func inboxRoutingConfigure(t *testing.T, h *eventHarness, target notification.Target) gitlab.EventsState {
	t.Helper()
	s := h.state()
	cfg := s.Config
	cfg.Target = target
	next, err := h.m.configureEventsSubscription(context.Background(), s.Metadata().Key, s.SubscriptionVersion, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if next.SubscriptionID != s.SubscriptionID || !next.Baseline.Equal(s.Baseline) {
		t.Fatal("route switch reset source baseline")
	}
	return next
}
func inboxRoutingDeliver(t *testing.T, h *eventHarness) {
	t.Helper()
	s := h.state()
	if err := h.m.notifications.Deliver(context.Background(), s.Metadata().Key, s.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
}
func inboxTarget(id MemberIdentity) notification.Target {
	return notification.Target{Mode: "inbox", AgentID: id.AgentID, MembershipID: id.MembershipID}
}

func TestInboxRoutingSwitchFreezesPendingAndPreviewDoesNotConsume(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	svc := eventLiveService(t, h)
	a := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	b := inboxRoutingMember(t, svc, "reviewer", "generation-reviewer")
	seedEventInbox(t, h, 1) // Already accepted for Room; retain this destination.
	inboxRoutingConfigure(t, h, inboxTarget(a))
	seedEventInbox(t, h, 2)
	inboxRoutingConfigure(t, h, inboxTarget(b))
	seedEventInbox(t, h, 3)
	for range 3 {
		inboxRoutingDeliver(t, h)
	}
	saved, err := svc.store.LoadRoom()
	if err != nil || len(saved.Messages) != 1 || saved.Messages[0].Source.ItemKey != h.state().Binding.URL+"|7|1" {
		t.Fatal("raw inbox notification reached Room", err)
	}
	for _, id := range []MemberIdentity{a, b} {
		first := inboxRoutingPreview(t, svc, id)
		second := inboxRoutingPreview(t, svc, id)
		if len(first.Entries) != 1 || second.SnapshotSeq != first.SnapshotSeq || second.Entries[0].Seq != first.Entries[0].Seq || first.Entries[0].Item.Route.Target != inboxTarget(id) {
			t.Fatal("preview consumed or crossed memberships")
		}
	}
	// Existing observations are not replayed when returning to public delivery.
	inboxRoutingConfigure(t, h, notification.Target{})
	seedEventInbox(t, h, 2)
	if len(h.state().Inbox) != 0 {
		t.Fatal("switch replayed an observed item")
	}
	for _, caller := range []Caller{{Author: Author{Kind: "agent", ID: a.AgentID}, MembershipID: a.MembershipID}, humanCaller} {
		req := apiRequest{ServiceInfo: svc.info, Caller: caller, AgentID: b.AgentID, MembershipID: b.MembershipID}
		if caller.Kind == "human" {
			req.RoomInstance = "stale"
		}
		body, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		svc.ServeHTTP(w, httptest.NewRequest("POST", "/v1/member/inbox", strings.NewReader(string(body))))
		if w.Code != 403 {
			t.Fatal("inbox preview accepted wrong caller or identity", w.Code)
		}
	}
}

func TestInboxRoutingAckFailureReopenAndDisabledBacklog(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	svc := eventLiveService(t, h)
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	inboxRoutingConfigure(t, h, inboxTarget(id))
	seedEventInbox(t, h, 1)
	s := h.state()
	cfg := s.Config
	cfg.Enabled = false
	if _, err := h.m.configureEventsSubscription(context.Background(), s.Metadata().Key, s.SubscriptionVersion, cfg); err != nil {
		t.Fatal(err)
	}
	disabled := h.state()
	if err := h.m.notifications.Deliver(context.Background(), disabled.Metadata().Key, disabled.SubscriptionVersion); !errors.Is(err, notification.ErrIdentity) {
		t.Fatal(err)
	}
	if len(h.state().Inbox) != 1 {
		t.Fatal("disable discarded backlog")
	}
	cfg.Enabled = true
	s = h.state()
	if _, err := h.m.configureEventsSubscription(context.Background(), s.Metadata().Key, s.SubscriptionVersion, cfg); err != nil {
		t.Fatal(err)
	}
	original := h.m.store.ops.createTemp
	h.m.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("source ack failure") }
	s = h.state()
	if err := h.m.notifications.Deliver(context.Background(), s.Metadata().Key, s.SubscriptionVersion); err == nil {
		t.Fatal("ack failure hidden")
	}
	h.m.store.ops.createTemp = original
	if inboxRoutingPreview(t, svc, id).SnapshotSeq != 1 || len(h.state().Inbox) != 1 {
		t.Fatal("durable handoff lost")
	}
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	h.m = testManager(t, root, managerTestProfiles(t))
	svc = eventLiveService(t, h)
	inboxRoutingDeliver(t, h)
	if inboxRoutingPreview(t, svc, id).SnapshotSeq != 1 || len(h.state().Inbox) != 0 {
		t.Fatal("recovery duplicated receipt")
	}
	saved, _ := svc.store.LoadRoom()
	if saved.LatestSeq != 0 {
		t.Fatal("inbox woke Room")
	}
}

func TestInboxRoutingRemovalRejoinAndCapacityAreVisible(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	svc := eventLiveService(t, h)
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	inboxRoutingConfigure(t, h, inboxTarget(id))
	seedEventInbox(t, h, 1)
	inboxRoutingDeliver(t, h)
	seedEventInbox(t, h, 2)
	if _, err := svc.remove(apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}); err != nil {
		t.Fatal(err)
	}
	replacement := inboxRoutingMember(t, svc, id.AgentID, strings.Repeat("2", 32))
	s := h.state()
	if err := h.m.notifications.Deliver(context.Background(), s.Metadata().Key, s.SubscriptionVersion); !errors.Is(err, notification.ErrIdentity) {
		t.Fatal(err)
	}
	if h.state().DeliveryErrorCode != "target_removed" || len(h.state().Inbox) != 1 || inboxRoutingPreview(t, svc, replacement).SnapshotSeq != 0 {
		t.Fatal("removed target inherited or not reported")
	}
	if _, err := os.Stat(filepath.Join(svc.store.dir, "inboxes", id.MembershipID, inboxEntryName(1))); err != nil {
		t.Fatal("old inbox discarded", err)
	}
	// Removed membership cannot be selected, but the source can always be disabled.
	s = h.state()
	cfg := s.Config
	if _, err := h.m.configureEventsSubscription(context.Background(), s.Metadata().Key, s.SubscriptionVersion, cfg); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	cfg.Enabled = false
	if _, err := h.m.configureEventsSubscription(context.Background(), s.Metadata().Key, s.SubscriptionVersion, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestInboxRoutingCapacityAndUnavailableService(t *testing.T) {
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
	seedEventInbox(t, h, 1)
	inboxRoutingDeliver(t, h)
	seedEventInbox(t, h, 2)
	s := h.state()
	if err := h.m.notifications.Deliver(context.Background(), s.Metadata().Key, s.SubscriptionVersion); !errors.Is(err, notification.ErrCapacity) {
		t.Fatal(err)
	}
	if h.state().DeliveryErrorCode != "inbox_capacity" || len(h.state().Inbox) != 1 {
		t.Fatal("capacity lost pending delivery")
	}
	svc.restarting.Store(true)
	s = h.state()
	if err := h.m.notifications.Deliver(context.Background(), s.Metadata().Key, s.SubscriptionVersion); !errors.Is(err, notification.ErrBusy) {
		t.Fatal(err)
	}
	svc.restarting.Store(false)
	if len(h.state().Inbox) != 1 {
		t.Fatal("restart lost delivery")
	}
}

func TestInboxRoutingTodosAdapter(t *testing.T) {
	h := newTodoHarness(t)
	svc, err := startService(h.m.roomDir(h.room.RoomID), h.room.RoomID, serviceConfig{}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	h.m.runtimes[h.room.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	h.m.mu.Unlock()
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	s := h.state()
	cfg := s.Config
	cfg.Target = inboxTarget(id)
	next, err := h.m.configureTodosSubscription(context.Background(), h.key, s.SubscriptionVersion, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.pages.pending = []gitlab.Todo{h.todo(1, 1, "pending")}
	h.newRunner()
	h.full()
	s = h.state()
	if err := h.m.notifications.Deliver(context.Background(), h.key, s.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	page := inboxRoutingPreview(t, svc, id)
	if page.SnapshotSeq != 1 || page.Entries[0].Item.Source.Channel != "todos" || page.Entries[0].Item.Route.Version != next.SubscriptionVersion || len(h.state().Inbox) != 0 {
		t.Fatal("Todos lost target or receipt")
	}
	saved, _ := svc.store.LoadRoom()
	if saved.LatestSeq != 0 {
		t.Fatal("Todos inbox published raw notification")
	}
}

func TestInboxRoutingPausedMemberStaysPaused(t *testing.T) {
	svc, err := newFixtureService(t.TempDir(), "inbox-paused", serviceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := svc.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	member, err := svc.add(apiRequest{AgentID: "assistant", Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}})
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	post := integrationPost(svc.info.RoomIdentity, 1)
	item := notification.Item{Key: post.Source.ItemKey, DeliveryID: post.DeliveryID, Text: post.Text, Source: post.Source, CreatedAt: time.Now().UTC(), Route: notification.Route{Target: inboxTarget(id), Version: 1}}
	if err := svc.receiveInbox(context.Background(), inboxTarget(id), item); err != nil {
		t.Fatal(err)
	}
	status, err := svc.target(id.MembershipID).Call[MemberStatus](context.Background(), InspectMember{id})
	if err != nil || !status.Paused || status.Active || status.Agent.TurnNumber != 0 {
		t.Fatal("Inbox enabled or scheduled paused member", status, err)
	}
	if inboxRoutingPreview(t, svc, id).SnapshotSeq != 1 {
		t.Fatal("paused member did not receive")
	}
}

func TestInboxRoutingCloseJoinsAdmittedDelivery(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	svc := eventLiveService(t, h)
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	inboxRoutingConfigure(t, h, inboxTarget(id))
	inboxRoutingPreview(t, svc, id) // Complete header creation before blocking the entry.
	seedEventInbox(t, h, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	original := svc.store.ops.createTemp
	svc.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		close(entered)
		<-release
		return original(dir, pattern)
	}
	delivered := make(chan error, 1)
	s := h.state()
	go func() {
		delivered <- h.m.notifications.Deliver(context.Background(), s.Metadata().Key, s.SubscriptionVersion)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("delivery did not reach persistence")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	err := svc.close(ctx)
	cancel()
	if err == nil || svc.closed {
		close(release)
		<-delivered
		t.Fatal("shutdown released Store before admitted delivery settled", err)
	}
	close(release)
	if err := <-delivered; err != nil {
		t.Fatal("admitted handoff was abandoned", err)
	}
	svc.store.ops.createTemp = original
	if err := svc.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.state().Inbox) != 0 {
		t.Fatal("settled durable receipt was not acknowledged")
	}
	// Reopen the service Store and recover the exact receipt after shutdown.
	svc = eventLiveService(t, h)
	if inboxRoutingPreview(t, svc, id).SnapshotSeq != 1 {
		t.Fatal("shutdown lost admitted delivery")
	}
}

func TestInboxRoutingStoppedInboxReleasesStore(t *testing.T) {
	dir := t.TempDir()
	svc, err := startService(dir, "inbox-stopped", serviceConfig{}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer svc.close(context.Background())
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	inboxRoutingPreview(t, svc, id)
	failure := errors.New("injected Inbox stop failure")
	svc.inboxMu.Lock()
	addr := svc.inboxes[id.MembershipID]
	svc.inboxMu.Unlock()
	if err := addr.Stop(failure); err != nil {
		t.Fatal(err)
	}
	if _, err := addr.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.close(context.Background()); !errors.Is(err, failure) || !svc.closed {
		t.Fatal("settled actor failure stranded Store ownership", err)
	}
	reopened, err := OpenStore(dir, "inbox-stopped")
	if err != nil {
		t.Fatal("settled failure prevented recovery", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
