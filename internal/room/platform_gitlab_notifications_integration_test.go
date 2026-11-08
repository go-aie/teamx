//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestGitLabUnifiedConfigurationPersistenceAndVersion(t *testing.T) {
	parallelFixtureTest(t)
	h, connection := newIndependentEventHarness(t)
	cfg := unifiedGitLabConfig(t, h)
	cfg.Todos.Enabled = true
	v, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, 0, cfg)
	if err != nil || v.Version != 1 || len(v.Errors) != 0 || v.Pending["events"] || v.Pending["todos"] {
		t.Fatal(v, err)
	}
	if v.Config.ConnectionID != connection.ID || v.Events.Config.(gitlab.EventsConfig).ConnectionID != connection.ID || v.Todos.Config.(gitlab.TodosConfig).ConnectionID != connection.ID {
		t.Fatal("shared connection not applied")
	}
	var saved gitlabNotificationRecord
	if err := readSnapshot(h.m.gitlabNotificationPath(h.room.RoomIdentity), &saved, maxGitLabConfigBytes); err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 || !reflect.DeepEqual(saved.Config, v.Config) {
		t.Fatal(saved)
	}
	if _, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, 0, cfg); !errors.Is(err, ErrConflict) {
		t.Fatal("stale version accepted", err)
	}
	// Configuration without any active collection still retains common settings.
	cfg = v.Config
	cfg.Events.Enabled = false
	cfg.Todos.Enabled = false
	v, err = h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, 1, cfg)
	if err != nil || v.Version != 2 || v.Config.ConnectionID != connection.ID || v.Events.Config.(gitlab.EventsConfig).Enabled || v.Todos.Config.(gitlab.TodosConfig).Enabled {
		t.Fatal(v, err)
	}
	h.m.mu.Lock()
	delete(h.m.gitlabNotifications, h.room.RoomIdentity)
	err = h.m.loadGitLabNotificationsLocked(h.room.RoomIdentity)
	reloaded, viewErr := h.m.gitlabNotificationViewLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	if err != nil || viewErr != nil || reloaded.Version != 2 || !reflect.DeepEqual(reloaded.Config, v.Config) {
		t.Fatal(reloaded, err, viewErr)
	}
}

func TestGitLabUnifiedApplyFailureIsDurableAndRetryDoesNotReapplySuccess(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	cfg := unifiedGitLabConfig(t, h)
	cfg.Events.Project = "remote/other"
	cfg.Todos.Enabled = true
	verify := h.m.platformProject
	h.m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 0, ErrRequest }
	v, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, 0, cfg)
	if err != nil || v.Config.Events.Project != "remote/other" || !v.Pending["events"] || v.Pending["todos"] || v.Errors["events"] == "" {
		t.Fatal(v, err)
	}
	todoVersion := v.Todos.Version
	h.m.mu.Lock()
	// A source still using the previous configuration may neither query nor deliver.
	state, _ := h.m.eventsSubscriptionLocked(h.m.testEventsKeyLocked(h.room.RoomIdentity))
	_, admissionErr := h.m.admitPlatformNotificationLocked(context.Background(), state)
	delete(h.m.gitlabNotifications, h.room.RoomIdentity)
	loadErr := h.m.loadGitLabNotificationsLocked(h.room.RoomIdentity)
	loaded, loadViewErr := h.m.gitlabNotificationViewLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	if !errors.Is(admissionErr, notification.ErrBusy) || loadErr != nil || loadViewErr != nil || !loaded.Pending["events"] || loaded.Errors["events"] == "" {
		t.Fatal(admissionErr, loaded, loadErr, loadViewErr)
	}
	h.m.platformProject = verify
	v, err = h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, loaded.Version, loaded.Config)
	if err != nil || v.Version != loaded.Version || v.Pending["events"] || v.Pending["todos"] || len(v.Errors) != 0 || v.Todos.Version != todoVersion || v.Events.Project.ProjectID != 8 {
		t.Fatal(v, err)
	}
}

