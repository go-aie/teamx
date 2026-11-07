package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func integrationPost(owner RoomIdentity, id int64) postIntegrationMessage {
	return postIntegrationMessage{RoomIdentity: owner, DeliveryID: fmt.Sprintf("gitlab-%d", id), Text: fmt.Sprintf("GitLab · event %d", id), Source: IntegrationSource{Provider: "gitlab", Channel: "events", Instance: "https://gitlab.example.com", ProjectID: 7, ItemKey: fmt.Sprintf("https://gitlab.example.com|7|%d", id), SubscriptionID: strings.Repeat("a", 32)}}
}

func TestGitLabIntegrationReceiptsSurviveClearAndReopen(t *testing.T) {
	h := newRoomHarness(t, emptyRoom(), nil)
	post := integrationPost(h.id, 1)
	first := roomCall[PostReceipt](t, h, post)
	roomCall[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: first.Seq})
	roomCall[PostReceipt](t, h, post)
	saved, err := h.store.LoadRoom()
	if err != nil || len(saved.Messages) != 0 || saved.LatestSeq != 1 || len(saved.IntegrationReceipts) != 1 {
		t.Fatal(saved, err)
	}
	// Reload the actor from the actual disk snapshot, while retaining root ownership.
	if err := h.rt.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.rt, err = actorx.NewRuntime(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRoom(h.store)
	if err != nil {
		t.Fatal(err)
	}
	h.addr, err = h.rt.Spawn(r)
	if err != nil {
		t.Fatal(err)
	}
	retry := roomCall[PostReceipt](t, h, post)
	if retry != first {
		t.Fatal(first, retry)
	}
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: humanCaller, Mode: ReadAll})
	if len(page.Messages) != 0 || page.SnapshotSeq != 1 {
		t.Fatal(page)
	}
	missing := post
	missing.Source.Channel = ""
	roomError[PostReceipt](t, h, missing, ErrRequest)
	missing = post
	missing.Source.ItemKey = ""
	roomError[PostReceipt](t, h, missing, ErrRequest)
	bad := post
	bad.Source.ItemKey += "-other"
	roomError[PostReceipt](t, h, bad, ErrConflict)
	roomError[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: Caller{Author: Author{Kind: "integration", ID: "gitlab"}}, ClientMessageID: "spoof", Text: "spoof"}, ErrIdentity)
	roomError[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: Caller{Author: Author{Kind: "integration", ID: "gitlab"}}, ThroughSeq: 1}, ErrIdentity)
}

func TestGitLabIntegrationCommitFailureDoesNotPublishReceipt(t *testing.T) {
	h := newRoomHarness(t, emptyRoom(), func(r *Room, _ *Store) {
		save := r.save
		calls := 0
		r.save = func(next RoomSnapshot) error {
			calls++
			if calls == 1 {
				return errors.New("injected save failure")
			}
			return save(next)
		}
	})
	post := integrationPost(h.id, 1)
	if _, err := h.addr.Call[PostReceipt](h.ctx, post); err == nil {
		t.Fatal("save failure accepted")
	}
	saved, _ := h.store.LoadRoom()
	if saved.LatestSeq != 0 || len(saved.IntegrationReceipts) != 0 {
		t.Fatal(saved)
	}
	if got := roomCall[PostReceipt](t, h, post); got.Seq != 1 {
		t.Fatal(got)
	}
}

