package room

import (
	"context"
	"net/http"
	"net/url"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func (m *roomManager) prepareGitlabReferenceAction(ctx context.Context, id MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(id.RoomIdentity)
	if svc == nil {
		m.mu.Unlock()
		return referenceActionSession{}, ErrIdentity
	}
	req := toolsRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id), Tool: "gitlab"}
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
	read := gitlab.NewReadAPI(admission.account.URL, admission.secret, m.platformHTTP)
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
	write := gitlab.NewNoteAPI(admission.account.URL, admission.secret, m.platformHTTP)
	guardedWrite := func(ctx context.Context, project, iid, discussion, body string) (gitlab.APIResponse, error) {
		if check(ctx) != nil {
			return gitlab.APIResponse{}, gitlab.ErrNoteNotSent
		}
		ctx, finish := requestContext(ctx)
		defer finish()
		// Never discard a known write outcome after revocation. Further requests
		// still pass the guard; the original action retains the admitted account.
		return write(ctx, project, iid, discussion, body)
	}
	caps := platform.ReferenceCapabilities{CredentialKinds: []string{"gitlab:pat"}, Actions: gitlab.ReferenceActions(guardedRead, guardedWrite, admission.account.Username)}
	return referenceActionSession{Access: access, Capabilities: caps, Check: check, Close: func() { m.finishPlatformCall(call) }}, nil
}

// Selected connection evidence is shared by internal capabilities and Agent tools.
func (m *roomManager) actionAccountLocked(req toolsRequest) (connection.Account, error) {
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err != nil {
		return connection.Account{}, err
	}
	file, err := m.connections.Load()
	if err != nil {
		return connection.Account{}, err
	}
	for _, b := range f.Bindings {
		if b.Tool != "gitlab" || b.AgentID != req.Caller.ID || b.MembershipID != req.Caller.MembershipID {
			continue
		}
		for _, c := range file.Connections {
			if c.ID == b.ConnectionID && c.Generation == b.ConnectionGeneration && c.Provider == "gitlab" {
				a, _, err := connection.ResolveAccount(file, c)
				return a, err
			}
		}
	}
	return connection.Account{}, ErrConflict
}
