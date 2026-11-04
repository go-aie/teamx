package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

const maxActivations = 10000

type roomActivation struct {
	RoomIdentity
	ActivateID string `json:"activate_id"`
	Status     string `json:"status"`
	Phase      string `json:"phase"`
	Error      string `json:"error,omitempty"`
}

func (m *roomManager) copyIndex() roomsIndex {
	next := m.index
	next.Rooms = append([]roomIndexEntry{}, next.Rooms...)
	next.Activations = append([]roomActivation{}, next.Activations...)
	next.WorkspaceOperations = make([]workspaceOperation, len(m.index.WorkspaceOperations))
	for i, op := range m.index.WorkspaceOperations {
		next.WorkspaceOperations[i] = cloneWorkspaceOperation(op)
	}
	return next
}

// Called with mu held. An operation ID is a receipt, never a command to replay.
// Failed operations require a new ID; running operations keep their owner even
// after HTTP disconnects. The complete bounded receipt log prevents old retries
// from selecting a Room again after a later successful switch.
func (m *roomManager) startActivation(id string, target RoomIdentity) (roomActivation, error) {
	if !safeMembershipID(id) {
		return roomActivation{}, ErrRequest
	}
	if err := m.store.check(true); err != nil {
		return roomActivation{}, err
	}
	if err := m.checkWorkspaceOperations(target); err != nil {
		return roomActivation{}, err
	}
	for _, op := range m.index.Activations {
		if op.ActivateID == id {
			if op.RoomIdentity != target {
				return roomActivation{}, ErrConflict
			}
			return op, nil
		}
	}
	if m.index.OwnedRoomID != "" {
		for _, pending := range m.index.WorkspaceOperations {
			if pending.RoomID == m.index.OwnedRoomID && !pending.finished() {
				return roomActivation{}, ErrBusy
			}
		}
	}
	if m.busy {
		return roomActivation{}, ErrBusy
	}
	if len(m.index.Activations) >= maxActivations {
		return roomActivation{}, ErrCapacity
	}
	found := false
	for _, e := range m.index.Rooms {
		found = found || (e.RoomIdentity == target && e.Status == "ready")
	}
	if !found {
		return roomActivation{}, ErrIdentity
	}
	op := roomActivation{RoomIdentity: target, ActivateID: id, Status: "running", Phase: "checking"}
	next := m.copyIndex()
	next.Activations = append(next.Activations, op)
	if err := m.saveIndex(next); err != nil {
		return roomActivation{}, err
	}
	m.busy = true
	m.cancelToolsLocked(func(*toolCall) bool { return true })
	m.operationDone = make(chan struct{})
	go m.runActivation(op)
	return op, nil
}

func (m *roomManager) activationPhase(phase string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return ErrClosing
	}
	next := m.copyIndex()
	next.Activations[len(next.Activations)-1].Phase = phase
	return m.saveIndex(next)
}

func (m *roomManager) runActivation(op roomActivation) {
	var runErr error
	defer func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if runErr != nil {
			next := m.copyIndex()
			at := len(next.Activations) - 1
			next.Activations[at].Status, next.Activations[at].Error = "failed", runErr.Error()
			if err := m.saveIndex(next); err != nil {
				// Preserve an in-memory diagnostic even if the durable outcome is unknown.
				m.index.Activations[at].Status = "failed"
				m.index.Activations[at].Error = errors.Join(runErr, err).Error()
			}
		}
		m.busy = false
		if runErr == nil {
			m.notifications.WakeRoomLocked(op.RoomIdentity)
		}
		close(m.operationDone)
	}()
	ctx, cancelTools := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	runErr = m.waitTools(ctx, nil)
	cancelTools()
	if runErr != nil {
		return
	}
	config, err := m.activationConfig(op.RoomIdentity)
	if err != nil {
		runErr = err
		return
	}
	m.mu.Lock()
	old := m.active
	previousID := m.index.OwnedRoomID
	if previousID == "" {
		previousID = m.index.ActiveRoomID
	}
	m.mu.Unlock()
	if old != nil && old.info.RoomIdentity == op.RoomIdentity && !old.closing.Load() {
		// Re-selecting the live Room neither enables paused members nor rebuilds it.
		runErr = m.commitActivation(op.RoomID, true)
		return
	}
	if err := m.activationPhase("closing"); err != nil {
		runErr = err
		return
	}
	if old != nil {
		ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
		err := old.close(ctx)
		cancel()
		old.store.mu.Lock()
		released := old.store.lock == nil
		old.store.mu.Unlock()
		if released {
			m.mu.Lock()
			m.active = nil
			m.mu.Unlock()
		}
		if err != nil {
			runErr = fmt.Errorf("old Room cleanup: %w", err)
			return
		}
	}
	// A dead manager's Store lock says nothing about surviving DSH/tool
	// processes. Cross-Room activation also requires durable settlement proof.
	if previousID != "" && previousID != op.RoomID {
		if err := m.checkRoomSettled(previousID); err != nil {
			runErr = err
			return
		}
	}
	if err := m.ownTarget(op.RoomID); err != nil {
		runErr = err
		return
	}
	if err := m.activationPhase("preparing"); err != nil {
		runErr = err
		return
	}
	var saved managedRoomConfig
	if err := readSnapshot(filepath.Join(m.roomDir(op.RoomID), "room-config.json"), &saved, maxPrivateFileBytes); err != nil {
		runErr = err
		return
	}
	if saved.RoomIdentity != op.RoomIdentity {
		runErr = ErrIdentity
		return
	}
	if _, err := m.ensureWorkspaces(context.Background(), saved, false); err != nil {
		runErr = err
		return
	}
	if err := m.activationPhase("opening"); err != nil {
		runErr = err
		return
	}
	svc, err := m.factory(m.roomDir(op.RoomID), op.RoomID, config)
	if err != nil {
		runErr = fmt.Errorf("activate target: %w", err)
		return
	}
	m.mu.Lock()
	m.active = svc
	m.mu.Unlock()
	if svc.info.RoomIdentity != op.RoomIdentity {
		runErr = ErrIdentity
	} else {
		// Publish the ready selection before allowing any new turn. Recovery is lazy:
		// assembling this service never opens a DSH process or replaces a Session.
		runErr = m.commitActivation(op.RoomID, false)
	}
	if runErr == nil {
		runErr = svc.resumeMembers(context.Background())
	}
	if runErr == nil {
		runErr = m.commitActivation(op.RoomID, true)
	}
	if runErr != nil {
		ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
		defer cancel()
		runErr = errors.Join(runErr, svc.close(ctx))
	}
}

