package room

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	larkruntime "github.com/go-aie/teamx/internal/app/builtinplatforms/lark"
	"github.com/go-aie/teamx/internal/app/platformtools"
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
	return larkruntime.BindActions(larkruntime.ActionHost{
		Context: callCtx, Access: access, AccountLabel: account.Username, Pin: pin, Check: check, Request: ErrRequest, Conflict: ErrConflict, Close: closeCall,
		CheckSelf: func(ctx context.Context, target platform.TargetIdentity) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.checkActionAuthorizationLocked(req, access, platform.ActionSpec{Operation: platform.OperationMessageSendSelf, TargetKind: "object", BindingEvidence: "self_delivery"}, target)
		},
		Images: func(ctx context.Context, refs []platformtools.ImageRef, guard func(context.Context) error) (platformtools.ImageLease, error) {
			return svc.prepareRoomActionImageLease(ctx, refs, guard)
		},
		ValidateInput: func(raw json.RawMessage, images bool) error {
			if images {
				var input lark.ImageSendInput
				return prefixDiagnostic(decodeRequestJSON(string(raw), &input), "input.")
			}
			var input struct {
				Body string `json:"body"`
			}
			return prefixDiagnostic(decodeRequestJSON(string(raw), &input), "input.")
		},
	}, capabilities, images)
}

func (m *roomManager) resolveLarkActionTarget(ctx context.Context, id MemberIdentity, l platform.TargetLocator) (platform.TargetResolution, referenceActionSession, error) {
	if l.Query != nil {
		query := l.Query
		if query.ObjectType != "chat" && query.ObjectType != "user" {
			return platform.TargetResolution{}, referenceActionSession{}, invalidField("locator.query.object_type", "expected chat or user")
		}
		if !utf8.ValidString(query.Text) || len([]rune(query.Text)) > 50 || strings.ContainsAny(query.Text, "\x00\r\n") {
			return platform.TargetResolution{}, referenceActionSession{}, invalidField("locator.query.text", "expected a nonempty name of at most 50 characters, without NUL or line breaks")
		}
	}
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
