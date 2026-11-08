package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Called with mu held. The persisted phase records authorization to remove
// data only after execution has settled. A restart never replays this operation.
func (m *roomManager) startDeletion(target RoomIdentity) (roomSummary, error) {
	if err := m.store.check(true); err != nil {
		return roomSummary{}, err
	}
	if m.roomBusyLocked(target) {
		return roomSummary{}, ErrBusy
	}
	next := m.copyIndex()
	at := -1
	for i, e := range next.Rooms {
		if e.RoomIdentity == target {
			at = i
			break
		}
	}
	if at < 0 {
		return roomSummary{}, ErrIdentity
	}
	e := &next.Rooms[at]
	if e.Status != "deleting" {
		phase := "closing"
		if e.Status == "creating" {
			// Reservation may have committed before its directory was created.
			if _, err := os.Lstat(m.roomDir(e.RoomID)); errors.Is(err, os.ErrNotExist) {
				phase = "removing"
			}
		}
		e.Status, e.DeletionPhase, e.Initialization = "deleting", phase, nil
	}
	e.Error, e.UpdatedAt = "", time.Now().UTC()
	if err := m.saveIndex(next); err != nil {
		return roomSummary{}, err
	}
	rt := m.reserveRuntimeLocked(target)
	if rt.service != nil {
		rt.service.closing.Store(true)
	}
	m.notifications.CancelRoomLocked(target)
	m.cancelPlatformCallsLocked(func(c *platformCall) bool { return c.owner == target })
	go m.runDeletion(e.roomSummary, rt)
	return e.roomSummary, nil
}

