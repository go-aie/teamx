package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func workspaceRequest(m *roomManager, room roomSummary, target workspaceConfig) workspaceApplyRequest {
	id, _ := newOpaqueID()
	return workspaceApplyRequest{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, OperationID: id, ExpectedVersion: 1, Target: target}
}
func awaitWorkspace(t *testing.T, m *roomManager) workspaceOperation {
	t.Helper()
	m.mu.Lock()
	done := m.operationDone
	m.mu.Unlock()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("workspace operation did not settle")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneWorkspaceOperation(m.index.WorkspaceOperations[len(m.index.WorkspaceOperations)-1])
}
func applyWorkspace(t *testing.T, m *roomManager, req workspaceApplyRequest) workspaceOperation {
	t.Helper()
	r := managerHTTP(m, "/v1/workspace/apply", req)
	var op workspaceOperation
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &op) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	if strings.Contains(r.Body.String(), "prepared_config") || strings.Contains(r.Body.String(), "previous_config") {
		t.Fatal("private config exposed")
	}
	return op
}
func queryWorkspace(t *testing.T, m *roomManager, room roomSummary, id string) workspaceResponse {
	t.Helper()
	q := url.Values{"service_epoch": {m.epoch}, "caller_id": {humanCaller.ID}, "room_id": {room.RoomID}, "room_instance": {room.RoomInstance}, "operation_id": {id}}
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/workspace?"+q.Encode(), nil))
	var response workspaceResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "prepared_config") || strings.Contains(w.Body.String(), "previous_config") || strings.Contains(w.Body.String(), "binary_path") {
		t.Fatal("private runtime leaked")
	}
	return response
}
func retryWorkspaceHTTP(t *testing.T, m *roomManager, room roomSummary, id string) {
	t.Helper()
	r := managerHTTP(m, "/v1/workspace/retry", map[string]any{"service_epoch": m.epoch, "caller": humanCaller, "room_id": room.RoomID, "room_instance": room.RoomInstance, "operation_id": id})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestWorkspaceApplyUntouchedMembersAndIdentity(t *testing.T) {
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	room := managerCreate(t, m, "plain-apply")
	other := managerCreate(t, m, "other")
	s := activateOK(t, m, room, "initial")
	saved, _ := readManagedRoom(t, m, room)
	// Inline teams need no tool environment; keep this path free of template helpers.
	for i := range saved.Members {
		saved.Members[i].ToolEnv = nil
	}
	if err := m.store.save(filepath.Join(m.roomDir(room.RoomID), "room-config.json"), saved, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(m.roomDir(room.RoomID), saved.Members[0].Workspace, "keep.txt")
	os.WriteFile(original, []byte("old files"), 0600)
	member := saved.Members[0]
	if _, err := s.control(context.Background(), apiRequest{AgentID: member.AgentID, MembershipID: member.MembershipID}, "pause"); err != nil {
		t.Fatal(err)
	}
	req := workspaceRequest(m, room, workspaceConfig{Root: "outputs"})
	applyWorkspace(t, m, req)
	op := awaitWorkspace(t, m)
	if op.Status != "completed" || !op.ConfigCommitted || len(op.SessionsUpdated) != len(saved.Members) {
		t.Fatal(op)
	}
	status := queryWorkspace(t, m, room, req.OperationID)
	if status.Version != 2 || status.Workspace.Root != "outputs" || status.Active == nil {
		t.Fatal(status)
	}
	if data, _ := os.ReadFile(original); string(data) != "old files" {
		t.Fatal("old files lost")
	}
	for _, member := range saved.Members {
		if _, err := os.Stat(filepath.Join(m.roomDir(room.RoomID), "outputs", member.MembershipID)); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(m.roomDir(room.RoomID), "agents", member.MembershipID, "session.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("untouched member rotated", err)
		}
	}
	list, _ := m.active.members(context.Background())
	for _, info := range list.Members {
		if info.MembershipID == member.MembershipID && !info.Execution.Paused {
			t.Fatal("manual pause lost")
		}
	}
	// Same operation is a receipt even after completion and with a stale version.
	before := m.active
	applyWorkspace(t, m, req)
	if m.active != before || len(m.index.WorkspaceOperations) != 1 {
		t.Fatal("replayed application")
	}
	changed := req
	changed.Target.Root = "other"
	if r := managerHTTP(m, "/v1/workspace/apply", changed); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	stale := req
	stale.OperationID, _ = newOpaqueID()
	if r := managerHTTP(m, "/v1/workspace/apply", stale); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	foreign := req
	foreign.RoomIdentity = other.RoomIdentity
	foreign.OperationID, _ = newOpaqueID()
	if r := managerHTTP(m, "/v1/workspace/apply", foreign); r.Code != 403 {
		t.Fatal(r.Code, r.Body.String())
	}
	oldEpoch := req
	oldEpoch.ServiceEpoch = "stale"
	if r := managerHTTP(m, "/v1/workspace/apply", oldEpoch); r.Code != 403 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := managerHTTP(m, "/v1/send", apiRequest{ServiceInfo: s.info, Caller: humanCaller, Text: "stale", ClientMessageID: "stale"}); r.Code != 403 {
		t.Fatal("old service retained access", r.Code)
	}
}

func TestWorkspaceApplyJoinsRunningSessionsBeforePreparation(t *testing.T) {
	m := activationTestManager(t)
	closing, release, started := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce, startedOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var once sync.Once
	var runs atomic.Int32
	m.factory = controlledActivationFactory(func(_ MemberIdentity, ctx context.Context) error {
		runs.Add(1)
		startedOnce.Do(func() { close(started) })
		<-ctx.Done()
		return ctx.Err()
	}, func(MemberIdentity) { once.Do(func() { close(closing) }); <-release })
	room := managerCreate(t, m, "running-apply")
	other := managerCreate(t, m, "other")
	s := activateOK(t, m, room, "initial")
	saved, _ := readManagedRoom(t, m, room)
	managedPost(t, m, s, "history")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("no active turn")
	}
	beforeRuns := runs.Load()
	req := workspaceRequest(m, room, workspaceConfig{Root: "settled-target"})
	// A disconnected HTTP caller does not cancel management ownership.
	data, _ := json.Marshal(req)
	httpReq := httptest.NewRequest(http.MethodPost, "/v1/workspace/apply", bytes.NewReader(data))
	canceled, stop := context.WithCancel(context.Background())
	stop()
	httpReq = httpReq.WithContext(canceled)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httpReq)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	select {
	case <-closing:
	case <-ctx.Done():
		t.Fatal("no Session cleanup")
	}
	if _, err := os.Stat(filepath.Join(m.roomDir(room.RoomID), req.Target.Root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("prepared before settlement", err)
	}
	applyWorkspace(t, m, req)
	status := queryWorkspace(t, m, room, req.OperationID)
	if status.Operation.Status != "running" || status.Operation.Phase != "waiting" || status.Observation == nil {
		t.Fatal(status)
	}
	if r := managerHTTP(m, "/v1/read", apiRequest{ServiceInfo: s.info, Caller: humanCaller, Mode: ReadAll}); r.Code != 200 || !strings.Contains(r.Body.String(), "history") {
		t.Fatal("history unavailable during cleanup", r.Code, r.Body.String())
	}
	for _, path := range []string{"/v1/send", "/v1/discussion/restart", "/v1/member/session/rotate"} {
		if r := managerHTTP(m, path, apiRequest{ServiceInfo: s.info, Caller: humanCaller}); r.Code != 409 {
			t.Fatal("mutation admitted", path, r.Code)
		}
	}
	if r := managerHTTP(m, "/v1/rooms/activate", activationRequest{ServiceEpoch: m.epoch, Caller: humanCaller, RoomIdentity: other.RoomIdentity, ActivateID: "competing"}); r.Code != 409 {
		t.Fatal("activation not serialized", r.Code)
	}
	if r := managerHTTP(m, "/v1/rooms/delete", map[string]any{"service_epoch": m.epoch, "caller": humanCaller, "room_id": room.RoomID, "room_instance": room.RoomInstance}); r.Code != 409 {
		t.Fatal("delete not serialized", r.Code)
	}
	releaseOnce.Do(func() { close(release) })
	op := awaitWorkspace(t, m)
	if op.Status != "completed" {
		t.Fatal(op)
	}
	if runs.Load() != beforeRuns {
		t.Fatal("interrupted requests replayed")
	}
	for _, member := range saved.Members {
		id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		ref, exists, err := (dshBackend{store: m.active.store}).reference(SessionOptions{MemberIdentity: id, StatePath: filepath.Join(m.active.store.dir, "agents", id.MembershipID, "session.json")})
		if err != nil || exists || ref.Directory != "" && (!ref.Pending || !strings.HasSuffix(ref.Directory, req.OperationID)) {
			t.Fatal("old reference restored", ref, err)
		}
		status, err := m.active.target(id.MembershipID).Call[MemberStatus](ctx, InspectMember{id})
		if err != nil || !status.Paused {
			t.Fatal("interrupted member resumed", status, err)
		}
		profile := m.active.dshProfiles[member.Runner.Profile]
		if profile.Workspace != filepath.Join(m.roomDir(room.RoomID), "settled-target", id.MembershipID) {
			t.Fatal(profile.Workspace)
		}
	}
	history, _ := m.active.store.LoadRoom()
	if len(history.Messages) != 1 || history.Messages[0].Text != "history" {
		t.Fatal("history changed")
	}
}

func TestWorkspaceApplyGitLabPATReplacementAndReceipt(t *testing.T) {
	f := newWorkspaceFixture(t)
	m := activationTestManager(t)
	m.workspaceRun = f.run
	m.platformHTTP = workspaceHTTPFunc(f.http)
	m.factory = controlledActivationFactory(nil, nil)
	room := managerCreate(t, m, "binding")
	activateOK(t, m, room, "initial")
	req := workspaceRequest(m, room, workspaceConfig{Repository: f.config.Workspace.Repository, Binding: &workspaceBinding{Provider: "gitlab", URL: "https://gitlab.invalid", Project: "group/project"}})
	req.PAT = "first-secret-pat"
	applyWorkspace(t, m, req)
	op := awaitWorkspace(t, m)
	if op.Status != "completed" {
		t.Fatal(op)
	}
	initialID := op.Target.Binding.CredentialID
	if op.Target.Root != "workspaces/repos/"+req.OperationID || initialID == "" {
		t.Fatal(op)
	}
	firstConfig, _ := readManagedRoom(t, m, room)
	firstPaths := make([]string, 0)
	for _, member := range firstConfig.Members {
		firstPaths = append(firstPaths, member.Workspace)
		if member.ToolEnv["TEAMX_GITLAB_TOOLS"] == "" || !strings.Contains(member.Instructions, "tools run gitlab --help") {
			t.Fatal("provider not attached")
		}
	}
	req.PAT = "ignored-secret-pat"
	applyWorkspace(t, m, req)
	req.PAT = ""
	applyWorkspace(t, m, req)
	entries, _ := os.ReadDir(filepath.Join(m.store.dir, "credentials"))
	if len(entries) != 1 {
		t.Fatal("duplicate stored PAT")
	}
	resolved, err := m.loadWorkspaceCredential(room.RoomIdentity, initialID)
	if err != nil || resolved.value != "first-secret-pat" {
		t.Fatal("duplicate replaced secret", err)
	}
	next := workspaceRequest(m, room, cloneWorkspaceOperation(op).Target)
	next.ExpectedVersion = 2
	next.PAT = "replacement-secret-pat"
	next.Target.Binding.CredentialID = ""
	applyWorkspace(t, m, next)
	newOp := awaitWorkspace(t, m)
	if newOp.Status != "completed" {
		t.Fatal(newOp)
	}
	if newOp.Target.Binding.CredentialID == initialID {
		t.Fatal("reused credential record")
	}
	secondConfig, _ := readManagedRoom(t, m, room)
	for i, member := range secondConfig.Members {
		if member.Workspace != firstPaths[i] {
			t.Fatal("replacement changed clone")
		}
		if member.Instructions != firstConfig.Members[i].Instructions {
			t.Fatal("duplicated usage")
		}
	}
	status := queryWorkspace(t, m, room, next.OperationID)
	if status.CredentialStatus != "configured" {
		t.Fatal(status)
	}
	// PAT exists only in the credential files, never config, resources or receipts.
	filepath.WalkDir(m.store.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.Contains(path, string(filepath.Separator)+"credentials"+string(filepath.Separator)) {
			return err
		}
		data, _ := os.ReadFile(path)
		if bytes.Contains(data, []byte("secret-pat")) {
			t.Errorf("PAT leaked into %s", path)
		}
		return nil
	})
	// Revisit uses local checks and the committed provider configuration.
	f.calls = nil
	other := managerCreate(t, m, "other")
	activateOK(t, m, other, "away")
	activateOK(t, m, room, "return")
	for _, call := range f.calls {
		if strings.HasPrefix(call, "api ") || strings.Contains(call, "clone") {
			t.Fatal("remote revisit", call)
		}
	}
	for _, mutate := range []func(*workspaceApplyRequest){func(r *workspaceApplyRequest) { r.Target.Repository.URL = "https://gitlab.invalid/group/other.git" }, func(r *workspaceApplyRequest) { r.Target.Binding = nil }, func(r *workspaceApplyRequest) { r.Target.Root = "different" }} {
		bad := workspaceRequest(m, room, newOp.Target)
		bad.ExpectedVersion = 3
		mutate(&bad)
		if r := managerHTTP(m, "/v1/workspace/apply", bad); r.Code == 200 {
			t.Fatal("unsupported binding change accepted")
		}
	}
}

