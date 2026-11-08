//go:build integration || e2e

package room

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestIndependentEventsAppHandler(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	eventLiveService(t, h)
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/api/v4/projects" {
			t.Fatalf("unexpected project picker request %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`[{"id":7,"name":"Project","path_with_namespace":"remote/project"},{"id":8,"name":"Other","path_with_namespace":"remote/other"}]`))}, nil
	})
	runAppHandler(t, h.m, "web_events_independent_handler.mjs")
	if h.state().Config.Enabled || h.state().Binding.ProjectID != 8 {
		t.Fatal("app did not persist independent settings")
	}
}

func TestIndependentEventsWithoutRepositoryAndWorkspaceChanges(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	entry, _ := h.m.workspaceRoom(h.room.RoomIdentity)
	saved, err := h.m.readWorkspaceRoomConfig(entry)
	if err != nil || saved.Workspace.Repository != nil || saved.Workspace.Binding != nil {
		t.Fatal("notification created a Git binding", err)
	}
	configPath := filepath.Join(h.m.roomDir(h.room.RoomID), "room-config.json")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	s := h.state()
	if s.UserID != 101 || s.Config.Project != "remote/project" || s.Binding.ProjectID != 7 {
		t.Fatal("wrong verified subscription identity")
	}
	// A failed local operation cannot block independent project API queries.
	h.m.index.WorkspaceOperations = append(h.m.index.WorkspaceOperations, workspaceOperation{RoomIdentity: h.room.RoomIdentity, OperationID: strings.Repeat("f", 32), Status: "failed"})
	h.prepare()
	h.clock.add(time.Second)
	h.pages.events = []gitlab.Event{h.event(1, 0)}
	h.step()
	if len(h.state().Inbox) != 1 {
		t.Fatal("local operation blocked remote notification")
	}
	if err := h.m.syncEventsSubscription(s.Metadata().Key, h.state().SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.configureEventsSubscription(context.Background(), s.Metadata().Key, h.state().SubscriptionVersion, s.Config); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(configPath)
	if string(before) != string(after) {
		t.Fatal("notification changed Workspace or tools")
	}
	// A different local binding remains only a suggestion for future user edits.
	setTestEffectiveWorkspace(t, h.m, h.room, testGitLabWorkspace(), 2)
	if err := h.m.syncEventsSubscription(s.Metadata().Key, h.state().SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	if h.state().Binding.Project != "remote/project" || !h.state().Baseline.Equal(s.Baseline) {
		t.Fatal("notification followed Workspace")
	}
}

func TestIndependentEventsReplacementKeepsOldInboxAndRejectsReplay(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	seedEventInbox(t, h, 1)
	before := h.state()
	h.clock.add(time.Second)
	cfg := before.Config
	cfg.Project = "remote/other"
	next, err := h.m.configureEventsSubscription(context.Background(), before.Metadata().Key, before.SubscriptionVersion, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if next.SubscriptionID == before.SubscriptionID || !next.Baseline.After(before.Baseline) || len(next.Inbox) != 0 || len(next.Observed) != 0 || next.Binding.ProjectID != 8 || next.ProjectID != 0 {
		t.Fatal("replacement reused old progress")
	}
	h.m.mu.Lock()
	old, ok := h.m.eventsSubscriptionLocked(before.Metadata().Key)
	h.m.mu.Unlock()
	if !ok || !old.Retired || old.Config.Enabled || len(old.Inbox) != 1 {
		t.Fatal("old inbox was not preserved")
	}
	if err := h.m.notifications.Deliver(context.Background(), old.Metadata().Key, old.SubscriptionVersion); !errors.Is(err, notification.ErrIdentity) {
		t.Fatal("retired subscription delivered", err)
	}
	if _, err := h.m.configureEventsSubscription(context.Background(), before.Metadata().Key, before.SubscriptionVersion, cfg); !errors.Is(err, ErrConflict) {
		t.Fatal("lost-response replay replaced current subscription", err)
	}
	if _, err := h.m.configureEventsSubscription(context.Background(), notification.Key{Owner: h.room.RoomIdentity, Source: gitlab.EventsSourceID}, 0, cfg); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate creation accepted", err)
	}
	q := url.Values{"service_epoch": {h.m.epoch}, "caller_id": {"operator"}, "room_id": {h.room.RoomID}, "room_instance": {h.room.RoomInstance}}
	response := managerHTTP(h.m, "/v1/platform/notifications?"+q.Encode(), nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"retired":true`) || strings.Contains(response.Body.String(), "fingerprint") {
		t.Fatal("invalid public subscription view")
	}
	// Recovery keeps one active subscription and the retired predecessor.
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, root, managerTestProfiles(t))
	state, err := restored.testEventsState(h.room.RoomIdentity)
	if err != nil || state.SubscriptionID != next.SubscriptionID || state.Binding.ProjectID != 8 {
		t.Fatal("replacement did not survive restart", err)
	}
	restored.mu.Lock()
	old, ok = restored.eventsSubscriptionLocked(before.Metadata().Key)
	restored.mu.Unlock()
	if !ok || !old.Retired || len(old.Inbox) != 1 {
		t.Fatal("restart lost old inbox")
	}
}

func TestIndependentEventsVerificationGuardsAndConnectionLifecycle(t *testing.T) {
	parallelFixtureTest(t)
	h, c := newIndependentEventHarness(t)
	s := h.state()
	for _, project := range []string{"https://other.invalid/remote/project", "https://gitlab.invalid/remote/project?token=private", "remote/../project", "remote/missing"} {
		cfg := s.Config
		cfg.Project = project
		if _, err := h.m.configureEventsSubscription(context.Background(), s.Metadata().Key, s.SubscriptionVersion, cfg); !errors.Is(err, ErrRequest) {
			t.Fatal("invalid project accepted", err)
		}
		if h.state().SubscriptionVersion != s.SubscriptionVersion {
			t.Fatal("failed verification changed progress")
		}
	}
	// References come from the subscription alone: no Workspace or tool config.
	refs := platformTestList(t, h.m)[0].References
	if len(refs) != 1 || refs[0].RoomIdentity != h.room.RoomIdentity {
		t.Fatal("missing independent connection reference")
	}
	if _, err := h.m.updatePlatformConnection(platformConnectionRequest{ID: c.ID, Version: c.Version}, true); !errors.Is(err, ErrConflict) {
		t.Fatal("deleted referenced connection", err)
	}
	req := platformTestRequest(h.m)
	req.Version = c.Version

	req.ReferencedRooms = []RoomIdentity{h.room.RoomIdentity}
	c = platformTestReauthorize(t, h.m, req)
	if h.state().Status != "credentials_invalid" {
		t.Fatal("rotation did not pause subscription")
	}
	c = verifyTodoAccount(t, h.m, c, 101)
	if err := h.m.syncEventsSubscription(s.Metadata().Key, h.state().SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	if h.state().SubscriptionID != s.SubscriptionID || !h.state().Baseline.Equal(s.Baseline) {
		t.Fatal("same-account rotation reset progress")
	}
	req.Version = c.Version

	c = platformTestReauthorize(t, h.m, req)
	verifyTodoAccount(t, h.m, c, 202)
	if err := h.m.syncEventsSubscription(s.Metadata().Key, h.state().SubscriptionVersion); !errors.Is(err, ErrConflict) {
		t.Fatal("account switch resumed old identity", err)
	}
	next, err := h.m.configureEventsSubscription(context.Background(), s.Metadata().Key, h.state().SubscriptionVersion, s.Config)
	if err != nil || next.SubscriptionID == s.SubscriptionID || next.UserID != 202 {
		t.Fatal("explicit account switch did not create new subscription", err)
	}
}

func TestIndependentEventsReplacementWaitsForOldWorker(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	b := startBlockedEventWorker(t, h)
	before := h.state()
	cfg := before.Config
	cfg.Project = "remote/other"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	old, err := h.m.configureEventsSubscription(ctx, before.Metadata().Key, before.SubscriptionVersion, cfg)
	if !errors.Is(err, context.DeadlineExceeded) || !old.Retired {
		t.Fatal("replacement did not wait for old query", err)
	}
	awaitEventSignal(t, b.canceled, "old query was not cancelled")
	key := notification.Key{Owner: h.room.RoomIdentity, Source: gitlab.EventsSourceID}
	if _, err := h.m.configureEventsSubscription(context.Background(), key, 0, cfg); !errors.Is(err, ErrBusy) {
		t.Fatal("replacement started before old query settled", err)
	}
	b.finish()
	awaitEventSignal(t, b.worker, "old query did not settle")
	h.m.mu.Lock()
	old, _ = h.m.eventsSubscriptionLocked(before.Metadata().Key)
	h.m.mu.Unlock()
	if len(old.Inbox) != 0 || len(old.Observed) != 0 {
		t.Fatal("late query wrote retired subscription")
	}
	// Stop workers before creating the replacement so the fixture cannot reuse
	// the deliberately blocked fetch function for an unrelated project.
	if err := h.m.notifications.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	next, err := h.m.configureEventsSubscription(context.Background(), key, 0, cfg)
	if err != nil || next.SubscriptionID == old.SubscriptionID || next.Binding.ProjectID != 8 {
		t.Fatal("replacement could not recover after settlement", err)
	}
}

func TestIndependentEventsPublicConfigureAndStaleVerification(t *testing.T) {
	parallelFixtureTest(t)
	h, c := newIndependentEventHarness(t)
	other := managerCreate(t, h.m, "api-only")
	req := map[string]any{"service_epoch": h.m.epoch, "caller": humanCaller, "room_id": other.RoomID, "room_instance": other.RoomInstance,
		"version": 0,
		"config":  gitlab.NotificationConfig{ConnectionID: c.ID, Events: gitlab.EventSettings{Enabled: true, Project: c.URL + "/remote/project"}}}
	response := managerHTTP(h.m, "/v1/platform/gitlab/notifications/configure", req)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"project":"remote/project"`) || !strings.Contains(response.Body.String(), `"recipient_id":101`) || strings.Contains(response.Body.String(), "test-platform-secret") {
		t.Fatal("independent HTTP configuration failed", response.Code)
	}
	req["config"] = gitlab.NotificationConfig{ConnectionID: c.ID, Events: gitlab.EventSettings{Enabled: true, Project: "remote/project"}}
	if response := managerHTTP(h.m, "/v1/platform/gitlab/notifications/configure", req); response.Code != 409 {
		t.Fatal("duplicate HTTP creation did not conflict", response.Code)
	}
	// Verification runs outside the Room gate. A concurrent edit must invalidate
	// its response before retirement or creation can change durable state.
	entered, release := make(chan struct{}), make(chan struct{})
	h.m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) {
		close(entered)
		<-release
		return 8, nil
	}
	before := h.state()
	cfg := before.Config
	cfg.Project = "remote/other"
	result := make(chan error, 1)
	go func() {
		_, err := h.m.configureEventsSubscription(context.Background(), before.Metadata().Key, before.SubscriptionVersion, cfg)
		result <- err
	}()
	awaitEventSignal(t, entered, "project verification did not start")
	edit := before.Config
	edit.Enabled = false
	_, editErr := h.m.configureEventsSubscription(context.Background(), before.Metadata().Key, before.SubscriptionVersion, edit)
	close(release)
	if editErr != nil {
		t.Fatal(editErr)
	}
	if err := <-result; !errors.Is(err, ErrConflict) {
		t.Fatal("late project verification was accepted", err)
	}
	if h.state().Retired || h.state().Binding.Project != before.Binding.Project || h.state().Config.Enabled {
		t.Fatal("late verification overwrote concurrent edit")
	}
}

func TestIndependentEventsWorkspaceLifecycleDoesNotCancelQuery(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	b := startBlockedEventWorker(t, h)
	// Workspace changes no longer have a notification cancellation hook.
	w := testGitLabWorkspace()
	setTestEffectiveWorkspace(t, h.m, h.room, w, 1)
	select {
	case <-b.canceled:
		t.Fatal("Workspace cancelled independent query")
	default:
	}
	h.m.mu.Lock()
	h.m.notifications.CancelRoomLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	awaitEventSignal(t, b.canceled, "room-wide cancellation did not stop independent query")
	b.finish()
	awaitEventSignal(t, b.worker, "independent query did not settle")
}

func TestIndependentEventsInjectsOnlyVerifiedCredentialSnapshot(t *testing.T) {
	parallelFixtureTest(t)
	h, c := newIndependentEventHarness(t)

	req := platformTestRequest(h.m)
	req.Version = c.Version
	req.ReferencedRooms = []RoomIdentity{h.room.RoomIdentity}
	c = verifyTodoAccount(t, h.m, platformTestReauthorize(t, h.m, req), 101)
	s := h.state()
	h.m.mu.Lock()
	binding, err := h.m.eventsBindingLocked(s)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	called := false
	h.m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })
	read := h.m.eventsRead(binding)
	changeTestConnectionRevision(t, h.m, c.ID)

	if _, err := read(context.Background(), "/projects/7/events", nil, nil, 1024); !errors.Is(err, ErrConflict) || called {
		t.Fatal("unverified environment credential reached HTTP transport", err)
	}
	h.m.mu.Lock()
	_, err = h.m.eventsBindingLocked(s)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal("metadata revision lost verified account", err)
	}
}
