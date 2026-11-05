package room

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type runtimeCleanup struct {
	owner RoomIdentity
	err   error
}

// Every wait shares ctx's deadline. Freeze all schedulers before joining any
// resources, then stop services concurrently with tools, queries and lifecycle
// workers. A failed owner never prevents another owner from receiving stop.
func (m *roomManager) shutdown(ctx context.Context) error {
	for !m.shutdownGate.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	defer m.shutdownGate.Unlock()
	m.mu.Lock()
	m.closing = true
	globalDone, globalBusy := m.operationDone, m.busy
	owners := map[RoomIdentity]*roomRuntime{}
	for _, entry := range m.index.Rooms {
		owners[entry.RoomIdentity] = nil
	}
	for owner, rt := range m.runtimes {
		owners[owner] = rt
		if rt.service != nil {
			rt.service.freeze()
		}
	}
	for _, rt := range m.runtimes {
		if rt.busy && rt.operationCancel != nil {
			rt.operationCancel()
		}
	}
	// These only signal under the gate. Waiting happens outside it.
	m.cancelPlatformCallsLocked(func(*platformCall) bool { return true })
	for owner := range owners {
		m.notifications.CancelRoomLocked(owner)
	}
	results := make(chan runtimeCleanup, len(owners)+2)
	for owner, rt := range owners {
		var svc *service
		var done chan struct{}
		if rt != nil {
			svc = rt.service
			if rt.busy {
				done = rt.operationDone
			}
		}
		go func() { results <- runtimeCleanup{owner, m.shutdownRuntime(ctx, owner, svc, done)} }()
	}
	m.mu.Unlock()
	go func() { results <- runtimeCleanup{err: m.notifications.Stop(ctx)} }()
	go func() {
		var err error
		if globalBusy {
			select {
			case <-globalDone:
			case <-ctx.Done():
				err = fmt.Errorf("shared management cleanup still owned: %w", ctx.Err())
			}
		}
		results <- runtimeCleanup{err: err}
	}()
	var cleanupErr error
	roomErrors := map[RoomIdentity]error{}
	// Each child has bounded waits, including close's management/cleanup gates.
	// Collect them all even when the shared context has already expired.
	for range len(owners) + 2 {
		result := <-results
		if result.owner != (RoomIdentity{}) {
			roomErrors[result.owner] = result.err
			if result.err != nil {
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("Room %s (%s): %w", result.owner.RoomID, result.owner.RoomInstance, result.err))
			}
		} else {
			cleanupErr = errors.Join(cleanupErr, result.err)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Keep prior failure evidence for unopened Rooms. A fresh attempt clears
	// that receipt before acquiring resources; a clean stop replaces it here.
	next := m.copyIndex()
	for owner, err := range roomErrors {
		if rt := m.runtimes[owner]; rt != nil {
			if err != nil {
				rt.status, rt.err = "failed", err.Error()
			} else if rt.service != nil {
				rt.status = "closed"
			}
		}
		owned := false
		for _, id := range next.OwnedRooms {
			owned = owned || id == owner
		}
		if !owned {
			continue
		}
		rt := m.runtimes[owner]
		if err == nil && (rt == nil || rt.service == nil) {
			// Not assembling a Room is never evidence that its historical
			// backend stopped normally. Preserve its previous receipt verbatim.
			continue
		}
		next.forgetShutdown(owner)
		record := roomShutdown{RoomIdentity: owner}
		if err != nil {
			record.Error = err.Error()
		}
		next.Shutdowns = append(next.Shutdowns, record)
	}
	// A latched uncertain index must remain reloadable after resources settle.
	// Do not replace its last on-disk evidence or invent a successful receipt.
	if m.store.check(true) == nil {
		cleanupErr = errors.Join(cleanupErr, m.saveIndex(next))
	}
	if cleanupErr != nil {
		return cleanupErr // unresolved resources retain root ownership
	}
	return m.store.Close()
}

func (m *roomManager) shutdownRuntime(ctx context.Context, owner RoomIdentity, svc *service, done <-chan struct{}) error {
	closed := make(chan error, 1)
	go func() {
		var err error
		if svc != nil {
			err = svc.close(ctx)
		}
		closed <- err
	}()
	var operationErr error
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			operationErr = fmt.Errorf("Room lifecycle worker still owned: %w", ctx.Err())
		}
	}
	toolErr := m.waitPlatformCalls(ctx, &owner)
	queryErr := m.notifications.WaitRoom(ctx, owner)
	closeErr := <-closed
	// An opening/rebuild worker may publish a new service after the first
	// snapshot. It observes closing and cannot resume scheduling.
	m.mu.Lock()
	current, _ := m.runtimeServiceLocked(owner)
	m.mu.Unlock()
	if current != nil && current != svc {
		closeErr = errors.Join(closeErr, current.close(ctx))
	}
	return errors.Join(operationErr, toolErr, queryErr, closeErr)
}