func TestWorkspaceApplyPreparationFailureAndExplicitRetry(t *testing.T) {
	f := newWorkspaceFixture(t)
	f.failClone = true
	m := activationTestManager(t)
	m.workspaceRun = f.run
	m.platformHTTP = workspaceHTTPFunc(f.http)
	m.factory = controlledActivationFactory(nil, nil)
	room := managerCreate(t, m, "failure")
	s := activateOK(t, m, room, "initial")
	req := workspaceRequest(m, room, workspaceConfig{Repository: f.config.Workspace.Repository})
	applyWorkspace(t, m, req)
	op := awaitWorkspace(t, m)
	if op.Status != "failed" || op.ErrorCode != "preparation_failed" || op.Phase != "preparing" {
		t.Fatal(op)
	}
	config, _ := readManagedRoom(t, m, room)
	if config.WorkspaceVersion != 1 || config.Workspace.Repository != nil {
		t.Fatal("failed target published")
	}
	before := len(f.calls)
	applyWorkspace(t, m, req)
	if len(f.calls) != before {
		t.Fatal("duplicate replayed failed prepare")
	}
	if r := managerHTTP(m, "/v1/send", apiRequest{ServiceInfo: s.info, Caller: humanCaller, Text: "blocked", ClientMessageID: "blocked"}); r.Code == 200 {
		t.Fatal("failed room executed")
	}
	retryWorkspaceHTTP(t, m, room, req.OperationID)
	op = awaitWorkspace(t, m)
	if op.Status != "failed" {
		t.Fatal("retry erased partial clone")
	}
	// An operator explicitly removes the inspected offline fixture's partial clone.
	for _, member := range op.Members {
		os.RemoveAll(filepath.Join(m.roomDir(room.RoomID), member.Workspace))
		os.Remove(filepath.Join(m.roomDir(room.RoomID), member.Workspace) + ".clone-pending")
	}
	f.failClone = false
	retryWorkspaceHTTP(t, m, room, req.OperationID)
	op = awaitWorkspace(t, m)
	if op.Status != "completed" || op.Target.Root != "workspaces/repos/"+req.OperationID {
		t.Fatal(op)
	}
}

