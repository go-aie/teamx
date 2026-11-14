package room

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func (m *roomManager) copyIndex() roomsIndex {
	next := m.index
	if next.DefaultAssistant != nil {
		record := *next.DefaultAssistant
		next.DefaultAssistant = &record
	}
	next.OwnedRooms = append([]RoomIdentity{}, next.OwnedRooms...)
	next.Opens = append([]roomOpen{}, next.Opens...)
	next.Shutdowns = append([]roomShutdown{}, next.Shutdowns...)
	next.Rooms = append([]roomIndexEntry{}, next.Rooms...)
	for i := range next.Rooms {
		next.Rooms[i].AgentNames = maps.Clone(next.Rooms[i].AgentNames)
	}
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
		rotation, err := store.loadRotation(identity)
		if err != nil {
			return err
		}
		if rotation != nil {
			scheduling, err := store.loadScheduling(identity)
			if err != nil {
				return err
			}
			if err := reconcileRotation(store, opts, rotation, &scheduling); err != nil {
				return fmt.Errorf("previous Room rotation: %w", err)
			}
		}
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
		if op.RoomIdentity == id && (op.Status == "completed" || op.OperationID == applying && op.ConfigCommitted && len(op.SessionsUpdated) == len(op.Members)) && op.PreparedConfig != nil && equalWorkspaceRoomConfig(saved, *op.PreparedConfig) {
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
	if err := m.validateRoomSnapshot(saved); err != nil {
		return serviceConfig{}, err
	}
	// Only a completed application (or its committed executor) authorizes a bound configuration.
	if saved.Workspace != nil && (saved.WorkspaceVersion > 1 || saved.Workspace.Repository != nil || saved.Workspace.Binding != nil) && !authorized {
		return serviceConfig{}, fmt.Errorf("%w: workspace configuration has no completed management operation", ErrRequest)
	}
	base := saved.Runtime.profile()
	if err := checkManagedRuntimeAvailability(base); err != nil {
		return serviceConfig{}, err
	}
	if m.config.endpoint == "" || !filepath.IsAbs(m.config.cliPath) {
		return serviceConfig{}, fmt.Errorf("%w: member CLI environment unavailable", ErrRequest)
	}
	profiles := make(map[string]dshProfile, len(saved.Members))
	targets := make(map[string]modelSessionSnapshot)
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
		if saved.Runtime.ManagedModels {
			if m.config.models == nil || member.ModelSelection == nil {
				return serviceConfig{}, ErrInvalid
			}
			target, err := m.config.models.target(MemberIdentity{RoomIdentity: id, AgentID: member.AgentID, MembershipID: member.MembershipID}, *member.ModelSelection, member.ModelSelectionVersion, p)
			// Existing generations always resume from their original execution fact,
			// including unverified imports and entries pending deletion.
			memberID := MemberIdentity{id, member.AgentID, member.MembershipID}
			opts := SessionOptions{MemberIdentity: memberID, StatePath: filepath.Join(dir, "agents", member.MembershipID, "session.json")}
			if ref, _, refErr := readDSHReference(opts.StatePath, memberID); refErr == nil && ref.Directory != "" {
				if frozen, loadErr := readModelSessionSnapshot(filepath.Join(filepath.Dir(opts.StatePath), ref.Directory+".model.json"), memberID, ref.Directory); loadErr == nil && (!ref.Pending || !frozen.Legacy) {
					target, err = frozen, nil
				}
			}
			if err != nil {
				p.Model = launcherCheckModel()
				if entry, resolveErr := m.config.models.catalog().resolve(*member.ModelSelection); resolveErr == nil {
					p.Model = entry.materialize()
				}
				p.modelError = "模型尚未验证或凭据不可用；请在设置中验证并修复。"
				profiles[member.Runner.Profile] = p
				continue
			}
			targets[member.Runner.Profile] = target
			p = target.Profile
		}
		profiles[member.Runner.Profile] = p
	}
	config := m.config
	config.agentNames = m.agentNames
	config.scheduling, _ = saved.Scheduling.effective() // validated before assembling profiles
	config.settings = m.settings
	config.modelTargets = targets
	config.workspaceOperationID = authorizedOperationID
	config.maxTurns, config.dshProfiles = saved.SessionMaxTurns, profiles
	config.persistentScheduling = true
	return config, nil
}

// Check local prerequisites without contacting the model or opening DSH.
func checkManagedRuntimeAvailability(profile dshProfile) error {
	if err := profile.checkCredentials(); err != nil {
		return err
	}
	info, err := os.Stat(profile.BinaryPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return fmt.Errorf("%w: invalid DSH executable", ErrRequest)
	}
	for _, arg := range profile.BinaryArgs {
		if filepath.IsAbs(arg) {
			if _, err := os.Stat(arg); err != nil {
				return fmt.Errorf("DSH artifact argument: %w", err)
			}
		}
	}
	return nil
}
