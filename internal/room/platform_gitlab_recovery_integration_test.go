//go:build integration || e2e

package room

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestGitLabStartupRecoveryKeepsUnconfirmedCredentialsPaused(t *testing.T) {
	parallelFixtureTest(t)
	for _, status := range []string{"refresh_pending", "refresh_unknown", "reauthorization_required", "credential_rejected"} {
		t.Run(status, func(t *testing.T) {
			h, _, todos := gitlabRecoveryFixture(t)
			code := "credential_unavailable"
			if status == "credential_rejected" {
				code = status
			}
			pauseGitLabRecovery(t, h.m, h.room.RoomIdentity, code)
			beforeEvents, beforeTodos := h.state(), todos.state()
			if status != "credential_rejected" {
				h.m.mu.Lock()
				file, err := h.m.platforms.Load()
				if err != nil {
					t.Fatal(err)
				}
				file.OAuthCredentials[0].State = status
				if status == "refresh_pending" || status == "refresh_unknown" {
					file.OAuthCredentials[0].RefreshAttemptID = strings.Repeat("f", 32)
				}
				err = h.m.store.save(h.m.platformsPathForTest(), file, 2<<20)
				h.m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := h.m.startPlatformNotifications(); err != nil {
				t.Fatal(err)
			}
			if err := h.m.notifications.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeEvents, h.state()) || !reflect.DeepEqual(beforeTodos, todos.state()) {
				t.Fatal("startup repaired an unconfirmed or rejected grant")
			}
		})
	}
}

func TestGitLabCredentialRecoveryOnLiveStartup(t *testing.T) {
	parallelFixtureTest(t)
	h, _, todos := gitlabRecoveryFixture(t)
	pauseGitLabRecovery(t, h.m, h.room.RoomIdentity, "credential_unavailable")
	beforeEvents, beforeTodos := h.state(), todos.state()
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	m := testManager(t, root, managerTestProfiles(t))
	m.notifications.Clock = h.clock.get
	m.mu.Lock()
	loadedEvents, _ := m.eventsSubscriptionLocked(beforeEvents.Metadata().Key)
	loadedTodos, _ := m.todosSubscriptionLocked(beforeTodos.Metadata().Key)
	m.mu.Unlock()
	if !reflect.DeepEqual(loadedEvents, beforeEvents) || !reflect.DeepEqual(loadedTodos, beforeTodos) {
		t.Fatal("offline loading repaired or queried a paused subscription")
	}
	eventStarted, todoStarted := make(chan struct{}, 1), make(chan struct{}, 1)
	m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		select {
		case eventStarted <- struct{}{}:
		default:
		}
		return eventFetchFunc(func(context.Context, gitlab.EventQuery) (gitlab.EventBatch, error) {
			return gitlab.EventBatch{Version: 1, Events: []gitlab.Event{}, Complete: true}, nil
		}), 7, nil
	}
	m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(context.Context, string, RoomIdentity, gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
		select {
		case todoStarted <- struct{}{}:
		default:
		}
		return todoFetchFunc(func(context.Context, gitlab.TodoQuery) (gitlab.TodoBatch, error) {
			return gitlab.TodoBatch{Version: 1, Todos: []gitlab.Todo{}, Complete: true}, nil
		}), nil
	}
	if err := m.startPlatformNotifications(); err != nil {
		t.Fatal(err)
	}
	awaitEventSignal(t, eventStarted, "recovered event collector did not start")
	awaitEventSignal(t, todoStarted, "recovered todo collector did not start")
	m.mu.Lock()
	defer m.mu.Unlock()
	events, _ := m.eventsSubscriptionLocked(beforeEvents.Metadata().Key)
	ts, _ := m.todosSubscriptionLocked(beforeTodos.Metadata().Key)
	if events.ErrorCode != "" || ts.ErrorCode != "" || events.SubscriptionVersion != beforeEvents.SubscriptionVersion+1 || ts.SubscriptionVersion != beforeTodos.SubscriptionVersion+1 ||
		!events.Baseline.Equal(beforeEvents.Baseline) || !ts.Baseline.Equal(beforeTodos.Baseline) {
		t.Fatalf("startup recovery: events=%s/%s v=%d/%d todos=%s/%s v=%d/%d", events.Status, events.ErrorCode, events.SubscriptionVersion, beforeEvents.SubscriptionVersion, ts.Status, ts.ErrorCode, ts.SubscriptionVersion, beforeTodos.SubscriptionVersion)
	}
}

