package room

import (
	"testing"
	"time"
)

func hasRoomOwner(owners []RoomIdentity, owner RoomIdentity) bool {
	for _, id := range owners {
		if id == owner {
			return true
		}
	}
	return false
}

func awaitRecovery(t *testing.T, m *roomManager) {
	t.Helper()
	m.mu.Lock()
	var done []chan struct{}
	for _, rt := range m.runtimes {
		if rt.busy {
			done = append(done, rt.operationDone)
		}
	}
	m.mu.Unlock()
	for _, ch := range done {
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
			t.Fatal("runtime recovery did not settle")
		}
	}
}

func restoredTestManager(t *testing.T, previous *roomManager) *roomManager {
	t.Helper()
	m := testManager(t, previous.store.dir, nil)
	m.config, m.factory = previous.config, previous.factory
	return m
}

func awaitOpenRuntime(t *testing.T, m *roomManager, owner RoomIdentity) {
	t.Helper()
	m.mu.Lock()
	rt := m.runtimes[owner]
	done := rt.operationDone
	m.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Room did not recover", owner)
	}
	if rt.service == nil || rt.status != "ready" {
		t.Fatal("Room recovery failed", rt)
	}
}
