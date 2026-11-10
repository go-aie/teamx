//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestGitLabDeliveryInactiveCrashWindowAndClear(t *testing.T) {
	parallelFixtureTest(t)
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
	parallelFixtureTest(t)
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
				s.Binding.PlatformGeneration = strings.Repeat("e", 32)
				err := h.m.notifications.SaveLocked(s)
				h.m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			case "credential":
				h.m.mu.Lock()
				file, loadErr := h.m.platforms.Load()
				if loadErr == nil {
					_, _, loadErr = h.m.platforms.LogoutOAuth(file.Platforms[0].CredentialID)
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
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
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
	cfg := unifiedGitLabConfig(t, h)
	cfg.IntervalSeconds = 3
	cfg.Events.Filter.Types = []string{"push"}
	req := map[string]any{"service_epoch": h.m.epoch, "room_id": h.room.RoomID, "room_instance": h.room.RoomInstance, "caller": humanCaller, "version": 0, "config": cfg}
	if r := managerHTTP(h.m, "/v1/platform/gitlab/notifications/configure", req); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := managerHTTP(h.m, "/v1/platform/gitlab/notifications/configure", req); r.Code != 409 {
		t.Fatal("stale version", r.Code)
	}
	req["version"] = 1
	for _, kind := range []string{"agent", "integration"} {
		req["caller"] = Caller{Author: Author{Kind: kind, ID: "gitlab"}}
		if r := managerHTTP(h.m, "/v1/platform/gitlab/notifications/configure", req); r.Code != 403 {
			t.Fatal(kind, r.Code)
		}
	}
	req["caller"] = humanCaller
	req["service_epoch"] = "old"
	if r := managerHTTP(h.m, "/v1/platform/gitlab/notifications/configure", req); r.Code != 403 {
		t.Fatal(r.Code)
	}
	req["service_epoch"] = h.m.epoch
	delete(req, "config")
	if r := managerHTTP(h.m, "/v1/platform/gitlab/notifications/sync", req); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	// Public send never accepts integration identity even on the active service.
	svc := eventLiveService(t, h)
	if r := managerHTTP(h.m, "/v1/send", apiRequest{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "integration", ID: "gitlab"}}, Text: "spoof", ClientMessageID: "spoof"}); r.Code != 403 {
		t.Fatal(r.Code)
	}

}

func TestGitLabDeliveryInboxAckFailureReopensWithoutDuplicate(t *testing.T) {
	parallelFixtureTest(t)
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
	parallelFixtureTest(t)
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
	parallelFixtureTest(t)
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
		{"missing subscription", func(r map[string]any) { delete(r, "subscription_id") }, 400},
		{"unknown subscription", func(r map[string]any) { r["subscription_id"] = strings.Repeat("b", 32) }, 400},
		{"duplicate creation", func(r map[string]any) { r["subscription_id"] = ""; r["subscription_version"] = 0 }, 400},
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
