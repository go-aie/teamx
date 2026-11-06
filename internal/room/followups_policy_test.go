package room

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func conversationRule(seq uint64) conversationPolicyChange {
	c := policyRule()
	c.SourceText = ""
	c.SourceSeq = seq
	c.TurnNumber = 1
	return conversationPolicyChange{Change: c, Mode: "explicit_long_term", Basis: "用户明确要求以后项目123的文档咨询自动办理，排期仍询问"}
}

func TestReferenceConversationPolicyExplicitAmendRevokeAndTokens(t *testing.T) {
	h, svc, _, _ := conversationHarness(t, 1)
	source := postConversationReply(t, h, "standing", "以后项目123的使用问题有文档就直接回复，交付时间先问我", 0)
	q := conversationRule(source.Seq)
	v, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), q)
	if err != nil || len(v.Effective) != 1 || v.Revision != 1 {
		t.Fatal(v, err)
	}
	if v.Effective[0].Source.Text != "以后项目123的使用问题有文档就直接回复，交付时间先问我" || v.Effective[0].Conversation.Message.Author != humanCaller.Author {
		t.Fatal(v)
	}
	// Next same-scope input can use the effective view; no UI activation required.
	read, err := h.store.readAgentPolicy(h.id)
	if err != nil || read.PolicyToken != v.PolicyToken || len(read.Effective) != 1 {
		t.Fatal(read, err)
	}
	firstToken := v.PolicyToken
	retry, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), q)
	if err != nil || !reflect.DeepEqual(retry, v) {
		t.Fatal(retry, err)
	}
	duplicate := q
	duplicate.Change.ID = "duplicate"
	duplicate.Change.Revision = 1
	if _, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), duplicate); !errors.Is(err, ErrConflict) {
		t.Fatal("same-scope rule duplicated", err)
	}
	change := postConversationReply(t, h, "amend", "改为仅回复文档安装咨询；交付时间仍先问我", 0)
	q.Change.Version, q.Change.Revision, q.Change.SourceSeq = 1, 1, change.Seq
	q.Change.Text = "只回复有文档依据的安装咨询"
	q.Change.Text += "；核实当前安装文档及账号，交付时间仍先询问"
	q.Basis = "用户明确收窄原规则"
	v, err = svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), q)
	if err != nil || len(v.Effective) != 1 || v.Effective[0].Version != 2 || v.PolicyToken == firstToken {
		t.Fatal(v, err)
	}
	secondToken := v.PolicyToken
	revoke := postConversationReply(t, h, "revoke", "撤销刚才的自动回复规则", 0)
	q.Change.Version, q.Change.Revision, q.Change.SourceSeq, q.Change.Status = 2, 2, revoke.Seq, "revoked"
	q.Basis = "明确撤销该条规则"
	v, err = svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), q)
	if err != nil || len(v.Effective) != 0 || v.PolicyToken == secondToken || v.Revision != 3 {
		t.Fatal(v, err)
	}
	roomCall[ClearReceipt](t, h.roomHarness, ClearMessages{RoomIdentity: h.id.RoomIdentity, Caller: humanCaller, ThroughSeq: revoke.Seq})
	reloadConversation(t, h)
	inboxRunning(t, h, 2, 1)
	q.Change.TurnNumber = 2
	retry, err = referenceTestService(h).referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), q)
	if err != nil || retry.Revision != 3 || len(retry.Effective) != 0 {
		t.Fatal(retry, err)
	}
	history, err := h.store.readPolicy(h.id)
	if err != nil || len(history.History) != 3 || history.History[0].Conversation.Message.Text == "" || history.History[2].Conversation.Message.Text != "撤销刚才的自动回复规则" {
		t.Fatal(history, err)
	}
	// A stale active request is recognized without reactivating a revoked rule.
	original := conversationRule(source.Seq)
	original.Change.TurnNumber = 2
	if got, err := h.store.changeConversationPolicy(h.id, agentCallerIdentity(h.id), original); err != nil || len(got.Effective) != 0 || got.Revision != 3 {
		t.Fatal(got, err)
	}
}

