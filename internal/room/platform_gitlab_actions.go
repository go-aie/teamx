package room

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/registry"
)

func (m *roomManager) prepareGitlabReferenceAction(ctx context.Context, id MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(id.RoomIdentity)
	if svc == nil {
		m.mu.Unlock()
		return referenceActionSession{}, ErrIdentity
	}
	req := toolsRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id), Tool: "gitlab", platformID: old.PlatformID}
	admission, call, callCtx, err := m.preparePlatformCallLocked(ctx, req, false)
	m.mu.Unlock()
	if err != nil {
		return referenceActionSession{}, err
	}
	access := gitlabReferenceAccess(id, admission.account, old.Receiver.ID)
	check := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.checkGitlabReferenceAccessLocked(callCtx, req, access)
	}
	requestContext := func(ctx context.Context) (context.Context, func()) {
		// Parent directly to the admitted call so binding revocation propagates
		// synchronously. Preserve the capability's shorter overall deadline.
		var merged context.Context
		var cancel context.CancelFunc
		if deadline, ok := ctx.Deadline(); ok {
			merged, cancel = context.WithDeadline(callCtx, deadline)
		} else {
			merged, cancel = context.WithCancel(callCtx)
		}
		stop := context.AfterFunc(ctx, cancel)
		if ctx.Err() != nil {
			cancel()
		}
		return merged, func() { stop(); cancel() }
	}
	read := m.gitlabAccountRead(id.RoomIdentity, admission.account, admission.secret)
	guardedRead := func(ctx context.Context, path string, q url.Values, h http.Header, limit int64) (gitlab.APIResponse, error) {
		if err := check(ctx); err != nil {
			return gitlab.APIResponse{}, err
		}
		ctx, finish := requestContext(ctx)
		defer finish()
		response, err := read(ctx, path, q, h, limit)
		if currentErr := check(ctx); currentErr != nil {
			return gitlab.APIResponse{}, currentErr
		}
		return response, err
	}
	guardedWrite := func(ctx context.Context, project, iid, discussion, body string) (gitlab.APIResponse, error) {
		if check(ctx) != nil {
			return gitlab.APIResponse{}, gitlab.ErrNoteNotSent
		}
		ctx, finish := requestContext(ctx)
		defer finish()
		// Never discard a known write outcome after revocation. Further requests
		// still pass the guard; the original action retains the admitted account.
		useCtx, credential, release, err := m.acquireGitLabAccess(ctx, id.RoomIdentity, admission.account, admission.secret, 30*time.Second)
		if err != nil {
			return gitlab.APIResponse{}, gitlab.ErrNoteNotSent
		}
		response, writeErr := gitlab.NewNoteAPIWithCredential(admission.account.URL, credential, m.platformHTTP)(useCtx, project, iid, discussion, body)
		_ = release() // preserve the known write outcome even when authorization changed
		return response, writeErr
	}
	caps := platform.ReferenceCapabilities{Resolve: func(ctx context.Context, l platform.TargetLocator, a platform.AccessContext) (platform.TargetResolution, error) {
		return gitlab.ResolveStoredActionTarget(ctx, l, a, guardedRead, admission.account.Username)
	}, CredentialKinds: []string{"gitlab:oauth"}, Actions: gitlab.ReferenceActions(guardedRead, guardedWrite, admission.account.Username)}
	return referenceActionSession{Access: access, Capabilities: caps, Check: check, Close: func() { m.finishPlatformCall(call) }}, nil
}

// Selected connection evidence is shared by internal capabilities and Agent tools.
func (m *roomManager) actionAccountLocked(req toolsRequest) (registry.Account, error) {
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err != nil {
		return registry.Account{}, err
	}
	file, err := m.platforms.Load()
	if err != nil {
		return registry.Account{}, err
	}
	b, d, err := m.resolveToolBindingLocked(f, req)
	if err != nil || d.Provider != "gitlab" {
		return registry.Account{}, ErrConflict
	}
	for _, c := range file.Platforms {
		if c.ID == b.PlatformID && c.Generation == b.PlatformGeneration && c.Provider == "gitlab" {
			a, _, err := registry.ResolveAccessAccount(file, c)
			return a, err
		}
	}
	return registry.Account{}, ErrConflict
}
