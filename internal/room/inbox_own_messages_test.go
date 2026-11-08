package room

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func ownLarkItem(key, message string, own bool) notification.Item {
	accountID := platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_reader"}
	chat := platform.ExternalID{Namespace: "lark:chat_id:cli_fixture", Value: "oc_chat"}
	sender := accountID
	if !own {
		sender.Value = "ou_other"
	}
	return notification.Item{Key: key, DeliveryID: "own-" + key, Text: "飞书\n发送者：我\n已处理", CreatedAt: time.Now().UTC(), Source: platform.SourceRef{
		Provider: "lark", Channel: "messages", Instance: lark.Origin("feishu"), Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "external_tenant"}, SubscriptionID: strings.Repeat("a", 32), ItemKey: key,
		Reference: platform.NotificationReference{Version: 2, RecipientID: accountID, AccountTenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "account_tenant"}, ChatID: chat,
			Target: platform.ObjectReference{Type: "message", ID: platform.ExternalID{Namespace: "lark:message_id:cli_fixture", Value: message}}}},
		Content: &notification.Content{Structured: true, Text: "已处理", Sender: &notification.Participant{ID: sender, IsReceiver: own, Type: "user"}, Conversation: &notification.Context{ID: chat, Type: "group"}}}
}

func TestOwnLarkInboxPreservesContextWithoutOpeningWork(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	items := []notification.Item{
		ownLarkItem("own-first", "om_root", true),
		ownLarkItem("incoming", "om_incoming", false),
		ownLarkItem("own-reply", "om_reply", true),
		ownLarkItem("own-standalone", "om_standalone", true),
	}
	for i := 1; i <= 2; i++ {
		items[i].Source.Reference.RootID = items[0].Source.Reference.Target.ID
		items[i].Source.Reference.ParentID = items[0].Source.Reference.Target.ID
	}
	items[3].Source.Reference.DiscussionID = platform.ExternalID{Namespace: "lark:thread_id:cli_fixture", Value: "omt_own"}
	for _, item := range items {
		q := ReceiveInbox{MemberIdentity: h.id, Item: item}
		first := inboxCall[InboxReceipt](t, h, q)
		if replay := inboxCall[InboxReceipt](t, h, q); replay != first {
			t.Fatal("own message was duplicated", first, replay)
		}
	}
	inboxRunning(t, h, 1, 4)
	svc := referenceTestService(h)
	resolve := func(_ context.Context, _ MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		return lark.ResolveNotification(source, time.Now().UTC()), platform.AccessContext{}, nil
	}
	page, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 4 || len(page.Sources) != 4 || len(page.Followups) != 1 {
		t.Fatal("context lost or own messages opened work", page)
	}
	projected, err := projectAgentInboxRead(h.id, page)
	if err != nil || !projected.Entries[0].Item.ContextOnly || projected.Entries[1].Item.ContextOnly || !projected.Entries[2].Item.ContextOnly || projected.Entries[0].Origin.Kind != "account" {
		t.Fatal("human/agent message contexts not distinguished", projected, err)
	}
	if page.Sources[0].ObjectRef != page.Sources[1].ObjectRef || page.Sources[1].ObjectRef != page.Sources[2].ObjectRef {
		t.Fatal("native reply linkage lost", page.Sources)
	}
	if _, err := h.store.ensureDiscussionFollowup(h.id, 4, 1, page.Sources[3].TargetRef); !errors.Is(err, ErrConflict) {
		t.Fatal("own thread created independent work", err)
	}
	if _, err := h.store.readFollowup(h.id, page.Sources[3].ObjectRef, referenceDigest("unit_", page.Sources[3].ObjectRef)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("own standalone message created a unit", err)
	}
	inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: page.Batch.Receipt})
	h.reload(t, true)
	status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if status.LatestSeq != 4 || status.ConsumedThrough != 4 {
		t.Fatal("context-only consumption did not survive restart", status)
	}
	preview := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
	human, err := h.store.projectInboxPreview(preview, status)
	if err != nil || len(human.Entries) != 4 || !human.Entries[3].Item.ContextOnly || human.Entries[0].Source.ObjectRef != page.Sources[0].ObjectRef {
		t.Fatal("human preview or source linkage lost after restart", human, err)
	}
}