func TestWorkspaceApplyRevisesSettledPreparationFailure(t *testing.T) {
	for _, repository := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "repository"}[repository], func(t *testing.T) {
			f := newWorkspaceFixture(t)
			f.failClone = true
			m := activationTestManager(t)
			m.workspaceRun = f.run
			m.platformHTTP = workspaceHTTPFunc(f.http)
			m.factory = controlledActivationFactory(nil, nil)
			room := managerCreate(t, m, "revise-failure")
			activateOK(t, m, room, "initial")
			original := workspaceRequest(m, room, testGitLabWorkspace())
			original.Target.Repository.URL = strings.TrimSuffix(original.Target.Repository.URL, ".git")
			applyWorkspace(t, m, original)
			failed := awaitWorkspace(t, m)
			if !failed.revisable() || !queryWorkspace(t, m, room, failed.OperationID).CanRevise {
				t.Fatal("settled failed preparation is not editable", failed)
			}
			partial := filepath.Join(m.roomDir(room.RoomID), failed.Members[0].Workspace, "partial")
			before, err := os.ReadFile(partial)
			if err != nil {
				t.Fatal(err)
			}
			corrected := workspaceRequest(m, room, workspaceConfig{Root: "outputs"})
			if repository {
				corrected.Target = testGitLabWorkspace()
			}
			corrected.ReplacesOperationID = failed.OperationID
			// Every uncertain or later-stage failure remains protected, including
			// against API callers bypassing the editable form.
			for _, mutate := range []func(*workspaceOperation){
				func(op *workspaceOperation) { op.ErrorCode = "interrupted" },
				func(op *workspaceOperation) { op.ConfigCommitted = true },
				func(op *workspaceOperation) { op.Phase = "committing" },
				func(op *workspaceOperation) { op.Preparation = &workspacePreparationProcess{Phase: "starting"} },
			} {
				blocked := cloneWorkspaceOperation(failed)
				mutate(&blocked)
				m.mu.Lock()
				m.index.WorkspaceOperations[0] = blocked
				m.mu.Unlock()
				response := managerHTTP(m, "/v1/workspace/apply", corrected)
				if response.Code == 200 {
					t.Fatal("replaced an uncertain operation")
				}
				m.mu.Lock()
				m.index.WorkspaceOperations[0] = cloneWorkspaceOperation(failed)
				m.mu.Unlock()
			}
			f.failClone = false
			applyWorkspace(t, m, corrected)
			completed := awaitWorkspace(t, m)
			if completed.Status != "completed" || completed.ReplacesOperationID != failed.OperationID {
				t.Fatal("corrected application did not complete", completed)
			}
			if repository && completed.Target.Root == failed.Target.Root {
				t.Fatal("reused failed clone directory")
			}
			if after, err := os.ReadFile(partial); err != nil || !bytes.Equal(before, after) {
				t.Fatal("lost original failure evidence", err)
			}
			m.mu.Lock()
			status := m.index.WorkspaceOperations[0].Status
			validationErr := m.validateIndex()
			m.mu.Unlock()
			if status != "superseded" {
				t.Fatal("old receipt not retained")
			}
			if validationErr != nil {
				t.Fatal("index cannot be reloaded", validationErr)
			}
			calls := len(f.calls)
			if op := applyWorkspace(t, m, original); op.Status != "superseded" {
				t.Fatal("old request replayed")
			}
			retryWorkspaceHTTP(t, m, room, failed.OperationID)
			applyWorkspace(t, m, corrected)
			if len(f.calls) != calls {
				t.Fatal("duplicate request performed preparation")
			}
			// Later configuration versions must not invalidate superseded history.
			next := workspaceRequest(m, room, completed.Target)
			next.ExpectedVersion = completed.TargetVersion
			applyWorkspace(t, m, next)
			if op := awaitWorkspace(t, m); op.Status != "completed" {
				t.Fatal(op)
			}
			m.mu.Lock()
			err = m.validateIndex()
			m.mu.Unlock()
			if err != nil {
				t.Fatal("superseded receipt blocks later versions", err)
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			reopened := testManager(t, m.store.dir, managerTestProfiles(t))
			if len(reopened.index.WorkspaceOperations) != 3 || reopened.index.WorkspaceOperations[0].Status != "superseded" {
				t.Fatal("superseded history did not survive restart")
			}
			if err := reopened.checkWorkspaceOperations(room.RoomIdentity); err != nil {
				t.Fatal("old failure still blocks room after restart", err)
			}
		})
	}
}

