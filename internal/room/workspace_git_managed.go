package room

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func requireGitlabRepositoryScope(a connection.Account) error {
	if a.Authorization.CredentialID != "" && !strings.Contains(" "+a.Authorization.Scopes+" ", " write_repository ") {
		return fmt.Errorf("%w: Git over HTTPS requires explicit write_repository authorization; import an application with api and write_repository and log in again", ErrRequest)
	}
	return nil
}

// Only network Git operations use the service. Local diff/commit stays local.
// Derive both directory and remote from the saved Workspace, never agent input.
func (m *roomManager) prepareWorkspaceGitLocked(ctx context.Context, req toolsRequest) (tool.Process, *platformCall, context.Context, error) {
	var p tool.Process
	if err := m.checkToolMemberLocked(ctx, req); err != nil {
		return p, nil, nil, err
	}
	if m.checkWorkspaceOperations(req.RoomIdentity) != nil || len(req.Args) == 0 {
		return p, nil, nil, ErrBusy
	}
	command := req.Args[0]
	if command != "fetch" && command != "push" && command != "ls-remote" {
		return p, nil, nil, ErrRequest
	}
	for _, arg := range req.Args[1:] {
		if arg == "" || strings.ContainsAny(arg, " \t\r\n\\:") || strings.HasPrefix(arg, "-") && arg != "--prune" && arg != "--tags" && arg != "--dry-run" && arg != "--force-with-lease" && arg != "--delete" && arg != "--heads" {
			return p, nil, nil, ErrRequest
		}
	}
	entry, err := m.workspaceRoom(req.RoomIdentity)
	if err != nil {
		return p, nil, nil, err
	}
	saved, err := m.readWorkspaceRoomConfig(entry)
	if err != nil || saved.Workspace == nil || saved.Workspace.Binding == nil || saved.Workspace.Repository == nil || saved.Workspace.Binding.ConnectionID == "" {
		return p, nil, nil, ErrRequest
	}
	resolved, err := m.resolveWorkspaceConnectionLocked(req.RoomIdentity, *saved.Workspace)
	if err != nil {
		return p, nil, nil, err
	}
	account, secret, err := m.resolvePlatformAccountLocked(req.RoomIdentity, resolved.record.ID, "gitlab")
	if err != nil {
		return p, nil, nil, err
	}
	if err := requireGitlabRepositoryScope(account); err != nil {
		return p, nil, nil, err
	}
	for _, member := range saved.Members {
		if member.AgentID == req.Caller.ID && member.MembershipID == req.Caller.MembershipID {
			if member.Workspace == "" {
				return p, nil, nil, ErrRequest
			}
			if err := checkWorkspaceDirectory(m.roomDir(req.RoomID), member.Workspace); err != nil {
				return p, nil, nil, err
			}
			p.Directory = filepath.Join(m.roomDir(req.RoomID), filepath.FromSlash(member.Workspace))
		}
	}
	if p.Directory == "" {
		return p, nil, nil, ErrIdentity
	}
	cli, err := os.Executable()
	if err != nil {
		return p, nil, nil, ErrRequest
	}
	tools := workspaceConnectionTools(*saved.Workspace.Binding)
	if saved.PlatformToolsVersion > 0 {
		tools = filepath.Join(tools, "git-only")
	}
	p.Definition = tool.Definition{Name: "workspace-git", Executable: "git", Environment: map[string]string{"TEAMX_PLATFORM_TOKEN": "token", "TEAMX_GITLAB_TOOLS": "tools", "GIT_ASKPASS": "askpass", "GIT_TERMINAL_PROMPT": "no_prompt"}}
	p.Values = map[string]string{"token": secret.Reveal(), "tools": filepath.Join(m.roomDir(req.RoomID), tools), "askpass": cli, "no_prompt": "0"}
	// Override local credential helpers and redirect settings; Git's askpass checks
	// the precise saved repository before answering a password prompt.
	req.Args = append([]string{"-c", "credential.helper=", "-c", "http.followRedirects=false", command, resolved.record.URL + "/" + saved.Workspace.Binding.Project + ".git"}, req.Args[1:]...)
	// Args are carried privately on the process rather than altering caller intent.
	p.Args = req.Args
	callCtx, cancel := context.WithCancel(ctx)
	call := &platformCall{owner: req.RoomIdentity, agentID: req.Caller.ID, membershipID: req.Caller.MembershipID, tool: "workspace-git", connectionID: account.ConnectionID, account: account, secret: secret, cancel: cancel, done: make(chan struct{})}
	if m.platformCalls == nil {
		m.platformCalls = map[*platformCall]bool{}
	}
	m.platformCalls[call] = true
	return p, call, callCtx, nil
}
