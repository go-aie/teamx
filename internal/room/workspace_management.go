package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform/notification"
)

var errWorkspaceOperationNotFound = errors.New("workspace operation not recorded")

// Workspace management uses the catalog epoch: it must remain addressable while
// the per-room service is closed, including after an interrupted config commit.
type workspaceApplyRequest struct {
	connection        *projectConnectionResolution
	ConnectionVersion uint64 `json:"connection_version,omitempty"`
	ServiceEpoch      string `json:"service_epoch"`
	RoomIdentity
	Caller              Caller          `json:"caller"`
	OperationID         string          `json:"operation_id"`
	ReplacesOperationID string          `json:"replaces_operation_id,omitempty"`
	ExpectedVersion     uint64          `json:"expected_version"`
	Target              workspaceConfig `json:"target"`
	PAT                 string          `json:"pat,omitempty"`
}

type workspaceResponse struct {
	RoomIdentity
	Workspace        *workspaceConfig            `json:"workspace"`
	Suggestions      *workspaceConfig            `json:"suggestions,omitempty"`
	Version          uint64                      `json:"workspace_version"`
	Members          []workspaceMemberPathRecord `json:"members"`
	CredentialStatus string                      `json:"credential_status"`
	CanRevise        bool                        `json:"can_revise"`
	Operation        *workspaceOperation         `json:"operation,omitempty"`
	Observation      *ServiceInfo                `json:"observation,omitempty"`
	Active           *ServiceInfo                `json:"active,omitempty"`
}

func (m *roomManager) workspaceStatus(id RoomIdentity, operationID string) (workspaceResponse, error) {
	entry, err := m.workspaceRoom(id)
	if err != nil || entry.Status != "ready" {
		return workspaceResponse{}, ErrIdentity
	}
	saved, err := m.readWorkspaceRoomConfig(entry)
	if err != nil {
		return workspaceResponse{}, err
	}
	result := workspaceResponse{RoomIdentity: id, Workspace: saved.Workspace, Version: saved.WorkspaceVersion, CredentialStatus: "missing"}
	// Suggestions are display-only; they never alter this Room's saved config.
	if team, ok := m.teams[saved.TeamID]; ok && team.Workspace != nil {
		suggestion, err := normalizeWorkspace(*team.Workspace, true)
		if err == nil {
			result.Suggestions = &suggestion
		}
	}
	for _, member := range saved.Members {
		result.Members = append(result.Members, workspaceMemberPathRecord{member.AgentID, member.MembershipID, member.Workspace})
	}
	if saved.Workspace != nil && saved.Workspace.Binding != nil {
		b := saved.Workspace.Binding
		if b.ConnectionID != "" {
			if _, err := m.resolveWorkspaceConnectionLocked(id, *saved.Workspace); err == nil {
				result.CredentialStatus = "configured"
			}
		} else if b.CredentialID != "" {
			if _, err := m.readWorkspaceCredential(id, b.CredentialID); err == nil {
				result.CredentialStatus = "configured"
			}
		} else if validWorkspacePAT(os.Getenv(b.TokenEnv)) {
			result.CredentialStatus = "configured"
		}
	}
	for _, op := range m.index.WorkspaceOperations {
		if op.RoomIdentity == id && (operationID == "" || op.OperationID == operationID) {
			copy := cloneWorkspaceOperation(op)
			// Internal recovery material is not a public runtime/configuration export.
			copy.PreparedConfig, copy.PreviousConfig, copy.RequestTarget = nil, nil, nil
			result.Operation = &copy
			result.CanRevise = op.revisable() && !m.roomBusyLocked(id) && reflect.DeepEqual(saved, *op.PreviousConfig)
			if !copy.finished() && copy.SourceEpoch != "" {
				result.Observation = &ServiceInfo{RoomIdentity: id, ServiceEpoch: copy.SourceEpoch}
			}
		}
	}
	if operationID != "" && result.Operation == nil {
		return workspaceResponse{}, errWorkspaceOperationNotFound
	}
	if svc, _ := m.runtimeServiceLocked(id); svc != nil && !svc.closing.Load() && !m.roomBusyLocked(id) {
		info := svc.info
		result.Active = &info
	}
	return result, nil
}

