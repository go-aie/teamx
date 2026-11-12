package room

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"time"
)

const maxWorkspaceOperations = 10000

type workspaceMemberPathRecord struct {
	AgentID      string `json:"agent_id"`
	MembershipID string `json:"membership_id"`
	Workspace    string `json:"workspace"`
}

// Durable intent and progress only. Saving/loading this record never prepares a
// workspace, rotates a Session, or publishes Target as effective configuration.
type workspacePreparationProcess struct {
	Phase string `json:"phase"` // starting or running; no command, arguments or environment
	PID   int    `json:"pid,omitempty"`
}

type workspaceOperation struct {
	Timings     map[string]int64             `json:"timings_ms,omitempty"`
	Preparation *workspacePreparationProcess `json:"preparation,omitempty"`
	RoomIdentity
	OperationID         string                      `json:"operation_id"`
	ReplacesOperationID string                      `json:"replaces_operation_id,omitempty"`
	PreviousVersion     uint64                      `json:"previous_version"`
	TargetVersion       uint64                      `json:"target_version"`
	Target              workspaceConfig             `json:"target"`
	Members             []workspaceMemberPathRecord `json:"members"`
	Status              string                      `json:"status"` // running, completed, failed, superseded
	Phase               string                      `json:"phase"`  // waiting, preparing, committing, sessions, completed
	ConfigCommitted     bool                        `json:"config_committed"`
	SessionsUpdated     []string                    `json:"sessions_updated,omitempty"`
	ErrorCode           string                      `json:"error_code,omitempty"`
	RotateMembers       []string                    `json:"rotate_members,omitempty"`
	SourceEpoch         string                      `json:"source_epoch,omitempty"`
	PreviousConfig      *managedRoomConfig          `json:"previous_config,omitempty"`
	RequestTarget       *workspaceConfig            `json:"request_target,omitempty"`
	PreparedConfig      *managedRoomConfig          `json:"prepared_config,omitempty"`
	CreatedAt           time.Time                   `json:"created_at"`
	UpdatedAt           time.Time                   `json:"updated_at"`
}

func (op workspaceOperation) finished() bool {
	return op.Status == "completed" || op.Status == "superseded"
}

func (op workspaceOperation) revisable() bool {
	return op.Status == "failed" && op.ErrorCode == "preparation_failed" && op.Phase == "preparing" &&
		!op.ConfigCommitted && op.Preparation == nil && op.PreparedConfig == nil && len(op.SessionsUpdated) == 0 && op.PreviousConfig != nil
}

