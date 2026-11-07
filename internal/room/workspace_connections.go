package room

import (
	"context"
	"fmt"
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
	projectID, err := m.verifyGitlabAccountProject(ctx, req.RoomIdentity, resolved.account, platform.Secret{}, req.Target.Binding.Project, verify)
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
	return func(ctx context.Context, dir string, env map[string]string, executable string, args ...string) (out []byte, err error) {
		m.mu.Lock()
		resolved, err := m.resolveWorkspaceConnectionLocked(owner, w)
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
		env = cloneToolEnv(env)
		delete(env, "TEAMX_PLATFORM_TOKEN")
		if !workspaceGitNetwork(executable, args) {
			// Revisit and local Git configuration must never refresh a remote grant.
			out, err = run(ctx, dir, env, executable, args...)
			m.mu.Lock()
			checkErr := m.checkProjectConnectionLocked(resolved)
			m.mu.Unlock()
			if checkErr != nil {
				return nil, checkErr
			}
			return out, err
		}
		if err := requireGitlabRepositoryScope(resolved.account); err != nil {
			return nil, err
		}
		useCtx, credential, finish, err := m.acquireGitLabAccess(ctx, owner, resolved.account, platform.Secret{}, maxGitLabTokenUse)
		if err != nil {
			return nil, err
		}
		defer func() {
			if checkErr := finish(); checkErr != nil {
				out = nil
				err = checkErr
			}
		}()
		env["TEAMX_PLATFORM_TOKEN"] = credential.Token.Reveal()
		return run(useCtx, dir, env, executable, args...)
	}
}

// Preparation owns these argv; only its network commands receive access material.
func workspaceGitNetwork(executable string, args []string) bool {
	if executable != "git" {
		return false
	}
	for len(args) >= 2 && args[0] == "-c" {
		args = args[2:]
	}
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "clone", "fetch", "push", "ls-remote":
		return true
	}
	return false
}

func bOwner(b gitlab.Binding) RoomIdentity {
	return RoomIdentity{RoomID: b.RoomID, RoomInstance: b.RoomInstance}
}
func (m *roomManager) gitLabBindingLocked(owner RoomIdentity, w workspaceConfig) (gitlab.Binding, error) {
	return m.gitLabProjectBindingLocked(owner, *w.Binding)
}
func (m *roomManager) gitLabProjectBindingLocked(owner RoomIdentity, b platform.ProjectBinding) (gitlab.Binding, error) {
	ref := gitlab.Binding{Version: 1, RoomID: owner.RoomID, RoomInstance: owner.RoomInstance, URL: b.URL, Project: b.Project}
	if b.ConnectionID != "" {
		resolved, err := m.resolveProjectConnectionLocked(owner, b)
		if err != nil {
			return ref, err
		}
		ref.ConnectionID, ref.ConnectionGeneration = b.ConnectionID, b.ConnectionGeneration
		ref.ConnectionVersion, ref.CredentialVersion, ref.CredentialFingerprint = resolved.record.Version, resolved.record.CredentialVersion, resolved.fingerprint()
		ref.ProjectID = b.ProjectID
	} else {
		return ref, ErrRequest
	}
	return ref, nil
}

func (r projectConnectionResolution) fingerprint() string { return r.account.CredentialFingerprint }
func (m *roomManager) projectConnectionRead(r projectConnectionResolution) gitlab.ReadAPI {
	return m.gitlabAccountRead(r.owner, r.account, platform.Secret{})
}