func (m *roomManager) serveWorkspace(w http.ResponseWriter, r *http.Request) {
	// Project access is checked outside the manager lock, before reserving an
	// operation or changing any frozen resource. Duplicate IDs remain receipts.
	var req workspaceApplyRequest
	if r.URL.Path == "/v1/workspace/apply" && r.Method == http.MethodPost {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
		dec.DisallowUnknownFields()
		if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
			writeAPIError(w, ErrRequest)
			return
		}
		if err := m.prepareWorkspaceConnection(r.Context(), &req); err != nil {
			writeAPIError(w, err)
			return
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.check(false); err != nil {
		writeAPIError(w, err)
		return
	}
	if r.URL.Path == "/v1/workspace" {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		if q.Get("service_epoch") != m.epoch || !validText(q.Get("caller_id")) {
			writeAPIError(w, ErrIdentity)
			return
		}
		result, err := m.workspaceStatus(RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}, q.Get("operation_id"))
		if errors.Is(err, errWorkspaceOperationNotFound) {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(apiError{Code: "operation_not_found", Error: err.Error()})
			return
		}
		if err != nil {
			writeAPIError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	dec.DisallowUnknownFields()
	if r.URL.Path != "/v1/workspace/apply" && (dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF) {
		writeAPIError(w, ErrRequest)
		return
	}
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	var err error
	var op workspaceOperation
	if r.URL.Path == "/v1/workspace/retry" {
		if req.ExpectedVersion != 0 || req.PAT != "" || req.ReplacesOperationID != "" || !reflect.DeepEqual(req.Target, workspaceConfig{}) {
			writeAPIError(w, ErrRequest)
			return
		}
		op, err = m.retryWorkspace(req.RoomIdentity, req.OperationID)
	} else {
		op, err = m.startWorkspaceApply(req)
	}
	req.PAT = ""
	if err != nil {
		writeAPIError(w, err)
		return
	}
	op.PreparedConfig, op.PreviousConfig, op.RequestTarget = nil, nil, nil
	_ = json.NewEncoder(w).Encode(op)
}

// Called under the catalog mutex. Identity is checked before receiving a new
// credential into storage; duplicate requests never overwrite its first value.
func (m *roomManager) startWorkspaceApply(req workspaceApplyRequest) (workspaceOperation, error) {
	checkedAt := time.Now()
	if m.closing {
		return workspaceOperation{}, ErrClosing
	}
	if !validRestartID(req.OperationID) {
		return workspaceOperation{}, ErrRequest
	}
	if err := m.store.check(false); err != nil {
		return workspaceOperation{}, err
	}
	input := req.Target

	canonical, err := normalizeWorkspace(input, true)
	// Existing room credential references are allowed only after ownership checks.
	if err != nil && input.Binding != nil && input.Binding.CredentialID != "" {
		canonical, err = normalizeWorkspace(input, false)
	}
	if err != nil {
		return workspaceOperation{}, err
	}
	for _, op := range m.index.WorkspaceOperations {
		if op.OperationID == req.OperationID {
			if op.RoomIdentity != req.RoomIdentity || op.ReplacesOperationID != req.ReplacesOperationID || op.PreviousVersion != req.ExpectedVersion || op.RequestTarget == nil || !equalWorkspace(*op.RequestTarget, canonical) {
				return workspaceOperation{}, ErrConflict
			}
			return cloneWorkspaceOperation(op), nil
		}
	}
	if err := m.store.check(true); err != nil {
		return workspaceOperation{}, err
	}
	if m.roomBusyLocked(req.RoomIdentity) {
		return workspaceOperation{}, ErrBusy
	}
	if len(m.index.WorkspaceOperations) >= maxWorkspaceOperations {
		return workspaceOperation{}, ErrCapacity
	}
	var replacement *workspaceOperation
	for _, pending := range m.index.WorkspaceOperations {
		if pending.OperationID == req.ReplacesOperationID && req.ReplacesOperationID != "" {
			if pending.RoomIdentity != req.RoomIdentity || !pending.revisable() {
				return workspaceOperation{}, ErrConflict
			}
			copy := pending
			replacement = &copy
		}
	}
	if req.ReplacesOperationID != "" && replacement == nil {
		return workspaceOperation{}, ErrConflict
	}
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	if replacement == nil {
		if svc == nil || svc.closing.Load() {
			return workspaceOperation{}, ErrIdentity
		}
	}
	for _, pending := range m.index.WorkspaceOperations {
		if pending.RoomIdentity == req.RoomIdentity && !pending.finished() && pending.OperationID != req.ReplacesOperationID {
			return workspaceOperation{}, ErrBusy
		}
	}
	entry, err := m.workspaceRoom(req.RoomIdentity)
	if err != nil {
		return workspaceOperation{}, err
	}
	saved, err := m.readWorkspaceRoomConfig(entry)
	if err != nil {
		return workspaceOperation{}, err
	}
	if saved.WorkspaceVersion != req.ExpectedVersion || req.ExpectedVersion == ^uint64(0) {
		return workspaceOperation{}, ErrConflict
	}
	if replacement != nil && (replacement.PreviousVersion != saved.WorkspaceVersion || !reflect.DeepEqual(saved, *replacement.PreviousConfig)) {
		return workspaceOperation{}, ErrConflict
	}
	target := canonical
	if target.Binding != nil && target.Binding.ConnectionID != "" {
		if req.connection == nil || m.checkProjectConnectionLocked(*req.connection) != nil {
			return workspaceOperation{}, ErrConflict
		}
		b := *target.Binding
		b.ConnectionGeneration, b.ProjectID = req.connection.record.Generation, req.connection.projectID
		target.Binding = &b
	}
	// First repository binding always gets a stable, fresh managed root. The
	// submitted root may be omitted or equal the currently displayed plain root.
	freshRepository := target.Repository != nil && (saved.Workspace == nil || saved.Workspace.Repository == nil)
	if freshRepository {
		if req.Target.Root != "" && (saved.Workspace == nil || canonical.Root != saved.Workspace.Root) {
			return workspaceOperation{}, ErrRequest
		}
		target.Root = "workspaces/repos/" + req.OperationID
	}
	if saved.Workspace != nil && saved.Workspace.Repository != nil {
		old := saved.Workspace
		if target.Repository == nil || *target.Repository != *old.Repository || target.Root != old.Root {
			return workspaceOperation{}, ErrConflict
		}
		if old.Binding != nil && (target.Binding == nil || old.Binding.Provider != target.Binding.Provider || old.Binding.URL != target.Binding.URL || old.Binding.Project != target.Binding.Project) {
			return workspaceOperation{}, ErrConflict
		}
	}
	if target.Binding != nil && req.PAT == "" && target.Binding.TokenEnv == "" && target.Binding.CredentialID == "" && target.Binding.ConnectionID == "" {
		return workspaceOperation{}, ErrRequest
	}
	if req.PAT != "" && (target.Binding == nil || target.Binding.TokenEnv != "" || target.Binding.CredentialID != "" || target.Binding.ConnectionID != "") {
		return workspaceOperation{}, ErrRequest
	}
	if req.PAT != "" && !validWorkspacePAT(req.PAT) {
		return workspaceOperation{}, ErrRequest
	}
	if target.Binding != nil && target.Binding.CredentialID != "" {
		if saved.Workspace == nil || saved.Workspace.Binding == nil || saved.Workspace.Binding.CredentialID != target.Binding.CredentialID {
			return workspaceOperation{}, ErrIdentity
		}
	}
	// Validate all target paths before writing a credential or reserving work.
	for _, member := range saved.Members {
		path := workspaceMemberPath(target, member.MembershipID)
		if err := checkWorkspaceDirectory(m.roomDir(req.RoomID), path); err != nil {
			return workspaceOperation{}, err
		}
		for _, old := range saved.Members {
			if (freshRepository || old.MembershipID != member.MembershipID) && workspacePathsOverlap(path, old.Workspace) {
				return workspaceOperation{}, ErrRequest
			}
		}
	}
	if req.PAT != "" {
		id, err := m.saveWorkspaceCredentialLocked(req.RoomIdentity, req.PAT)
		if err != nil {
			return workspaceOperation{}, err
		}
		b := *target.Binding
		b.CredentialID = id
		target.Binding = &b
	}
	target, err = normalizeWorkspace(target, false)
	if err != nil {
		return workspaceOperation{}, err
	}
	now := time.Now().UTC()
	previousData, _ := json.Marshal(saved)
	var previous managedRoomConfig
	_ = json.Unmarshal(previousData, &previous)
	sourceEpoch := ""
	if replacement != nil {
		sourceEpoch = replacement.SourceEpoch
	} else {
		sourceEpoch = svc.info.ServiceEpoch
	}
	op := workspaceOperation{Timings: map[string]int64{"local_checks": time.Since(checkedAt).Milliseconds()}, PreviousConfig: &previous, SourceEpoch: sourceEpoch, RoomIdentity: req.RoomIdentity, OperationID: req.OperationID, ReplacesOperationID: req.ReplacesOperationID, PreviousVersion: req.ExpectedVersion, TargetVersion: req.ExpectedVersion + 1, Target: target, RequestTarget: &canonical, RequestPAT: req.PAT != "", Status: "running", Phase: "waiting", CreatedAt: now, UpdatedAt: now}
	for _, member := range saved.Members {
		op.Members = append(op.Members, workspaceMemberPathRecord{member.AgentID, member.MembershipID, workspaceMemberPath(target, member.MembershipID)})
	}
	if err := m.saveWorkspaceOperationLocked(op); err != nil {
		return workspaceOperation{}, err
	}
	m.launchWorkspace(op)
	return cloneWorkspaceOperation(op), nil
}

func (m *roomManager) retryWorkspace(id RoomIdentity, operationID string) (workspaceOperation, error) {
	if m.closing {
		return workspaceOperation{}, ErrClosing
	}
	for _, op := range m.index.WorkspaceOperations {
		if op.OperationID != operationID {
			continue
		}
		if op.RoomIdentity != id {
			return workspaceOperation{}, ErrIdentity
		}
		if op.Status != "failed" {
			return cloneWorkspaceOperation(op), nil
		}
		if m.roomBusyLocked(id) {
			return workspaceOperation{}, ErrBusy
		}
		if op.RequestTarget == nil {
			return workspaceOperation{}, ErrRequest
		}
		if op.Preparation != nil {
			if !preparationGroupSettled(op.Preparation.PID) {
				return workspaceOperation{}, ErrBusy
			}
			op.Preparation = nil
		}
		// A retry is an explicit recovery request for this frozen target. Reconcile
		// files before advancing; no blind replay across an uncertain config commit.
		entry, err := m.workspaceRoom(id)
		if err != nil {
			return workspaceOperation{}, err
		}
		saved, err := m.readWorkspaceRoomConfig(entry)
		if err != nil {
			return workspaceOperation{}, err
		}
		if saved.WorkspaceVersion == op.TargetVersion {
			if op.PreparedConfig == nil || !reflect.DeepEqual(saved, *op.PreparedConfig) {
				return workspaceOperation{}, ErrConflict
			}
		} else if saved.WorkspaceVersion != op.PreviousVersion || op.ConfigCommitted || op.PreviousConfig == nil || !reflect.DeepEqual(saved, *op.PreviousConfig) {
			return workspaceOperation{}, ErrConflict
		}
		op.Status, op.ErrorCode, op.UpdatedAt = "running", "", time.Now().UTC()
		if err := m.saveWorkspaceOperationLocked(op); err != nil {
			return workspaceOperation{}, err
		}
		m.launchWorkspace(op)
		return cloneWorkspaceOperation(op), nil
	}
	return workspaceOperation{}, ErrIdentity
}

func (m *roomManager) launchWorkspace(op workspaceOperation) {
	rt := m.reserveRuntimeLocked(op.RoomIdentity)
	m.cancelWorkspaceNotificationsLocked(op.RoomIdentity)
	m.cancelPlatformCallsLocked(func(c *platformCall) bool { return c.owner == op.RoomIdentity })
	// Establish the execution boundary before returning the receipt. close drains
	// requests already admitted and joins all member/Session/tool ownership.
	if rt.service != nil {
		rt.service.closing.Store(true)
	}
	go m.runWorkspace(op, rt)
}

func (m *roomManager) runWorkspace(op workspaceOperation, rt *roomRuntime) {
	phaseStarted := time.Now()
	recordTiming := func() {
		if op.Timings == nil {
			op.Timings = map[string]int64{}
		}
		op.Timings[op.Phase] += time.Since(phaseStarted).Milliseconds()
		phaseStarted = time.Now()
	}
	code := "cleanup_failed"
	var runErr, executionErr error
	defer func() {
		if runErr != nil {
			recordTiming()
			if op.Phase == "completed" {
				op.Phase = "sessions"
			}
			op.Status, op.ErrorCode, op.UpdatedAt = "failed", code, time.Now().UTC()
			if op.Preparation != nil {
				op.ErrorCode = "cleanup_failed"
			}
			if errors.Is(runErr, ErrCommitUnknown) {
				op.ErrorCode = "commit_unknown"
			}
			if err := m.saveWorkspaceOperation(op); err != nil {
				m.mu.Lock()
				for i := range m.index.WorkspaceOperations {
					if m.index.WorkspaceOperations[i].OperationID == op.OperationID {
						m.index.WorkspaceOperations[i] = cloneWorkspaceOperation(op)
					}
				}
				m.mu.Unlock()
			}
		}
		m.mu.Lock()
		m.finishRuntimeLocked(rt, errors.Join(runErr, executionErr))
		m.notifications.EnsureRoomLocked(op.RoomIdentity)
		m.mu.Unlock()
	}()
	queryCtx, queryCancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	m.mu.Lock()
	queries := m.cancelWorkspaceNotificationsLocked(op.RoomIdentity)
	m.mu.Unlock()
	runErr = m.stopRoomGrants(queryCtx, op.RoomIdentity)
	if runErr == nil {
		runErr = notification.Wait(queryCtx, queries)
	}
	if runErr == nil {
		runErr = m.waitPlatformCalls(queryCtx, &op.RoomIdentity)
	}
	queryCancel()
	if runErr != nil {
		return
	}
	advance := func(phase string) error {
		recordTiming()
		op.Phase, op.UpdatedAt = phase, time.Now().UTC()
		return m.saveWorkspaceOperation(op)
	}
	tracked := context.WithValue(rt.operationContext, preparationTrackerKey{}, preparationTracker(func(phase string, pid int) error {
		if phase == "settled" {
			op.Preparation = nil
		} else {
			op.Preparation = &workspacePreparationProcess{Phase: phase, PID: pid}
		}
		op.UpdatedAt = time.Now().UTC()
		return m.saveWorkspaceOperation(op)
	}))
	m.mu.Lock()
	old, _ := m.runtimeServiceLocked(op.RoomIdentity)
	m.mu.Unlock()
	if old != nil {
		ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
		err := old.close(ctx)
		cancel()
		if err != nil {
			runErr = err
			return
		}
		m.mu.Lock()
		m.setRuntimeServiceLocked(op.RoomIdentity, nil)
		m.mu.Unlock()
	}
	if err := m.checkRoomSettled(op.RoomID); err != nil {
		runErr = err
		return
	}
	if err := m.pausePendingWorkspaceTurns(op.RoomIdentity); err != nil {
		runErr = err
		return
	}
	m.mu.Lock()
	entry, err := m.workspaceRoom(op.RoomIdentity)
	var saved managedRoomConfig
	if err == nil {
		saved, err = m.readWorkspaceRoomConfig(entry)
	}
	m.mu.Unlock()
	if err != nil {
		runErr = err
		return
	}
	if saved.WorkspaceVersion == op.PreviousVersion && (op.PreviousConfig == nil || !reflect.DeepEqual(saved, *op.PreviousConfig)) {
		runErr = ErrConflict
		return
	}
	if workspaceOperationPhase(op.Phase) <= workspaceOperationPhase("preparing") {
		code = "preparation_failed"
		if err := advance("preparing"); err != nil {
			runErr = err
			return
		}
		saved.Workspace, saved.WorkspaceVersion = &op.Target, op.TargetVersion
		for i := range saved.Members {
			saved.Members[i].Workspace = op.Members[i].Workspace
		}
		prepared, err := m.ensureWorkspaces(tracked, saved, true)
		if err != nil {
			runErr = err
			return
		}
		for i := range saved.Members {
			member := &saved.Members[i]
			if len(prepared.ToolEnv) > 0 {
				member.ToolEnv = cloneToolEnv(member.ToolEnv)
				for key, value := range prepared.ToolEnv {
					member.ToolEnv[key] = value
				}
			}
			if prepared.Instructions != "" && !strings.Contains(member.Instructions, prepared.Instructions) {
				member.Instructions += "\n\n" + prepared.Instructions
			}
		}
		sessionStore, err := OpenStore(m.roomDir(op.RoomID), op.RoomID)
		if err != nil {
			runErr = err
			return
		}
		op.RotateMembers = nil
		for _, member := range op.Members {
			id := MemberIdentity{RoomIdentity: op.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
			opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(sessionStore.dir, "agents", id.MembershipID, "session.json")}
			ref, exists, err := (dshBackend{store: sessionStore}).reference(opts)
			if err != nil {
				runErr = err
				break
			}
			if exists || ref.Pending {
				op.RotateMembers = append(op.RotateMembers, member.MembershipID)
			}
		}
		runErr = errors.Join(runErr, sessionStore.Close())
		if runErr != nil {
			return
		}
		op.PreparedConfig = &saved
		if err := advance("committing"); err != nil {
			runErr = err
			return
		}
	}
	code = "commit_unknown"
	if op.PreparedConfig == nil {
		runErr = ErrInvalid
		return
	}
	if saved.WorkspaceVersion != op.PreviousVersion && !reflect.DeepEqual(saved, *op.PreparedConfig) {
		runErr = ErrConflict
		return
	}
	// Recovery of an already prepared target uses only local checks, with no
	// second clone or platform query. Any changed/missing target remains blocked.
	verifiedAt := time.Now()
	if _, err := m.ensureWorkspaces(tracked, *op.PreparedConfig, false); err != nil {
		runErr = err
		return
	}
	op.Timings["target_check"] += time.Since(verifiedAt).Milliseconds()
	phaseStarted = time.Now()
	// The operation's complete target precedes the first cross-file commit.
	if !op.ConfigCommitted {
		m.mu.Lock()
		runErr = m.store.save(filepath.Join(m.roomDir(op.RoomID), "room-config.json"), *op.PreparedConfig, maxPrivateFileBytes)
		m.mu.Unlock()
		if runErr != nil {
			return
		}
		op.ConfigCommitted = true
		if err := advance("sessions"); err != nil {
			runErr = err
			return
		}
	}
	code = "session_failed"
	store, err := OpenStore(m.roomDir(op.RoomID), op.RoomID)
	if err != nil {
		runErr = err
		return
	}
	for _, member := range op.Members {
		updated := false
		for _, id := range op.SessionsUpdated {
			updated = updated || id == member.MembershipID
		}

		id := MemberIdentity{RoomIdentity: op.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		requiresRotation := false
		for _, membership := range op.RotateMembers {
			requiresRotation = requiresRotation || membership == member.MembershipID
		}
		if err := reconcileWorkspaceSession(store, id, op.OperationID, requiresRotation, updated); err != nil {
			runErr = err
			break
		}
		if updated {
			continue
		}
		op.SessionsUpdated = append(op.SessionsUpdated, member.MembershipID)
		op.UpdatedAt = time.Now().UTC()
		if err := m.saveWorkspaceOperation(op); err != nil {
			runErr = err
			break
		}
	}
	runErr = errors.Join(runErr, store.Close())
	if runErr != nil {
		return
	}
	// Assemble while the operation still blocks all external mutations.
	config, err := m.workspaceActivationConfig(op.RoomIdentity, op.OperationID)
	if err != nil {
		runErr = err
		return
	}
	svc, err := m.factory(m.roomDir(op.RoomID), op.RoomID, config)
	if err != nil {
		runErr = err
		return
	}
	svc.settleTools = func(ctx context.Context) error { return m.waitPlatformCalls(ctx, &op.RoomIdentity) }
	m.mu.Lock()
	m.setRuntimeServiceLocked(op.RoomIdentity, svc)
	if m.closing {
		svc.freeze()
		runErr = ErrClosing
	} else {
		rt.resuming = true
	}
	m.mu.Unlock()
	if runErr != nil {
		ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
		runErr = errors.Join(runErr, svc.close(ctx))
		cancel()
		return
	}
	if svc.info.RoomIdentity != op.RoomIdentity {
		runErr = ErrIdentity
		ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
		_ = svc.close(ctx)
		cancel()
		return
	}
	op.Status = "completed"
	if err := advance("completed"); err != nil {
		runErr = err
		op.Status, op.Phase = "running", "sessions"
	}
	if runErr == nil {
		// Configuration and all references are committed. A later scheduling error
		// is an execution diagnostic, never authorization to replay this operation.
		if err := svc.resumeMembers(context.Background()); err != nil {
			executionErr = err
			ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
			svc.closing.Store(true)
			executionErr = errors.Join(executionErr, m.waitPlatformCalls(ctx, &op.RoomIdentity), svc.close(ctx))
			cancel()
			m.mu.Lock()
			next := m.copyIndex()
			for i := range next.Rooms {
				if next.Rooms[i].RoomIdentity == op.RoomIdentity {
					next.Rooms[i].Error = "workspace applied; member scheduling failed; inspect execution before activating"
				}
			}
			_ = m.saveIndex(next)
			m.mu.Unlock()
		}
	}
	// A runtime opening failure must retain a blocking operation, not a completed
	// receipt permitting partial member execution. Persist before releasing owner.
	if runErr != nil {
		ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
		svc.closing.Store(true)
		runErr = errors.Join(runErr, m.waitPlatformCalls(ctx, &op.RoomIdentity), svc.close(ctx))
		cancel()
	}
}

func cloneToolEnv(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for k, v := range input {
		result[k] = v
	}
	return result
}

// Stable generation names make interrupted reference writes reconcilable. A
// member that has never opened a Session receives no synthetic rotation.
func updateWorkspaceSession(store *Store, id MemberIdentity, operationID string) error {
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(store.dir, "agents", id.MembershipID, "session.json")}
	ref, exists, err := (dshBackend{store: store}).reference(opts)
	if err != nil {
		return err
	}
	desired := filepath.Base(dshStateDir(opts.StatePath)) + "-" + operationID
	if ref.Directory == desired && ref.Pending {
		return nil
	}
	if !exists && !ref.Pending {
		return nil
	}
	if exists {
		directory := ref.Directory
		if directory == "" {
			directory = filepath.Base(dshStateDir(opts.StatePath))
		}
		if err := dsh.CheckReset(filepath.Join(filepath.Dir(opts.StatePath), directory)); err != nil {
			return err
		}
	}
	next := dshReference{Version: formatVersion, MemberIdentity: id, Directory: desired, Pending: true}
	store.mu.Lock()
	defer store.mu.Unlock()
	path, err := store.dshPath(id, "session.json", true)
	if err != nil {
		return err
	}
	return store.save(path, next, maxPrivateFileBytes)
}

// A recorded progress bit never substitutes for checking the actual reference.
func reconcileWorkspaceSession(store *Store, id MemberIdentity, operationID string, rotate, updated bool) error {
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(store.dir, "agents", id.MembershipID, "session.json")}
	ref, exists, err := (dshBackend{store: store}).reference(opts)
	if err != nil {
		return err
	}
	desired := filepath.Base(dshStateDir(opts.StatePath)) + "-" + operationID
	if !rotate {
		if exists || ref.Pending {
			return ErrConflict
		}
		return nil
	}
	if ref.Directory == desired && ref.Pending {
		return nil
	}
	if updated || !exists && !ref.Pending {
		return ErrConflict
	}
	return updateWorkspaceSession(store, id, operationID)
}

// Public history stays observable while member execution and the old service
// are closed. This reads an atomic committed snapshot and has no write path.
func (m *roomManager) workspaceHistory(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != "/v1/read" || r.Method != http.MethodPost {
		return false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	r.Body = io.NopCloser(strings.NewReader(string(data)))
	if err != nil {
		writeAPIError(w, ErrRequest)
		return true
	}
	var req apiRequest
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return true
	}
	m.mu.Lock()
	pending := false
	for _, op := range m.index.WorkspaceOperations {
		if op.RoomIdentity == req.RoomIdentity && !op.finished() && op.SourceEpoch == req.ServiceEpoch {
			pending = true
			break
		}
	}
	m.mu.Unlock()
	if !pending {
		return false
	}
	if req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return true
	}
	var state RoomSnapshot
	if err := readSnapshot(filepath.Join(m.roomDir(req.RoomID), "room.json"), &state, maxRoomFileBytes); err != nil {
		writeAPIError(w, err)
		return true
	}
	if state.RoomIdentity != req.RoomIdentity || state.validate() != nil {
		writeAPIError(w, ErrIdentity)
		return true
	}
	page, err := (&Room{state: state}).Read(nil, ReadMessages{RoomIdentity: req.RoomIdentity, Caller: req.Caller, Mode: req.Mode, Latest: req.Latest, After: req.After, SnapshotSeq: req.SnapshotSeq, Limit: req.Limit, Peek: req.Peek})
	if err != nil {
		writeAPIError(w, err)
		return true
	}
	_ = json.NewEncoder(w).Encode(page)
	return true
}

// Closing cancels active turns and revokes scheduler slots. Queued unread work
// also needs explicit continuation; otherwise an untouched member would replay
// the old request as soon as the newly assembled service restores enable intent.
func (m *roomManager) pausePendingWorkspaceTurns(owner RoomIdentity) error {
	store, err := OpenStore(m.roomDir(owner.RoomID), owner.RoomID)
	if err != nil {
		return err
	}
	defer store.Close()
	state, err := store.LoadRoom()
	if err != nil {
		return err
	}
	if state.RoomIdentity != owner {
		return ErrIdentity
	}
	for _, member := range state.Members {
		id := MemberIdentity{RoomIdentity: owner, AgentID: member.AgentID, MembershipID: member.MembershipID}
		unread, _ := (&Room{state: state}).unread(member)
		agent, err := store.LoadAgent(id)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		unsafe := unread > 0 || agent.Turn != nil && agent.Turn.Status != "completed"
		if !unsafe {
			continue
		}
		intent, err := store.loadScheduling(id)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		intent.Enabled = false
		if err := store.saveScheduling(intent); err != nil {
			return err
		}
	}
	return nil
}