func TestGitLabCredentialRecoveryWaitsForPreviousWorker(t *testing.T) {
	parallelFixtureTest(t)
	h := newTodoHarness(t)
	b := startBlockedTodo(t, h)
	replacement := make(chan struct{}, 1)
	h.m.mu.Lock()
	pause, err := h.m.invalidatePlatformQueriesLocked(h.connection.ID)
	h.m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(context.Context, string, RoomIdentity, gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
		select {
		case replacement <- struct{}{}:
		default:
		}
		return todoFetchFunc(func(context.Context, gitlab.TodoQuery) (gitlab.TodoBatch, error) {
			return gitlab.TodoBatch{Version: 1, Todos: []gitlab.Todo{}, Complete: true}, nil
		}), nil
	}
	if err == nil {
		err = h.m.resumeGitLabSubscriptionsLocked(h.connection.ID, true)
	}
	pause.release()
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	awaitEventSignal(t, b.canceled, "old query was not canceled")
	select {
	case <-replacement:
		t.Fatal("replacement ran before its predecessor settled")
	default:
	}
	b.finish()
	awaitEventSignal(t, b.done, "old query did not settle")
	awaitEventSignal(t, replacement, "recovered query did not start")
	if s := h.state(); len(s.Inbox) != 0 || s.ErrorCode != "" {
		t.Fatalf("recovered worker: status=%s error=%s inbox=%d", s.Status, s.ErrorCode, len(s.Inbox))
	}
}

func TestGitLabAuthorizationRecoversOnlyOriginalSubscriptions(t *testing.T) {
	parallelFixtureTest(t)
	for _, scenario := range []string{"same_account", "different_account", "recovery_save_blocked"} {
		t.Run(scenario, func(t *testing.T) {
			g := newGitlabLoginHarness(t)
			initial, cookie := g.login(t)
			g.callback(initial, cookie, nil)
			m := g.h.m
			fixtures := testManager(t, t.TempDir(), managerTestProfiles(t))
			m.teams = fixtures.teams
			room := managerCreate(t, m, "gitlab-recovery")
			c := platformTestList(t, m)[0]
			m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 7, nil }
			events, err := m.configureEventsSubscription(context.Background(), notification.Key{Owner: room.RoomIdentity, Source: gitlab.EventsSourceID}, 0,
				gitlab.EventsConfig{Target: fixtureInboxTarget(t, m, room.RoomIdentity), Enabled: true, PlatformID: c.ID, Project: "group/project"})
			if err != nil {
				t.Fatal(err)
			}
			todos, err := m.configureTodosSubscription(context.Background(), notification.Key{Owner: room.RoomIdentity, Source: gitlab.TodosSourceID}, 0,
				gitlab.TodosConfig{Target: fixtureInboxTarget(t, m, room.RoomIdentity), Enabled: true, PlatformID: c.ID})
			if err != nil {
				t.Fatal(err)
			}
			pauseGitLabRecovery(t, m, room.RoomIdentity, "credential_rejected")
			if scenario == "different_account" {
				g.user.Store(202)
			}
			if scenario == "recovery_save_blocked" {
				m.mu.Lock()
				s, _ := m.eventsSubscriptionLocked(events.Metadata().Key)
				s.SubscriptionVersion = ^uint64(0) - 1
				err = m.notifications.SaveLocked(s)
				m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			g.req.Version = c.Version
			g.req.Affected = []gitlabAuthPlatform{{ID: c.ID, Version: c.Version, ReferencedRooms: []RoomIdentity{room.RoomIdentity}}}
			next, nextCookie := g.login(t)
			g.callback(next, nextCookie, nil)
			result := g.status(t, next, nextCookie)
			if scenario == "different_account" {
				if result.Status != "failed" || result.ErrorCode != "identity_mismatch" {
					t.Fatal("different account was adopted")
				}
			} else if result.Status != "succeeded" || g.posts.Load() != 2 || g.revokes.Load() != 0 {
				t.Fatal("notification recovery changed authorization ownership", result.Status, result.ErrorCode)
			}
			if scenario == "recovery_save_blocked" && (result.Followup == nil || result.Followup.Notifications != "pending" || len(result.Followup.PendingPlatforms) != 1) {
				t.Fatal("saved authorization hid pending notification recovery", result.Followup)
			}
			if scenario == "same_account" && (result.Followup == nil || result.Followup.Notifications != "restored") {
				t.Fatal("successful recovery was not confirmed", result.Followup)
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			e, _ := m.eventsSubscriptionLocked(events.Metadata().Key)
			if scenario == "same_account" {
				ts, _ := m.todosSubscriptionLocked(todos.Metadata().Key)
				if e.Status == "credentials_invalid" || ts.Status == "credentials_invalid" || e.ErrorCode != "" || ts.ErrorCode != "" ||
					!e.Baseline.Equal(events.Baseline) || !ts.Baseline.Equal(todos.Baseline) || ts.Binding.CredentialFingerprint == todos.Binding.CredentialFingerprint {
					t.Fatal("same-account authorization did not recover the original subscriptions")
				}
			} else if e.Status != "credentials_invalid" {
				t.Fatal("excluded subscription was resumed")
			}
		})
	}
}