func (op workspaceOperation) validate() error {
	if op.ReplacesOperationID != "" && (!validRestartID(op.ReplacesOperationID) || op.ReplacesOperationID == op.OperationID) {
		return ErrInvalid
	}
	for phase, elapsed := range op.Timings {
		if elapsed < 0 || phase != "local_checks" && phase != "target_check" && workspaceOperationPhase(phase) == 0 {
			return ErrInvalid
		}
	}
	if !safeMembershipID(op.RoomID) || !validText(op.RoomInstance) || !validRestartID(op.OperationID) ||
		op.PreviousVersion == math.MaxUint64 || op.TargetVersion != op.PreviousVersion+1 ||
		op.CreatedAt.IsZero() || op.UpdatedAt.Before(op.CreatedAt) || len(op.Members) == 0 || len(op.Members) > maxMembers {
		return ErrInvalid
	}
	if op.RequestTarget != nil {
		normalized, err := normalizeWorkspace(*op.RequestTarget, true)
		if err != nil || !equalWorkspace(normalized, *op.RequestTarget) || op.PreviousConfig == nil || op.PreviousConfig.RoomIdentity != op.RoomIdentity || op.PreviousConfig.WorkspaceVersion != op.PreviousVersion || !validText(op.SourceEpoch) {
			return ErrInvalid
		}
	}
	if op.PreparedConfig != nil {
		c := op.PreparedConfig
		if c.RoomIdentity != op.RoomIdentity || c.WorkspaceVersion != op.TargetVersion || c.Workspace == nil || !equalWorkspace(*c.Workspace, op.Target) || validateWorkspaceSnapshot(*c) != nil || len(c.Members) != len(op.Members) || workspaceOperationPhase(op.Phase) < workspaceOperationPhase("committing") {
			return ErrInvalid
		}
	}
	if op.PreparedConfig != nil {
		for i, member := range op.PreparedConfig.Members {
			if member.AgentID != op.Members[i].AgentID || member.MembershipID != op.Members[i].MembershipID {
				return ErrIdentity
			}
		}
	}
	w, err := normalizeWorkspace(op.Target, false)
	if err != nil || !equalWorkspace(w, op.Target) {
		return ErrInvalid
	}
	phase := workspaceOperationPhase(op.Phase)
	if op.Preparation != nil {
		p := op.Preparation
		if phase < workspaceOperationPhase("preparing") || op.Status == "completed" || (p.Phase != "starting" || p.PID != 0) && (p.Phase != "running" || p.PID <= 0) {
			return ErrInvalid
		}
	}
	if phase == 0 || (op.ConfigCommitted && phase < workspaceOperationPhase("committing")) ||
		(phase >= workspaceOperationPhase("sessions") && !op.ConfigCommitted) ||
		(len(op.SessionsUpdated) > 0 && phase < workspaceOperationPhase("sessions")) {
		return ErrInvalid
	}
	switch op.Status {
	case "running":
		if op.Phase == "completed" || op.ErrorCode != "" {
			return ErrInvalid
		}
	case "completed":
		if op.Phase != "completed" || !op.ConfigCommitted || len(op.SessionsUpdated) != len(op.Members) || op.ErrorCode != "" {
			return ErrInvalid
		}
	case "failed", "superseded":
		// Error codes are bounded static diagnostics, never raw subprocess/API
		// errors or form values which could contain credentials.
		switch op.ErrorCode {
		case "interrupted", "cleanup_failed", "preparation_failed", "commit_unknown", "session_failed", "credential_missing":
		default:
			return ErrInvalid
		}
		if op.Phase == "completed" {
			return ErrInvalid
		}
		if op.Status == "superseded" {
			copy := op
			copy.Status = "failed"
			if !copy.revisable() {
				return ErrInvalid
			}
		}
	default:
		return ErrInvalid
	}
	ids, agents := map[string]bool{}, map[string]bool{}
	for _, member := range op.Members {
		if !safeMembershipID(member.MembershipID) || !validText(member.AgentID) || ids[member.MembershipID] || agents[member.AgentID] ||
			member.Workspace != workspaceMemberPath(w, member.MembershipID) {
			return ErrInvalid
		}
		ids[member.MembershipID], agents[member.AgentID] = true, true
	}
	rotating := map[string]bool{}
	for _, id := range op.RotateMembers {
		if !ids[id] || rotating[id] {
			return ErrInvalid
		}
		rotating[id] = true
	}
	updated := map[string]bool{}
	for _, id := range op.SessionsUpdated {
		if !ids[id] || updated[id] {
			return ErrInvalid
		}
		updated[id] = true
	}
	return nil
}

func workspaceOperationPhase(phase string) int {
	switch phase {
	case "waiting":
		return 1
	case "preparing":
		return 2
	case "committing":
		return 3
	case "sessions":
		return 4
	case "completed":
		return 5
	}
	return 0
}

func (m *roomManager) validateWorkspaceOperations() error {
	if len(m.index.WorkspaceOperations) > maxWorkspaceOperations {
		return ErrCapacity
	}
	ids, pending := map[string]bool{}, map[RoomIdentity]bool{}
	operations := map[string]workspaceOperation{}
	for _, op := range m.index.WorkspaceOperations {
		if err := op.validate(); err != nil || ids[op.OperationID] {
			return fmt.Errorf("%w: workspace operation record", ErrInvalid)
		}
		ids[op.OperationID] = true
		operations[op.OperationID] = op
		found := false
		for _, entry := range m.index.Rooms {
			if entry.RoomIdentity != op.RoomIdentity || (entry.Status != "ready" && entry.Status != "deleting") {
				continue
			}
			found = true
			if entry.Status == "deleting" {
				continue // The room directory may already have been removed.
			}
			for _, c := range []*managedRoomConfig{op.PreviousConfig, op.PreparedConfig} {
				if c != nil {
					if err := validateInitialization(entry, roomInitialization{Config: *c}); err != nil {
						return err
					}
				}
			}
			config, err := m.readWorkspaceRoomConfig(entry)
			if err != nil {
				return err
			}
			if len(op.Members) != len(config.Members) ||
				(op.Status == "completed" && config.WorkspaceVersion < op.TargetVersion) ||
				(op.Status == "superseded" && config.WorkspaceVersion < op.PreviousVersion) ||
				(!op.finished() && config.WorkspaceVersion != op.PreviousVersion && config.WorkspaceVersion != op.TargetVersion) {
				return fmt.Errorf("%w: workspace operation version or members", ErrInvalid)
			}
			for i, member := range config.Members {
				if op.Members[i].MembershipID != member.MembershipID || op.Members[i].AgentID != member.AgentID {
					return ErrIdentity
				}
			}
		}
		if !found {
			return ErrIdentity
		}
		if !op.finished() {
			if pending[op.RoomIdentity] {
				return fmt.Errorf("%w: overlapping workspace operations", ErrInvalid)
			}
			pending[op.RoomIdentity] = true
		}
	}
	replaced := map[string]bool{}
	for _, op := range m.index.WorkspaceOperations {
		if op.ReplacesOperationID == "" {
			continue
		}
		previous, exists := operations[op.ReplacesOperationID]
		found := exists && previous.Status == "superseded" && previous.RoomIdentity == op.RoomIdentity &&
			previous.PreviousVersion == op.PreviousVersion && previous.CreatedAt.Before(op.CreatedAt) && reflect.DeepEqual(previous.PreviousConfig, op.PreviousConfig)
		if !found || replaced[op.ReplacesOperationID] {
			return ErrInvalid
		}
		replaced[op.ReplacesOperationID] = true
	}
	for _, op := range m.index.WorkspaceOperations {
		if op.Status == "superseded" && !replaced[op.OperationID] {
			return ErrInvalid
		}
	}
	return nil
}

