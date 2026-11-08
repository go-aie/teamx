//go:build integration || e2e

package room

import (
	"testing"
	"time"
)

func TestOwnLarkRuleReplyBlockedButHumanReplyAllowed(t *testing.T) {
	parallelFixtureTest(t)
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
	parallelFixtureTest(t)
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