func TestReferenceConversationPolicyInferredSuggestionConfirmation(t *testing.T) {
	h, svc, units, sources := conversationHarness(t, 1)
	habit := postConversationReply(t, h, "habit", "这次请用简短中文回复", 0)
	q := conversationPolicyChange{Change: PolicyChange{ID: "language", Kind: "preference", Status: "suggested", Text: "简短中文回复", SourceSeq: habit.Seq, TurnNumber: 1}, Mode: "inferred_habit", Basis: "多次本次偏好只支持提出建议"}
	view, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), q)
	if err != nil || len(view.Effective) != 0 {
		t.Fatal(view, err)
	}
	before := view.PolicyToken
	bad := q
	bad.Change.Status = "active"
	bad.Change.Version, bad.Change.Revision = 1, 1
	bad.Change.SourceSeq = postConversationReply(t, h, "another-habit", "本次继续简短中文回复", 0).Seq
	if _, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), bad); !errors.Is(err, ErrRequest) {
		t.Fatal("inference activated", err)
	}
	receipt, err := svc.publishPolicySuggestion(h.ctx, h.id, 1, q.Change.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.publishPolicySuggestion(h.ctx, h.id, 1, q.Change.ID, 1)
	if err != nil || again != receipt {
		t.Fatal("repeated confirmation question", again, err)
	}
	// Consent to an ordinary one-off question cannot confirm a standing suggestion.
	unit := askConversation(t, h, svc, units[0], sources[0].TargetRef, "这次是否按方案 A 回复？")
	once := postConversationReply(t, h, "once", "可以", unit.Questions[0].Seq)
	confirm := q
	confirm.Mode = "confirm_suggestion"
	confirm.Change.Status = "active"
	confirm.Change.Version, confirm.Change.Revision, confirm.Change.SourceSeq = 1, 1, once.Seq
	confirm.ProposalSeq = receipt.Seq
	confirm.Basis = "尝试将本次任务同意解释为长期同意"
	if _, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), confirm); !errors.Is(err, ErrRequest) {
		t.Fatal("one-off consent promoted", err)
	}
	answer := postConversationReply(t, h, "confirm", "以后就这样", receipt.Seq)
	confirm.Change.SourceSeq = answer.Seq
	confirm.Basis = "用户在准确建议下确认今后处理方式"
	// Changing the proposal while activating is forbidden, including scope/expiry.
	changed := confirm
	changed.Change.Text = "自动执行全部写操作"
	if _, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), changed); !errors.Is(err, ErrRequest) {
		t.Fatal(err)
	}
	view, err = svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), confirm)
	if err != nil || len(view.Effective) != 1 || view.PolicyToken == before {
		t.Fatal(view, err)
	}
	r := view.Effective[0]
	if r.Conversation.Proposal.Version != 1 || r.Conversation.Proposal.Seq != receipt.Seq || r.Source.Text != "以后就这样" {
		t.Fatal(r)
	}
	roomCall[ClearReceipt](t, h.roomHarness, ClearMessages{RoomIdentity: h.id.RoomIdentity, Caller: humanCaller, ThroughSeq: answer.Seq})
	reloadConversation(t, h)
	inboxRunning(t, h, 2, 1)
	svc = referenceTestService(h)
	restored, err := h.store.readPolicy(h.id)
	if err != nil || len(restored.Effective) != 1 || !reflect.DeepEqual(restored.Effective[0], r) {
		t.Fatal(restored, err)
	}
	confirm.Change.TurnNumber = 2
	if got, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), confirm); err != nil || got.Revision != 2 {
		t.Fatal(got, err)
	}
}

func TestReferenceConversationPolicyStaleSuggestionAndContextConfirmation(t *testing.T) {
	h, svc, _, _ := conversationHarness(t, 1)
	source := postConversationReply(t, h, "habit", "这次先核对文档", 0)
	q := conversationRule(source.Seq)
	q.Mode = "inferred_habit"
	q.Change.Status = "suggested"
	if _, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), q); err != nil {
		t.Fatal(err)
	}
	proposal, err := svc.publishPolicySuggestion(h.ctx, h.id, 1, q.Change.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	// The durable Room receipt survives clearing even before activation.
	roomCall[ClearReceipt](t, h.roomHarness, ClearMessages{RoomIdentity: h.id.RoomIdentity, Caller: humanCaller, ThroughSeq: proposal.Seq})
	reloadConversation(t, h)
	inboxRunning(t, h, 2, 1)
	svc = referenceTestService(h)
	if receipt, err := svc.publishPolicySuggestion(h.ctx, h.id, 2, q.Change.ID, 1); err != nil || receipt != proposal {
		t.Fatal(receipt, err)
	}
	// Saving a revised suggestion invalidates confirmation of the previous one.
	source2 := postConversationReply(t, h, "habit2", "本次仅安装问题", 0)
	q.Change.TurnNumber = 2
	q.Change.Version, q.Change.Revision, q.Change.SourceSeq = 1, 1, source2.Seq
	q.Change.Text = "仅安装咨询自动回复"
	if _, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), q); err != nil {
		t.Fatal(err)
	}
	oldReply := postConversationReply(t, h, "old-confirm", "确认之前的建议", 0)
	stale := q
	stale.Mode = "confirm_suggestion"
	stale.Change.Status = "active"
	stale.Change.SourceSeq = oldReply.Seq
	stale.Change.Revision = 2
	stale.ProposalSeq = proposal.Seq
	if _, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), stale); !errors.Is(err, ErrConflict) {
		t.Fatal(stale, err)
	}
	proposal2, err := svc.publishPolicySuggestion(h.ctx, h.id, 2, q.Change.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	answer := postConversationReply(t, h, "new-confirm", "以后仅安装咨询按刚才那份建议办理", 0)
	confirm := q
	confirm.Mode = "confirm_suggestion"
	confirm.Change.Status = "active"
	confirm.Change.Version, confirm.Change.Revision, confirm.Change.SourceSeq = 2, 2, answer.Seq
	confirm.ProposalSeq = proposal2.Seq
	confirm.Basis = "无回复引用，但用户明确指出刚才的安装咨询建议"
	v, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), confirm)
	if err != nil || len(v.Effective) != 1 || v.Effective[0].Text != "仅安装咨询自动回复" {
		t.Fatal(v, err)
	}
}

