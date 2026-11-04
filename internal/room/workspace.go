package room

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

type preparedWorkspaces struct {
	ToolEnv      map[string]string
	Instructions string
}

type workspacePreparer struct {
	run           platform.Execute
	transport     http.RoundTripper
	write         func(string, any) error
	remove        func(string) error
	credential    func(RoomIdentity, string) (workspaceSecret, error)
	connection    func(RoomIdentity, workspaceConfig) (projectConnectionResolution, error)
	credentialDir string
}

// Internal capability only. The management application in step 3 must establish
// durable ownership and settle Sessions before using initial=true. Preparation
// never edits effective room/member/Session snapshots or enables scheduling.
func (m *roomManager) ensureWorkspaces(ctx context.Context, saved managedRoomConfig, initial bool) (preparedWorkspaces, error) {
	m.mu.Lock()
	_, ownerErr := m.workspaceRoom(saved.RoomIdentity)
	storeErr := m.store.check(true)
	closing := m.closing
	m.mu.Unlock()
	if ownerErr != nil {
		return preparedWorkspaces{}, ownerErr
	}
	if storeErr != nil {
		return preparedWorkspaces{}, storeErr
	}
	if closing {
		return preparedWorkspaces{}, ErrClosing
	}
	p := workspacePreparer{run: runPreparation, transport: m.platformHTTP, credential: m.loadWorkspaceCredential, credentialDir: filepath.Join(m.store.dir, "credentials"),
		write: func(name string, v any) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.closing {
				return ErrClosing
			}
			if err := m.store.check(true); err != nil {
				return err
			}
			return m.store.save(name, v, maxPrivateFileBytes)
		}, remove: func(name string) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			if err := m.store.check(true); err != nil {
				return err
			}
			if err := os.Remove(name); err != nil {
				m.store.uncertain = true
				return ErrCommitUnknown
			}
			if err := m.store.ops.syncDir(filepath.Dir(name)); err != nil {
				m.store.uncertain = true
				return ErrCommitUnknown
			}
			return nil
		}}
	if m.workspaceRun != nil {
		p.run = m.workspaceRun
	}
	if saved.Workspace != nil && saved.Workspace.Binding != nil && saved.Workspace.Binding.ConnectionID != "" {
		p.connection = func(owner RoomIdentity, w workspaceConfig) (projectConnectionResolution, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.resolveWorkspaceConnectionLocked(owner, w)
		}
		p.run = m.connectionExecute(saved.RoomIdentity, *saved.Workspace, p.run)
	}
	return p.ensure(ctx, m.roomDir(saved.RoomID), saved, initial)
}