func TestOwnLarkRuleReplyBlockedButHumanReplyAllowed(t *testing.T) {
	f := newLarkReplyContract(t)
	f.grant(t)
	ref := f.page.Entries[0].Source.TargetRef
	// Find the exact native message named by the resolved source.
	v := f.resolve(t, ref)
	messageID := v.Resolution.Target.Message.Value
	original := f.reader.messages[messageID]
	original.Sender.ID = "ou_reader"
	f.reader.messages[messageID] = original
	c := policyRule()
	if _, err := f.h.svc.store.changePolicy(f.id, humanCaller, c); err != nil {
		t.Fatal(err)
	}
	q := f.request(t, ref)
	q.Authority = agentActionAuthority{Type: "rule", ID: c.ID, Version: 1, Reason: "按长期规则回复"}
	if _, err := f.run(q); err == nil || f.calls.Load() != 0 {
		t.Fatal("own message triggered an automatic send", err, f.calls.Load())
	}
	q.Authority = agentActionAuthority{Type: "human_reply", RoomSeq: f.post.Seq, Reason: "真人明确要求回复这条本人消息"}
	if receipt, err := f.run(q); err != nil || receipt.Status != "succeeded" || f.calls.Load() != 1 {
		t.Fatal("explicit human instruction was blocked", receipt, err)
	}
}

func TestOwnLarkOriginUsesExactDurableReceipt(t *testing.T) {
	f := newLarkReplyContract(t)
	f.grant(t)
	ref := f.page.Entries[0].Source.TargetRef
	f.resolve(t, ref)
	e := InboxEntry{Version: 1, MemberIdentity: f.id, Seq: 3, Item: ownLarkItem("echo", "om_receipt", true), ReceivedAt: time.Now().UTC()}
	store := f.h.svc.store
	lookup := func() inboxMessageOrigin {
		t.Helper()
		store.mu.Lock()
		origins, err := store.inboxMessageOriginsLocked([]InboxEntry{e})
		store.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		return origins[e.Seq]
	}
	if o := lookup(); o.Kind != "account" || !inboxContextOnly(e) {
		t.Fatal("message before receipt must remain safe context", o)
	}
	q := f.request(t, ref)
	if r, err := f.run(q); err != nil || r.Status != "succeeded" {
		t.Fatal(r, err)
	}
	if o := lookup(); o.Kind != "agent" || o.ActionID != q.ID || o.AgentID != f.id.AgentID {
		t.Fatal("late receipt did not enrich origin", o)
	}
	// A manual message with identical text, another chat or another app cannot match.
	for _, mutate := range []func(*InboxEntry){
		func(e *InboxEntry) { e.Item.Source.Reference.Target.ID.Value = "om_manual" },
		func(e *InboxEntry) {
			e.Item.Content.Conversation.ID.Value = "oc_other"
			e.Item.Source.Reference.ChatID.Value = "oc_other"
		},
		func(e *InboxEntry) { e.Item.Source.Reference.AccountTenant.Value = "other_tenant" },
		func(e *InboxEntry) { e.Item.Source.Reference.Target.ID.Namespace = "lark:message_id:cli_other" },
	} {
		original := e
		content, chat := *e.Item.Content, *e.Item.Content.Conversation
		content.Conversation, e.Item.Content = &chat, &content
		mutate(&e)
		if o := lookup(); o.Kind != "account" {
			t.Fatal("origin matched without exact native scope", o)
		}
		e = original
	}
	// Restart must preserve attribution without a transient in-memory send registry.
	storeDir := store.dir
	if err := f.h.m.close(); err != nil {
		t.Fatal(err)
	}
	var err error
	store, err = OpenStore(storeDir, f.id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if o := lookup(); o.Kind != "agent" || o.ActionID != q.ID {
		t.Fatal("restart lost send attribution", o)
	}
	// Durable unknown intent with no returned ID must never be labelled manual/agent.
	store.mu.Lock()
	actions, err := store.loadActionsLocked(f.id)
	var exact referenceAction
	if err == nil {
		a := actions[0]
		exact = a
		a.Status, a.Result = "unknown", nil
		err = store.saveActionLocked(f.id, a)
	}
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if o := lookup(); o.Kind != "account" || !o.UncertainSend || !inboxContextOnly(e) {
		t.Fatal("unknown send was guessed or became a trigger", o)
	}
	// Another member can use the same account; exact evidence wins over an
	// unrelated uncertain send from the receiving member.
	member := f.h.members[1]
	other := MemberIdentity{RoomIdentity: f.id.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	if err := store.initReferenceFollowups(other); err != nil {
		t.Fatal(err)
	}
	exact.Request.TargetRef = targetReference(other, exact.Target)
	exact.Access.AgentID, exact.Access.MembershipID = other.AgentID, other.MembershipID
	exact.IdempotencyKey = referenceDigest("", other, exact.Request.ID)[:32]
	basis := *exact.Basis
	basis.TargetRef = exact.Request.TargetRef
	exact.Basis = &basis
	store.mu.Lock()
	err = store.saveActionLocked(other, exact)
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if o := lookup(); o.Kind != "agent" || o.AgentID != other.AgentID || o.UncertainSend {
		t.Fatal("another member's exact receipt was missed", o)
	}
}
