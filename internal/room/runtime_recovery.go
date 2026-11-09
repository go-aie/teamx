package room

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// A receipt records settlement, never permission to replay an execution. It is
// invalidated durably before the next assembly acquires external resources.
type roomShutdown struct {
	RoomIdentity
	Error string `json:"error,omitempty"`
}

func (index *roomsIndex) forgetShutdown(id RoomIdentity) {
	kept := index.Shutdowns[:0]
	for _, record := range index.Shutdowns {
		if record.RoomIdentity != id {
			kept = append(kept, record)
		}
	}
	index.Shutdowns = kept
}

// Reserve before inspecting disk. Recovery is independent of the old UI
// selection and one slow or broken Room cannot hold up any other Room.
func (m *roomManager) restoreRuntimes() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return ErrClosing
	}
	if err := m.store.check(true); err != nil {
		return err
	}
	if m.recoveryStarted {
		return nil
	}
	m.recoveryStarted = true
	for _, entry := range m.index.Rooms {
		if entry.Status == "creating" || m.runtimes[entry.RoomIdentity] != nil {
			continue
		}
		rt := m.reserveRuntimeLocked(entry.RoomIdentity)
		rt.status = "opening"
		go m.restoreRuntime(entry, rt)
	}
	return nil
}

func (m *roomManager) recoveryOperationErrorLocked(entry roomIndexEntry) error {
	if entry.Status == "deleting" {
		return fmt.Errorf("%w: deletion %s requires explicit retry", ErrBusy, entry.DeletionPhase)
	}
	if err := m.checkWorkspaceOperations(entry.RoomIdentity); err != nil {
		return fmt.Errorf("workspace recovery requires explicit retry: %w", err)
	}
	var open *roomOpen
	for i := len(m.index.Opens) - 1; i >= 0; i-- {
		op := m.index.Opens[i]
		if op.RoomIdentity == entry.RoomIdentity {
			open = &op
			break
		}
	}
	if open != nil && open.Status != "completed" {
		return fmt.Errorf("open %s requires explicit retry: %s", open.OpenID, open.Error)
	}
	for _, record := range m.index.Shutdowns {
		if record.RoomIdentity == entry.RoomIdentity && record.Error != "" {
			return fmt.Errorf("previous Room shutdown requires inspection: %s", record.Error)
		}
	}
	return nil
}

func (m *roomManager) restoreRuntime(entry roomIndexEntry, rt *roomRuntime) {
	m.mu.Lock()
	err := m.recoveryOperationErrorLocked(entry)
	m.mu.Unlock()
	needed := false
	if err == nil {
		needed, err = m.runtimeRecoveryNeeded(entry.RoomIdentity)
	}
	m.mu.Lock()
	if err == nil && m.closing {
		err = ErrClosing
	}
	if err != nil || !needed {
		m.finishRuntimeLocked(rt, err)
		m.mu.Unlock()
		return
	}
	next := m.copyIndex()
	next.OwnedRooms = appendOwner(next.OwnedRooms, entry.RoomIdentity)
	next.forgetShutdown(entry.RoomIdentity)
	err = m.saveIndex(next)
	if err != nil {
		m.finishRuntimeLocked(rt, err)
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	m.runOpen(roomOpen{RoomIdentity: entry.RoomIdentity}, rt)
}

// An untouched or wholly paused, settled team stays lazy. Inspect only the
// authoritative roster, including enable intent even if ownership was absent in
// an old index. Actual execution eligibility remains loadMembers' responsibility.
func (m *roomManager) runtimeRecoveryNeeded(owner RoomIdentity) (bool, error) {
	store, err := OpenStore(m.roomDir(owner.RoomID), owner.RoomID)
	if err != nil {
		return false, err
	}
	defer store.Close()
	state, err := store.LoadRoom()
	if err != nil {
		return false, err
	}
	if state.RoomIdentity != owner {
		return false, ErrIdentity
	}
	needed := false
	for _, member := range state.Members {
		id := MemberIdentity{RoomIdentity: owner, AgentID: member.AgentID, MembershipID: member.MembershipID}
		intent, err := store.loadScheduling(id)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		rotation, err := store.loadRotation(id)
		if err != nil {
			return false, err
		}
		needed = needed || rotation != nil && rotation.Phase != "completed"
		if intent.Version == formatVersion {
			intent, err = convertScheduling(intent)
			if err != nil {
				return false, err
			}
			intent.Limit = 0
			if err := store.saveScheduling(intent); err != nil {
				return false, err
			}
		}
		needed = needed || intent.Enabled
		agent, err := store.LoadAgent(id)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		needed = needed || agent.Turn != nil && agent.Turn.Status != "completed"
		opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(store.dir, "agents", id.MembershipID, "session.json")}
		ref, exists, err := (dshBackend{store: store}).reference(opts)
		if err != nil {
			return false, err
		}
		if exists {
			dir := ref.Directory
			needed = needed || dsh.CheckResume(filepath.Join(filepath.Dir(opts.StatePath), dir)) != nil
		} else if agent.Turn != nil && !ref.Pending {
			needed = true // retain a missing-backend diagnostic even while paused
		}
	}
	var restart discussionRestart
	err = readSnapshot(filepath.Join(store.dir, "discussion-restart.json"), &restart, maxPrivateFileBytes)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return needed || err == nil && restart.Status != "completed", nil
}
