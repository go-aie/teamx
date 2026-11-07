package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func awaitEventSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(what)
	}
}

type blockedEventWorker struct {
	entered  chan struct{}
	canceled chan struct{}
	release  chan struct{}
	once     sync.Once
	worker   <-chan struct{}
}

func (b *blockedEventWorker) finish() { b.once.Do(func() { close(b.release) }) }

func startBlockedEventWorker(t *testing.T, h *eventHarness) *blockedEventWorker {
	t.Helper()
	blocked := &blockedEventWorker{entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(blocked.finish) // registered after manager cleanup; release first
	event := h.event(1, 0)
	h.m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		return eventFetchFunc(func(ctx context.Context, _ gitlab.EventQuery) (gitlab.EventBatch, error) {
			close(blocked.entered)
			<-ctx.Done()
			close(blocked.canceled)
			<-blocked.release // deliberately late even after cancellation
			return gitlab.EventBatch{Version: 1, Events: []gitlab.Event{event}, Complete: true, ResponseBytes: 200}, nil
		}), 7, nil
	}
	h.clock.add(time.Second)
	if err := h.m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	awaitEventSignal(t, blocked.entered, "query did not start")
	h.m.mu.Lock()
	state, _ := h.m.eventsSubscriptionLocked(h.m.testEventsKeyLocked(h.room.RoomIdentity))
	blocked.worker = h.m.notifications.DoneLocked(state.Metadata().Key)
	h.m.mu.Unlock()
	if blocked.worker == nil {
		t.Fatal("query lost its owner")
	}
	return blocked
}

func TestGitLabEventsDisableWaitsAndRejectsLateResult(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	b := startBlockedEventWorker(t, h)
	before := h.state()
	cfg := before.Config
	cfg.Enabled = false
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result, err := h.m.configureEventsSubscription(ctx, h.m.testEventsKey(h.room.RoomIdentity), before.SubscriptionVersion, cfg)
	if !errors.Is(err, context.DeadlineExceeded) || result.Config.Enabled {
		t.Fatal(result, err)
	}
	awaitEventSignal(t, b.canceled, "disable did not cancel the query")
	if s := h.state(); s.Config.Enabled || !s.Baseline.Equal(before.Baseline) || len(s.Inbox) != 0 {
		t.Fatal(s)
	}
	select {
	case <-b.worker:
		t.Fatal("reported settlement before query returned")
	default:
	}
	b.finish()
	awaitEventSignal(t, b.worker, "disabled query did not settle")
	s := h.state()
	if len(s.Inbox) != 0 || len(s.Observed) != 0 || !s.Watermark.Equal(before.Watermark) {
		t.Fatal("late canceled response was accepted", s)
	}
	// Turning back on keeps the existing subscription and baseline.
	h.m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		return eventFetchFunc(func(context.Context, gitlab.EventQuery) (gitlab.EventBatch, error) {
			return gitlab.EventBatch{Version: 1, Events: []gitlab.Event{}, Complete: true}, nil
		}), 7, nil
	}
	cfg.Enabled = true
	enabled, err := h.m.configureEventsSubscription(context.Background(), h.m.testEventsKey(h.room.RoomIdentity), s.SubscriptionVersion, cfg)
	if err != nil || enabled.SubscriptionID != before.SubscriptionID || !enabled.Baseline.Equal(before.Baseline) {
		t.Fatal(enabled, err)
	}
}

func TestGitLabEventsDeletionJoinsQueryBeforeRemovingRoom(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	b := startBlockedEventWorker(t, h)
	path := h.m.testNotificationPath(t, h.room.RoomIdentity)
	beginDeletion(t, h.m, h.room)
	awaitEventSignal(t, b.canceled, "deletion did not cancel query")
	if _, err := os.Stat(path); err != nil {
		t.Fatal("removed inbox while query still owned", err)
	}
	select {
	case <-b.worker:
		t.Fatal("query completed before release")
	default:
	}
	b.finish()
	if remaining := awaitDeletion(t, h.m, h.room); remaining != nil {
		t.Fatal(remaining)
	}
	awaitEventSignal(t, b.worker, "deleted query did not finish")
	if _, err := os.Stat(h.m.roomDir(h.room.RoomID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("late result recreated deleted room", err)
	}
	if _, err := h.m.testEventsState(h.room.RoomIdentity); !errors.Is(err, ErrIdentity) {
		t.Fatal("deleted subscription retained", err)
	}
}

func TestGitLabEventsShutdownRetainsLockUntilQuerySettlement(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	b := startBlockedEventWorker(t, h)
	eventLiveService(t, h)
	otherRoom := managerCreate(t, h.m, "healthy-background-room")
	eventLiveService(t, &eventHarness{m: h.m, room: otherRoom})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := h.m.shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("shutdown released unsettled query", err)
	}
	awaitEventSignal(t, b.canceled, "shutdown did not cancel query")
	for _, room := range []roomSummary{h.room, otherRoom} {
		store, err := OpenStore(h.m.roomDir(room.RoomID), room.RoomID)
		if err != nil {
			t.Fatal("unsettled query skipped Room service cleanup", err)
		}
		store.Close()
	}
	if other, err := newRoomManager(h.m.store.dir, 7); !errors.Is(err, ErrLocked) {
		if other != nil {
			_ = other.close()
		}
		t.Fatal("root lock released before query settlement", err)
	}
	b.finish()
	awaitEventSignal(t, b.worker, "query failed to settle")
	if err := h.m.shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, h.m.store.dir, managerTestProfiles(t))
	s, err := reopened.testEventsState(h.room.RoomIdentity)
	if err != nil || len(s.Inbox) != 0 || len(s.Observed) != 0 {
		t.Fatal("late shutdown result persisted", s, err)
	}
}

