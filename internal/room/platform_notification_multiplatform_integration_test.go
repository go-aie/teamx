//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func sharedGitLabPlatforms(t *testing.T, m *roomManager) (string, string) {
	t.Helper()
	first := platformTestSave(t, m, platformTestRequest(m))
	m.mu.Lock()
	defer m.mu.Unlock()
	file, err := m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	app := file.OAuthApplications[0]
	tokens := platform.OAuthTokens{AccessToken: platform.NewSecret("second-user-access"), RefreshToken: platform.NewSecret("second-user-refresh"), ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"api"}}
	second := strings.Repeat("d", 32)
	_, err = m.platforms.CommitOAuthLogin(nil, second, "Second GitLab", app, "", humanCaller.ID, tokens, platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), UserID: 202, Username: "user202"})
	if err != nil {
		t.Fatal(err)
	}
	file, err = m.platforms.Load()
	if err != nil || len(file.OAuthApplications) != 1 || len(file.OAuthCredentials) != 2 {
		t.Fatal("not a shared-application fixture", err)
	}
	return first.ID, second
}

func platformGitLabView(t *testing.T, m *roomManager, owner RoomIdentity, id string) gitlabNotificationView {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	v, err := m.gitlabNotificationViewLocked(owner, id)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func applyPlatformGitLab(t *testing.T, m *roomManager, owner RoomIdentity, version uint64, cfg gitlab.NotificationConfig) gitlabNotificationView {
	t.Helper()
	v, err := m.configureGitLabNotifications(context.Background(), owner, version, cfg)
	if err != nil || len(v.Errors) != 0 || v.Pending["events"] || v.Pending["todos"] {
		t.Fatal(v, err)
	}
	return v
}

func TestGitLabPlatformsIndependentDefaultsPauseRecoveryAndDelivery(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "two-platforms")
	a, b := sharedGitLabPlatforms(t, m)
	clock := &eventTestClock{now: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)}
	m.notifications.Clock = clock.get
	for _, id := range []string{a, b} {
		draft := platformGitLabView(t, m, room.RoomIdentity, id)
		if draft.Version != 0 || !draft.Config.Enabled || !draft.Config.Todos.Enabled || draft.Config.Events.Enabled {
			t.Fatal("wrong first-use defaults", draft)
		}
		applyPlatformGitLab(t, m, room.RoomIdentity, 0, draft.Config)
	}
	// A read of first-use defaults cannot enable collection by itself.
	va, vb := platformGitLabView(t, m, room.RoomIdentity, a), platformGitLabView(t, m, room.RoomIdentity, b)
	if va.Todos.SubscriptionID == vb.Todos.SubscriptionID || va.Todos.RecipientID != 101 || vb.Todos.RecipientID != 202 {
		t.Fatal("account or subscription identities mixed")
	}
	// Exercise the real account/HTTP resolver for both grants under one app.
	seen := map[string]bool{}
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v4/todos" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		token := r.Header.Get("Authorization")
		if token != "Bearer test-platform-secret" && token != "Bearer second-user-access" {
			t.Fatalf("wrong authorization %q", token)
		}
		seen[token] = true
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("[]"))}, nil
	})
	for _, v := range []gitlabNotificationView{va, vb} {
		m.mu.Lock()
		scope, _, err := m.resolveTodosPlatformLocked(room.RoomIdentity, v.PlatformID)
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		fetcher, err := m.preparePlatformTodos(context.Background(), "", room.RoomIdentity, scope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fetcher.FetchTodos(context.Background(), gitlab.TodoQuery{State: "pending"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 {
		t.Fatal("did not use two exact grants")
	}
	// Accept the same native todo ID independently for each recipient.
	m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(_ context.Context, _ string, _ RoomIdentity, scope gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
		return todoFetchFunc(func(_ context.Context, q gitlab.TodoQuery) (gitlab.TodoBatch, error) {
			batch := gitlab.TodoBatch{Version: 1, Complete: true, ResponseBytes: 200}
			if q.State == "pending" {
				batch.Todos = []gitlab.Todo{{ID: 1, RecipientID: scope.Binding.UserID, ProjectID: 7, Project: "group/project", Action: "mentioned", Reason: "mentioned", Kind: "merge_request", TargetType: "MergeRequest", TargetID: 70, TargetIID: 42, Title: "review", URL: "https://gitlab.invalid/group/project/-/merge_requests/42", CreatedAt: clock.get(), State: "pending"}}
			}
			return batch, nil
		}), nil
	}
	clock.add(time.Second)
	for _, v := range []gitlabNotificationView{va, vb} {
		key := notification.Key{Owner: room.RoomIdentity, Source: gitlab.TodosSourceID, SubscriptionID: v.Todos.SubscriptionID}
		m.mu.Lock()
		runner := m.notifications.NewRunnerLocked(key)
		m.mu.Unlock()
		for i := 0; i < 2; i++ {
			if _, stop := runner.Step(context.Background()); stop {
				t.Fatal("collector stopped")
			}
		}
	}
	state := func(id string) gitlab.TodosState {
		t.Helper()
		m.mu.Lock()
		defer m.mu.Unlock()
		s, err := m.currentTodosLocked(room.RoomIdentity, id)
		if err != nil || s == nil {
			t.Fatal(err)
		}
		return *s
	}
	beforeA, beforeB := state(a), state(b)
	if len(beforeA.Inbox) != 1 || len(beforeB.Inbox) != 1 {
		t.Fatal("missing separate pending evidence")
	}
	cfg := va.Config
	cfg.Enabled = false
	va = applyPlatformGitLab(t, m, room.RoomIdentity, va.Version, cfg)
	paused := state(a)
	if !va.Config.Todos.Enabled || va.Config.Enabled || paused.Config.Enabled || !reflect.DeepEqual(beforeB, state(b)) || !reflect.DeepEqual(beforeA.Inbox, paused.Inbox) || !beforeA.Baseline.Equal(paused.Baseline) {
		t.Fatal("pause changed preferences, progress or other account")
	}
	key := paused.Metadata().Key
	if err := m.notifications.Deliver(context.Background(), key, paused.SubscriptionVersion); !errors.Is(err, notification.ErrIdentity) {
		t.Fatal("disabled delivery admitted", err)
	}
	// Recover a disabled platform without reapplying first-use defaults.
	root := m.store.dir
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	m = testManager(t, root, managerTestProfiles(t))
	m.notifications.Clock = clock.get
	restored := platformGitLabView(t, m, room.RoomIdentity, a)
	if restored.Config.Enabled || !restored.Config.Todos.Enabled || restored.Todos.SubscriptionID != va.Todos.SubscriptionID || !reflect.DeepEqual(state(a), paused) || !reflect.DeepEqual(state(b), beforeB) {
		t.Fatal("restart reset an account")
	}
	cfg = restored.Config
	cfg.Enabled = true
	restored = applyPlatformGitLab(t, m, room.RoomIdentity, restored.Version, cfg)
	if restored.Todos.SubscriptionID != paused.SubscriptionID || !reflect.DeepEqual(state(a).Inbox, paused.Inbox) || !state(a).Baseline.Equal(paused.Baseline) {
		t.Fatal("resume replaced pending progress")
	}
	// Actual Room delivery keeps separate receipts and the exact recipients.
	h := &eventHarness{t: t, m: m, room: room}
	eventLiveService(t, h)
	for _, id := range []string{a, b} {
		s := state(id)
		if err := m.notifications.Deliver(context.Background(), s.Metadata().Key, s.SubscriptionVersion); err != nil {
			t.Fatal(err)
		}
	}
	_, snapshot := readManagedRoom(t, m, room)
	if len(snapshot.Messages) != 2 || len(snapshot.IntegrationReceipts) != 2 {
		t.Fatal("accounts merged their notifications", snapshot)
	}
	if snapshot.Messages[0].Source.SubscriptionID == snapshot.Messages[1].Source.SubscriptionID || snapshot.Messages[0].Source.Reference.RecipientID == snapshot.Messages[1].Source.Reference.RecipientID {
		t.Fatal("delivery lost account identity")
	}
	q := url.Values{"service_epoch": {m.epoch}, "caller_id": {"operator"}, "room_id": {room.RoomID}, "room_instance": {room.RoomInstance}}
	if r := managerHTTP(m, "/v1/platform/gitlab/notifications?"+q.Encode(), nil); r.Code != http.StatusBadRequest {
		t.Fatal("ambiguous platform selected", r.Code)
	}
	q.Set("platform_id", b)
	q.Set("target_mode", "room")
	if r := managerHTTP(m, "/v1/platform/gitlab/notifications?"+q.Encode(), nil); r.Code != http.StatusOK {
		t.Fatal(r.Body.String())
	}
	q.Del("platform_id")
	response := managerHTTP(m, "/v1/platform/notifications?"+q.Encode(), nil)
	var all struct {
		GitLabPlatforms []gitlabNotificationView `json:"gitlab_platforms"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &all) != nil || len(all.GitLabPlatforms) != 2 {
		t.Fatal("multi-platform discovery failed", response.Body.String())
	}
}

func TestGitLabPlatformsApplyLockAndFailuresAreIndependent(t *testing.T) {
	parallelFixtureTest(t)
	h, a := newIndependentEventHarness(t)
	// Add another exact grant under A's application.
	h.m.mu.Lock()
	file, _ := h.m.platforms.Load()
	app := file.OAuthApplications[0]
	b := strings.Repeat("d", 32)
	_, err := h.m.platforms.CommitOAuthLogin(nil, b, "Second GitLab", app, "", humanCaller.ID, platform.OAuthTokens{AccessToken: platform.NewSecret("second-user-access"), RefreshToken: platform.NewSecret("second-user-refresh"), ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"api"}}, platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), UserID: 202, Username: "user202"})
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	blocked := startBlockedEventWorker(t, h)
	defer blocked.finish()
	cfg := unifiedGitLabConfig(t, h)
	cfg.Events.Project = "remote/other"
	done := make(chan error, 1)
	go func() {
		_, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, 0, cfg)
		done <- err
	}()
	awaitEventSignal(t, blocked.canceled, "A was not canceled")
	// B can configure while A is still joining an uncooperative old query.
	vb := applyPlatformGitLab(t, h.m, h.room.RoomIdentity, 0, gitlab.DefaultNotificationConfig(b))
	h.m.mu.Lock()
	s, _ := h.m.currentTodosLocked(h.room.RoomIdentity, b)
	_, admitErr := h.m.admitPlatformNotificationLocked(context.Background(), *s)
	h.m.mu.Unlock()
	if admitErr != nil {
		t.Fatal("A blocked B admission", admitErr)
	}
	if _, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, 1, cfg); !errors.Is(err, ErrBusy) {
		t.Fatal("A lost its apply lock", err)
	}
	blocked.finish()
	awaitEventSignal(t, blocked.worker, "old A query did not settle")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	va := platformGitLabView(t, h.m, h.room.RoomIdentity, a.ID)
	beforeB := platformGitLabView(t, h.m, h.room.RoomIdentity, b)
	// One platform's durable apply failure does not change B's desired or live version.
	verify := h.m.platformProject
	h.m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 0, ErrRequest }
	cfg = va.Config
	cfg.Events.Project = "remote/failure"
	failed, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, va.Version, cfg)
	if err != nil || !failed.Pending["events"] || failed.Errors["events"] == "" || !reflect.DeepEqual(beforeB, platformGitLabView(t, h.m, h.room.RoomIdentity, b)) {
		t.Fatal("failure crossed platforms", failed, err)
	}
	h.m.platformProject = verify
	// Stale version of A must not be accepted merely because B has that version.
	if _, err := h.m.configureGitLabNotifications(context.Background(), h.room.RoomIdentity, vb.Version, cfg); !errors.Is(err, ErrConflict) {
		t.Fatal("version conflict crossed platforms", err)
	}
}
