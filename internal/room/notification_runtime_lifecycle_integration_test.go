//go:build integration || e2e

package room

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

type settlingSearchReader struct {
	*lark.Client
	entered, canceled, release chan struct{}
}

func (r *settlingSearchReader) SearchMessages(ctx context.Context, _ platform.AccountIdentity, _ lark.MessageSearchRequest) (lark.MessageSearchPage, error) {
	close(r.entered)
	<-ctx.Done()
	close(r.canceled)
	<-r.release // Cancellation alone has not settled this reader.
	return lark.MessageSearchPage{Items: []lark.MessageSearchItem{}}, nil
}

func blockedLarkSearch(t *testing.T) (*larkHarness, *settlingSearchReader, <-chan struct{}, func()) {
	t.Helper()
	h := newLarkHarness(t)
	cfg := h.m.defaultLarkConfig(h.c.ID)
	cfg.Target = platformMemberTarget(notificationMemberIdentity(h.room.RoomIdentity, h.members[0]))
	cfg.Groups.Enabled = false
	h.configure(cfg)
	r := &settlingSearchReader{entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	finish := func() { once.Do(func() { close(r.release) }) }
	t.Cleanup(finish)
	h.m.notifications.Source(lark.MessagesSourceID).(*lark.SearchSource).Reader = func(q lark.QueryScope) (lark.MessageReader, error) {
		client, err := h.m.larkClients.New(q.Profile)
		r.Client = client
		return r, err
	}
	h.clock.add(3 * time.Second)
	if err := h.m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	awaitEventSignal(t, r.entered, "messages search did not start")
	done := h.m.notifications.DoneLocked(h.state().Metadata().Key)
	if done == nil {
		t.Fatal("messages worker lost settlement handle")
	}
	return h, r, done, finish
}

func TestMessagesDisableTimeoutKeepsDurableVersionAndWaitsForSearchSettlement(t *testing.T) {
	parallelFixtureTest(t)
	h, reader, done, finish := blockedLarkSearch(t)
	before := h.state()
	cfg := before.Config
	cfg.Enabled = false
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if _, err := h.m.configureLarkSubscription(ctx, before.Metadata().Key, before.SubscriptionVersion, cfg); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("disable treated cancellation as settlement", err)
	}
	awaitEventSignal(t, reader.canceled, "disable did not cancel search")
	select {
	case <-done:
		t.Fatal("worker settled before its query")
	default:
	}
	paused := h.state()
	if paused.Config.Enabled || paused.SubscriptionVersion != before.SubscriptionVersion+1 || paused.Baseline != before.Baseline || paused.SubscriptionID != before.SubscriptionID {
		t.Fatal("lost response changed the durable opt-out or source identity")
	}
	// A lost response does not authorize replay with an outdated version.
	if _, err := h.m.configureLarkSubscription(t.Context(), before.Metadata().Key, before.SubscriptionVersion, cfg); !errors.Is(err, ErrConflict) {
		t.Fatal("stale configuration replay succeeded", err)
	}
	finish()
	awaitEventSignal(t, done, "disabled messages query did not settle")
	if after := h.state(); after.SubscriptionVersion != paused.SubscriptionVersion || after.Search.Metrics.Pages != paused.Search.Metrics.Pages || after.LastCheck != paused.LastCheck || len(after.Inbox) != 0 {
		t.Fatal("late result crossed the persisted configuration fence")
	}
}

func TestMessagesShutdownRetainsStoreUntilSearchSettlementAndSupportsRetry(t *testing.T) {
	parallelFixtureTest(t)
	h, reader, done, finish := blockedLarkSearch(t)
	before := h.state()
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if err := h.m.shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("shutdown released an unsettled reader", err)
	}
	awaitEventSignal(t, reader.canceled, "shutdown did not cancel messages search")
	if other, err := newRoomManager(h.m.store.dir, 7); !errors.Is(err, ErrLocked) {
		if other != nil {
			_ = other.close()
		}
		t.Fatal("Store lock released before messages query settlement", err)
	}
	finish()
	awaitEventSignal(t, done, "shutdown query did not settle")
	if err := h.m.shutdown(t.Context()); err != nil {
		t.Fatal("shutdown retry", err)
	}
	reopened := testManager(t, h.m.store.dir, managerTestProfiles(t))
	state, ok := reopened.notifications.StateLocked(before.Metadata().Key)
	if !ok {
		t.Fatal("source opt-in lost on reopen")
	}
	after := state.(lark.State)
	if after.SubscriptionID != before.SubscriptionID || after.Baseline != before.Baseline || after.Search.Metrics.Pages != before.Search.Metrics.Pages || len(after.Inbox) != 0 {
		t.Fatal("late shutdown search result changed saved progress")
	}
}