func TestGitLabEventsSlowSubscriptionDoesNotBlockOtherRoom(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	b := startBlockedEventWorker(t, h)
	other := managerCreate(t, h.m, "other-events")
	setTestEffectiveWorkspace(t, h.m, other, testGitLabWorkspace(), 1)
	observed := make(chan struct{}, 1)
	h.m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(_ context.Context, _ string, binding gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		if binding.RoomID != other.RoomID {
			t.Errorf("unexpected new preparation for blocked subscription")
		}
		return eventFetchFunc(func(_ context.Context, q gitlab.EventQuery) (gitlab.EventBatch, error) {
			if q.Mode == "window" {
				select {
				case observed <- struct{}{}:
				default:
				}
			}
			return gitlab.EventBatch{Version: 1, Events: []gitlab.Event{}, Complete: true}, nil
		}), 7, nil
	}
	if _, err := seedTestEvents(h.m, other.RoomIdentity, gitlab.EventsConfig{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	h.clock.add(time.Second)
	awaitEventSignal(t, observed, "slow first subscription blocked second room window")
	otherState, err := h.m.testEventsState(other.RoomIdentity)
	// The signal precedes the final atomic snapshot commit; wait for its result.
	deadline := time.Now().Add(5 * time.Second)
	for err == nil && otherState.LastFull.IsZero() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		otherState, err = h.m.testEventsState(other.RoomIdentity)
	}
	if err != nil || otherState.LastFull.IsZero() {
		t.Fatal(otherState, err)
	}
	h.m.mu.Lock()
	h.m.notifications.CancelRoomLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	b.finish()
	awaitEventSignal(t, b.worker, "blocked subscription did not settle")
}

func TestGitLabEventsWorkspaceChangePreservesIndependentSubscription(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	h.prepare()
	h.clock.add(time.Second)
	newWorkspace := testGitLabWorkspace()
	newWorkspace.Binding.ConnectionID = strings.Repeat("e", 32)

	h.runner.Fetcher = eventFetchFunc(func(context.Context, gitlab.EventQuery) (gitlab.EventBatch, error) {
		setTestEffectiveWorkspace(t, h.m, h.room, newWorkspace, 2)
		return gitlab.EventBatch{Version: 1, Events: []gitlab.Event{h.event(1, 0)}, Complete: true}, nil
	})
	_, stop := h.runner.Step(context.Background())
	s := h.state()
	if stop || s.Status != "running" || len(s.Inbox) != 1 || len(s.Observed) != 1 || s.LastCheck.IsZero() {
		t.Fatal("Workspace change interrupted independent subscription", s)
	}
	cfg := s.Config
	cfg.Enabled = false
	if _, err := h.m.configureEventsSubscription(context.Background(), h.m.testEventsKey(h.room.RoomIdentity), s.SubscriptionVersion, cfg); err != nil {
		t.Fatal("could not disable invalid old binding", err)
	}
	disabled := h.state()
	cfg.Enabled = true
	resumed, err := h.m.configureEventsSubscription(context.Background(), h.m.testEventsKey(h.room.RoomIdentity), disabled.SubscriptionVersion, cfg)
	if err != nil || resumed.Binding.ConnectionID != s.Binding.ConnectionID || resumed.SubscriptionID != s.SubscriptionID || !resumed.Baseline.Equal(s.Baseline) {
		t.Fatal("explicit credential refresh reset subscription progress", resumed, err)
	}
}

func TestGitLabEventsNoImplicitEnableAndCorruptStateFailsClosed(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "bound-no-monitoring")
	setTestEffectiveWorkspace(t, m, room, testGitLabWorkspace(), 1)
	m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(context.Context, string, gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		t.Error("binding auto-enabled event monitoring")
		return nil, 0, errors.New("unexpected")
	}
	if err := m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.roomDir(room.RoomID), "notifications")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("binding created monitoring state", err)
	}
	h := newEventHarness(t, gitlab.EventsConfig{})
	path := h.m.testNotificationPath(t, h.room.RoomIdentity)
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"format":999,"pat":"never-echo-private-value"}`), 0600); err != nil {
		t.Fatal(err)
	}
	restored, err := newRoomManager(h.m.store.dir, 7)
	if restored != nil {
		_ = restored.close()
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("corrupt state silently reset", err)
	}
}
