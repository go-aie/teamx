//go:build integration || e2e

package room

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRuntimeShutdownStopsEveryRoomDespiteUnsettledMember(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
	entered := make(chan MemberIdentity, 8)
	canceled := make(chan MemberIdentity, 8)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	m.factory = controlledActivationFactory(func(id MemberIdentity, ctx context.Context) error {
		entered <- id
		<-ctx.Done()
		canceled <- id
		if id.RoomIdentity == a.RoomIdentity {
			<-release
		}
		return ctx.Err()
	}, nil)
	for _, room := range []roomSummary{a, b} {
		openOK(t, m, room.RoomIdentity, room.CreateID)
		m.mu.Lock()
		svc := m.runtimes[room.RoomIdentity].service
		m.mu.Unlock()
		managedPost(t, m, svc, room.CreateID)
		select {
		case id := <-entered:
			if id.RoomIdentity != room.RoomIdentity {
				t.Fatal("unexpected Room started", id)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("turn did not start")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- m.shutdown(ctx) }()
	for range 2 {
		select {
		case <-canceled:
		case <-time.After(3 * time.Second):
			t.Fatal("one unsettled Room prevented another Room's stop")
		}
	}
	err := <-stopped
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), a.RoomID) {
		t.Fatal("missing per-Room settlement error", err)
	}
	if other, err := newRoomManager(m.store.dir, 7); !errors.Is(err, ErrLocked) {
		if other != nil {
			other.close()
		}
		t.Fatal("root ownership released before A settled", err)
	}
	var disk roomsIndex
	if err := readSnapshot(m.indexPath(), &disk, maxRoomsFileBytes); err != nil {
		t.Fatal(err)
	}
	var aError string
	for _, record := range disk.Shutdowns {
		if record.RoomIdentity == a.RoomIdentity {
			aError = record.Error
		}
	}
	if aError == "" || !hasRoomOwner(disk.OwnedRooms, a.RoomIdentity) || !hasRoomOwner(disk.OwnedRooms, b.RoomIdentity) {
		t.Fatal("failed settlement lost durable diagnostics/ownership", disk.Shutdowns)
	}
	if store, err := OpenStore(m.roomDir(b.RoomID), b.RoomID); err != nil {
		t.Fatal("healthy B did not settle in shared deadline", err)
	} else {
		store.Close()
	}
	select {
	case id := <-entered:
		t.Fatal("shutdown handed off to a waiting member", id)
	default:
	}
	unblock()
	if err := m.close(); err != nil {
		t.Fatal("settlement could not be joined on retry", err)
	}
}

func TestRuntimeShutdownToolTimeoutStillClosesAllServices(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	a, b := managerCreate(t, m, "a"), managerCreate(t, m, "b")
	openOK(t, m, a.RoomIdentity, "a")
	openOK(t, m, b.RoomIdentity, "b")
	toolCtx, cancelTool := context.WithCancel(context.Background())
	call := &platformCall{owner: a.RoomIdentity, cancel: cancelTool, done: make(chan struct{})}
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { m.finishPlatformCall(call) }) }
	m.mu.Lock()
	if m.platformCalls == nil {
		m.platformCalls = map[*platformCall]bool{}
	}
	m.platformCalls[call] = true
	m.mu.Unlock()
	t.Cleanup(finish)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := m.shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unsettled tool did not retain shutdown", err)
	}
	if toolCtx.Err() == nil {
		t.Fatal("shutdown did not cancel tool")
	}
	for _, room := range []roomSummary{a, b} {
		if store, err := OpenStore(m.roomDir(room.RoomID), room.RoomID); err != nil {
			t.Fatal("tool timeout skipped service cleanup", room, err)
		} else {
			store.Close()
		}
	}
	if other, err := newRoomManager(m.store.dir, 7); err == nil {
		other.close()
		t.Fatal("tool timeout released root ownership")
	}
	finish()
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeShutdownCancelsRoomOperationBeforeWaiting(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	a, b := managerCreate(t, m, "operation"), managerCreate(t, m, "service")
	openOK(t, m, b.RoomIdentity, "b")
	m.mu.Lock()
	rt := m.reserveRuntimeLocked(a.RoomIdentity)
	operationCtx := rt.operationContext
	m.mu.Unlock()
	go func() {
		<-operationCtx.Done()
		m.mu.Lock()
		m.finishRuntimeLocked(rt, nil)
		m.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.shutdown(ctx); err != nil {
		t.Fatal("shutdown waited without canceling lifecycle work", err)
	}
	if operationCtx.Err() == nil || !m.runtimes[b.RoomIdentity].service.grantsFrozen.Load() {
		t.Fatal("shutdown failed to cancel operation or freeze Room")
	}
}