// A real Room actor/store with service lifecycle guards; no member/model is
// assembled here. Scheduler behavior is covered independently below.
func eventLiveService(t *testing.T, h *eventHarness) *service {
	t.Helper()
	svc, err := startService(h.m.roomDir(h.room.RoomID), h.room.RoomID, serviceConfig{}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	h.m.runtimes[h.room.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	h.m.mu.Unlock()
	return svc
}
func seedEventInbox(t *testing.T, h *eventHarness, id int64) {
	t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	s, _ := h.m.eventsSubscriptionLocked(h.m.testEventsKeyLocked(h.room.RoomIdentity))
	s.ProjectID = 7
	if _, _, err := s.Accept([]gitlab.Event{h.event(id, 0)}, h.clock.get()); err != nil {
		t.Fatal(err)
	}
	if err := h.m.notifications.SaveLocked(s); err != nil {
		t.Fatal(err)
	}
}

func TestGitLabDeliveryInactiveCrashWindowAndClear(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	seedEventInbox(t, h, 1)
	before := h.state()
	if err := h.m.notifications.Deliver(context.Background(), h.m.testEventsKey(h.room.RoomIdentity), before.SubscriptionVersion); !errors.Is(err, notification.ErrBusy) {
		t.Fatal(err)
	}
	if len(h.state().Inbox) != 1 {
		t.Fatal("inactive room lost pending event")
	}
	svc := eventLiveService(t, h)
	// Simulate the crash window exactly: Room commits, inbox is not acknowledged.
	var entry gitlab.EventsInboxEntry
	for _, v := range before.Inbox {
		entry = v
	}
	post := gitlabEventMessage(before, entry)
	receipt, err := svc.room.Call[PostReceipt](context.Background(), post)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.room.Call[ClearReceipt](context.Background(), ClearMessages{RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ThroughSeq: receipt.Seq}); err != nil {
		t.Fatal(err)
	}
	// The inbox still exists after clearing. Recovery must find the durable receipt.
	if err := h.m.notifications.Deliver(context.Background(), h.m.testEventsKey(h.room.RoomIdentity), before.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	page, err := svc.room.Call[ReadPage](context.Background(), ReadMessages{RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, Mode: ReadAll})
	if err != nil || page.SnapshotSeq != 1 || len(page.Messages) != 0 || len(h.state().Inbox) != 0 {
		t.Fatal(page, err, h.state())
	}
}

func TestGitLabDeliveryChecksScopeAndStableActivation(t *testing.T) {
	for _, boundary := range []string{"busy", "restart", "disabled", "version", "instance", "binding", "credential"} {
		t.Run(boundary, func(t *testing.T) {
			h := newEventHarness(t, gitlab.EventsConfig{})
			seedEventInbox(t, h, 1)
			svc := eventLiveService(t, h)
			s := h.state()
			owner, version := h.room.RoomIdentity, s.SubscriptionVersion
			switch boundary {
			case "busy":
				h.m.busy = true
				defer func() { h.m.busy = false }()
			case "restart":
				svc.restarting.Store(true)
				defer svc.restarting.Store(false)
			case "disabled":
				cfg := s.Config
				cfg.Enabled = false
				if _, err := h.m.configureEventsSubscription(context.Background(), h.m.testEventsKey(owner), version, cfg); err != nil {
					t.Fatal(err)
				}
			case "version":
				version++
			case "instance":
				owner.RoomInstance = "old-instance"
			case "binding":
				// Invalidate the subscription itself, independent of Workspace.
				h.m.mu.Lock()
				s.Binding.ConnectionGeneration = strings.Repeat("e", 32)
				err := h.m.notifications.SaveLocked(s)
				h.m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			case "credential":
				h.m.mu.Lock()
				file, loadErr := h.m.connections.Load()
				if loadErr == nil {
					_, _, loadErr = h.m.connections.LogoutOAuth(file.Connections[0].CredentialID)
				}
				h.m.mu.Unlock()
				if loadErr != nil {
					t.Fatal(loadErr)
				}
			}
			if err := h.m.notifications.Deliver(context.Background(), h.m.testEventsKey(owner), version); err == nil {
				t.Fatal("crossed boundary")
			}
			saved, err := svc.store.LoadRoom()
			if err != nil || saved.LatestSeq != 0 || len(h.state().Inbox) != 1 {
				t.Fatal(saved, err)
			}
		})
	}
}

func TestGitLabEventsManagementAuthorizationAndStatus(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	view := func() *httptest.ResponseRecorder {
		q := url.Values{"service_epoch": {h.m.epoch}, "caller_id": {"operator"}, "room_id": {h.room.RoomID}, "room_instance": {h.room.RoomInstance}}
		w := httptest.NewRecorder()
		h.m.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/platform/notifications?"+q.Encode(), nil))
		return w
	}
	r := view()
	if r.Code != 200 || strings.Contains(r.Body.String(), "GITLAB_PAT") || strings.Contains(r.Body.String(), "binding") {
		t.Fatal(r.Code, r.Body.String())
	}
	req := map[string]any{"service_epoch": h.m.epoch, "room_id": h.room.RoomID, "room_instance": h.room.RoomInstance, "caller": humanCaller, "provider": "gitlab", "channel": "events", "subscription_id": h.state().SubscriptionID, "subscription_version": h.state().SubscriptionVersion, "config": gitlab.EventsConfig{Enabled: true, IntervalSeconds: 3, Filter: gitlab.EventsFilter{Branches: []string{"main"}}}}
	if r := managerHTTP(h.m, "/v1/platform/notifications/configure", req); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := managerHTTP(h.m, "/v1/platform/notifications/configure", req); r.Code != 409 {
		t.Fatal("stale version", r.Code)
	}
	req["subscription_version"] = h.state().SubscriptionVersion
	for _, kind := range []string{"agent", "integration"} {
		req["caller"] = Caller{Author: Author{Kind: kind, ID: "gitlab"}}
		if r := managerHTTP(h.m, "/v1/platform/notifications/configure", req); r.Code != 403 {
			t.Fatal(kind, r.Code)
		}
	}
	req["caller"] = humanCaller
	req["service_epoch"] = "old"
	if r := managerHTTP(h.m, "/v1/platform/notifications/configure", req); r.Code != 403 {
		t.Fatal(r.Code)
	}
	req["service_epoch"] = h.m.epoch
	delete(req, "config")
	if r := managerHTTP(h.m, "/v1/platform/notifications/sync", req); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	// Public send never accepts integration identity even on the active service.
	svc := eventLiveService(t, h)
	if r := managerHTTP(h.m, "/v1/send", apiRequest{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "integration", ID: "gitlab"}}, Text: "spoof", ClientMessageID: "spoof"}); r.Code != 403 {
		t.Fatal(r.Code)
	}

}

func TestGitLabIntegrationNotificationsRespectPauseBudgetAndFault(t *testing.T) {
	for _, mode := range []string{"pause", "budget", "fault", "running"} {
		t.Run(mode, func(t *testing.T) {
			b := newControlledBackend()
			h, target, id := memberHarness(t, b, nil, func(m *MemberAgent) {
				m.MaxTurns = 2
				if mode == "budget" {
					m.MaxTurns = 1
				}
			})
			roomCall[MemberInfo](t, h, AttachMember{MemberIdentity: id, Target: target})
			if mode != "pause" {
				n := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
				controlMember(t, h, target, id, "run", n, false, false)
				awaitTurn(t, h, b)
				if mode == "fault" {
					b.finish <- errors.Join(errDSHPaused, errors.New("interrupted external operation"))
				} else {
					b.finish <- nil
				}
				waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
			}
			receipt := roomCall[PostReceipt](t, h, integrationPost(h.id, 2))
			status := waitMember(t, h.ctx, target, id, func(s MemberStatus) bool {
				return s.Notification != nil && s.Notification.NewestUnreadSeq == receipt.Seq
			})
			if mode == "running" {
				awaitTurn(t, h, b)
				b.finish <- nil
				status = waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
			}
			if mode == "fault" && (!status.Paused || !strings.Contains(status.Error, "interrupted external operation")) {
				t.Fatal(status)
			}
			if status.Active || mode == "pause" && (!status.Paused || b.opens.Load() != 0) || (mode == "budget" || mode == "fault") && status.Agent.TurnNumber != 1 {
				t.Fatal(status)
			}
			page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: Caller{Author: Author{Kind: "agent", ID: id.AgentID}, MembershipID: id.MembershipID}, Mode: ReadUnread, Peek: true})
			if len(page.Messages) != 2 || page.Messages[1].Author.Kind != "integration" {
				t.Fatal(page)
			}
		})
	}
}

