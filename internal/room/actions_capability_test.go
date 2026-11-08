package room

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestCapabilityBasisSupportsIndependentProviderOperationAndTarget(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	receiveReference(t, h, "seed", referenceTestSource("seed"))
	referenceRunning(t, h, 1, 0, 1)
	svc := referenceTestService(h)
	access := contractAccess(h.id, platform.IdentityScope{Provider: "contract", Instance: "contract://work"})
	access.ActionAuthorizationVersion = 3
	target := platform.TargetIdentity{IdentityScope: access.Account.IdentityScope, Object: platform.NativeIdentity{Type: "Mailbox", ID: platform.ExternalID{Namespace: "contract:mailbox", Value: "private-storage"}}}
	// Native authorization can permit a target in another tenant. The common
	// proof binds both exact identities without inventing a same-tenant rule.
	target.Tenant = platform.ExternalID{Namespace: "contract:tenant", Value: "shared-storage"}
	r := platform.TargetResolution{Status: platform.ResolutionResolved, Target: &target, Account: access.Account, ObjectVerified: true, Complete: true, CheckedAt: time.Now().UTC()}
	ref, err := h.store.saveActionTarget(h.id, r, access)
	if err != nil {
		t.Fatal(err)
	}
	operation := platform.Operation("memo.save")
	spec := platform.ActionSpec{Operation: operation, BindingEvidence: "private_storage", RequiresAuthorization: true, TargetKind: "object", ObjectTypes: []string{"Mailbox"}, InputSchema: json.RawMessage(`{"type":"object"}`), Retry: platform.RetryPolicy{MaxAttempts: 1}}
	evidence := platform.CapabilityEvidence{Kind: spec.BindingEvidence, CredentialVersion: access.CredentialVersion, Account: access.Account, Target: target, Operator: humanCaller.ID}
	var proof *platform.CapabilityEvidence
	writes := 0
	unknown := false
	prepare := func(context.Context, MemberIdentity, platform.AccessContext) (referenceActionSession, error) {
		return referenceActionSession{Access: access, CapabilityBasis: proof, Check: func(ctx context.Context) error { return ctx.Err() }, Close: func() {}, Capabilities: platform.ReferenceCapabilities{Actions: map[platform.Operation]platform.ActionCapability{operation: {
			Spec: spec,
			Inspect: func(_ context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
				return platform.TargetActionInspection{Target: q.Target, Account: a.Account}, nil
			},
			Execute: func(_ context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
				writes++
				result := platform.TargetActionResult{Target: q.Target, Account: a.Account, Status: "succeeded", ResultID: platform.ExternalID{Namespace: "contract:receipt", Value: "saved"}, RetryClass: "never"}
				if unknown {
					result.Status, result.ResultID = "unknown", platform.ExternalID{}
				}
				return result, nil
			},
		}}}}, nil
	}
	q := referenceActionRequest{followupCommand: followupCommand{TurnNumber: 1}, TargetRef: ref, ID: "save-once", Operation: operation, Input: ActionInput{Body: "chosen content"}}
	for _, failure := range []string{"missing", "wrong-account", "wrong-target", "wrong-kind", "old-credential", "business-basis"} {
		t.Run(failure, func(t *testing.T) {
			copy := evidence
			proof = &copy
			request := q
			switch failure {
			case "missing":
				proof = nil
			case "wrong-account":
				copy.Account.ID.Value = "another-account"
			case "wrong-target":
				copy.Target.Object.ID.Value = "another-mailbox"
			case "wrong-kind":
				copy.Kind = "unrelated"
			case "old-credential":
				copy.CredentialVersion = access.CredentialVersion + 1
			case "business-basis":
				request.PolicyToken, request.Authority = strings.Repeat("a", 64), referenceActionAuthority{RoomSeq: 1, Reason: "different authority mode"}
			}
			if _, err := svc.executeReferenceAction(h.ctx, h.id, request, prepare); err == nil || writes != 0 {
				t.Fatal("invalid basis admitted a write", err, writes)
			}
		})
	}
	proof = &evidence
	receipt, err := svc.executeReferenceAction(h.ctx, h.id, q, prepare)
	if err != nil || receipt.Status != "succeeded" || writes != 1 {
		t.Fatal("generic capability could not execute", receipt, err, writes)
	}
	item := notification.Item{Source: contractSource("echo", "private-storage"), Content: &notification.Content{ContextOnly: true}}
	item.Source.Reference.NoteID = receipt.Result.ResultID
	entry := InboxEntry{Seq: 1, Item: item}
	h.store.notificationAdapters = func(provider string) notification.Capabilities {
		if provider == "contract" {
			return notification.Capabilities{MatchAction: func(_ notification.View, q platform.TargetActionRequest, _ platform.AccessContext) bool {
				return q.Operation == operation && sameNativeTarget(q.Target, target)
			}}
		}
		return notificationCapabilities(provider)
	}
	h.store.mu.Lock()
	origins, originErr := h.store.inboxMessageOriginsLocked([]InboxEntry{entry})
	h.store.mu.Unlock()
	if originErr != nil || origins[1].Kind != "agent" || origins[1].ActionID != q.ID {
		t.Fatal("native message matching required IM chat or operation assumptions", origins, originErr)
	}
	// Business-authority actions cannot obtain the capability path merely by
	// omitting policy_token/authority, even when a proof exists in the session.
	spec.BindingEvidence = ""
	other := q
	other.ID, other.Input.Body = "business-without-basis", "different content"
	if _, err := svc.executeReferenceAction(h.ctx, h.id, other, prepare); err == nil || writes != 1 {
		t.Fatal("missing business authority bypassed the registered contract", err, writes)
	}
	spec.BindingEvidence = evidence.Kind
	unknown = true
	q.ID, q.Input.Body = "unknown", "uncertain content"
	if receipt, err = svc.executeReferenceAction(h.ctx, h.id, q, prepare); err != nil || receipt.Status != "unknown" || writes != 2 {
		t.Fatal(receipt, err, writes)
	}
	if _, err = svc.executeReferenceAction(h.ctx, h.id, q, prepare); err != nil || writes != 2 {
		t.Fatal("receipt replay resent a write", err, writes)
	}
	q.ID = "new-id-same-intent"
	if _, err = svc.executeReferenceAction(h.ctx, h.id, q, prepare); err == nil || writes != 2 {
		t.Fatal("new ID bypassed uncertain intent", err, writes)
	}
	reloadConversation(t, h)
	svc = referenceTestService(h)
	q.ID = "save-once"
	q.Input.Body = "chosen content"
	if receipt, err = svc.executeReferenceAction(h.ctx, h.id, q, prepare); err != nil || receipt.Status != "succeeded" || writes != 2 {
		t.Fatal("generic capability receipt did not survive reopen", receipt, err, writes)
	}
}