func TestGitLabUnifiedSettingsValidateBeforePersistence(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	for _, mutate := range []func(*gitlab.NotificationConfig){
		func(c *gitlab.NotificationConfig) { c.ConnectionID = "" },
		func(c *gitlab.NotificationConfig) { c.IntervalSeconds = 121 },
		func(c *gitlab.NotificationConfig) {
			c.Target = notification.Target{Mode: "inbox", AgentID: "unknown", MembershipID: "unknown"}
		},
		func(c *gitlab.NotificationConfig) { c.IntervalSeconds = 2 },
	} {
		cfg := unifiedGitLabConfig(t, h)
		mutate(&cfg)
		if _, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, 0, cfg); err == nil {
			t.Fatal("invalid settings accepted", cfg)
		}
		if _, err := os.Stat(h.m.gitlabNotificationPath(h.room.RoomIdentity)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid settings persisted", err)
		}
	}
}

func TestGitLabUnifiedHTTPIdentityAndNoSecondConfigurationAuthority(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	base := map[string]any{"service_epoch": h.m.epoch, "room_id": h.room.RoomID, "room_instance": h.room.RoomInstance, "caller": humanCaller, "version": uint64(0), "config": unifiedGitLabConfig(t, h)}
	for _, change := range []func(map[string]any){
		func(r map[string]any) { r["caller"] = Caller{Author: Author{Kind: "agent", ID: "worker"}} },
		func(r map[string]any) { r["service_epoch"] = "old" },
		func(r map[string]any) { r["room_instance"] = "old" },
		func(r map[string]any) { r["channel"] = "events" },
	} {
		req := map[string]any{}
		for k, v := range base {
			req[k] = v
		}
		change(req)
		if response := managerHTTP(h.m, "/v1/platform/gitlab/notifications/configure", req); response.Code == http.StatusOK {
			t.Fatal(response.Body.String())
		}
	}
	response := managerHTTP(h.m, "/v1/platform/gitlab/notifications/configure", base)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var v gitlabNotificationView
	if err := json.Unmarshal(response.Body.Bytes(), &v); err != nil || v.Version != 1 {
		t.Fatal(v, err)
	}
	state := h.state()
	legacy := map[string]any{"service_epoch": h.m.epoch, "room_id": h.room.RoomID, "room_instance": h.room.RoomInstance, "caller": humanCaller, "provider": "gitlab", "channel": "events", "subscription_id": state.SubscriptionID, "subscription_version": state.SubscriptionVersion, "config": state.Config}
	if response := managerHTTP(h.m, "/v1/platform/notifications/configure", legacy); response.Code == http.StatusOK {
		t.Fatal("source endpoint changed canonical settings")
	}
	q := url.Values{"service_epoch": {h.m.epoch}, "caller_id": {"operator"}, "room_id": {h.room.RoomID}, "room_instance": {h.room.RoomInstance}}
	response = managerHTTP(h.m, "/v1/platform/gitlab/notifications?"+q.Encode(), nil)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
}

func TestGitLabUnifiedRestartKeepsOneConfiguration(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	cfg := unifiedGitLabConfig(t, h)
	cfg.Events.Enabled = false
	v, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	restored, err := newRoomManager(h.m.store.dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	restored.mu.Lock()
	loaded, err := restored.gitlabNotificationViewLocked(h.room.RoomIdentity)
	restored.mu.Unlock()
	if err != nil || loaded.Version != v.Version || !reflect.DeepEqual(loaded.Config, v.Config) || loaded.Pending["events"] || loaded.Pending["todos"] {
		t.Fatal(loaded, err)
	}
}

func TestGitLabUnifiedChangeDrainsOldQueryAndRejectsConcurrentSave(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	blocked := startBlockedEventWorker(t, h)
	cfg := unifiedGitLabConfig(t, h)
	cfg.Events.Project = "remote/other"
	done := make(chan error, 1)
	go func() {
		_, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, 0, cfg)
		done <- err
	}()
	awaitEventSignal(t, blocked.canceled, "unified save did not cancel old query")
	if _, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, 1, cfg); !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent save accepted", err)
	}
	select {
	case err := <-done:
		t.Fatal("saved before old query settled", err)
	case <-time.After(10 * time.Millisecond):
	}
	blocked.finish()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("save did not finish after query settled")
	}
	if h.state().Binding.ProjectID != 8 {
		t.Fatal("replacement was not applied")
	}
}

