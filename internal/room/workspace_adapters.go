package room

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/scion"
	"github.com/go-aie/teamx/internal/workspace"
	workspacegit "github.com/go-aie/teamx/internal/workspace/git"
	workspacescion "github.com/go-aie/teamx/internal/workspace/scion"
)

// Backend selection and platform bridges live at the Room composition boundary.
// Kind-less snapshots preserve their existing Git meaning.
func workspaceRepositoryAdapter(run platform.Execute, w workspaceConfig) (workspace.Repository, error) {
	registry := workspace.Registry{"git": func(source workspace.Source) workspace.Repository {
		cloneURL := source.URL
		if w.Binding != nil {
			cloneURL = w.Binding.URL + "/" + w.Binding.Project + ".git"
		}
		return workspacegit.Adapter{Source: source, CloneURL: cloneURL, Run: workspace.Execute(run)}
	}}
	registry["scion"] = func(source workspace.Source) workspace.Repository {
		return workspacescion.Adapter{Source: source, Run: workspace.Execute(run)}
	}
	return registry.Open(w.Repository.Kind, workspace.Source{URL: w.Repository.URL, Branch: w.Repository.Branch})
}

type gitLabWorkspaceAccess struct {
	write          func(string, any) error
	connection     func(RoomIdentity, workspaceConfig) (projectPlatformResolution, error)
	connectionRead func(projectPlatformResolution) gitlab.ReadAPI
}

func (p gitLabWorkspaceAccess) prepare(ctx context.Context, dir string, saved managedRoomConfig, initial bool) (workspace.Access, error) {
	result := workspace.Access{Env: map[string]string{}}
	w := *saved.Workspace
	if w.Binding != nil {
		b := w.Binding
		ref := gitlab.Binding{Version: 1, RoomID: saved.RoomID, RoomInstance: saved.RoomInstance, URL: b.URL, Project: b.Project}
		if b.PlatformID != "" {
			if p.connection == nil {
				return result, ErrIdentity
			}
			resolved, err := p.connection(saved.RoomIdentity, w)
			if err != nil || resolved.record.Generation != b.PlatformGeneration {
				return result, ErrConflict
			}
			ref.PlatformID, ref.PlatformGeneration, ref.ProjectID = b.PlatformID, b.PlatformGeneration, b.ProjectID
		} else {
			return result, ErrIdentity
		}
		tools := "resources/platforms/gitlab"
		if b.PlatformID != "" {
			tools = filepath.ToSlash(workspacePlatformTools(*b))
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
		workspaceRead := p.read(saved.RoomIdentity, w)
		prepared, err := prepare(ctx, filepath.Join(dir, filepath.FromSlash(tools)), ref, initial, p.write, workspaceRead)
		if err != nil {
			return result, err
		}
		result.Env, result.Instructions = prepared.Env, prepared.Instructions
		// Frozen Room resources retain their original bytes. Update only the
		// supplied Agent guidance; this does not require a resource migration.
		result.Instructions = strings.ReplaceAll(result.Instructions, "tools run gitlab", "tools run ENTRY")
		result.Instructions = strings.ReplaceAll(result.Instructions, "tools skill gitlab", "tools skill ENTRY")
		result.Instructions += "\nENTRY 使用 tools list 返回的准确平台入口，不使用平台类型别名。\n"
		result.ResourceDir = filepath.Join(dir, filepath.FromSlash(tools))
		var userName, userEmail string
		result.Author = func(ctx context.Context) (string, string, error) {
			if userName == "" {
				var err error
				userName, userEmail, err = gitlab.User(ctx, workspaceRead)
				if err != nil {
					return "", "", err
				}
			}
			return userName, userEmail, nil
		}
	}
	return result, nil
}

func (p gitLabWorkspaceAccess) read(owner RoomIdentity, w workspaceConfig) gitlab.ReadAPI {
	return func(ctx context.Context, path string, q url.Values, h http.Header, limit int64) (gitlab.APIResponse, error) {
		if p.connection == nil || p.connectionRead == nil {
			return gitlab.APIResponse{}, ErrIdentity
		}
		current, err := p.connection(owner, w)
		if err != nil {
			return gitlab.APIResponse{}, err
		}
		return p.connectionRead(current)(ctx, path, q, h, limit)
	}
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
	p := workspacePreparer{run: runPreparation, repository: workspaceRepositoryAdapter,
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
	if saved.Workspace != nil && saved.Workspace.Binding != nil && saved.Workspace.Binding.Provider == "scion" {
		p.run = runScionPreparation
		if m.workspaceRun != nil {
			p.run = m.workspaceRun
		}
		p.run = m.scionWorkspaceExecute(saved.RoomIdentity, *saved.Workspace, p.run)
		p.access = func(ctx context.Context, _ string, _ managedRoomConfig, initial bool) (workspace.Access, error) {
			if initial {
				w := *saved.Workspace
				b := w.Binding
				branch, err := scion.VerifyRepository(ctx, p.run, b.Project, w.Repository.Branch)
				if err != nil || branch != w.Repository.Branch {
					return workspace.Access{}, ErrConflict
				}
			}
			return workspace.Access{Env: map[string]string{}, Instructions: "当前 Workspace 是原生 Scion 工作副本。先运行 tools list，使用返回的准确平台入口执行 tools skill ENTRY 读取协议；日常命令使用成员显式启用的 Scion 平台。提交时显式提供 --author 'Name <email>'；提交作者与平台操作账号分别核对。"}, nil
		}
		return p.ensure(ctx, m.roomDir(saved.RoomID), saved, initial)
	}
	if m.workspaceRun != nil {
		p.run = m.workspaceRun
	}
	access := gitLabWorkspaceAccess{write: p.write}
	if saved.Workspace != nil && saved.Workspace.Binding != nil && saved.Workspace.Binding.PlatformID != "" {
		access.connection = func(owner RoomIdentity, w workspaceConfig) (projectPlatformResolution, error) {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.resolveWorkspacePlatformLocked(owner, w)
		}
		access.connectionRead = m.projectPlatformRead
		p.run = m.connectionExecute(saved.RoomIdentity, *saved.Workspace, p.run)
	}
	p.access = access.prepare
	result, err := p.ensure(ctx, m.roomDir(saved.RoomID), saved, initial)
	if err == nil && access.connection != nil {
		resolved, resolveErr := access.connection(saved.RoomIdentity, *saved.Workspace)
		if resolveErr != nil {
			return preparedWorkspaces{}, resolveErr
		}
		if resolved.account.Authorization.CredentialID != "" {
			result.Instructions += "\nOAuth 仓库网络操作使用 \"$TEAMX_CLI\" workspace-git fetch|push|ls-remote [分支或引用]，由服务限定当前仓库并管理短期凭据。不要使用普通 git fetch/push 的 askpass；本地 diff/commit 继续用 git。需要 write_repository 授权；每次最多运行 5 分钟，中断后先核对远端结果。\n"
		}
	}
	return result, err
}