func TestBindingEvidencePreparationIsOptionalAndProviderRegistered(t *testing.T) {
	const operation = platform.Operation("memo.save")
	access := contractAccess(MemberIdentity{}, platform.IdentityScope{Provider: "contract", Instance: "contract://work"})
	target := platform.TargetIdentity{IdentityScope: access.Account.IdentityScope, Object: platform.NativeIdentity{Type: "Mailbox", ID: platform.ExternalID{Namespace: "contract:mailbox", Value: "private"}}}
	spec := platform.ActionSpec{Operation: operation, BindingEvidence: "private_storage", RequiresAuthorization: true, TargetKind: "object", ObjectTypes: []string{"Mailbox"}, InputSchema: json.RawMessage(`{"type":"object"}`), Retry: platform.RetryPolicy{MaxAttempts: 1}}
	caps := platform.ReferenceCapabilities{Actions: map[platform.Operation]platform.ActionCapability{operation: {Spec: spec}}}
	m := &roomManager{toolCatalog: []tool.Definition{{Name: "memo", Provider: "contract"}}, referenceAdapters: map[string]referenceAdapterFactory{"contract": func() platform.ReferenceCapabilities { return caps }}}
	request := toolBindingChange{Caller: humanCaller, Enabled: true, Binding: tool.Binding{Tool: "memo", ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: []tool.ActionGrant{{Operation: string(operation), TargetKind: "object", ObjectType: "Mailbox"}}}}}
	if err := m.prepareBindingEvidence(t.Context(), &request); err == nil {
		t.Fatal("declared proof requirement silently passed without an adapter")
	}
	prepared := 0
	m.bindingEvidencePreparers = map[string]bindingEvidencePreparer{"contract": func(_ context.Context, req *toolBindingChange) error {
		prepared++
		req.Binding.CapabilityEvidence = []platform.CapabilityEvidence{{Kind: "private_storage", Account: access.Account, Target: target, CredentialVersion: 1, Operator: req.Caller.ID}}
		return nil
	}}
	if err := m.prepareBindingEvidence(t.Context(), &request); err != nil || prepared != 1 {
		t.Fatal("registered provider could not prepare evidence", err, prepared)
	}
	if err := validateBindingEvidence(request.Binding, caps, humanCaller.ID); err != nil {
		t.Fatal("generic binding rejected a non-user target", err)
	}
	if bindingConfirmsAccount(request.Binding, access, caps) {
		t.Fatal("enabling an unrelated capability was treated as account ownership")
	}
	if err := m.prepareBindingEvidence(t.Context(), &request); err == nil || prepared != 1 {
		t.Fatal("client-supplied proof was accepted or dispatched", err, prepared)
	}
}