func TestGitLabDeliveryInboxAckFailureReopensWithoutDuplicate(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	seedEventInbox(t, h, 1)
	svc := eventLiveService(t, h)
	before := h.state()
	original := h.m.store.ops.createTemp
	h.m.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("injected inbox ack failure") }
	if err := h.m.notifications.Deliver(context.Background(), h.m.testEventsKey(h.room.RoomIdentity), before.SubscriptionVersion); err == nil {
		t.Fatal("ack failure hidden")
	}
	h.m.store.ops.createTemp = original
	saved, err := svc.store.LoadRoom()
	if err != nil || saved.LatestSeq != 1 || len(saved.IntegrationReceipts) != 1 || len(h.state().Inbox) != 1 {
		t.Fatal(saved, err)
	}
	// Close and reopen both authorities from disk before redelivering.
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	h.m = testManager(t, root, managerTestProfiles(t))
	svc = eventLiveService(t, h)
	if err := h.m.notifications.Deliver(context.Background(), h.m.testEventsKey(h.room.RoomIdentity), before.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	saved, err = svc.store.LoadRoom()
	if err != nil || saved.LatestSeq != 1 || len(saved.Messages) != 1 || len(h.state().Inbox) != 0 {
		t.Fatal(saved, err)
	}
}

func TestGitLabEventsWorkerDeliversCurrentRoomAndDrainsInactiveBacklog(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	seedEventInbox(t, h, 1)
	h.m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		return eventFetchFunc(func(context.Context, gitlab.EventQuery) (gitlab.EventBatch, error) {
			return gitlab.EventBatch{Version: 1, Events: []gitlab.Event{}, Complete: true}, nil
		}), 7, nil
	}
	if err := h.m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	// Querying an inactive room must retain its backlog without a live service.
	if len(h.state().Inbox) != 1 {
		t.Fatal("inactive event lost")
	}
	svc := eventLiveService(t, h)
	h.m.mu.Lock()
	h.m.notifications.WakeRoomLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.state().Inbox) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	saved, err := svc.store.LoadRoom()
	if err != nil || len(h.state().Inbox) != 0 || saved.LatestSeq != 1 || saved.Messages[0].Author.Kind != "integration" {
		t.Fatal(saved, err)
	}
}