// The executor and persistence tests share the same monotonic record writer.
// Callers cannot overwrite an operation's intent or move progress backwards.
func (m *roomManager) saveWorkspaceOperation(op workspaceOperation) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveWorkspaceOperationLocked(op)
}

func (m *roomManager) saveWorkspaceOperationLocked(op workspaceOperation) error {
	if m.closing {
		return ErrClosing
	}
	if err := m.store.check(true); err != nil {
		return err
	}
	if err := op.validate(); err != nil {
		return err
	}
	entry, err := m.workspaceRoom(op.RoomIdentity)
	if err != nil || entry.Status != "ready" {
		return ErrIdentity
	}
	if op.Status == "completed" {
		config, err := m.readWorkspaceRoomConfig(entry)
		if err != nil {
			return err
		}
		if config.Workspace == nil || config.WorkspaceVersion != op.TargetVersion || !equalWorkspace(*config.Workspace, op.Target) || op.PreparedConfig != nil && !equalWorkspaceRoomConfig(config, *op.PreparedConfig) {
			return ErrConflict
		}
	}
	next := m.copyIndex()
	replacing := false
	for i, previous := range next.WorkspaceOperations {
		if previous.OperationID != op.OperationID {
			if previous.RoomIdentity == op.RoomIdentity && !previous.finished() {
				if previous.OperationID != op.ReplacesOperationID || !previous.revisable() || previous.PreviousVersion != op.PreviousVersion || !reflect.DeepEqual(previous.PreviousConfig, op.PreviousConfig) {
					return ErrBusy
				}
				previous.Status, previous.UpdatedAt = "superseded", op.CreatedAt
				next.WorkspaceOperations[i] = previous
				replacing = true
			}
			continue
		}
		if previous.RoomIdentity != op.RoomIdentity || previous.ReplacesOperationID != op.ReplacesOperationID || previous.PreviousVersion != op.PreviousVersion || previous.TargetVersion != op.TargetVersion ||
			!equalWorkspace(previous.Target, op.Target) || !reflect.DeepEqual(previous.RequestTarget, op.RequestTarget) || previous.SourceEpoch != op.SourceEpoch || !reflect.DeepEqual(previous.PreviousConfig, op.PreviousConfig) || (previous.PreparedConfig != nil && (!reflect.DeepEqual(previous.PreparedConfig, op.PreparedConfig) || !reflect.DeepEqual(previous.RotateMembers, op.RotateMembers))) || !reflect.DeepEqual(previous.Members, op.Members) || !previous.CreatedAt.Equal(op.CreatedAt) ||
			op.UpdatedAt.Before(previous.UpdatedAt) || workspaceOperationPhase(op.Phase) < workspaceOperationPhase(previous.Phase) ||
			(previous.ConfigCommitted && !op.ConfigCommitted) || (previous.finished() && !reflect.DeepEqual(previous, op)) {
			return ErrConflict
		}
		for _, member := range previous.SessionsUpdated {
			found := false
			for _, updated := range op.SessionsUpdated {
				found = found || member == updated
			}
			if !found {
				return ErrConflict
			}
		}
		next.WorkspaceOperations[i] = cloneWorkspaceOperation(op)
		return m.saveIndex(next)
	}
	if len(next.WorkspaceOperations) >= maxWorkspaceOperations {
		return ErrCapacity
	}
	if (op.ReplacesOperationID != "") != replacing {
		return ErrConflict
	}
	if m.roomBusyLocked(op.RoomIdentity) {
		return ErrBusy
	}
	// New records reserve an operation against the effective config, without
	// changing that config or touching a workspace directory.
	config, err := m.readWorkspaceRoomConfig(entry)
	if err != nil {
		return err
	}
	if config.WorkspaceVersion != op.PreviousVersion || op.Status != "running" || op.Phase != "waiting" || len(op.Members) != len(config.Members) {
		return ErrConflict
	}
	for i, member := range config.Members {
		if op.Members[i].MembershipID != member.MembershipID || op.Members[i].AgentID != member.AgentID {
			return ErrIdentity
		}
		if err := checkWorkspaceDirectory(m.roomDir(op.RoomID), op.Members[i].Workspace); err != nil {
			return err
		}
		for _, existing := range config.Members {
			if existing.MembershipID != member.MembershipID && workspacePathsOverlap(existing.Workspace, op.Members[i].Workspace) {
				return fmt.Errorf("%w: target workspace overlaps another member directory", ErrInvalid)
			}
		}
	}
	next.WorkspaceOperations = append(next.WorkspaceOperations, cloneWorkspaceOperation(op))
	return m.saveIndex(next)
}