func (m *roomManager) runDeletion(target roomSummary, rt *roomRuntime) {
	var runErr error
	defer func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if runErr != nil {
			next := m.copyIndex()
			for i := range next.Rooms {
				if next.Rooms[i].RoomIdentity == target.RoomIdentity {
					next.Rooms[i].Error = runErr.Error()
					if err := m.saveIndex(next); err != nil {
						m.index.Rooms[i].Error = errors.Join(runErr, err).Error()
					}
					break
				}
			}
		}
		m.finishRuntimeLocked(rt, runErr)
		if runErr == nil {
			delete(m.runtimes, target.RoomIdentity)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	runErr = m.stopRoomGrants(ctx, target.RoomIdentity)
	if runErr == nil {
		runErr = m.waitPlatformCalls(ctx, &target.RoomIdentity)
	}
	if runErr == nil {
		runErr = m.notifications.WaitRoom(ctx, target.RoomIdentity)
	}
	cancel()
	if runErr != nil {
		runErr = fmt.Errorf("notification query cleanup: %w", runErr)
		return
	}
	m.mu.Lock()
	active, _ := m.runtimeServiceLocked(target.RoomIdentity)
	m.mu.Unlock()
	if active != nil && active.info.RoomIdentity == target.RoomIdentity {
		ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
		err := active.close(ctx)
		cancel()
		active.store.mu.Lock()
		released := active.store.lock == nil
		active.store.mu.Unlock()
		if released && err == nil {
			m.mu.Lock()
			m.setRuntimeServiceLocked(target.RoomIdentity, nil)
			m.mu.Unlock()
		}
		if err != nil {
			runErr = fmt.Errorf("Room cleanup: %w", err)
			return
		}
	}
	if target.DeletionPhase != "removing" {
		if err := m.checkDeletionSettled(target.RoomIdentity); err != nil {
			runErr = err
			return
		}
		m.mu.Lock()
		next := m.copyIndex()
		for i := range next.Rooms {
			if next.Rooms[i].RoomIdentity == target.RoomIdentity {
				next.Rooms[i].DeletionPhase = "removing"
			}
		}
		runErr = m.saveIndex(next)
		m.mu.Unlock()
		if runErr != nil {
			return
		}
	}
	// Scope deletion to the generated Room directory. RemoveAll unlinks nested
	// symlinks; it does not follow them into external projects or team templates.
	dir := m.roomDir(target.RoomID)
	if runErr = os.RemoveAll(dir); runErr != nil {
		return
	}
	if runErr = syncDirectory(filepath.Dir(dir)); errors.Is(runErr, os.ErrNotExist) {
		runErr = syncDirectory(m.store.dir) // No Room directory was ever created.
	}
	if runErr != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	next := m.copyIndex()
	next.Rooms = next.Rooms[:0]
	for _, e := range m.index.Rooms {
		if e.RoomIdentity != target.RoomIdentity {
			next.Rooms = append(next.Rooms, e)
		}
	}
	next.WorkspaceOperations = next.WorkspaceOperations[:0]
	for _, op := range m.index.WorkspaceOperations {
		if op.RoomIdentity != target.RoomIdentity {
			next.WorkspaceOperations = append(next.WorkspaceOperations, op)
		}
	}
	next.OwnedRooms = nil
	for _, owner := range m.index.OwnedRooms {
		if owner != target.RoomIdentity {
			next.OwnedRooms = append(next.OwnedRooms, owner)
		}
	}
	next.Opens = nil
	next.forgetShutdown(target.RoomIdentity)
	for _, op := range m.index.Opens {
		if op.RoomIdentity != target.RoomIdentity {
			next.Opens = append(next.Opens, op)
		}
	}
	runErr = m.saveIndex(next)
	if runErr == nil {
		m.notifications.ForgetRoomLocked(target.RoomIdentity)
		delete(m.gitlabNotifications, target.RoomIdentity)
	}
}

func (m *roomManager) checkDeletionSettled(target RoomIdentity) error {
	if err := m.checkWorkspacePreparationSettled(target.RoomID); err != nil {
		return err
	}
	dir := m.roomDir(target.RoomID)
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalid
	}
	// Deletion must not instantiate a Room or require readable chat history.
	// Hold the same OS lock as OpenStore while checking all execution evidence.
	lock, err := lockDirectory(dir)
	if err != nil {
		return err
	}
	defer unlockDirectory(lock)
	if _, err := os.Stat(filepath.Join(dir, "room.json")); errors.Is(err, os.ErrNotExist) {
		// An interrupted creation has not published a Room or opened a backend.
		if _, err := os.Lstat(filepath.Join(dir, "agents")); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: incomplete Room has private execution data", ErrInvalid)
		}
		var config managedRoomConfig
		if err := readSnapshot(filepath.Join(dir, "room-config.json"), &config, maxPrivateFileBytes); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if config.RoomIdentity != target {
			return ErrIdentity
		}
		return nil
	}
	members, err := deletionMembers(dir, target)
	if err != nil {
		return err
	}
	for _, member := range members {
		identity := MemberIdentity{RoomIdentity: target, AgentID: member.AgentID, MembershipID: member.MembershipID}
		path := filepath.Join(dir, "agents", member.MembershipID, "session.json")
		ref, exists, err := readDSHReference(path, identity)
		if err != nil {
			return err
		}
		if ref.Pending {
			continue // A rotation commits only after old cleanup; history is checked below.
		}
		if exists {
			backend := ref.Directory
			if err := dsh.CheckReset(filepath.Join(filepath.Dir(path), backend)); err != nil {
				return fmt.Errorf("private Session %s has no verified settlement: %w", member.AgentID, err)
			}
		} else {
			var agent AgentSnapshot
			err := readSnapshot(filepath.Join(filepath.Dir(path), "agent.json"), &agent, maxPrivateFileBytes)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if agent.validate() != nil || agent.MemberIdentity != identity {
				return ErrIdentity
			}
			if agent.Turn != nil {
				return fmt.Errorf("%w: Room has a recorded turn without backend settlement", ErrInvalid)
			}
		}
	}
	// Check historical generations and removed memberships too; no backend
	// directory is discarded solely because the current reference moved on.
	agents, err := os.ReadDir(filepath.Join(dir, "agents"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, agent := range agents {
		if !agent.IsDir() {
			return fmt.Errorf("%w: invalid private member directory", ErrInvalid)
		}
		path := filepath.Join(dir, "agents", agent.Name())
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() != "session.json.dsh" && !strings.HasPrefix(entry.Name(), "session.json.dsh-") {
				continue
			}
			if !entry.IsDir() {
				return fmt.Errorf("%w: invalid backend directory", ErrInvalid)
			}
			if err := dsh.CheckReset(filepath.Join(path, entry.Name())); err != nil {
				return fmt.Errorf("private Session %s/%s has no verified settlement: %w", agent.Name(), entry.Name(), err)
			}
		}
	}
	return nil
}

type deletionMember struct {
	AgentID      string `json:"agent_id"`
	MembershipID string `json:"membership_id"`
}

// Deletion accepts only the current validated snapshot, like normal loading.
func deletionMembers(dir string, expected RoomIdentity) ([]deletionMember, error) {
	var state RoomSnapshot
	if err := readSnapshot(filepath.Join(dir, "room.json"), &state, maxRoomFileBytes); err != nil {
		return nil, err
	}
	if err := state.validate(); err != nil {
		return nil, err
	}
	if state.RoomIdentity != expected {
		return nil, ErrIdentity
	}
	if state.Members == nil || len(state.Members) > maxMembers {
		return nil, ErrInvalid
	}
	agents, memberships := map[string]bool{}, map[string]bool{}
	for _, member := range state.Members {
		if !validText(member.AgentID) || !safeMembershipID(member.MembershipID) || agents[member.AgentID] || memberships[member.MembershipID] {
			return nil, ErrIdentity
		}
		agents[member.AgentID], memberships[member.MembershipID] = true, true
	}
	members := make([]deletionMember, 0, len(state.Members))
	for _, m := range state.Members {
		members = append(members, deletionMember{m.AgentID, m.MembershipID})
	}
	return members, nil
}