func TestWorkspaceApplyReconcilesCrossFileInterruption(t *testing.T) {
	for _, boundary := range []string{"config", "member-progress"} {
		t.Run(boundary, func(t *testing.T) {
			m := activationTestManager(t)
			m.factory = controlledActivationFactory(nil, nil)
			room := managerCreate(t, m, "partial")
			s := activateOK(t, m, room, "initial")
			managedPost(t, m, s, "completed")
			config, _ := readManagedRoom(t, m, room)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for _, member := range config.Members {
				id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
				waitMember(t, ctx, s.target(id.MembershipID), id, func(status MemberStatus) bool { return !status.Active && status.Agent.TurnNumber == 1 })
			}
			rename := m.store.ops.rename
			var injected atomic.Bool
			m.store.ops.rename = func(from, to string) error {
				if boundary == "config" && filepath.Base(to) == "room-config.json" && injected.CompareAndSwap(false, true) {
					if err := rename(from, to); err != nil {
						return err
					}
					return errors.New("ambiguous config replacement")
				}
				if boundary == "member-progress" && to == m.indexPath() && !injected.Load() {
					var index roomsIndex
					data, _ := os.ReadFile(from)
					if json.Unmarshal(data, &index) == nil && len(index.WorkspaceOperations) > 0 && len(index.WorkspaceOperations[0].SessionsUpdated) == 1 && injected.CompareAndSwap(false, true) {
						return errors.New("failed progress replacement")
					}
				}
				return rename(from, to)
			}
			req := workspaceRequest(m, room, workspaceConfig{Root: "after-interruption"})
			applyWorkspace(t, m, req)
			op := awaitWorkspace(t, m)
			if !injected.Load() || op.Status != "failed" {
				t.Fatal("boundary not exercised", op)
			}
			root := m.store.dir
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			restored, err := newRoomManager(root, 7)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.close()
			restored.config = m.config
			restored.factory = controlledActivationFactory(nil, nil)
			if err := restored.restoreActive(); err != nil || restored.active != nil {
				t.Fatal("restart resumed partial operation", err)
			}
			query := queryWorkspace(t, restored, room, req.OperationID)
			if query.Operation.Status != "failed" {
				t.Fatal(query)
			}
			retryWorkspaceHTTP(t, restored, room, req.OperationID)
			op = awaitWorkspace(t, restored)
			if op.Status != "completed" {
				t.Fatal(op)
			}
			final, _ := readManagedRoom(t, restored, room)
			if final.WorkspaceVersion != 2 || final.Workspace.Root != "after-interruption" {
				t.Fatal(final)
			}
			for _, member := range final.Members {
				id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
				opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(restored.active.store.dir, "agents", id.MembershipID, "session.json")}
				ref, _, err := (dshBackend{store: restored.active.store}).reference(opts)
				if err != nil || !ref.Pending || !strings.HasSuffix(ref.Directory, req.OperationID) {
					t.Fatal("old session survived", ref, err)
				}
			}
		})
	}
}