func (p workspacePreparer) ensure(ctx context.Context, dir string, saved managedRoomConfig, initial bool) (preparedWorkspaces, error) {
	result := preparedWorkspaces{ToolEnv: map[string]string{}}
	if err := validateWorkspaceSnapshot(saved); err != nil {
		return result, err
	}
	w := *saved.Workspace
	cloneURL := ""
	if w.Repository != nil {
		cloneURL = w.Repository.URL
	}
	if w.Binding != nil {
		// The GitLab project check verifies this clone URL. A browser URL without
		// .git redirects, which Git intentionally refuses during preparation.
		cloneURL = w.Binding.URL + "/" + w.Binding.Project + ".git"
	}
	// Check every target before mutating any directory or querying the platform.
	exists := make([]bool, len(saved.Members))
	for i, member := range saved.Members {
		if !safeMembershipID(member.MembershipID) {
			return result, ErrIdentity
		}
		for _, other := range saved.Members[:i] {
			if workspacePathsOverlap(member.Workspace, other.Workspace) {
				return result, ErrInvalid
			}
		}
		if err := checkWorkspaceDirectory(dir, member.Workspace); err != nil {
			return result, err
		}
		workspace := filepath.Join(dir, filepath.FromSlash(member.Workspace))
		if w.Repository != nil {
			if _, err := os.Lstat(workspace + ".clone-pending"); !errors.Is(err, os.ErrNotExist) {
				return result, errors.New("incomplete workspace clone; preserve and inspect target")
			}
			var err error
			exists[i], err = p.checkClone(ctx, workspace, w.Repository.URL, initial, cloneURL)
			if err != nil {
				return result, err
			}
		}
	}
	var workspaceRead gitlab.ReadAPI
	if w.Binding != nil {
		b := w.Binding
		ref := gitlab.Binding{Version: 1, RoomID: saved.RoomID, RoomInstance: saved.RoomInstance, URL: b.URL, Project: b.Project, TokenEnv: b.TokenEnv, CredentialID: b.CredentialID}
		if b.ConnectionID != "" {
			if p.connection == nil {
				return result, ErrIdentity
			}
			resolved, err := p.connection(saved.RoomIdentity, w)
			if err != nil || resolved.record.Generation != b.ConnectionGeneration {
				return result, ErrConflict
			}
			ref.ConnectionID, ref.ConnectionGeneration, ref.ProjectID = b.ConnectionID, b.ConnectionGeneration, b.ProjectID
		} else if b.CredentialID != "" {
			if p.credential == nil {
				return result, ErrIdentity
			}
			if _, err := p.credential(saved.RoomIdentity, b.CredentialID); err != nil {
				return result, err
			}
			ref.CredentialFile = filepath.Join(p.credentialDir, b.CredentialID+".json")
		} else if initial && !validWorkspacePAT(os.Getenv(b.TokenEnv)) {
			return result, errors.New("workspace credential is missing or invalid")
		}
		tools := "resources/platforms/gitlab"
		if b.ConnectionID != "" {
			tools = filepath.ToSlash(workspaceConnectionTools(*b))
		}
		if saved.PlatformToolsVersion > 0 {
			tools += "/git-only"
		}
		if err := checkWorkspaceDirectory(dir, tools); err != nil {
			return result, err
		}
		prepare := gitlab.Prepare
		if saved.PlatformToolsVersion > 0 {
			prepare = gitlab.PrepareGit
		}
		workspaceRead = p.gitLabRead(saved.RoomIdentity, w, ref)
		prepared, err := prepare(ctx, filepath.Join(dir, filepath.FromSlash(tools)), ref, initial, p.write, workspaceRead)
		if err != nil {
			return result, err
		}
		result.ToolEnv, result.Instructions = prepared.Env, prepared.Instructions
	}
	var userName, userEmail string
	for i, member := range saved.Members {
		if err := ctx.Err(); err != nil {
			return result, errors.New("workspace preparation cancelled")
		}
		workspace := filepath.Join(dir, filepath.FromSlash(member.Workspace))
		if w.Repository == nil {
			if err := os.MkdirAll(workspace, 0700); err != nil {
				return result, err
			}
			continue
		}
		env := result.ToolEnv
		if !exists[i] {
			if err := os.MkdirAll(filepath.Dir(workspace), 0700); err != nil {
				return result, err
			}
			marker := workspace + ".clone-pending"
			// The marker survives failed clone commands, including Git's own removal of
			// an incomplete .git directory. Explicit retry cannot silently overwrite it.
			if err := p.write(marker, struct {
				URL string `json:"url"`
			}{w.Repository.URL}); err != nil {
				return result, err
			}
			args := []string{"clone"}
			if w.Repository.Branch != "" {
				args = append(args, "--branch", w.Repository.Branch)
			}
			args = append(args, "--", cloneURL, workspace)
			if _, err := p.git(ctx, dir, env, args...); err != nil {
				return result, err
			}
			if _, err := p.checkClone(ctx, workspace, w.Repository.URL, false, cloneURL); err != nil {
				return result, err
			}
			if err := p.remove(marker); err != nil {
				return result, err
			}
		}
		if w.Binding == nil {
			continue
		}
		// Authentication is room-specific. These contain references only, never PAT.
		if initial {
			for _, kv := range [][2]string{{"credential.helper", ""}, {"core.askPass", env["GIT_ASKPASS"]}} {
				if _, err := p.git(ctx, workspace, env, "config", "--local", "--replace-all", kv[0], kv[1]); err != nil {
					return result, err
				}
			}
		}
		if initial {
			if _, err := p.git(ctx, workspace, env, "config", "--local", "--replace-all", "credential.useHttpPath", "true"); err != nil {
				return result, err
			}
		} else if w.Binding.ConnectionID != "" {
			value, err := p.git(ctx, workspace, env, "config", "--local", "--get", "credential.useHttpPath")
			if err != nil || string(value) != "true" {
				return result, errors.New("Git credential path scope differs from connection binding")
			}
		}

		if !initial {
			askpass, err := p.git(ctx, workspace, env, "config", "--local", "--get", "core.askPass")
			if err != nil || string(askpass) != env["GIT_ASKPASS"] && string(askpass) != filepath.Join(env["TEAMX_GITLAB_TOOLS"], "git-askpass.sh") {
				return result, errors.New("Git askpass differs from room binding; explicit application required")
			}
			helpers, err := p.git(ctx, workspace, env, "config", "--local", "--get-all", "credential.helper")
			if err != nil || len(helpers) != 0 {
				return result, errors.New("Git credential helpers differ from room binding; explicit application required")
			}
		}
		name, err := p.git(ctx, workspace, env, "config", "--get", "user.name")
		if err != nil {
			if !missingGitConfig(err) {
				return result, err
			}
			name = nil
		}
		email, err := p.git(ctx, workspace, env, "config", "--get", "user.email")
		if err != nil {
			if !missingGitConfig(err) {
				return result, err
			}
			email = nil
		}
		if len(name) > 0 && len(email) > 0 {
			continue
		}
		if !initial {
			return result, errors.New("Git author missing; explicit workspace application required")
		}
		if userName == "" {
			var err error
			userName, userEmail, err = gitlab.User(ctx, workspaceRead)
			if err != nil {
				return result, err
			}
			if !validText(userName) || !validText(userEmail) || strings.ContainsAny(userName+userEmail, "\r\n\x00") {
				return result, ErrInvalid
			}
		}
		for _, kv := range [][2]string{{"user.name", userName}, {"user.email", userEmail}} {
			if kv[0] == "user.name" && len(name) > 0 || kv[0] == "user.email" && len(email) > 0 {
				continue
			}
			if _, err := p.git(ctx, workspace, env, "config", "--local", "--replace-all", kv[0], kv[1]); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

func (p workspacePreparer) git(ctx context.Context, dir string, env map[string]string, args ...string) ([]byte, error) {
	prefix := []string{"-c", "http.followRedirects=false"}
	if env["TEAMX_GITLAB_TOOLS"] != "" {
		prefix = append(prefix, "-c", "credential.helper=", "-c", "credential.useHttpPath=true")
	}
	return p.run(ctx, dir, env, "git", append(prefix, args...)...)
}

func (p workspacePreparer) checkClone(ctx context.Context, workspace, url string, initial bool, equivalentURLs ...string) (bool, error) {
	matchesURL := func(value []byte) bool {
		return string(value) == url || slices.Contains(equivalentURLs, string(value))
	}
	entries, err := os.ReadDir(workspace)
	if errors.Is(err, os.ErrNotExist) || err == nil && len(entries) == 0 {
		if initial {
			return false, nil
		}
		return false, errors.New("member clone is missing; explicit workspace application required")
	}
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(filepath.Join(workspace, ".git"))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("nonempty workspace is not a managed clone; preserve and inspect it")
	}
	// Reject symlinked Git metadata before running commands that might mutate it.
	err = filepath.WalkDir(filepath.Join(workspace, ".git"), func(_ string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.Type()&os.ModeSymlink != 0 {
			return ErrInvalid
		}
		return nil
	})
	if err != nil {
		return false, errors.New("unsafe Git metadata; preserve and inspect it")
	}
	top, err := p.git(ctx, workspace, nil, "rev-parse", "--show-toplevel")
	actual, _ := filepath.EvalSymlinks(workspace)
	if err != nil || string(top) != actual {
		return false, errors.New("incomplete clone; preserve and inspect it")
	}
	origin, err := p.git(ctx, workspace, nil, "config", "--local", "--get-all", "remote.origin.url")
	if err != nil || !matchesURL(origin) {
		return false, errors.New("workspace origin mismatch; preserve and inspect it")
	}
	// Also check effective URL rewriting locally; do not contact the remote.
	origin, err = p.git(ctx, workspace, nil, "remote", "get-url", "--all", "origin")
	if err != nil || !matchesURL(origin) {
		return false, errors.New("workspace effective origin mismatch; preserve and inspect it")
	}
	push, err := p.git(ctx, workspace, nil, "remote", "get-url", "--push", "--all", "origin")
	if err != nil || !matchesURL(push) {
		return false, errors.New("workspace push origin mismatch; preserve and inspect it")
	}
	return true, nil
}

func missingGitConfig(err error) bool {
	var exit preparationExitError
	return errors.As(err, &exit) && exit.code == 1
}

// Resolve each read independently and recheck shared connection identity after
// the complete HTTP response; credentials never enter prepared member state.
func (p workspacePreparer) gitLabRead(owner RoomIdentity, w workspaceConfig, b gitlab.Binding) gitlab.ReadAPI {
	return func(ctx context.Context, path string, q url.Values, h http.Header, limit int64) (gitlab.APIResponse, error) {
		var secret platform.Secret
		var admitted projectConnectionResolution
		if b.ConnectionID != "" {
			current, err := p.connection(owner, w)
			if err != nil {
				return gitlab.APIResponse{}, err
			}
			admitted = current
			secret = platform.NewSecret(current.secret.value)
		} else if b.CredentialID != "" {
			value, err := p.credential(owner, b.CredentialID)
			if err != nil {
				return gitlab.APIResponse{}, err
			}
			secret = platform.NewSecret(value.value)
		} else {
			secret = platform.NewSecret(os.Getenv(b.TokenEnv))
		}
		response, err := gitlab.NewReadAPI(b.URL, secret, p.transport)(ctx, path, q, h, limit)
		if b.ConnectionID != "" {
			current, e := p.connection(owner, w)
			// Reloading allocates a new IdentityBinding pointer; compare its contents.
			if e != nil || !reflect.DeepEqual(current.record, admitted.record) || current.secret.value != admitted.secret.value {
				return gitlab.APIResponse{}, ErrConflict
			}
		}
		return response, err
	}
}
