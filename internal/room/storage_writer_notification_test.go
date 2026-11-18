package room

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func TestNotificationConfirmationRechecksSharedWriterHealth(t *testing.T) {
	h := newEventHarness(t, gitlab.EventsConfig{})
	s := h.state()
	s.ProjectID = 7
	if _, _, err := s.Accept([]gitlab.Event{h.event(1, 0)}, h.clock.get()); err != nil {
		t.Fatal(err)
	}
	// Freeze the management snapshot under its owner, then exercise only the
	// common writer concurrently. Production notification Gate removal is S4.
	h.m.mu.Lock()
	index := h.m.copyIndex()
	h.m.mu.Unlock()
	entered, block, release := snapshotBarrier(t)
	syncDir := h.m.store.ops.syncDir
	fault := errors.New("management directory sync result lost")
	h.m.store.ops.syncDir = func(dir string) error {
		if dir == filepath.Dir(h.m.indexPath()) {
			block()
			return fault
		}
		return syncDir(dir)
	}
	management := make(chan error, 1)
	go func() { management <- h.m.store.save(h.m.indexPath(), index, maxRoomsFileBytes) }()
	waitSnapshotBoundary(t, entered)
	h.m.mu.Lock()
	err := h.m.notifications.SaveLocked(s)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal("source snapshot failed while management sync was blocked", err)
	}
	release()
	if err := waitSnapshotResult(t, management); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	item := s.PendingItems()[0]
	h.m.mu.Lock()
	err = h.m.notifications.SaveLocked(s.Acknowledge(item.Key))
	h.m.mu.Unlock()
	if !errors.Is(err, ErrCommitUnknown) {
		t.Fatal("source confirmation bypassed shared freeze", err)
	}
	if len(h.state().Inbox) != 1 {
		t.Fatal("rejected confirmation advanced installed source state")
	}
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, h.m.store.dir, managerTestProfiles(t))
	disk, err := reopened.testEventsState(h.room.RoomIdentity)
	if err != nil || len(disk.Inbox) != 1 {
		t.Fatal("reopen lost the committed pending item", disk, err)
	}
}