func TestReferenceConversationPolicyProvenanceFencesAndFailures(t *testing.T) {
	h, svc, _, _ := conversationHarness(t, 1)
	source := postConversationReply(t, h, "explicit", "以后文档咨询自动回复，排期询问", 0)
	agent := roomCall[PostReceipt](t, h.roomHarness, h.post(agentCaller("B", "generation-B"), "fake-source", "以后都自动写入"))
	base := conversationRule(source.Seq)
	for _, mutate := range []func(*conversationPolicyChange){
		func(q *conversationPolicyChange) { q.Change.SourceSeq = agent.Seq },
		func(q *conversationPolicyChange) { q.Change.SourceText = "伪造用户原话" },
		func(q *conversationPolicyChange) { q.Change.SourceSeq = 999 },
		func(q *conversationPolicyChange) { q.Change.TurnNumber = 0 },
		func(q *conversationPolicyChange) { q.Change.TurnNumber = 2 },
		func(q *conversationPolicyChange) { q.Change.Kind = "decision" },
		func(q *conversationPolicyChange) { q.Change.Scope.Project = "" },
		func(q *conversationPolicyChange) { q.Mode = "unknown" },
	} {
		q := base
		mutate(&q)
		if _, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), q); err == nil {
			t.Fatal("invalid evidence accepted", q)
		}
	}
	other := agentCaller("B", "generation-B")
	if _, err := svc.referencePolicyChange(h.ctx, h.id, other, base); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	original := h.store.ops
	h.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("no disk") }
	if v, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), base); err == nil || v.PolicyToken != "" || v.Record.Version != 0 {
		t.Fatal("failed save reported remembered", v, err)
	}
	h.store.ops = original
	if view, err := h.store.readPolicy(h.id); err != nil || view.Revision != 0 {
		t.Fatal(view, err)
	}
	h.store.ops.syncDir = func(string) error { return errors.New("fsync failed after rename") }
	if v, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), base); !errors.Is(err, ErrCommitUnknown) || v.PolicyToken != "" || v.Record.Version != 0 {
		t.Fatal(v, err)
	}
	if _, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), base); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	h.store.ops = original
	reloadConversation(t, h)
	inboxRunning(t, h, 2, 1)
	svc = referenceTestService(h)
	got, err := h.store.readPolicy(h.id)
	if err != nil || got.Revision != 1 || len(got.Effective) != 1 {
		t.Fatal(got, err)
	}
	base.Change.TurnNumber = 2
	if v, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), base); err != nil || v.Revision != 1 {
		t.Fatal(v, err)
	}
	// Uncertain persistence never falls back to an older broader rule in service.
	// R4 must consume the returned failure before making any execution attempt.
}

func TestReferenceConversationPolicyConcurrentCASAndExpiry(t *testing.T) {
	h, svc, _, _ := conversationHarness(t, 1)
	source := postConversationReply(t, h, "explicit", "以后文档咨询自动回复", 0)
	q := conversationRule(source.Seq)
	expires := time.Now().Add(time.Hour)
	q.Change.ExpiresAt = &expires
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), q); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err := h.store.readPolicy(h.id)
	if err != nil || p.Revision != 1 {
		t.Fatal(p, err)
	}
	effective := agentPolicy(p.PolicySnapshot, 1, expires.Add(time.Second))
	if len(effective.Effective) != 0 {
		t.Fatal(effective)
	}
}
