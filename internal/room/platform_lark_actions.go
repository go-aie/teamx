package room

import (
	"context"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

func (m *roomManager) prepareLarkReferenceAction(ctx context.Context, id MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
	return m.prepareLarkReferenceActions(ctx, id, old, lark.AcceptedActionCapabilities)
}

// Acceptance can register this same managed implementation on an isolated
// test manager. This cannot change the production registry or its accepted modes.
func (m *roomManager) prepareLarkReferenceActionWithReadiness(ctx context.Context, id MemberIdentity, old platform.AccessContext, replyDisabled, sendDisabled string) (referenceActionSession, error) {
	return m.prepareLarkReferenceActions(ctx, id, old, func(reader lark.SendReader, reply lark.ReplyWriter, send lark.SendWriter) platform.ReferenceCapabilities {
		return lark.ActionCapabilities(reader, reply, send, replyDisabled, sendDisabled)
	})
}

func (m *roomManager) prepareLarkReferenceActions(ctx context.Context, id MemberIdentity, old platform.AccessContext, capabilities func(lark.SendReader, lark.ReplyWriter, lark.SendWriter) platform.ReferenceCapabilities) (referenceActionSession, error) {
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(id.RoomIdentity)
	if svc == nil {
		m.mu.Unlock()
		return referenceActionSession{}, ErrIdentity
	}
	req := toolsRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id), Tool: "lark", platformID: old.PlatformID}
	admission, call, callCtx, err := m.preparePlatformCallLocked(ctx, req, false)
	m.mu.Unlock()
	if err != nil {
		return referenceActionSession{}, err
	}
	closeCall := func() { m.finishPlatformCall(call) }
	account := admission.account
	if account.CLIProfile == nil || old.Account.Valid() && (old.Account != account.Identity || old.PlatformID != account.PlatformID || old.Generation != account.Generation) {
		closeCall()
		return referenceActionSession{}, ErrIdentity
	}
	pin, err := m.larkClients.Pin(callCtx, *account.CLIProfile, account.Identity)
	if err != nil {
		closeCall()
		return referenceActionSession{}, err
	}
	access := larkReferenceAccess(id, account, platform.SourceRef{})
	access.Receiver = old.Receiver
	m.mu.Lock()
	bindings, bindingErr := m.readToolBindingsLocked(id.RoomIdentity)
	if bindingErr == nil {
		for _, b := range bindings.Bindings {
			if b.Tool == "lark" && b.AgentID == id.AgentID && b.MembershipID == id.MembershipID && b.PlatformID == access.PlatformID && b.PlatformGeneration == access.Generation && b.ActionAuthorization != nil {
				access.ActionAuthorizationVersion = b.ActionAuthorization.Revision
			}
		}
	}
	m.mu.Unlock()
	if bindingErr != nil {
		pin.Close()
		closeCall()
		return referenceActionSession{}, bindingErr
	}

	check := func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.checkLarkReferenceAccessLocked(callCtx, req, access)
	}
	// Capabilities always execute on the admitted context: binding revocation,
	// member removal and shutdown synchronously cancel blocking CLI children.
	merge := func(ctx context.Context) (context.Context, func()) {
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
	caps := capabilities(pin.SendReader(), func(ctx context.Context, message, body, key string) (string, error) {
		ctx, finish := merge(ctx)
		defer finish()
		return pin.ReplyText(ctx, message, body, key, check)
	}, func(ctx context.Context, target platform.TargetIdentity, body, key string) (string, error) {
		ctx, finish := merge(ctx)
		defer finish()
		guard := check
		if target.Object.Type == "user" && target.Object.ID == account.Identity.ID {
			granted := access
			guard = func(ctx context.Context) error {
				if e := check(ctx); e != nil {
					return e
				}
				m.mu.Lock()
				defer m.mu.Unlock()
				return m.checkActionAuthorizationLocked(req, granted, platform.ActionSpec{Operation: platform.OperationMessageSendSelf, TargetKind: "object", BindingEvidence: "self_delivery"}, target)
			}
		}
		return pin.SendText(ctx, target, body, key, guard)
	})
	resolve := caps.Resolve
	caps.Resolve = func(ctx context.Context, l platform.TargetLocator, a platform.AccessContext) (platform.TargetResolution, error) {
		if err := check(ctx); err != nil {
			return platform.TargetResolution{}, err
		}
		ctx, finish := merge(ctx)
		defer finish()
		r, err := resolve(ctx, l, a)
		r.AccountLabel = account.Username
		if e := check(ctx); e != nil {
			return platform.TargetResolution{}, e
		}
		return r, err
	}
	// Keep inspection and post-write verification within the same managed call.
	for op, cap := range caps.Actions {
		inspect, execute := cap.Inspect, cap.Execute
		cap.Inspect = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
			if err := check(ctx); err != nil {
				return platform.TargetActionInspection{}, err
			}
			ctx, finish := merge(ctx)
			defer finish()
			return inspect(ctx, q, a)
		}
		cap.Execute = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
			ctx, finish := merge(ctx)
			defer finish()
			// Preserve a known outcome on settlement; do not post-check a revoked binding.
			return execute(ctx, q, a)
		}
		caps.Actions[op] = cap
	}
	return referenceActionSession{Access: access, Capabilities: caps, Check: check, Close: func() { pin.Close(); closeCall() }}, nil
}

func (m *roomManager) resolveLarkActionTarget(ctx context.Context, id MemberIdentity, l platform.TargetLocator) (platform.TargetResolution, referenceActionSession, error) {
	old := platform.AccessContext{}
	if l.Source == nil {
		if _, ok := ctx.Value(toolEntryContextKey{}).(toolEntryContext); !ok {
			return platform.TargetResolution{}, referenceActionSession{}, invalidField("tool", "select an exact entry from teamx tools list")
		}
	}
	if l.Source != nil {
		m.mu.Lock()
		var err error
		old.PlatformID, err = m.sourcePlatformLocked(id.RoomIdentity, *l.Source)
		m.mu.Unlock()
		if err != nil {
			return platform.TargetResolution{}, referenceActionSession{}, err
		}
	}
	if l.Source != nil && l.Source.Reference.RecipientID.Valid() {
		old.Receiver = platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: l.Source.Instance, Tenant: l.Source.Reference.AccountTenant}, ID: l.Source.Reference.RecipientID}
	}
	session, err := m.prepareLarkReferenceAction(ctx, id, old)
	if err != nil {
		return platform.TargetResolution{}, session, err
	}
	r, err := session.Capabilities.Resolve(ctx, l, session.Access)
	return r, session, err
}
