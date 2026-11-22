package notification

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestExplicitCancellationDoesNotRestartChangedSubscription(t *testing.T) {
	entered, release := make(chan struct{}, 4), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	source := fixtureSource{id: SourceID{"fixture", "changes"}, step: func(ctx context.Context, _ StepHost) (time.Duration, bool) {
		entered <- struct{}{}
		<-ctx.Done()
		<-release
		return time.Second, true
	}}
	f := newNotificationFixture(t, source)
	defer unblock()
	s := fixtureSubscription("cancel", source.id)
	f.save(t, s)
	if err := f.m.Start(); err != nil {
		t.Fatal(err)
	}
	signal(t, entered)
	s.Version++
	f.save(t, s)
	done := f.m.CancelLocked(s.K)
	unblock()
	signal(t, done)
	if f.m.DoneLocked(s.K) != nil {
		t.Fatal("explicit cancellation restarted a new version")
	}
	// Configuration recovery is an explicit restart, including the same version.
	f.m.EnsureLocked(s.K)
	signal(t, entered)
	done = f.m.ChangedLocked(s.K)
	signal(t, done)
	signal(t, entered)
}

func TestPlatformPauseFencesStartsAndWaitsForExactPredecessors(t *testing.T) {
	entered := make(chan struct{}, 8)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	source := fixtureSource{id: SourceID{"fixture", "changes"}, step: func(ctx context.Context, _ StepHost) (time.Duration, bool) {
		entered <- struct{}{}
		<-ctx.Done()
		<-release
		return time.Second, true
	}}
	f := newNotificationFixture(t, source)
	defer unblock()
	a, b := fixtureSubscription("a", source.id), fixtureSubscription("b", source.id)
	b.K.Owner.RoomID = "second-room"
	b.Platform = "shared-connection"
	other := fixtureSubscription("other", source.id)
	other.Platform = "unrelated-connection"
	for _, s := range []fixtureState{a, b, other} {
		f.save(t, s)
	}
	if err := f.m.Start(); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		signal(t, entered)
	}
	otherDone := f.m.DoneLocked(other.K)
	pause := f.m.PausePlatforms(a.Platform, b.Platform)
	defer pause.Release()
	nested := f.m.PausePlatforms(a.Platform)
	defer nested.Release()
	for _, s := range []fixtureState{a, b} {
		if err := f.m.InvalidatePlatformLocked(s.Platform); err != nil {
			t.Fatal(err)
		}
		f.m.EnsureLocked(s.K)
		f.m.WakeLocked(s.K)
		f.m.ChangedLocked(s.K)
		runner := f.m.NewRunnerLocked(s.K).(fixtureRunner)
		if _, _, err := runner.h.Read(context.Background()); !errors.Is(err, ErrBusy) {
			t.Fatal("manual query admitted during pause", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := Wait(ctx, pause.Done()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("cancellation counted as settlement", err)
	}
	cancel()
	unblock()
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := Wait(ctx, pause.Done()); err != nil {
		t.Fatal("waited on a replacement worker", err)
	}
	if f.m.DoneLocked(a.K) != nil || f.m.DoneLocked(b.K) != nil {
		t.Fatal("paused platform restarted")
	}
	select {
	case <-otherDone:
		t.Fatal("unrelated platform was canceled")
	default:
	}
	// Recovery installs runnable state under the hold. Release is the only
	// boundary that can restart it; concurrent holds must all be released.
	for _, s := range []fixtureState{a, b} {
		next := f.state(t, s.K)
		next.Version++
		next.RuntimeState.Reset(true, false)
		f.save(t, next)
		f.m.EnsureLocked(s.K)
	}
	pause.Release()
	pause.Release()
	if f.m.DoneLocked(a.K) != nil {
		t.Fatal("one release bypassed another pause")
	}
	if f.m.DoneLocked(b.K) == nil {
		t.Fatal("recovered shared connection did not restart")
	}
	nested.Release()
	if f.m.DoneLocked(a.K) == nil {
		t.Fatal("last release did not restart recovered state")
	}
}

func TestPlatformPauseIncludesIndependentDeliverySettlement(t *testing.T) {
	source := fixtureSource{id: SourceID{"fixture", "changes"}}
	f := newNotificationFixture(t, source)
	s := fixtureSubscription("delivery", source.id)
	s.Inbox["item"] = Item{Key: "item", DeliveryID: "stable", CreatedAt: time.Now()}
	f.save(t, s)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	f.m.host.Deliver = func(context.Context, Metadata, Item) error { close(entered); <-release; return nil }
	finished := make(chan error, 1)
	go func() { finished <- f.m.Deliver(context.Background(), s.K, s.Version) }()
	signal(t, entered)
	pause := f.m.PausePlatforms(s.Platform)
	defer pause.Release()
	if len(pause.Done()) != 1 {
		t.Fatal("independent delivery receipt omitted")
	}
	if err := f.m.Deliver(context.Background(), s.K, s.Version); !errors.Is(err, ErrBusy) {
		t.Fatal("new delivery admitted during pause", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := Wait(ctx, pause.Done()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("delivery released before acknowledgement", err)
	}
	cancel()
	unblock()
	awaitNotification(t, finished)
	if err := Wait(context.Background(), pause.Done()); err != nil {
		t.Fatal(err)
	}
	if len(f.state(t, s.K).Inbox) != 0 {
		t.Fatal("accepted delivery was not acknowledged")
	}
}

func TestInvalidatedCredentialsRemainPausedAfterRelease(t *testing.T) {
	source := fixtureSource{id: SourceID{"fixture", "changes"}}
	f := newNotificationFixture(t, source)
	s := fixtureSubscription("invalid", source.id)
	s.Offset, s.Seen["observed"] = 42, true
	s.Inbox["item"] = Item{Key: "item", DeliveryID: "pending", CreatedAt: time.Now()}
	f.save(t, s)
	if err := f.m.InvalidatePlatformLocked(s.Platform); err != nil {
		t.Fatal(err)
	}
	after := f.state(t, s.K)
	if !after.QueryPaused() || after.Offset != 42 || !after.Seen["observed"] || len(after.Inbox) != 1 {
		t.Fatal("invalidation lost progress or resumed credentials")
	}
	if f.m.DoneLocked(s.K) != nil {
		t.Fatal("failed authorization resumed the worker")
	}
}
