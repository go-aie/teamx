package notification

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-aie/actorx"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func awaitNotification(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("independent operation did not finish before releasing blocked work")
	}
}
func TestRoomSourceOwnerSaveIsolationAndCommittedReads(t *testing.T) {
	source := SourceID{"fixture", "changes"}
	f := newNotificationFixture(t, fixtureSource{id: source})
	a := fixtureSubscription("a", source)
	b := fixtureSubscription("b", source)
	b.K.Owner.RoomID = "other-room"
	f.save(t, a)
	f.save(t, b)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	save := f.m.host.Save
	f.m.host.Save = func(path string, value any, limit int64) error {
		if filepath.Base(path) == filepath.Base(f.path(a.K)) && strings.Contains(path, a.K.Owner.RoomID+string(filepath.Separator)) {
			close(entered)
			<-release
		}
		return save(path, value, limit)
	}
	a.Offset = 1
	done := make(chan error, 1)
	go func() { done <- f.m.SaveLocked(a) }()
	<-entered
	durations := make([]time.Duration, 0, 100)
	for range 100 {
		start := time.Now()
		state, ok := f.m.StateLocked(a.K)
		if !ok || state.(fixtureState).Offset != 0 {
			t.Fatal("uncommitted state leaked")
		}
		durations = append(durations, time.Since(start))
	}
	b.Offset = 2
	other := make(chan error, 1)
	go func() { other <- f.m.SaveLocked(b) }()
	awaitNotification(t, other)
	if f.state(t, b.K).Offset != 2 {
		t.Fatal("other Room did not install its save")
	}
	slices.Sort(durations)
	t.Logf("blocked source: 100/100 committed reads p50=%s p95=%s p99=%s", durations[49], durations[94], durations[98])
	unblock()
	awaitNotification(t, done)
}
func TestDeliveryReceiptDoesNotBlockQueryAndAcknowledgesLatestState(t *testing.T) {
	source := SourceID{"fixture", "changes"}
	f := newNotificationFixture(t, fixtureSource{id: source})
	state := fixtureSubscription("delivery", source)
	state.Inbox["first"] = Item{Key: "first", DeliveryID: "stable-first", CreatedAt: time.Now()}
	f.save(t, state)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	f.m.host.Deliver = func(context.Context, Metadata, Item) error { close(entered); <-release; return nil }
	done := make(chan error, 1)
	go func() { done <- f.m.Deliver(context.Background(), state.K, 1) }()
	<-entered
	query := &session{m: f.m, key: state.K, version: 1}
	current, _, err := query.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	next := current.(fixtureState)
	next.Inbox["second"] = Item{Key: "second", DeliveryID: "stable-second", CreatedAt: time.Now()}
	if err := query.Commit(next); err != nil {
		t.Fatal("query could not commit while receipt was pending", err)
	}
	// A second query captures the state before the acknowledgement is installed.
	stale := &session{m: f.m, key: state.K, version: 1}
	candidate, _, err := stale.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unblock()
	awaitNotification(t, done)
	after := f.state(t, state.K)
	if len(after.Inbox) != 1 || after.Inbox["second"].Key != "second" {
		t.Fatal("acknowledgement overwrote concurrent query", after)
	}
	if err := stale.Commit(candidate); !errors.Is(err, ErrBusy) {
		t.Fatal("late query reinstated acknowledged identity", err)
	}
	if err := f.m.SaveFrom(next, current); !errors.Is(err, ErrBusy) {
		t.Fatal("stale configuration overwrote acknowledgement", err)
	}
}
func TestSourceOwnerCloseRetainsAcceptedWriteAndRejectsNewWork(t *testing.T) {
	source := SourceID{"fixture", "changes"}
	f := newNotificationFixture(t, fixtureSource{id: source})
	state := fixtureSubscription("closing", source)
	f.save(t, state)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	save := f.m.host.Save
	f.m.host.Save = func(path string, value any, limit int64) error {
		close(entered)
		<-release
		return save(path, value, limit)
	}
	state.Offset = 1
	done := make(chan error, 1)
	go func() { done <- f.m.SaveLocked(state) }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := f.m.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("close dropped its in-flight source writer", err)
	}
	if got, ok := f.m.StateLocked(state.K); !ok || got.(fixtureState).Offset != 0 {
		t.Fatal("uncommitted projection during close")
	}
	unblock()
	awaitNotification(t, done)
	if err := f.m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.m.SaveLocked(state); !errors.Is(err, ErrClosing) && !errors.Is(err, actorx.ErrStopped) {
		t.Fatal("closed owner accepted a new write", err)
	}
}

type countingExpiryState struct {
	fixtureState
	reads *atomic.Int64
}

func (s countingExpiryState) CloneState() State {
	return countingExpiryState{s.fixtureState.copy(), s.reads}
}
func (s countingExpiryState) PendingItems() []Item {
	s.reads.Add(1)
	return s.fixtureState.PendingItems()
}
func (s countingExpiryState) Expire(cutoff time.Time) State {
	next := s.CloneState().(countingExpiryState)
	for key, item := range next.Inbox {
		if Expired(item.CreatedAt, cutoff) {
			delete(next.Inbox, key)
		}
	}
	return next
}
func TestSourceExpiryScheduleAvoidsIdleInventoryScans(t *testing.T) {
	source := SourceID{"fixture", "changes"}
	f := newNotificationFixture(t, fixtureSource{id: source})
	now := time.Now().UTC()
	f.m.Clock = func() time.Time { return now }
	f.m.host.Expiry = func() ExpiryPolicy { return ExpiryPolicy{3, true} }
	save := f.m.host.Save
	var writes atomic.Int64
	f.m.host.Save = func(path string, value any, limit int64) error { writes.Add(1); return save(path, value, limit) }
	var memoryBefore, memoryAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&memoryBefore)
	tasksBefore := runtime.NumGoroutine()
	state := countingExpiryState{fixtureState: fixtureSubscription("scheduled", source), reads: &atomic.Int64{}}
	for i := range 1000 {
		key := fmt.Sprint(i)
		state.Inbox[key] = Item{Key: key, DeliveryID: key, CreatedAt: now}
	}
	f.save(t, state)
	for range 100 {
		if err := f.m.ExpireTargetLocked(state.K.Owner, state.Target); err != nil {
			t.Fatal(err)
		}
	}
	if writes.Load() != 1 {
		t.Fatal("idle source maintenance rewrote snapshots", writes.Load())
	}
	if state.reads.Load() != 1 {
		t.Fatal("idle checks rescanned the pending inventory", state.reads.Load())
	}
	now = now.Add(4 * 24 * time.Hour)
	if err := f.m.ExpireTargetLocked(state.K.Owner, state.Target); err != nil {
		t.Fatal(err)
	}
	after, ok := f.m.StateLocked(state.K)
	if !ok || after.Metadata().Pending != 0 {
		t.Fatal("due source did not expire")
	}
	if writes.Load() != 2 {
		t.Fatal("due expiration did not use one commit", writes.Load())
	}
	runtime.GC()
	runtime.ReadMemStats(&memoryAfter)
	t.Logf("100 idle checks: one 1000-item inventory scan, zero writes; due check: one expiration commit; goroutines delta=%d heap delta=%d bytes", runtime.NumGoroutine()-tasksBefore, int64(memoryAfter.HeapAlloc)-int64(memoryBefore.HeapAlloc))
}
