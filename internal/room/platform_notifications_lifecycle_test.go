package room

import (
	"context"
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