func TestPlatformNotificationsRequireExplicitSourceAndSubscription(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	state := h.state()
	q := url.Values{"service_epoch": {h.m.epoch}, "caller_id": {"operator"}, "room_id": {h.room.RoomID}, "room_instance": {h.room.RoomInstance}}
	w := httptest.NewRecorder()
	h.m.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/platform/notifications?"+q.Encode(), nil))
	var list struct {
		Subscriptions []notificationView `json:"subscriptions"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Subscriptions) != 3 {
		t.Fatal("invalid notification list", w.Code, w.Body.String())
	}
	view := list.Subscriptions[0]
	if view.SourceID != gitlab.EventsSourceID || view.SubscriptionID != state.SubscriptionID || view.Version != state.SubscriptionVersion {
		t.Fatal("list omitted explicit identity", view)
	}
	request := func() map[string]any {
		return map[string]any{
			"service_epoch": h.m.epoch, "room_id": h.room.RoomID, "room_instance": h.room.RoomInstance, "caller": humanCaller,
			"provider": "gitlab", "channel": "events", "subscription_id": state.SubscriptionID, "subscription_version": state.SubscriptionVersion,
			"config": state.Config,
		}
	}
	for _, test := range []struct {
		name   string
		change func(map[string]any)
		code   int
	}{
		{"missing source", func(r map[string]any) { delete(r, "provider") }, 400},
		{"missing channel", func(r map[string]any) { delete(r, "channel") }, 400},
		{"unknown channel", func(r map[string]any) { r["channel"] = "unknown" }, 400},
		{"missing subscription", func(r map[string]any) { delete(r, "subscription_id") }, 409},
		{"unknown subscription", func(r map[string]any) { r["subscription_id"] = strings.Repeat("b", 32) }, 409},
		{"duplicate creation", func(r map[string]any) { r["subscription_id"] = ""; r["subscription_version"] = 0 }, 409},
		{"unknown config field", func(r map[string]any) { r["config"] = map[string]any{"enabled": true, "unknown": true} }, 400},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := request()
			test.change(req)
			response := managerHTTP(h.m, "/v1/platform/notifications/configure", req)
			if response.Code != test.code {
				t.Fatal(response.Code, response.Body.String())
			}
			if h.state().SubscriptionVersion != state.SubscriptionVersion {
				t.Fatal("invalid request changed subscription")
			}
		})
	}
	// Source identity, opaque item key and provider spelling are mandatory on disk.
	old := httptest.NewRecorder()
	h.m.ServeHTTP(old, httptest.NewRequest(http.MethodGet, "/v1/gitlab/events?"+q.Encode(), nil))
	if old.Code == 200 {
		t.Fatal("retired route still responds")
	}
}
