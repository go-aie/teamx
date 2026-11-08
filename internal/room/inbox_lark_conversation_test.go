package room

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

func TestSelfConversationInboxHistoryDoesNotScheduleOrOpenWork(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	manual := ownLarkItem("self-manual", "om_manual", true)
	manual.Content.Conversation.Type = "p2p"
	echo := ownLarkItem("self-agent", "om_echo", true)
	echo.Content.Conversation.Type = "p2p"
	// There is deliberately no send receipt. Native conversation identity, not
	// matching text, receipt timing, or whether a human wrote it, owns exclusion.
	for _, item := range []ReceiveInbox{{MemberIdentity: h.id, Item: manual}, {MemberIdentity: h.id, Item: echo}} {
		inboxCall[InboxReceipt](t, h, item)
	}
	svc := referenceTestService(h)
	calls := 0
	svc.resolveInboxAdmission = func(_ context.Context, source platform.SourceRef) (json.RawMessage, error) {
		calls++
		return json.Marshal(lark.ConversationRelation{Account: platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: source.Instance, Tenant: source.Reference.AccountTenant}, ID: source.Reference.RecipientID}, ChatID: source.Reference.ChatID.Value, Kind: "self"})
	}
	check := func(svc *service) InboxStatus {
		t.Helper()
		status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
		status, err := svc.reconcileInboxAdmission(h.ctx, h.id, status)
		if err != nil {
			t.Fatal(err)
		}
		return status
	}
	status := check(svc)
	member := &MemberAgent{inbox: &status}
	if member.inboxReady() || status.AutomaticReady || status.AutomaticBlocked || calls != 1 {
		t.Fatal("self history scheduled work or repeated relationship lookup", status, calls)
	}
	h.reload(t, true) // Reopen Store, not just reconstruct the runner.
	room, err := NewRoom(h.store)
	if err != nil {
		t.Fatal(err)
	}
	h.roomHarness.addr, err = h.rt.Spawn(room)
	if err != nil {
		t.Fatal(err)
	}
	svc = referenceTestService(h)
	svc.resolveInboxAdmission = func(context.Context, platform.SourceRef) (json.RawMessage, error) {
		t.Fatal("persisted native proof was lost")
		return nil, nil
	}
	status = check(svc)
	member.inbox = &status
	if member.inboxReady() {
		t.Fatal("restart offered self history")
	}
	preview := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
	human, err := h.store.projectInboxPreview(preview, status)
	if err != nil || len(human.Entries) != 2 || human.Entries[0].AutomaticAdmission.ReasonCode != "self_excluded" || human.Entries[1].AutomaticAdmission.ReasonCode != "self_excluded" {
		t.Fatal("history evidence not inspectable", human, err)
	}
	// Independent business input still wakes the member and can pass a mixed
	// batch without replaying either self record as model input or a todo.
	group := ownLarkItem("group-incoming", "om_group", false)
	group.Source.Reference.ChatID.Value = "oc_group"
	group.Content.Conversation.ID.Value = "oc_group"
	inboxCall[InboxReceipt](t, h, ReceiveInbox{MemberIdentity: h.id, Item: group})
	status = check(svc)
	member.inbox = &status
	if !member.inboxReady() {
		t.Fatal("group business was blocked", status)
	}
	inboxRunning(t, h, 1, 3)
	page, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, func(_ context.Context, _ MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		return lark.ResolveNotification(source, time.Now().UTC()), platform.AccessContext{}, nil
	})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Seq != 3 || len(page.Sources) != 1 || len(page.Followups) != 1 {
		t.Fatal("self history reached Agent or opened business work", page, err)
	}
	status = inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: page.Batch.Receipt})
	if status.ConsumedThrough != 3 {
		t.Fatal("excluded history blocked business acknowledgement", status)
	}
	h.reload(t, true)
	if got := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); got.ConsumedThrough != 3 || got.LatestSeq != 3 {
		t.Fatal("history/ack did not survive independent reopen", got)
	}
}

func TestUnknownInboxConversationWaitsAndCannotAcquireAuthority(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	item := ownLarkItem("unknown", "om_unknown", true)
	item.Content = nil // Legacy notices must not infer self or peer from the author.
	inboxCall[InboxReceipt](t, h, ReceiveInbox{MemberIdentity: h.id, Item: item})
	svc := referenceTestService(h)
	calls := 0
	svc.resolveInboxAdmission = func(context.Context, platform.SourceRef) (json.RawMessage, error) {
		calls++
		return nil, errors.New("incomplete")
	}
	for range 2 {
		status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
		status, err := svc.reconcileInboxAdmission(h.ctx, h.id, status)
		if err != nil || !status.AutomaticBlocked || (&MemberAgent{inbox: &status}).inboxReady() {
			t.Fatal("unverified conversation scheduled work", status, err)
		}
	}
	if calls != 1 {
		t.Fatal("failed verification was hot retried", calls)
	}
	preview := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
	view, err := h.store.projectInboxPreview(preview, InboxStatus{})
	if err != nil || view.Entries[0].AutomaticAdmission.ReasonCode != "relationship_pending" {
		t.Fatal("pending relationship was hidden", view, err)
	}
	inboxRunning(t, h, 1, 1)
	if _, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, referenceTestResolver); !errors.Is(err, ErrBusy) {
		t.Fatal("unverified history reached automatic processing", err)
	}
	if _, err := svc.resolveFollowupEvent(h.ctx, h.id, 1, 1, false, referenceTestResolver); !errors.Is(err, ErrBusy) {
		t.Fatal("direct followup resolution bypassed inbox exclusion", err)
	}
}
