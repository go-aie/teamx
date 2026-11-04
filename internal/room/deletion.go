package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	if m.busy {
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
	m.busy = true
	m.notifications.CancelRoomLocked(target)
	m.cancelToolsLocked(func(c *toolCall) bool { return c.owner == target })
	m.operationDone = make(chan struct{})
	go m.runDeletion(e.roomSummary)
	return e.roomSummary, nil
}

func (m *roomManager) runDeletion(target roomSummary) {
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
		m.busy = false
		close(m.operationDone)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	runErr = m.waitTools(ctx, &target.RoomIdentity)
	if runErr == nil {
		runErr = m.notifications.WaitRoom(ctx, target.RoomIdentity)
	}
	cancel()
	if runErr != nil {
		runErr = fmt.Errorf("notification query cleanup: %w", runErr)
		return
	}
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()
	if active != nil && active.info.RoomIdentity == target.RoomIdentity {
		ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
		err := active.close(ctx)
		cancel()
		active.store.mu.Lock()
		released := active.store.lock == nil
		active.store.mu.Unlock()
		if released {
			m.mu.Lock()
			m.active = nil
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
	m.mu.Lock()
	runErr = m.cleanupWorkspaceCredentialsLocked(target.RoomIdentity)
	m.mu.Unlock()
	if runErr != nil {
		return
	}
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
	next.Activations = next.Activations[:0]
	for _, op := range m.index.Activations {
		if op.RoomIdentity != target.RoomIdentity {
			next.Activations = append(next.Activations, op)
		}
	}
	next.WorkspaceOperations = next.WorkspaceOperations[:0]
	for _, op := range m.index.WorkspaceOperations {
		if op.RoomIdentity != target.RoomIdentity {
			next.WorkspaceOperations = append(next.WorkspaceOperations, op)
		}
	}
	if next.ActiveRoomID == target.RoomID {
		next.ActiveRoomID = ""
	}
	if next.OwnedRoomID == target.RoomID {
		next.OwnedRoomID = ""
	}
	runErr = m.saveIndex(next)
	if runErr == nil {
		m.notifications.ForgetRoomLocked(target.RoomIdentity)
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
	defer lock.Close()
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
			if backend == "" {
				backend = filepath.Base(dshStateDir(path))
			}
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

// Deletion reads only ownership and member identities. Unknown business fields
// are skipped, not migrated or accepted by the normal strict Room loader.
func deletionMembers(dir string, expected RoomIdentity) ([]deletionMember, error) {
	f, err := os.Open(filepath.Join(dir, "room.json"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxRoomFileBytes {
		return nil, ErrInvalid
	}
	var state struct {
		Version int `json:"version"`
		RoomIdentity
		Members []deletionMember `json:"members"`
	}
	dec := json.NewDecoder(io.LimitReader(f, maxRoomFileBytes+1))
	if dec.Decode(&state) != nil || dec.Decode(new(any)) != io.EOF {
		return nil, ErrInvalid
	}
	if err := validateHeader(state.Version, state.RoomIdentity); err != nil {
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
	return state.Members, nil
}