func TestWorkspaceApplySupplementBindingPreservesCheckout(t *testing.T) {
	f := newWorkspaceFixture(t)
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	m.workspaceRun = f.run
	m.platformHTTP = workspaceHTTPFunc(f.http)
	room := managerCreate(t, m, "supplement")
	activateOK(t, m, room, "initial")
	req := workspaceRequest(m, room, workspaceConfig{Repository: f.config.Workspace.Repository})
	applyWorkspace(t, m, req)
	op := awaitWorkspace(t, m)
	if op.Status != "completed" {
		t.Fatal(op)
	}
	saved, _ := readManagedRoom(t, m, room)
	dir := filepath.Join(m.roomDir(room.RoomID), saved.Members[0].Workspace)
	workspaceGit(t, dir, "checkout", "-b", "my-work")
	os.WriteFile(filepath.Join(dir, "README"), []byte("local edits"), 0600)
	f.calls = nil
	next := workspaceRequest(m, room, cloneWorkspaceOperation(op).Target)
	next.ExpectedVersion = 2
	next.Target.Binding = &workspaceBinding{Provider: "gitlab", URL: "https://gitlab.invalid", Project: "group/project"}
	next.PAT = "supplement-pat"
	applyWorkspace(t, m, next)
	op = awaitWorkspace(t, m)
	if op.Status != "completed" {
		t.Fatal(op)
	}
	if workspaceGit(t, dir, "branch", "--show-current") != "my-work" {
		t.Fatal("checkout changed")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "README")); string(data) != "local edits" {
		t.Fatal("edits lost")
	}
	for _, call := range f.calls {
		if strings.Contains(call, "clone") || strings.Contains(call, "checkout") {
			t.Fatal("supplement changed repo", call)
		}
	}
}

