package room

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func gitlabRecoveryFixture(t *testing.T) (*eventHarness, managedPlatform, *todoHarness) {
	t.Helper()
	h, c := newIndependentEventHarness(t)
	s, err := h.m.configureTodosSubscription(context.Background(), notification.Key{Owner: h.room.RoomIdentity, Source: gitlab.TodosSourceID}, 0, gitlab.TodosConfig{Enabled: true, PlatformID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	todos := &todoHarness{t: t, m: h.m, room: h.room, clock: h.clock, connection: c, key: s.Metadata().Key, baseline: s.Baseline, pages: &todoTestPages{}}
	todos.install()
	todos.newRunner()
	return h, c, todos
}

func pauseGitLabRecovery(t *testing.T, m *roomManager, owner RoomIdentity, code string) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, state := range m.notifications.StatesLocked(owner) {
		next, err := state.InvalidateCredential()
		if err != nil {
			t.Fatal(err)
		}
		switch s := next.(type) {
		case gitlab.EventsState:
			s.ErrorCode = code
			next = s
		case gitlab.TodosState:
			s.ErrorCode = code
			next = s
		}
		if err := m.notifications.SaveLocked(next); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGitLabCredentialRecoveryPreservesSubscriptionsAndProgress(t *testing.T) {
	for _, code := range []string{"credential_unavailable", "credential_rejected"} {
		t.Run(code, func(t *testing.T) {
			h, c, todos := gitlabRecoveryFixture(t)
			h.prepare()
			h.pages.events = []gitlab.Event{h.event(1, 0)}
			h.full()
			todos.pages.pending = []gitlab.Todo{todos.todo(1, 0, "pending")}
			todos.full()
			pauseGitLabRecovery(t, h.m, h.room.RoomIdentity, code)
			beforeEvents, beforeTodos := h.state(), todos.state()
			authorizeTestPlatform(t, h.m, c, 101, "recovered-test-secret")
			if len(beforeEvents.Inbox) != 1 || len(beforeTodos.Inbox) != 1 {
				t.Fatal("fixture must retain a pending notification for each source")
			}
			h.m.mu.Lock()
			err := h.m.resumeGitLabSubscriptionsLocked(c.ID, true)
			h.m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			events, ts := h.state(), todos.state()
			if events.Status != "running" || ts.Status != "running" || events.ErrorCode != "" || ts.ErrorCode != "" ||
				events.SubscriptionVersion != beforeEvents.SubscriptionVersion+1 || ts.SubscriptionVersion != beforeTodos.SubscriptionVersion+1 ||
				ts.Binding.CredentialFingerprint == beforeTodos.Binding.CredentialFingerprint {
				t.Fatal("paused subscriptions were not versioned and resumed")
			}
			if events.SubscriptionID != beforeEvents.SubscriptionID || ts.SubscriptionID != beforeTodos.SubscriptionID ||
				!events.Baseline.Equal(beforeEvents.Baseline) || !ts.Baseline.Equal(beforeTodos.Baseline) ||
				!events.LastFull.Equal(beforeEvents.LastFull) || !ts.LastFull.Equal(beforeTodos.LastFull) ||
				!reflect.DeepEqual(events.Config, beforeEvents.Config) || !reflect.DeepEqual(ts.Config, beforeTodos.Config) ||
				!reflect.DeepEqual(events.Inbox, beforeEvents.Inbox) || !reflect.DeepEqual(ts.Inbox, beforeTodos.Inbox) ||
				!reflect.DeepEqual(events.Observed, beforeEvents.Observed) || !reflect.DeepEqual(ts.Observed, beforeTodos.Observed) {
				t.Fatal("recovery changed subscription identity, settings, progress or backlog")
			}
			h.m.mu.Lock()
			err = h.m.resumeGitLabSubscriptionsLocked(c.ID, true)
			h.m.mu.Unlock()
			if err != nil || !reflect.DeepEqual(events, h.state()) || !reflect.DeepEqual(ts, todos.state()) {
				t.Fatal("repeated account checks changed a recovered subscription", err)
			}
		})
	}
}

func TestGitLabCredentialRecoveryKeepsUnrelatedPausesAndIdentities(t *testing.T) {
	for _, scenario := range []string{"disabled", "retired", "capacity_pause", "binding_changed", "different_user", "different_instance", "different_generation", "different_platform", "removed_member", "busy_room", "unapplied_settings", "rejected_on_startup", "unavailable_account"} {
		t.Run(scenario, func(t *testing.T) {
			h, c, _ := gitlabRecoveryFixture(t)
			pauseGitLabRecovery(t, h.m, h.room.RoomIdentity, "credential_unavailable")
			h.m.mu.Lock()
			defer h.m.mu.Unlock()
			before := []notification.State{}
			for _, state := range h.m.notifications.StatesLocked(h.room.RoomIdentity) {
				switch s := state.(type) {
				case gitlab.EventsState:
					switch scenario {
					case "disabled":
						s.Config.Enabled = false
						s.RuntimeState.Reset(false, false)
					case "retired":
						s, _ = gitlab.RetireEvents(s)
					case "capacity_pause", "binding_changed":
						s.Status, s.ErrorCode = "capacity", "capacity"
						if scenario == "binding_changed" {
							s.Status, s.ErrorCode = "binding_changed", "binding_changed"
						}
					case "different_user":
						s.UserID++
					case "different_instance":
						s.Binding.URL = "https://another-gitlab.invalid"
					case "different_generation":
						s.Binding.PlatformGeneration = strings.Repeat("f", 32)
					case "removed_member":
						s.SubscriptionID = strings.Repeat("f", 32)
						s.Config.Target = notification.Target{Mode: "inbox", AgentID: "removed", MembershipID: strings.Repeat("f", 32)}
					case "rejected_on_startup":
						s.ErrorCode = "credential_rejected"
					}
					state = s
				case gitlab.TodosState:
					switch scenario {
					case "disabled":
						s.Config.Enabled = false
						s.RuntimeState.Reset(false, false)
					case "retired":
						s, _ = gitlab.RetireTodos(s)
					case "capacity_pause", "binding_changed":
						s.Status, s.ErrorCode = "capacity", "capacity"
						if scenario == "binding_changed" {
							s.Status, s.ErrorCode = "binding_changed", "binding_changed"
						}
					case "different_user":
						s.Binding.UserID++
					case "different_instance":
						s.Binding.URL = "https://another-gitlab.invalid"
					case "different_generation":
						s.Binding.PlatformGeneration = strings.Repeat("f", 32)
					case "removed_member":
						s.SubscriptionID = strings.Repeat("f", 32)
						s.Config.Target = notification.Target{Mode: "inbox", AgentID: "removed", MembershipID: strings.Repeat("f", 32)}
					case "rejected_on_startup":
						s.ErrorCode = "credential_rejected"
					}
					state = s
				}
				if err := h.m.notifications.SaveLocked(state); err != nil {
					t.Fatal(err)
				}
				before = append(before, state)
			}
			if scenario == "busy_room" {
				h.m.runtimes[h.room.RoomIdentity] = &roomRuntime{busy: true}
				defer delete(h.m.runtimes, h.room.RoomIdentity)
			}
			if scenario == "unapplied_settings" {
				h.m.gitlabNotifications[h.room.RoomIdentity] = map[notificationScope]gitlabNotificationRecord{
					notificationScopeFor(c.ID, notification.Target{}): {Config: gitlab.NotificationConfig{PlatformID: c.ID}},
				}
			}
			if scenario == "unavailable_account" {
				file, _ := h.m.platforms.Load()
				file.OAuthCredentials[0].State = "reauthorization_required"
				if err := h.m.store.save(h.m.platformsPathForTest(), file, 2<<20); err != nil {
					t.Fatal(err)
				}
			}
			id := c.ID
			if scenario == "different_platform" {
				id = strings.Repeat("e", 32)
			}
			if err := h.m.resumeGitLabSubscriptionsLocked(id, scenario != "rejected_on_startup"); err != nil {
				t.Fatal(err)
			}
			for _, previous := range before {
				after, _ := h.m.notifications.StateLocked(previous.Metadata().Key)
				if !reflect.DeepEqual(previous, after) {
					t.Fatal("recovery modified an excluded subscription", previous.Metadata().Key.Source)
				}
			}
		})
	}
}
