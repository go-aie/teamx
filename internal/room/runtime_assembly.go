package room

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func (m *roomManager) copyIndex() roomsIndex {
	next := m.index
	next.OwnedRooms = append([]RoomIdentity{}, next.OwnedRooms...)
	next.Opens = append([]roomOpen{}, next.Opens...)
	next.Shutdowns = append([]roomShutdown{}, next.Shutdowns...)
	next.Rooms = append([]roomIndexEntry{}, next.Rooms...)
	next.WorkspaceOperations = make([]workspaceOperation, len(m.index.WorkspaceOperations))
	for i, op := range m.index.WorkspaceOperations {
		next.WorkspaceOperations[i] = cloneWorkspaceOperation(op)
	}
	return next
}

func (m *roomManager) checkWorkspacePreparationSettled(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, op := range m.index.WorkspaceOperations {
		if op.RoomID == id && op.Preparation != nil && !preparationGroupSettled(op.Preparation.PID) {
			return fmt.Errorf("%w: workspace preparation settlement is unverified", ErrBusy)
		}
	}
	return nil
}

func (m *roomManager) checkRoomSettled(id string) error {
	if err := m.checkWorkspacePreparationSettled(id); err != nil {
		return err
	}
	store, err := OpenStore(m.roomDir(id), id)
	if err != nil {
		return fmt.Errorf("previous Room settlement: %w", err)
	}
	defer store.Close()
	state, err := store.LoadRoom()
	if err != nil {
		return err
	}
	m.mu.Lock()
	var expected RoomIdentity
	for _, entry := range m.index.Rooms {
		if entry.RoomID == id {
			expected = entry.RoomIdentity
			break
		}
	}
	m.mu.Unlock()
	if state.RoomIdentity != expected {
		return ErrIdentity
	}
	for _, member := range state.Members {
		identity := MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		opts := SessionOptions{MemberIdentity: identity, StatePath: filepath.Join(store.dir, "agents", member.MembershipID, "session.json")}
		ref, exists, err := (dshBackend{store: store}).reference(opts)
		if err != nil {
			return err
		}
		if ref.Pending {
			continue
		} // A rotation commits only after old cleanup.
		if exists {
			dir := ref.Directory
			if err := dsh.CheckReset(filepath.Join(filepath.Dir(opts.StatePath), dir)); err != nil {
				return fmt.Errorf("previous Room %s member %s has no verified settlement: %w", id, member.AgentID, err)
			}
		} else {
			agent, err := store.LoadAgent(identity)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if agent.Turn != nil {
				return fmt.Errorf("%w: previous Room has a recorded turn without backend settlement", ErrInvalid)
			}
		}
	}
	return nil
}

// Assemble profiles exclusively from the frozen room configuration. Neither
// opening nor restart reads roles from the current team catalog.
func (m *roomManager) activationConfig(id RoomIdentity) (serviceConfig, error) {
	return m.workspaceActivationConfig(id, "")
}

func (m *roomManager) workspaceActivationConfig(id RoomIdentity, applying string) (serviceConfig, error) {
	var saved managedRoomConfig
	dir := m.roomDir(id.RoomID)
	if err := readSnapshot(filepath.Join(dir, "room-config.json"), &saved, maxPrivateFileBytes); err != nil {
		return serviceConfig{}, err
	}
	if saved.RoomIdentity != id {
		return serviceConfig{}, ErrIdentity
	}
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return serviceConfig{}, ErrClosing
	}
	for _, op := range m.index.WorkspaceOperations {
		if op.RoomIdentity == id && !op.finished() && op.OperationID != applying {
			m.mu.Unlock()
			return serviceConfig{}, ErrBusy
		}
	}
	authorized := false
	authorizedOperationID := ""
	for _, op := range m.index.WorkspaceOperations {
		if op.RoomIdentity == id && (op.Status == "completed" || op.OperationID == applying && op.ConfigCommitted && len(op.SessionsUpdated) == len(op.Members)) && op.PreparedConfig != nil && reflect.DeepEqual(saved, *op.PreparedConfig) {
			authorized = true
			authorizedOperationID = op.OperationID
		}
	}
	var entry roomIndexEntry
	for _, e := range m.index.Rooms {
		if e.RoomIdentity == id {
			entry = e
			break
		}
	}
	m.mu.Unlock()
	if err := validateInitialization(entry, roomInitialization{Config: saved}); err != nil {
		return serviceConfig{}, err
	}
	// Only a completed application (or its committed executor) authorizes a bound configuration.
	if saved.Workspace != nil && (saved.WorkspaceVersion > 1 || saved.Workspace.Repository != nil || saved.Workspace.Binding != nil) && !authorized {
		return serviceConfig{}, fmt.Errorf("%w: workspace configuration has no completed management operation", ErrRequest)
	}
	base := saved.Runtime.profile()
	if err := base.checkCredentials(); err != nil {
		return serviceConfig{}, err
	}
	info, err := os.Stat(base.BinaryPath)
	if err != nil {
		return serviceConfig{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return serviceConfig{}, fmt.Errorf("%w: invalid DSH executable", ErrRequest)
	}
	for _, arg := range base.BinaryArgs {
		if filepath.IsAbs(arg) {
			if _, err := os.Stat(arg); err != nil {
				return serviceConfig{}, fmt.Errorf("DSH artifact argument: %w", err)
			}
		}
	}
	if m.config.endpoint == "" || !filepath.IsAbs(m.config.cliPath) {
		return serviceConfig{}, fmt.Errorf("%w: member CLI environment unavailable", ErrRequest)
	}
	profiles := make(map[string]dshProfile, len(saved.Members))
	for _, member := range saved.Members {
		p := base
		p.Instructions = member.Instructions
		p.Workspace = filepath.Join(dir, filepath.FromSlash(member.Workspace))
		if err := checkWorkspaceDirectory(dir, member.Workspace); err != nil {
			return serviceConfig{}, err
		}
		info, err := os.Stat(p.Workspace)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return serviceConfig{}, fmt.Errorf("member workspace: %w", err)
		}
		if info != nil && !info.IsDir() {
			return serviceConfig{}, fmt.Errorf("%w: member workspace is not a directory", ErrRequest)
		}
		p.ToolEnv = make(map[string]string)
		for k, v := range base.ToolEnv {
			p.ToolEnv[k] = v
		}
		for k, v := range member.ToolEnv {
			p.ToolEnv[k] = resolveTeamResource(dir, v)
		}
		profiles[member.Runner.Profile] = p
	}
	config := m.config
	config.settings = m.settings
	config.workspaceOperationID = authorizedOperationID
	config.maxTurns, config.dshProfiles = saved.MaxTurns, profiles
	config.persistentScheduling = true
	return config, nil
}
