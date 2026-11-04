package room

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

// Requires manager.mu. The caller supplies a validated effective Workspace or a
// pending operator target, never an agent-selected connection or target URL.
func (m *roomManager) resolveWorkspaceConnectionLocked(owner RoomIdentity, w workspaceConfig) (projectConnectionResolution, error) {
	normalized, err := normalizeWorkspace(w, true)
	if err != nil || normalized.Binding == nil {
		return projectConnectionResolution{}, ErrRequest
	}
	return m.resolveProjectConnectionLocked(owner, *normalized.Binding)
}

func (m *roomManager) prepareWorkspaceConnection(ctx context.Context, req *workspaceApplyRequest) error {
	if req.Target.Binding == nil || req.Target.Binding.ConnectionID == "" {
		return nil
	}
	m.mu.Lock()
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		m.mu.Unlock()
		return ErrIdentity
	}
	for _, op := range m.index.WorkspaceOperations {
		if op.OperationID == req.OperationID {
			m.mu.Unlock()
			return nil
		} // startWorkspaceApply checks the exact intent
	}
	if m.closing || m.roomBusyLocked(req.RoomIdentity) {
		m.mu.Unlock()
		return ErrBusy
	}
	replacement := false
	for _, op := range m.index.WorkspaceOperations {
		if op.OperationID == req.ReplacesOperationID && op.RoomIdentity == req.RoomIdentity && op.revisable() {
			replacement = true
		}
	}
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	if !replacement && (svc == nil || svc.closing.Load()) {
		m.mu.Unlock()
		return ErrIdentity
	}
	entry, err := m.workspaceRoom(req.RoomIdentity)
	var saved managedRoomConfig
	if err == nil {
		saved, err = m.readWorkspaceRoomConfig(entry)
	}
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if saved.WorkspaceVersion != req.ExpectedVersion {
		m.mu.Unlock()
		return ErrConflict
	}
	resolved, err := m.resolveWorkspaceConnectionLocked(req.RoomIdentity, req.Target)
	if err == nil && req.ConnectionVersion != resolved.record.Version {
		err = ErrConflict
	}
	verify := m.platformProject
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if verify == nil {
		verify = verifyGitLabProject
	}
	projectID, err := verify(ctx, resolved.record.URL, req.Target.Binding.Project, resolved.secret)
	if err != nil || projectID <= 0 {
		return fmt.Errorf("%w: project access or identity verification failed", ErrRequest)
	}
	if req.Target.Binding.ProjectID != 0 && req.Target.Binding.ProjectID != projectID || saved.Workspace != nil && saved.Workspace.Binding != nil && saved.Workspace.Binding.ConnectionID != "" && saved.Workspace.Binding.ProjectID != projectID {
		return ErrConflict
	}
	resolved.projectID = projectID
	req.connection = &resolved
	return nil
}

func workspaceConnectionTools(b workspaceBinding) string {
	return filepath.Join("resources", "platforms", "gitlab-connections", b.ConnectionID, b.ConnectionGeneration)
}

// Go-owned subprocesses receive a short-lived value only through their process
// environment. It is never added to prepared ToolEnv or a Session configuration.
func (m *roomManager) connectionExecute(owner RoomIdentity, w workspaceConfig, run platform.Execute) platform.Execute {
	return func(ctx context.Context, dir string, env map[string]string, executable string, args ...string) ([]byte, error) {
		m.mu.Lock()
		resolved, err := m.resolveWorkspaceConnectionLocked(owner, w)
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
		env = cloneToolEnv(env)
		env["TEAMX_PLATFORM_TOKEN"] = resolved.secret.value
		out, err := run(ctx, dir, env, executable, args...)
		m.mu.Lock()
		currentErr := m.checkProjectConnectionLocked(resolved)
		m.mu.Unlock()
		if currentErr != nil {
			return nil, ErrConflict
		}
		return out, err
	}
}

