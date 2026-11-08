package room

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// The adapter deliberately uses neither chats nor user/open_id namespaces.
// Its proof is private; the Inbox only observes verification and the decision.
func contractNotificationCapabilities(provider string) notification.Capabilities {
	if provider != "contract" {
		return notificationCapabilities(provider)
	}
	return notification.Capabilities{Admission: &notification.AdmissionCapability{
		EvidencePath: func(s platform.SourceRef) string {
			return filepath.Join("contract-evidence", referenceDigest("", s.Reference.RecipientID, s.Reference.Object.ID)+".json")
		},
		Inspect: func(item notification.Item, raw json.RawMessage) (notification.Admission, error) {
			if len(raw) == 0 {
				return notification.Admission{ReasonCode: "proof_missing"}, nil
			}
			var proof struct {
				Account platform.ExternalID
				Object  platform.ExternalID
			}
			if json.Unmarshal(raw, &proof) != nil || proof.Account != item.Source.Reference.RecipientID || proof.Object != item.Source.Reference.Object.ID {
				return notification.Admission{}, ErrIdentity
			}
			decision := notification.AdmissionAllow
			if proof.Object.Value == "quiet" {
				decision = notification.AdmissionExcluded
			}
			return notification.Admission{Verified: true, Decision: decision, ReasonCode: "contract_verified"}, nil
		},
	}}
}

func TestInboxAdmissionOptionalProviderAndOpaqueEvidenceRecovery(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	h.store.notificationAdapters = contractNotificationCapabilities
	for _, source := range []platform.SourceRef{contractSource("quiet", "quiet"), contractSource("business", "business"), referenceTestSource("ordinary")} {
		receiveReference(t, h, source.ItemKey, source)
	}
	status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	svc := referenceTestService(h)
	blocked, err := svc.reconcileInboxAdmission(h.ctx, h.id, status)
	if err != nil || !blocked.AutomaticBlocked || (&MemberAgent{inbox: &blocked}).inboxReady() {
		t.Fatal("missing resolver admitted unverified input", blocked, err)
	}
	inboxRunning(t, h, 1, 3)
	if _, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, referenceTestResolver); !errors.Is(err, ErrBusy) {
		t.Fatal("direct read bypassed admission", err)
	}
	queries := 0
	svc = referenceTestService(h)
	svc.resolveInboxAdmission = func(_ context.Context, s platform.SourceRef) (json.RawMessage, error) {
		queries++
		return json.Marshal(struct{ Account, Object platform.ExternalID }{s.Reference.RecipientID, s.Reference.Object.ID})
	}
	ready, err := svc.reconcileInboxAdmission(h.ctx, h.id, status)
	if err != nil || ready.AutomaticBlocked || !ready.AutomaticReady || !(&MemberAgent{inbox: &ready}).inboxReady() || queries != 2 {
		t.Fatal("optional admission blocked independent input", ready, err, queries)
	}
	resolve := func(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		if source.Provider != "contract" {
			return referenceTestResolver(ctx, id, source)
		}
		access := contractAccess(id, platform.IdentityScope{Provider: source.Provider, Instance: source.Instance, Tenant: source.Tenant})
		target := platform.TargetIdentity{IdentityScope: access.Account.IdentityScope, Object: platform.NativeIdentity{Type: source.Reference.Object.Type, ID: source.Reference.Object.ID}, Message: source.Reference.NoteID}
		return platform.TargetResolution{Status: platform.ResolutionResolved, Target: &target, Account: access.Account, CheckedAt: time.Now().UTC(), ObjectVerified: true, MessageVerified: true, Complete: true}, access, nil
	}
	page, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, resolve)
	if err != nil || len(page.Entries) != 2 || page.Entries[0].Seq != 2 || page.Entries[1].Seq != 3 {
		t.Fatal("native exclusion did not pass through common batch processing", page, err)
	}
	if got := inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: page.Batch.Receipt}); got.ConsumedThrough != 3 {
		t.Fatal("excluded proof blocked mixed-batch consumption", got)
	}
	h.reload(t, true)
	h.store.notificationAdapters = contractNotificationCapabilities
	preview := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
	view, err := h.store.projectInboxPreview(preview, InboxStatus{})
	if err != nil || len(view.Entries) != 3 || view.Entries[0].AutomaticAdmission == nil || view.Entries[0].AutomaticAdmission.Decision != notification.AdmissionExcluded {
		t.Fatal("native proof or historical evidence was lost on reopen", view, err)
	}
}

func TestInboxAdmissionRejectsContradictoryOrMismatchedEvidence(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	h.store.notificationAdapters = contractNotificationCapabilities
	receiveReference(t, h, "business", contractSource("business", "business"))
	svc := referenceTestService(h)
	svc.resolveInboxAdmission = func(context.Context, platform.SourceRef) (json.RawMessage, error) {
		return json.RawMessage(`{"Account":{"namespace":"wrong","value":"other"},"Object":{}}`), nil
	}
	status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if _, err := svc.reconcileInboxAdmission(h.ctx, h.id, status); !errors.Is(err, ErrInvalid) {
		t.Fatal("mismatched native proof admitted", err)
	}
	if (notification.Admission{Verified: false, Decision: notification.AdmissionAllow}).Valid() || (notification.Admission{Verified: true}).Valid() {
		t.Fatal("verification and decision may contradict")
	}
}