// This ownership record precedes target assembly/resume and is distinct from
// the last successful selection. A failed activation must not lose the identity
// of the Room whose external resources may still require inspection.
func (m *roomManager) ownTarget(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return ErrClosing
	}
	next := m.copyIndex()
	next.OwnedRoomID = id
	next.Activations[len(next.Activations)-1].Phase = "opening"
	return m.saveIndex(next)
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
			if dir == "" {
				dir = filepath.Base(dshStateDir(opts.StatePath))
			}
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

func (m *roomManager) commitActivation(id string, completed bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return ErrClosing
	}
	next := m.copyIndex()
	if completed {
		next.ActiveRoomID = id
	}
	for i := range next.Rooms {
		if next.Rooms[i].RoomID == id || (m.active != nil && next.Rooms[i].RoomID == m.index.ActiveRoomID) {
			next.Rooms[i].UpdatedAt = time.Now().UTC()
		}
	}
	at := len(next.Activations) - 1
	if completed {
		next.Activations[at].Status, next.Activations[at].Phase = "completed", "ready"
	} else {
		next.Activations[at].Phase = "resuming"
	}
	return m.saveIndex(next)
}

// Assemble profiles exclusively from the frozen room configuration. Neither
// activation nor restart reads roles from the current team catalog.
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
	config.workspaceOperationID = authorizedOperationID
	config.maxTurns, config.dshProfiles = saved.MaxTurns, profiles
	config.persistentScheduling = true
	return config, nil
}

// Restore only the last successfully selected Room. Interrupted activations
// are never continued, including those that had already stopped the old Room.
// Errors are retained in the catalog so the operator can inspect and retry.
func (m *roomManager) restoreActive() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, op := range m.index.WorkspaceOperations {
		if !op.finished() && op.RoomID == m.index.ActiveRoomID {
			return nil
		}
	}
	if m.index.ActiveRoomID == "" {
		return nil
	}
	if n := len(m.index.Activations); n > 0 && m.index.Activations[n-1].Status != "completed" {
		return nil
	}
	for _, e := range m.index.Rooms {
		if e.RoomID == m.index.ActiveRoomID {
			if e.Status == "deleting" {
				return nil
			}
			_, err := m.startActivation("restore-"+m.epoch, e.RoomIdentity)
			return err
		}
	}
	return ErrIdentity
}

func (m *roomManager) shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closing = true
	done := m.operationDone
	busy := m.busy
	m.mu.Unlock()
	if err := m.waitTools(ctx, nil); err != nil {
		return fmt.Errorf("tools still owned: %w", err)
	}
	if err := m.notifications.Stop(ctx); err != nil {
		return fmt.Errorf("notification queries still owned: %w", err)
	}
	if busy {
		select {
		case <-done:
		case <-ctx.Done():
			return fmt.Errorf("management cleanup still owned: %w", ctx.Err())
		}
	}
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()
	var cleanupErr error
	if active != nil {
		cleanupErr = active.close(ctx)
		active.store.mu.Lock()
		released := active.store.lock == nil
		active.store.mu.Unlock()
		if !released {
			return cleanupErr
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return errors.Join(cleanupErr, m.store.Close())
}