func TestWorkspaceApplyMemberFailureBlocksTeamAndRetriesSameGeneration(t *testing.T) {
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	room := managerCreate(t, m, "member-failure")
	s := activateOK(t, m, room, "initial")
	saved, _ := readManagedRoom(t, m, room)
	managedPost(t, m, s, "completed")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, member := range saved.Members {
		id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		waitMember(t, ctx, s.target(id.MembershipID), id, func(status MemberStatus) bool { return !status.Active && status.Agent.TurnNumber == 1 })
	}
	// Corrupt the second member only during assembly to verify the all-member gate.
	m.factory = func(dir, id string, config serviceConfig) (*service, error) {
		path := filepath.Join(dir, "agents", saved.Members[1].MembershipID, "agent.json")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte("broken-checkpoint"), 0600); err != nil {
			return nil, err
		}
		svc, openErr := newService(dir, id, config)
		_ = os.WriteFile(path, data, 0600)
		return svc, openErr
	}
	req := workspaceRequest(m, room, workspaceConfig{Root: "new-member-config"})
	applyWorkspace(t, m, req)
	op := awaitWorkspace(t, m)
	if op.Status != "failed" || op.ErrorCode != "session_failed" || !op.ConfigCommitted || len(op.SessionsUpdated) != len(op.Members) {
		t.Fatal(op)
	}
	if m.active != nil {
		t.Fatal("failed partial team published")
	}
	rootBefore := op.Target.Root
	m.factory = controlledActivationFactory(nil, nil)
	retryWorkspaceHTTP(t, m, room, req.OperationID)
	op = awaitWorkspace(t, m)
	if op.Status != "completed" || op.Target.Root != rootBefore {
		t.Fatal(op)
	}
	for _, member := range saved.Members {
		var ref dshReference
		if err := readSnapshot(filepath.Join(m.roomDir(room.RoomID), "agents", member.MembershipID, "session.json"), &ref, maxPrivateFileBytes); err != nil || !strings.HasSuffix(ref.Directory, req.OperationID) {
			t.Fatal("retry changed generation", ref, err)
		}
	}
}