func TestGitLabUnifiedDisabledConfigReferencesItsConnection(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "disabled-notifications")
	c := verifyTodoAccount(t, m, platformTestSave(t, m, platformTestRequest(m)), 101)
	cfg, _ := gitlab.NormalizeNotificationConfig(gitlab.NotificationConfig{ConnectionID: c.ID})
	v, err := m.configureGitLabNotifications(context.Background(), room.RoomIdentity, 0, cfg)
	if err != nil || v.Version != 1 {
		t.Fatal(v, err)
	}
	m.mu.Lock()
	refs, err := m.platformConnectionReferencesLocked(c.ID)
	states := m.notifications.StatesLocked(room.RoomIdentity)
	m.mu.Unlock()
	if err != nil || len(refs) != 1 || refs[0].RoomIdentity != room.RoomIdentity || len(states) != 0 {
		t.Fatal(refs, states, err)
	}
}

func TestGitLabUnifiedUnchangedCollectorIsNotCancelled(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	blocked := startBlockedEventWorker(t, h)
	defer func() {
		h.m.mu.Lock()
		h.m.notifications.CancelRoomLocked(h.room.RoomIdentity)
		h.m.mu.Unlock()
		blocked.finish()
		awaitEventSignal(t, blocked.worker, "unchanged worker did not settle")
	}()
	cfg := unifiedGitLabConfig(t, h)
	cfg.Todos.Filter.Reasons = []string{"mentioned"}
	v, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, 0, cfg)
	if err != nil || v.Version != 1 || v.Pending["events"] {
		t.Fatal(v, err)
	}
	select {
	case <-blocked.canceled:
		t.Fatal("unchanged Events query was cancelled for disabled Todo preferences")
	default:
	}
}

func TestGitLabUnifiedShutdownFencesAnApplyingRequest(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	blocked := startBlockedEventWorker(t, h)
	cfg := unifiedGitLabConfig(t, h)
	cfg.Events.Project = "remote/other"
	done := make(chan error, 1)
	go func() {
		_, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, 0, cfg)
		done <- err
	}()
	awaitEventSignal(t, blocked.canceled, "old query was not cancelled")
	h.m.mu.Lock()
	h.m.closing = true
	h.m.mu.Unlock()
	blocked.finish()
	select {
	case err := <-done:
		if !errors.Is(err, ErrClosing) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("applying request did not settle during shutdown")
	}
	var saved gitlabNotificationRecord
	if err := readSnapshot(h.m.gitlabNotificationPath(h.room.RoomIdentity), &saved, maxGitLabConfigBytes); err != nil {
		t.Fatal(err)
	}
	if saved.Version != 1 || saved.Config.Events.Project != "remote/other" || len(saved.Errors) != 0 {
		t.Fatal("request wrote status after shutdown fence", saved)
	}
}

func TestGitLabCommonIntervalChangesBothCollectorsWithoutReplacingProgress(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	cfg := unifiedGitLabConfig(t, h)
	cfg.Todos.Enabled = true
	v, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, 0, cfg)
	if err != nil || len(v.Errors) != 0 {
		t.Fatal(v, err)
	}
	// Preserve real pending evidence while the Room is unavailable for delivery.
	h.pages.events = []gitlab.Event{h.event(1, 1)}
	h.clock.add(2 * time.Second)
	h.newRunner()
	h.prepare()
	h.full()
	before := h.state()
	if len(before.Inbox) != 1 {
		t.Fatal("missing pending event", before)
	}
	cfg = v.Config
	cfg.IntervalSeconds = 120
	v, err = h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, v.Version, cfg)
	after := h.state()
	if err != nil || v.Pending["events"] || v.Pending["todos"] || len(v.Errors) != 0 || v.Config.IntervalSeconds != 120 || v.Events.Config.(gitlab.EventsConfig).IntervalSeconds != 120 || v.Todos.Config.(gitlab.TodosConfig).IntervalSeconds != 120 {
		t.Fatal("common interval not applied to both collectors", v, err)
	}
	if after.SubscriptionID != before.SubscriptionID || !after.Baseline.Equal(before.Baseline) || !reflect.DeepEqual(after.Observed, before.Observed) || !reflect.DeepEqual(after.Inbox, before.Inbox) {
		t.Fatal("interval update replaced progress or pending delivery", before, after)
	}
	if v.Todos.SubscriptionID == "" {
		t.Fatal("Todo collector missing")
	}
	// Loading the persisted configuration restores the one common value.
	h.m.mu.Lock()
	delete(h.m.gitlabNotifications, h.room.RoomIdentity)
	err = h.m.loadGitLabNotificationsLocked(h.room.RoomIdentity)
	loaded, viewErr := h.m.gitlabNotificationViewLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	if err != nil || viewErr != nil || loaded.Config.IntervalSeconds != 120 || loaded.Todos.SubscriptionID != v.Todos.SubscriptionID {
		t.Fatal(loaded, err, viewErr)
	}
}