func cloneWorkspaceOperation(op workspaceOperation) workspaceOperation {
	if op.Timings != nil {
		timings := make(map[string]int64, len(op.Timings))
		for key, value := range op.Timings {
			timings[key] = value
		}
		op.Timings = timings
	}
	if op.Preparation != nil {
		p := *op.Preparation
		op.Preparation = &p
	}
	// Config contains maps and nested runtime values; use the snapshot codec to own them.
	if op.PreviousConfig != nil {
		data, _ := json.Marshal(op.PreviousConfig)
		op.PreviousConfig = new(managedRoomConfig)
		_ = json.Unmarshal(data, op.PreviousConfig)
	}
	if op.PreparedConfig != nil {
		data, _ := json.Marshal(op.PreparedConfig)
		op.PreparedConfig = new(managedRoomConfig)
		_ = json.Unmarshal(data, op.PreparedConfig)
	}
	if op.RequestTarget != nil {
		w, _ := normalizeWorkspace(*op.RequestTarget, true)
		op.RequestTarget = &w
	}
	op.RotateMembers = append([]string(nil), op.RotateMembers...)
	op.Members = append([]workspaceMemberPathRecord(nil), op.Members...)
	op.SessionsUpdated = append([]string(nil), op.SessionsUpdated...)
	if op.Target.Repository != nil {
		repository := *op.Target.Repository
		op.Target.Repository = &repository
	}
	if op.Target.Binding != nil {
		binding := *op.Target.Binding
		op.Target.Binding = &binding
	}
	return op
}

func (m *roomManager) checkWorkspaceOperations(id RoomIdentity) error {
	for _, op := range m.index.WorkspaceOperations {
		if op.RoomIdentity == id && !op.finished() {
			return fmt.Errorf("%w: unfinished workspace operation; inspect and explicitly recover", ErrBusy)
		}
	}
	return nil
}

func (m *roomManager) workspaceRoom(id RoomIdentity) (roomIndexEntry, error) {
	for _, entry := range m.index.Rooms {
		if entry.RoomIdentity == id {
			return entry, nil
		}
	}
	return roomIndexEntry{}, ErrIdentity
}

func (m *roomManager) readWorkspaceRoomConfig(entry roomIndexEntry) (managedRoomConfig, error) {
	if entry.Initialization != nil {
		if err := validateInitialization(entry, *entry.Initialization); err != nil {
			return managedRoomConfig{}, err
		}
		return entry.Initialization.Config, nil
	}
	var config managedRoomConfig
	if err := readSnapshot(filepath.Join(m.roomDir(entry.RoomID), "room-config.json"), &config, maxPrivateFileBytes); err != nil {
		return managedRoomConfig{}, err
	}
	return config, validateInitialization(entry, roomInitialization{Config: config})
}