// This private tool exchange is intentionally separate from ordinary management
// APIs. Browser requests and humans cannot use it. No caller supplies a URL,
// connection ID or credential ID: all are derived from the owning Room snapshot.
func (m *roomManager) serveWorkspaceToolCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Mode") != "" || r.Header.Get("X-TeamX-Tool") != "credential" {
		writeAPIError(w, ErrIdentity)
		return
	}
	var req struct {
		ServiceInfo
		Caller Caller `json:"caller"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, rt := m.runtimeServiceLocked(req.RoomIdentity)
	if m.closing || m.busy || rt != nil && rt.busy && !rt.resuming || s == nil || s.closing.Load() || s.restarting.Load() || s.info != req.ServiceInfo || req.Caller.Kind != "agent" {
		writeAPIError(w, ErrIdentity)
		return
	}
	list, err := s.room.Call[MemberList](r.Context(), ListMembers{s.info.RoomIdentity})
	if err != nil {
		writeAPIError(w, ErrIdentity)
		return
	}
	found := false
	for _, member := range list.Members {
		if member.AgentID == req.Caller.ID && member.MembershipID == req.Caller.MembershipID {
			found = true
		}
	}
	entry, err := m.workspaceRoom(req.RoomIdentity)
	if !found || err != nil || m.checkWorkspaceOperations(req.RoomIdentity) != nil {
		writeAPIError(w, ErrIdentity)
		return
	}
	saved, err := m.readWorkspaceRoomConfig(entry)
	if err != nil || saved.Workspace == nil {
		writeAPIError(w, ErrIdentity)
		return
	}
	resolved, err := m.resolveWorkspaceConnectionLocked(req.RoomIdentity, *saved.Workspace)
	if err != nil {
		writeAPIError(w, ErrIdentity)
		return
	}
	// Explicit private wire DTO: workspaceSecret remains redacted everywhere else.
	_ = json.NewEncoder(w).Encode(struct {
		Token        string `json:"token"`
		URL          string `json:"url"`
		Project      string `json:"project"`
		ProjectID    int64  `json:"project_id"`
		ConnectionID string `json:"connection_id"`
		Generation   string `json:"connection_generation"`
	}{resolved.secret.value, resolved.record.URL, saved.Workspace.Binding.Project, saved.Workspace.Binding.ProjectID, resolved.record.ID, resolved.record.Generation})
}

func connectionFingerprint(secret workspaceSecret) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(secret.value)))
}

func bOwner(b gitlab.Binding) RoomIdentity {
	return RoomIdentity{RoomID: b.RoomID, RoomInstance: b.RoomInstance}
}
func (m *roomManager) gitLabBindingLocked(owner RoomIdentity, w workspaceConfig) (gitlab.Binding, error) {
	return m.gitLabProjectBindingLocked(owner, *w.Binding)
}
func (m *roomManager) gitLabProjectBindingLocked(owner RoomIdentity, b platform.ProjectBinding) (gitlab.Binding, error) {
	ref := gitlab.Binding{Version: 1, RoomID: owner.RoomID, RoomInstance: owner.RoomInstance, URL: b.URL, Project: b.Project, TokenEnv: b.TokenEnv, CredentialID: b.CredentialID}
	if b.ConnectionID != "" {
		resolved, err := m.resolveProjectConnectionLocked(owner, b)
		if err != nil {
			return ref, err
		}
		ref.ConnectionID, ref.ConnectionGeneration = b.ConnectionID, b.ConnectionGeneration
		ref.ConnectionVersion, ref.CredentialVersion, ref.CredentialFingerprint = resolved.record.Version, resolved.record.CredentialVersion, connectionFingerprint(resolved.secret)
		ref.ProjectID = b.ProjectID
	} else if b.CredentialID != "" {
		if _, err := m.readWorkspaceCredential(owner, b.CredentialID); err != nil {
			return ref, err
		}
		ref.CredentialFile = filepath.Join(m.store.dir, "credentials", b.CredentialID+".json")
	} else if !validWorkspacePAT(os.Getenv(b.TokenEnv)) {
		return ref, platform.ErrCredential
	}
	return ref, nil
}