func TestWorkspaceApplyConcurrentRequestsAndRedactedErrors(t *testing.T) {
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	room := managerCreate(t, m, "concurrent")
	activateOK(t, m, room, "initial")
	req := workspaceRequest(m, room, workspaceConfig{Root: "concurrent-target"})
	var wg sync.WaitGroup
	codes := make(chan int, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- managerHTTP(m, "/v1/workspace/apply", req).Code }()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatal(code)
		}
	}
	op := awaitWorkspace(t, m)
	if op.Status != "completed" || len(m.index.WorkspaceOperations) != 1 {
		t.Fatal(op)
	}
	// Error bodies never echo URL credentials or a submitted PAT.
	bad := workspaceRequest(m, room, workspaceConfig{Repository: &workspaceRepository{URL: "https://user:never-echo-this@gitlab.invalid/group/project.git"}})
	bad.ExpectedVersion = 2
	bad.PAT = "never-echo-this"
	r := managerHTTP(m, "/v1/workspace/apply", bad)
	if r.Code == 200 || strings.Contains(r.Body.String(), "never-echo-this") {
		t.Fatal(r.Code, r.Body.String())
	}
	retry := map[string]any{"service_epoch": m.epoch, "caller": humanCaller, "room_id": room.RoomID, "room_instance": room.RoomInstance, "operation_id": req.OperationID, "pat": "never-echo-this"}
	if r := managerHTTP(m, "/v1/workspace/retry", retry); r.Code != 400 || strings.Contains(r.Body.String(), "never-echo-this") {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestWorkspaceApplyHealthySessionUsesFreshGeneration(t *testing.T) {
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	room := managerCreate(t, m, "healthy-session")
	s := activateOK(t, m, room, "initial")
	saved, _ := readManagedRoom(t, m, room)
	managedPost(t, m, s, "first-message")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, member := range saved.Members {
		id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		waitMember(t, ctx, s.target(id.MembershipID), id, func(status MemberStatus) bool { return !status.Active && status.Agent.TurnNumber == 1 })
		caller := Caller{Author: Author{Kind: "agent", ID: member.AgentID}, MembershipID: member.MembershipID}
		r := managerHTTP(m, "/v1/read", apiRequest{ServiceInfo: s.info, Caller: caller, Mode: ReadUnread})
		var page ReadPage
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &page) != nil || page.ReadReceipt == "" {
			t.Fatal(r.Code, r.Body.String())
		}
		if r := managerHTTP(m, "/v1/read/confirm", apiRequest{ServiceInfo: s.info, Caller: caller, ReadReceipt: page.ReadReceipt}); r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	req := workspaceRequest(m, room, workspaceConfig{Root: "fresh-context"})
	applyWorkspace(t, m, req)
	op := awaitWorkspace(t, m)
	if op.Status != "completed" {
		t.Fatal(op)
	}
	for _, member := range saved.Members {
		id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		status, err := m.active.target(id.MembershipID).Call[MemberStatus](ctx, InspectMember{id})
		if err != nil || status.Paused || status.TurnBudget.Used != 1 {
			t.Fatal("healthy enable intent or budget lost", status, err)
		}
	}
	managedPost(t, m, m.active, "second-message")
	for _, member := range saved.Members {
		id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		waitMember(t, ctx, m.active.target(id.MembershipID), id, func(status MemberStatus) bool { return !status.Active && status.Agent.TurnNumber == 2 })
		path := filepath.Join(m.roomDir(room.RoomID), "agents", id.MembershipID, "session.json")
		var ref dshReference
		if err := readSnapshot(path, &ref, maxPrivateFileBytes); err != nil || ref.Pending || !strings.HasSuffix(ref.Directory, req.OperationID) {
			t.Fatal("new turn used wrong Session", ref, err)
		}
		if _, err := os.Stat(filepath.Join(dshStateDir(path), "backend.json")); err != nil {
			t.Fatal("old Session evidence removed", err)
		}
	}
}

func TestWorkspaceRetryRequiresPreparationProcessSettlement(t *testing.T) {
	for _, phase := range []string{"starting", "running"} {
		t.Run(phase, func(t *testing.T) {
			f := newWorkspaceFixture(t)
			m := activationTestManager(t)
			m.factory = controlledActivationFactory(nil, nil)
			m.platformHTTP = workspaceHTTPFunc(f.http)
			room := managerCreate(t, m, "preparer-settlement")
			activateOK(t, m, room, "initial")
			// Settle an initial clone first. The interrupted process below is a local
			// verification on an existing clone; no clone-pending marker is eligible
			// for replay merely because the process group has exited.
			m.workspaceRun = f.run
			baseReq := workspaceRequest(m, room, workspaceConfig{Repository: f.config.Workspace.Repository})
			applyWorkspace(t, m, baseReq)
			baseOp := awaitWorkspace(t, m)
			if baseOp.Status != "completed" {
				t.Fatal(baseOp.Status, baseOp.ErrorCode)
			}

			var cmd *exec.Cmd
			if phase == "running" {
				cmd = exec.CommandContext(context.Background(), "sh", "-c", "exec sleep 30")
				configurePreparation(cmd)
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = cmd.Cancel(); _ = cmd.Wait() })
			}
			m.workspaceRun = func(ctx context.Context, _ string, _ map[string]string, _ string, _ ...string) ([]byte, error) {
				if err := trackPreparation(ctx, "starting", 0); err != nil {
					return nil, err
				}
				if cmd != nil {
					if err := trackPreparation(ctx, "running", cmd.Process.Pid); err != nil {
						return nil, err
					}
				}
				return nil, errors.New("injected unsettled preparation")
			}
			target := baseOp.Target
			target.Binding = &workspaceBinding{Provider: "gitlab", URL: "https://gitlab.invalid", Project: "group/project", TokenEnv: "GITLAB_PAT"}
			req := workspaceRequest(m, room, target)
			req.ExpectedVersion = 2
			applyWorkspace(t, m, req)
			op := awaitWorkspace(t, m)
			if op.Status != "failed" || op.ErrorCode != "cleanup_failed" || op.Preparation == nil {
				t.Fatal(op)
			}
			f.calls = nil
			root, config := m.store.dir, m.config
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			restored, err := newRoomManager(root, 7)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.close()
			restored.config, restored.factory, restored.workspaceRun = config, controlledActivationFactory(nil, nil), f.run
			restored.platformHTTP = workspaceHTTPFunc(f.http)
			if err := restored.restoreActive(); err != nil || restored.active != nil {
				t.Fatal("resumed an unsettled preparer", err)
			}
			retry := map[string]any{"service_epoch": restored.epoch, "caller": humanCaller, "room_id": room.RoomID, "room_instance": room.RoomInstance, "operation_id": req.OperationID}
			if r := managerHTTP(restored, "/v1/workspace/retry", retry); r.Code != 409 {
				t.Fatal("unverified process allowed retry", r.Code, r.Body.String())
			}
			if len(f.calls) != 0 {
				t.Fatal("ran preparation before settlement")
			}
			if err := restored.checkDeletionSettled(room.RoomIdentity); !errors.Is(err, ErrBusy) {
				t.Fatal("deletion ignored preparer ownership", err)
			}
			if cmd == nil {
				return
			} // A spawn/PID gap requires offline inspection, never a guess.
			_ = cmd.Cancel()
			_ = cmd.Wait()
			if !preparationGroupSettled(cmd.Process.Pid) {
				t.Fatal("fixture process group did not settle")
			}
			retryWorkspaceHTTP(t, restored, room, req.OperationID)
			op = awaitWorkspace(t, restored)
			if op.Status != "completed" || op.Preparation != nil {
				t.Fatal(op)
			}
		})
	}
}

