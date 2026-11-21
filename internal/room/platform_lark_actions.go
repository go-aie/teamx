package room

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

func (m *roomManager) prepareLarkReferenceAction(ctx context.Context, id MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
	return m.prepareLarkReferenceContentActions(ctx, id, old, lark.AcceptedActionCapabilities, true)
}

// Acceptance can register this same managed implementation on an isolated
// test manager. This cannot change the production registry or its accepted modes.
func (m *roomManager) prepareLarkReferenceActionWithReadiness(ctx context.Context, id MemberIdentity, old platform.AccessContext, replyDisabled, sendDisabled string) (referenceActionSession, error) {
	return m.prepareLarkReferenceActions(ctx, id, old, func(reader lark.SendReader, reply lark.ReplyWriter, send lark.SendWriter) platform.ReferenceCapabilities {
		return lark.ActionCapabilities(reader, reply, send, replyDisabled, sendDisabled)
	})
}

func (m *roomManager) prepareLarkReferenceActions(ctx context.Context, id MemberIdentity, old platform.AccessContext, capabilities func(lark.SendReader, lark.ReplyWriter, lark.SendWriter) platform.ReferenceCapabilities) (referenceActionSession, error) {
	return m.prepareLarkReferenceContentActions(ctx, id, old, capabilities, false)
}

func (m *roomManager) prepareLarkReferenceContentActions(ctx context.Context, id MemberIdentity, old platform.AccessContext, capabilities func(lark.SendReader, lark.ReplyWriter, lark.SendWriter) platform.ReferenceCapabilities, images bool) (referenceActionSession, error) {
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
	cancelContent := func() {}
	if images {
		callCtx, cancelContent = context.WithTimeout(callCtx, 180*time.Second)
	}
	closeCall := func() { cancelContent(); m.finishPlatformCall(call) }
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
		if final, ok := ctx.Value(actionDispatchCheckKey{}).(actionDispatchCheck); ok {
			merged = context.WithValue(merged, actionDispatchCheckKey{}, final)
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

	var lease *roomActionImages
	var leaseMu sync.Mutex
	var selected lark.ImageSendInput
	if images {
		source := func(ctx context.Context, input lark.ImageSendInput) (lark.PreparedImageMessage, error) {
			leaseMu.Lock()
			defer leaseMu.Unlock()
			if lease == nil {
				refs := make([]roomActionImageRef, len(input.Images))
				for i, ref := range input.Images {
					refs[i] = roomActionImageRef{MessageSeq: ref.MessageSeq, AttachmentID: ref.AttachmentID}
				}
				var err error
				lease, err = svc.prepareRoomActionImages(ctx, refs, check)
				if err != nil {
					return lark.PreparedImageMessage{}, err
				}
				selected = input
			} else if !reflect.DeepEqual(selected, input) {
				return lark.PreparedImageMessage{}, ErrConflict
			}
			if err := lease.Check(ctx); err != nil {
				return lark.PreparedImageMessage{}, err
			}
			message := lark.PreparedImageMessage{Body: input.Body}
			for _, img := range lease.images {
				message.Images = append(message.Images, lark.SendImage{Data: img.Data, MIME: img.Attachment.MIMEType, SHA256: img.SHA256})
			}
			return message, nil
		}
		caps = lark.WithImageActions(caps, pin.SendReader(), source, func(ctx context.Context, q platform.TargetActionRequest, message lark.PreparedImageMessage) (string, []string, error) {
			ctx, finish := merge(ctx)
			defer finish()
			final, ok := ctx.Value(actionDispatchCheckKey{}).(actionDispatchCheck)
			if !ok || lease == nil {
				return "", nil, lark.ErrSendNotSent
			}
			return pin.SendImages(ctx, q.Target, message, q.IdempotencyKey, lease.Check, func(ctx context.Context, digest string) error { return final(ctx, q, digest) })
		}, pin.ImageUploadReady)
	}
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
			if isLarkSend(q.Operation) {
				var content any = &struct {
					Body string `json:"body"`
				}{}
				if images {
					content = &lark.ImageSendInput{}
				}
				if err := decodeRequestJSON(string(q.Input), content); err != nil {
					return platform.TargetActionInspection{}, prefixDiagnostic(err, "input.")
				}
			}
			ctx, finish := merge(ctx)
			defer finish()
			inspection, err := inspect(ctx, q, a)
			var native *lark.Error
			if images && errors.As(err, &native) {
				if native.Code == "invalid_send_input" {
					return inspection, invalidField("input", "provide a nonempty body and/or 1-4 distinct {message_seq,attachment_id} Room images; paths, URLs and resource keys are not accepted")
				}
				if strings.HasPrefix(native.Code, "image_upload_") {
					return inspection, conflictReason("input.images", native.Code, larkImagePermissionHint(native.Code))
				}
			}
			return inspection, err
		}
		cap.Execute = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
			ctx, finish := merge(ctx)
			defer finish()
			// Preserve a known outcome on settlement; do not post-check a revoked binding.
			return execute(ctx, q, a)
		}
		caps.Actions[op] = cap
	}
	return referenceActionSession{Access: access, Capabilities: caps, Check: check, Close: func() {
		leaseMu.Lock()
		if lease != nil {
			lease.Close()
		}
		leaseMu.Unlock()
		pin.Close()
		closeCall()
	}}, nil
}

func larkImagePermissionHint(code string) string {
	hint := "retry_check: refresh image.upload on the original connection; unchecked or expired observations cannot authorize an upload."
	switch code {
	case "image_upload_application_scopes_missing":
		hint = "configure_application: open this connection's official application permissions page and enable user im:resource; user login alone cannot repair an application gap."
	case "image_upload_user_scopes_missing", "image_upload_credential_unavailable":
		hint = "authorize_user: when the user requests repair, read platforms skill and use connect authorize on this exact existing account; preserve member grants and notification settings."
	case "image_upload_account_pinning_unavailable", "image_upload_identity_mismatch":
		hint = "revalidate_account: verify the original fixed user account in settings; do not switch identities. An unsupported host cannot be repaired by login."
	}
	return hint + " Text sending remains independent. No message was sent; do not fall back to text or a subset of images."
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