func TestGitLabRemovedSettingsAndPerSourceWritesAreRejected(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	// Both old write paths are closed even before a unified configuration exists.
	for _, channel := range []string{"events", "todos"} {
		for _, action := range []string{"configure", "sync"} {
			request := map[string]any{"service_epoch": h.m.epoch, "room_id": h.room.RoomID, "room_instance": h.room.RoomInstance, "caller": humanCaller, "provider": "gitlab", "channel": channel, "subscription_id": h.state().SubscriptionID, "subscription_version": h.state().SubscriptionVersion}
			if response := managerHTTP(h.m, "/v1/platform/notifications/"+action, request); response.Code != http.StatusBadRequest {
				t.Fatal("per-source write accepted", channel, action, response.Code)
			}
		}
	}
	for _, mutate := range []func(map[string]any){
		func(c map[string]any) { c["events"].(map[string]any)["interval_seconds"] = 5 },
		func(c map[string]any) { c["todos"].(map[string]any)["interval_seconds"] = 60 },
		func(c map[string]any) { c["todos"].(map[string]any)["inactive_interval_seconds"] = 240 },
		func(c map[string]any) { c["todos"].(map[string]any)["project_scope"] = "current" },
		func(c map[string]any) {
			c["events"].(map[string]any)["filter"].(map[string]any)["branches"] = []string{"main"}
		},
		func(c map[string]any) {
			c["events"].(map[string]any)["filter"].(map[string]any)["ignore_users"] = []string{"bot"}
		},
	} {
		data, _ := json.Marshal(unifiedGitLabConfig(t, h))
		var cfg map[string]any
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
		mutate(cfg)
		request := map[string]any{"service_epoch": h.m.epoch, "room_id": h.room.RoomID, "room_instance": h.room.RoomInstance, "caller": humanCaller, "version": 0, "config": cfg}
		if response := managerHTTP(h.m, "/v1/platform/gitlab/notifications/configure", request); response.Code != http.StatusBadRequest {
			t.Fatal("removed setting accepted", cfg, response.Code)
		}
	}
}