func TestWorkspaceQueryUnrecordedOperationIsExplicit(t *testing.T) {
	m := activationTestManager(t)
	room := managerCreate(t, m, "unrecorded")
	id, _ := newOpaqueID()
	q := url.Values{"service_epoch": {m.epoch}, "caller_id": {humanCaller.ID}, "room_id": {room.RoomID}, "room_instance": {room.RoomInstance}, "operation_id": {id}}
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/workspace?"+q.Encode(), nil))
	if w.Code != 404 || !strings.Contains(w.Body.String(), "operation_not_found") {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(m.index.WorkspaceOperations) != 0 {
		t.Fatal("query allocated an operation")
	}
}

func TestWorkspaceQuerySuggestionsAreDisplayOnly(t *testing.T) {
	m := activationTestManager(t)
	room := managerCreate(t, m, "workspace-suggestions")
	saved, _ := readManagedRoom(t, m, room)
	suggestion := workspaceConfig{Root: "suggested", Repository: &workspaceRepository{URL: "https://gitlab.invalid/group/project.git"}, Binding: &workspaceBinding{Provider: "gitlab", URL: "https://gitlab.invalid", Project: "group/project", TokenEnv: "ROOM_GITLAB_TOKEN"}}
	m.teams = map[string]teamDefinition{saved.TeamID: {Workspace: &suggestion}}
	response := queryWorkspace(t, m, room, "")
	if response.Suggestions == nil || !equalWorkspace(*response.Suggestions, suggestion) || response.Workspace.Repository != nil || response.Workspace.Binding != nil || response.Operation != nil {
		t.Fatal("suggestions became effective configuration", response)
	}
	unchanged, _ := readManagedRoom(t, m, room)
	if !equalWorkspace(*unchanged.Workspace, *saved.Workspace) || unchanged.WorkspaceVersion != saved.WorkspaceVersion {
		t.Fatal("query changed saved configuration")
	}
	delete(m.teams, saved.TeamID)
	if response := queryWorkspace(t, m, room, ""); response.Suggestions != nil || !equalWorkspace(*response.Workspace, *saved.Workspace) {
		t.Fatal("unavailable template changed current configuration")
	}
}
