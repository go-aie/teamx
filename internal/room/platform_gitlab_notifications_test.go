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

func unifiedGitLabConfig(t *testing.T, h *eventHarness) gitlab.NotificationConfig {
	t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	v, err := h.m.gitlabNotificationViewLocked(h.room.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	return v.Config
}

func TestGitLabUnifiedConfigurationPersistenceAndVersion(t *testing.T) {
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
	h, _ := newIndependentEventHarness(t)
	for _, mutate := range []func(*gitlab.NotificationConfig){
		func(c *gitlab.NotificationConfig) { c.ConnectionID = "" },
		func(c *gitlab.NotificationConfig) { c.Events.IntervalSeconds = 31 },
		func(c *gitlab.NotificationConfig) {
			c.Target = notification.Target{Mode: "inbox", AgentID: "unknown", MembershipID: "unknown"}
		},
		func(c *gitlab.NotificationConfig) { c.Todos.Enabled = true; c.Todos.ProjectScope = "current" },
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
	cfg.Todos.IntervalSeconds = 60
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
