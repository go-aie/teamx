package room

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/registry"
)

// Internal resolution shares admission and cancellation with tools, but requires
// neither a CLI executable, private process environment nor Workspace directory.
func (m *roomManager) resolveGitlabFollowup(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(id.RoomIdentity)
	if svc == nil {
		m.mu.Unlock()
		return platform.TargetResolution{}, platform.AccessContext{}, ErrIdentity
	}
	req := toolsRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id), Tool: "gitlab"}
	platformID, err := m.sourcePlatformLocked(id.RoomIdentity, source)
	req.platformID = platformID
	if err != nil {
		m.mu.Unlock()
		return platform.TargetResolution{}, platform.AccessContext{}, err
	}
	admission, call, callCtx, err := m.preparePlatformCallLocked(ctx, req, false)
	m.mu.Unlock()
	if err != nil {
		return platform.TargetResolution{}, platform.AccessContext{}, err
	}
	if scope, _ := ctx.Value(referenceResolutionScopeKey{}).(*referenceResolutionScope); scope != nil {
		scope.ctx, scope.finish = callCtx, func() { m.finishPlatformCall(call) }
	} else {
		defer m.finishPlatformCall(call)
	}
	access := gitlabReferenceAccess(id, admission.account, source.Reference.RecipientID)
	check := func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.checkGitlabReferenceAccessLocked(callCtx, req, access)
	}
	read := m.gitlabAccountRead(id.RoomIdentity, admission.account, admission.secret)
	guarded := func(ctx context.Context, path string, q url.Values, h http.Header, limit int64) (gitlab.APIResponse, error) {
		if err := check(); err != nil {
			return gitlab.APIResponse{}, err
		}
		response, err := read(ctx, path, q, h, limit)
		if currentErr := check(); currentErr != nil {
			return gitlab.APIResponse{}, currentErr
		}
		return response, err
	}
	result, err := gitlab.ResolveTarget(callCtx, platform.TargetLocator{Source: &source}, access, guarded)
	if currentErr := check(); currentErr != nil {
		return platform.TargetResolution{}, access, currentErr
	}
	return result, access, err
}
func (m *roomManager) checkGitlabReferenceAccessLocked(ctx context.Context, req toolsRequest, access platform.AccessContext) error {
	if req.platformID != "" && req.platformID != access.PlatformID {
		return ErrIdentity
	}
	req.platformID = access.PlatformID
	var err error
	req, err = constrainToolRequest(ctx, req)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.checkToolMemberLocked(ctx, req); err != nil {
		return err
	}
	current, err := m.actionAccountLocked(req)
	if err != nil {
		return err
	}
	if gitlabReferenceAccess(MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}, current, access.Receiver.ID) != access {
		return ErrConflict
	}
	return nil
}

// Capability descriptions do not resolve credentials or issue HTTP requests.
func gitlabReferenceCapabilities() platform.ReferenceCapabilities {
	return platform.ReferenceCapabilities{CredentialKinds: []string{"gitlab:oauth"}, Actions: gitlab.ReferenceActions(nil, nil, "")}
}

func gitlabAccount(origin string, id platform.ExternalID) platform.AccountIdentity {
	return platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "gitlab", Instance: origin}, ID: id}
}

// Converts existing connection evidence at the GitLab boundary. Neither the new
// account contract nor access/cursor contracts require a username or numeric ID.
func gitlabReferenceAccess(id MemberIdentity, account registry.Account, receiver platform.ExternalID) platform.AccessContext {
	a := platform.AccessContext{
		RoomIdentity: id.RoomIdentity, AgentID: id.AgentID, MembershipID: id.MembershipID,
		Account:    gitlabAccount(account.URL, platform.ExternalID{Namespace: "gitlab:rest:id", Value: strconv.FormatInt(account.UserID, 10)}),
		PlatformID: account.PlatformID, Generation: account.Generation,
		PlatformVersion: account.PlatformVersion, CredentialVersion: account.CredentialVersion,
	}
	if receiver != (platform.ExternalID{}) {
		a.Receiver = gitlabAccount(account.URL, receiver)
	}
	return a
}
