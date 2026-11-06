package room

import (
	"context"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/lark"
)

func larkReferenceAccess(id MemberIdentity, account connection.Account, source platform.SourceRef) platform.AccessContext {
	a := platform.AccessContext{RoomIdentity: id.RoomIdentity, AgentID: id.AgentID, MembershipID: id.MembershipID, Account: account.Identity, ConnectionID: account.ConnectionID, Generation: account.Generation, ConnectionVersion: account.ConnectionVersion, CredentialVersion: account.CredentialVersion}
	if source.Reference.RecipientID.Valid() {
		a.Receiver = platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: source.Instance, Tenant: source.Reference.AccountTenant}, ID: source.Reference.RecipientID}
	}
	return a
}
func (m *roomManager) checkLarkReferenceAccessLocked(ctx context.Context, req toolsRequest, access platform.AccessContext) error {
	a, _, err := m.resolvePlatformAccountLocked(req.RoomIdentity, access.ConnectionID, "lark")
	if err != nil {
		return err
	}
	if a.Identity != access.Account || a.Generation != access.Generation || a.ConnectionVersion != access.ConnectionVersion || a.CredentialVersion != access.CredentialVersion {
		return ErrConflict
	}
	return m.checkLarkReadLocked(ctx, req, a)
}
func (m *roomManager) resolveLarkFollowup(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(id.RoomIdentity)
	if svc == nil {
		m.mu.Unlock()
		return platform.TargetResolution{}, platform.AccessContext{}, ErrIdentity
	}
	req := toolsRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id), Tool: "lark"}
	admission, call, callCtx, err := m.preparePlatformCallLocked(ctx, req, false)
	m.mu.Unlock()
	if err != nil {
		return platform.TargetResolution{}, platform.AccessContext{}, err
	}
	if scope, _ := ctx.Value(referenceResolutionScopeKey{}).(*referenceResolutionScope); scope != nil {
		scope.ctx = callCtx
		scope.finish = func() { m.finishPlatformCall(call) }
	} else {
		defer m.finishPlatformCall(call)
	}
	access := larkReferenceAccess(id, admission.account, source)
	if admission.account.CLIProfile == nil {
		return platform.TargetResolution{}, access, ErrConflict
	}
	reader, err := lark.New(*admission.account.CLIProfile)
	if err != nil {
		return platform.TargetResolution{}, access, err
	}
	result, err := lark.ResolveTarget(callCtx, platform.TargetLocator{Source: &source}, access, reader)
	m.mu.Lock()
	check := m.checkLarkReferenceAccessLocked(callCtx, req, access)
	m.mu.Unlock()
	if check != nil {
		return platform.TargetResolution{}, access, check
	}
	return result, access, err
}