func TestGitLabUnifiedBackfillFreezesBeforeCollectorsAndAcrossLaterEnable(t *testing.T) {
	parallelFixtureTest(t)
	h, c := newIndependentEventHarness(t)
	room := managerCreate(t, h.m, "historical-gitlab")
	cfg := gitlab.NotificationConfig{ConnectionID: c.ID, BackfillSeconds: 3600, Events: gitlab.EventSettings{Enabled: true, Project: "remote/project"}}
	expected := h.clock.get().Add(-time.Hour)
	v, err := h.m.configureGitLabNotifications(context.Background(), room.RoomIdentity, 0, cfg)
	if err != nil || len(v.Errors) != 0 || !v.BackfillLocked || !v.Config.BackfillStart.Equal(expected) || v.Config.BackfillSeconds != 0 || !v.Events.Baseline.Equal(expected) {
		t.Fatal(v, err)
	}
	h.clock.add(2 * time.Hour)
	cfg = v.Config
	cfg.Todos.Enabled = true
	v, err = h.m.configureGitLabNotifications(context.Background(), room.RoomIdentity, v.Version, cfg)
	if err != nil || len(v.Errors) != 0 || !v.Events.Baseline.Equal(expected) || !v.Todos.Baseline.Equal(expected) {
		t.Fatal(v, err)
	}
	h.m.mu.Lock()
	delete(h.m.gitlabNotifications, room.RoomIdentity)
	err = h.m.loadGitLabNotificationsLocked(room.RoomIdentity)
	reloaded, viewErr := h.m.gitlabNotificationViewLocked(room.RoomIdentity)
	h.m.mu.Unlock()
	if err != nil || viewErr != nil || !reloaded.BackfillLocked || !reflect.DeepEqual(v.Config, reloaded.Config) {
		t.Fatal(reloaded, err, viewErr)
	}
	changed := v.Config
	changed.BackfillStart = expected.Add(-time.Hour)
	if _, err := h.m.configureGitLabNotifications(context.Background(), room.RoomIdentity, v.Version, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("locked start changed", err)
	}
	cfg = v.Config
	cfg.Events.Enabled = false
	cfg.Todos.Enabled = false
	v, err = h.m.configureGitLabNotifications(context.Background(), room.RoomIdentity, v.Version, cfg)
	if err != nil || !v.BackfillLocked || !v.Config.BackfillStart.Equal(expected) {
		t.Fatal(v, err)
	}
}

func TestGitLabUnifiedBackfillFailureKeepsStartAndInvalidTimesNeverPersist(t *testing.T) {
	parallelFixtureTest(t)
	h, c := newIndependentEventHarness(t)
	room := managerCreate(t, h.m, "retry-historical-gitlab")
	cfg := gitlab.NotificationConfig{ConnectionID: c.ID, BackfillStart: h.clock.get().Add(time.Second), Events: gitlab.EventSettings{Enabled: true, Project: "remote/project"}}
	if _, err := h.m.configureGitLabNotifications(context.Background(), room.RoomIdentity, 0, cfg); err == nil {
		t.Fatal("future start accepted")
	}
	if _, err := os.Stat(h.m.gitlabNotificationPath(room.RoomIdentity)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid settings persisted", err)
	}
	cfg.BackfillStart = time.Time{}
	cfg.BackfillSeconds = 86400
	verify := h.m.platformProject
	h.m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 0, ErrRequest }
	v, err := h.m.configureGitLabNotifications(context.Background(), room.RoomIdentity, 0, cfg)
	if err != nil || !v.BackfillLocked || !v.Pending["events"] || v.Config.BackfillStart.IsZero() {
		t.Fatal(v, err)
	}
	start := v.Config.BackfillStart
	h.clock.add(time.Hour)
	h.m.platformProject = verify
	v, err = h.m.configureGitLabNotifications(context.Background(), room.RoomIdentity, v.Version, v.Config)
	if err != nil || v.Pending["events"] || !v.Events.Baseline.Equal(start) {
		t.Fatal(v, err)
	}
}

func TestGitLabUnifiedExistingHistoricalSubscriptionKeepsChoice(t *testing.T) {
	parallelFixtureTest(t)
	h, c := newIndependentEventHarness(t)
	room := managerCreate(t, h.m, "existing-historical")
	cfg := gitlab.EventsConfig{Enabled: true, ConnectionID: c.ID, Project: "remote/project", BackfillSeconds: 3600}
	source, err := h.m.configureEventsSubscription(context.Background(), notification.Key{Owner: room.RoomIdentity, Source: gitlab.EventsSourceID}, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	v, err := h.m.gitlabNotificationViewLocked(room.RoomIdentity)
	h.m.mu.Unlock()
	if err != nil || !v.BackfillLocked || v.Config.BackfillSeconds != 3600 {
		t.Fatal(v, err)
	}
	shared := v.Config
	shared.IntervalSeconds = 10
	v, err = h.m.configureGitLabNotifications(context.Background(), room.RoomIdentity, v.Version, shared)
	if err != nil || len(v.Errors) != 0 || !v.Events.Baseline.Equal(source.Baseline) {
		t.Fatal(v, err)
	}
}
