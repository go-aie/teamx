package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func conversationHarness(t *testing.T, n int) (*inboxHarness, *service, []followupUnit, []followupSourceSummary) {
	t.Helper()
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	for i := 0; i < n; i++ {
		source := referenceTestSource(fmt.Sprint(i))
		source.Reference.DiscussionID = platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: fmt.Sprintf("thread-%d", i)}
		source.Reference.NoteID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: fmt.Sprint(100 + i)}
		receiveReference(t, h, fmt.Sprint(i), source)
	}
	inboxRunning(t, h, 1, uint64(n))
	svc := referenceTestService(h)
	page, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, referenceTestResolver)
	if err != nil {
		t.Fatal(err)
	}
	units := []followupUnit{}
	for _, source := range page.Sources {
		u, err := h.store.ensureDiscussionFollowup(h.id, source.Seq, 1, source.TargetRef)
		if err != nil {
			t.Fatal(err)
		}
		units = append(units, u)
	}
	return h, svc, units, page.Sources
}

func conversationCommand(u followupUnit, turn uint64) followupCommand {
	return followupCommand{ObjectRef: u.ObjectRef, UnitRef: u.Ref, Revision: u.Revision, TurnNumber: turn}
}

func reloadConversation(t *testing.T, h *inboxHarness) {
	t.Helper()
	h.reload(t, true)
	r, err := NewRoom(h.store)
	if err != nil {
		t.Fatal(err)
	}
	h.roomHarness.addr, err = h.rt.Spawn(r)
	if err != nil {
		t.Fatal(err)
	}
}

func askConversation(t *testing.T, h *inboxHarness, svc *service, u followupUnit, messageRef, text string) followupUnit {
	t.Helper()
	got, err := svc.askFollowup(h.ctx, h.id, followupQuestionChange{followupCommand: conversationCommand(u, 1), MessageRefs: []string{messageRef}, Text: text,
		Proposed: &followupIntent{Operation: platform.OperationDiscussionReply, Body: "本次按方案 A 答复", Constraints: "仅回答此条原生消息的排期问题"}})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func postConversationReply(t *testing.T, h *inboxHarness, key, text string, replyTo uint64) PostReceipt {
	t.Helper()
	post := h.post(humanCaller, key, text)
	post.ReplyTo = replyTo
	return roomCall[PostReceipt](t, h.roomHarness, post)
}

func TestReferenceConversationImmediateAnswerAndRecovery(t *testing.T) {
	h, svc, units, sources := conversationHarness(t, 1)
	u := askConversation(t, h, svc, units[0], sources[0].TargetRef, "本次排期按方案 A 回复，可以吗？")
	q := u.Questions[0]
	// Retrying even with the original version never asks again.
	retry, err := svc.askFollowup(h.ctx, h.id, followupQuestionChange{followupCommand: conversationCommand(units[0], 1), MessageRefs: []string{sources[0].TargetRef}, Text: q.Text, Proposed: q.Proposed})
	if err != nil || !reflect.DeepEqual(retry, u) {
		t.Fatal(retry, err)
	}
	answer := postConversationReply(t, h, "answer", "这次按方案 A 回复", q.Seq)
	u, err = svc.readFollowupConversation(h.ctx, h.id, conversationCommand(u, 1))
	if err != nil || u.Status != "pending" || len(u.Questions[0].Answers) != 1 || len(u.Instructions) != 0 {
		t.Fatal(u, err)
	}
	c := followupInstructionChange{followupCommand: conversationCommand(u, 1), QuestionRef: q.Ref, SourceSeq: answer.Seq, Intent: *q.Proposed, Basis: "用户准确回答了本次排期问题"}
	authority, err := h.store.followupAnswerBasis(h.id, c)
	if err != nil || authority.Source.Text != "这次按方案 A 回复" || authority.TargetRef != u.TargetRef || len(authority.MessageRefs) != 1 {
		t.Fatal(authority, err)
	}
	// Mock continuation only: no R4 adapter, no remote write, and no UI decision.
	calls := 0
	mockContinue := func(b followupReplyBasis) {
		if b.QuestionRef != q.Ref || b.Intent.Body != q.Proposed.Body {
			t.Fatal(b)
		}
		calls++
	}
	mockContinue(authority)
	u, err = h.store.updateFollowup(h.id, followupUpdate{ObjectRef: u.ObjectRef, UnitRef: u.Ref, Revision: u.Revision, TurnNumber: 1, Status: "completed", Summary: "本次业务已确认", Evidence: "模拟办理结果"})
	if err != nil || calls != 1 {
		t.Fatal(u, err)
	}
	roomCall[ClearReceipt](t, h.roomHarness, ClearMessages{RoomIdentity: h.id.RoomIdentity, Caller: humanCaller, ThroughSeq: answer.Seq})
	reloadConversation(t, h)
	inboxRunning(t, h, 2, 1)
	svc = referenceTestService(h)
	restored, err := svc.readFollowupConversation(h.ctx, h.id, conversationCommand(u, 2))
	if err != nil || !reflect.DeepEqual(u, restored) {
		t.Fatal(restored, err)
	}
	c.followupCommand = conversationCommand(restored, 2)
	if got, err := h.store.followupAnswerBasis(h.id, c); err != nil || !reflect.DeepEqual(got, authority) {
		t.Fatal(got, err)
	}
	// Saved question references, human original and source linkage survive clear.
	p, err := h.store.listFollowups(h.id, followupQuery{Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range p.Items {
		if item.Ref == u.Ref {
			t.Fatal("completed conversation injected as active", item)
		}
	}
	if _, err := os.Stat(filepath.Join(h.store.dir, "agents", h.id.MembershipID, "matters")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("legacy writes", err)
	}
}

func TestReferenceConversationAmbiguousAnswersAndAccurateScope(t *testing.T) {
	h, svc, units, sources := conversationHarness(t, 2)
	a := askConversation(t, h, svc, units[0], sources[0].TargetRef, "讨论一：采用 A？")
	b := askConversation(t, h, svc, units[1], sources[1].TargetRef, "讨论二：采用 A？")
	answer := postConversationReply(t, h, "ambiguous", "可以", 0)
	if err := h.store.syncFollowupReplies(h.id, conversationCommand(a, 1)); err != nil {
		t.Fatal(err)
	}
	for _, u := range []followupUnit{a, b} {
		if got, err := h.store.readFollowup(h.id, u.ObjectRef, u.Ref); err != nil || len(got.Questions[0].Answers) != 0 {
			t.Fatal("guessed latest question", got, err)
		}
	}
	if _, err := h.store.linkFollowupReply(h.id, followupReplyChange{followupCommand: conversationCommand(b, 1), SourceSeq: answer.Seq}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	// A clarification identifies only one question; other work stays waiting.
	clarification := postConversationReply(t, h, "clarify", "我说的是讨论一的方案 A", 0)
	a, err := h.store.linkFollowupReply(h.id, followupReplyChange{followupCommand: conversationCommand(a, 1), QuestionRef: a.Questions[0].Ref, SourceSeq: clarification.Seq, Basis: "用户明确指出讨论一"})
	if err != nil || a.Status != "pending" {
		t.Fatal(a, err)
	}
	wrong := postConversationReply(t, h, "wrong", "可以", b.Questions[0].Seq)
	if _, err := h.store.linkFollowupReply(h.id, followupReplyChange{followupCommand: conversationCommand(a, 1), QuestionRef: a.Questions[0].Ref, SourceSeq: wrong.Seq, Basis: "不能覆盖显式回复目标"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	c := followupInstructionChange{followupCommand: conversationCommand(a, 1), QuestionRef: a.Questions[0].Ref, SourceSeq: clarification.Seq, Intent: *a.Questions[0].Proposed, Basis: "本次准确方案"}
	bad := c
	bad.Intent.Body = "以后所有排期都采用 A"
	if _, err := h.store.followupAnswerBasis(h.id, bad); !errors.Is(err, ErrConflict) {
		t.Fatal("broadened answer", err)
	}
	bad = c
	bad.SourceSeq = answer.Seq
	if _, err := h.store.followupAnswerBasis(h.id, bad); !errors.Is(err, ErrIdentity) {
		t.Fatal("unassociated human accepted", err)
	}
	bad = c
	bad.QuestionRef = b.Questions[0].Ref
	if _, err := h.store.saveFollowupInstruction(h.id, bad); err == nil {
		t.Fatal("cross discussion instruction")
	}
	if _, err := svc.askFollowup(h.ctx, h.id, followupQuestionChange{followupCommand: conversationCommand(a, 1), MessageRefs: []string{sources[1].TargetRef}, Text: "错误原生消息"}); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	// Changing wording cannot repeatedly ask the same pending question.
	if _, err := svc.askFollowup(h.ctx, h.id, followupQuestionChange{followupCommand: conversationCommand(b, 1), MessageRefs: []string{sources[1].TargetRef}, Text: "讨论二再问一次"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestReferenceConversationDeferredInstructionsAndNewQuestions(t *testing.T) {
	h, svc, units, sources := conversationHarness(t, 1)
	u := askConversation(t, h, svc, units[0], sources[0].TargetRef, "本次按 A？")
	answer := postConversationReply(t, h, "defer", "按 A，工具恢复后再处理", u.Questions[0].Seq)
	u, err := svc.readFollowupConversation(h.ctx, h.id, conversationCommand(u, 1))
	if err != nil {
		t.Fatal(err)
	}
	c := followupInstructionChange{followupCommand: conversationCommand(u, 1), QuestionRef: u.Questions[0].Ref, SourceSeq: answer.Seq, Intent: *u.Questions[0].Proposed, Basis: "跨回合落实本次答复"}
	u, err = h.store.saveFollowupInstruction(h.id, c)
	if err != nil || len(u.Instructions) != 1 {
		t.Fatal(u, err)
	}
	if got, err := h.store.saveFollowupInstruction(h.id, c); err != nil || !reflect.DeepEqual(got, u) {
		t.Fatal(got, err)
	}
	if _, err := h.store.updateFollowup(h.id, followupUpdate{ObjectRef: u.ObjectRef, UnitRef: u.Ref, Revision: u.Revision, TurnNumber: 1, Status: "completed", Summary: "尚未落实", Evidence: "不能越过待落实指令"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	page, err := h.store.listFollowups(h.id, followupQuery{Limit: 25})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range page.Items {
		if item.Ref == u.Ref {
			found = len(item.Instructions) == 1 && len(item.Questions) == 0
		}
	}
	if !found {
		t.Fatal(page)
	}
	roomCall[ClearReceipt](t, h.roomHarness, ClearMessages{RoomIdentity: h.id.RoomIdentity, Caller: humanCaller, ThroughSeq: answer.Seq})
	reloadConversation(t, h)
	inboxRunning(t, h, 2, 1)
	restored, err := h.store.readFollowup(h.id, u.ObjectRef, u.Ref)
	if err != nil || !reflect.DeepEqual(restored, u) {
		t.Fatal(restored, err)
	}
	if _, err := h.store.finishFollowupInstruction(h.id, conversationCommand(u, 1), u.Instructions[0].Ref, 1, "fulfilled", "模拟办理已核实", 0); !errors.Is(err, ErrConflict) {
		t.Fatal("old turn", err)
	}
	u, err = h.store.finishFollowupInstruction(h.id, conversationCommand(u, 2), u.Instructions[0].Ref, 1, "fulfilled", "模拟办理已核实", 0)
	if err != nil || activeFollowupInstruction(u.Instructions[0], time.Now()) {
		t.Fatal(u, err)
	}
	c.followupCommand = conversationCommand(u, 2)
	got, err := h.store.saveFollowupInstruction(h.id, c)
	if err != nil || got.Instructions[0].Status != "fulfilled" {
		t.Fatal("retried instruction reopened", got, err)
	}
	// Later questions in the same native thread cannot reuse the previous answer.
	svc = referenceTestService(h)
	u, err = svc.askFollowup(h.ctx, h.id, followupQuestionChange{followupCommand: conversationCommand(u, 2), MessageRefs: []string{sources[0].TargetRef}, Text: "同讨论后来提出的另一项问题，采用 B？", Proposed: &followupIntent{Operation: platform.OperationDiscussionReply, Body: "方案 B"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.followupAnswerBasis(h.id, followupInstructionChange{followupCommand: conversationCommand(u, 2), QuestionRef: u.Questions[1].Ref, SourceSeq: answer.Seq, Intent: *u.Questions[1].Proposed, Basis: "尝试套用旧答复"}); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
}

func TestReferenceConversationQuestionCommitGapAndSaveFailures(t *testing.T) {
	h, svc, units, sources := conversationHarness(t, 1)
	c := followupQuestionChange{followupCommand: conversationCommand(units[0], 1), MessageRefs: []string{sources[0].TargetRef}, Text: "持久问题？"}
	original := h.store.ops
	h.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("save failed") }
	if _, err := svc.askFollowup(h.ctx, h.id, c); err == nil {
		t.Fatal("failed save reported success")
	}
	h.store.ops = original
	room, err := h.store.LoadRoom()
	if err != nil || room.LatestSeq != 0 {
		t.Fatal("posted before saving", room, err)
	}
	u, q, err := h.store.prepareFollowupQuestion(h.id, c)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: h.id.RoomIdentity, Caller: agentCallerIdentity(h.id), ClientMessageID: q.ClientMessageID, Text: q.Text, FollowupNotice: true})
	if err != nil {
		t.Fatal(err)
	}
	answer := postConversationReply(t, h, "gap-answer", "可以", receipt.Seq)
	// The Room post committed, but the unit receipt was never installed.
	if u.Questions[0].Seq != 0 {
		t.Fatal(u)
	}
	if err := h.store.syncFollowupReplies(h.id, conversationCommand(u, 1)); err != nil {
		t.Fatal(err)
	}
	u, err = h.store.readFollowup(h.id, u.ObjectRef, u.Ref)
	if err != nil || u.Questions[0].Seq != receipt.Seq || u.Questions[0].Answers[0].Message.Seq != answer.Seq {
		t.Fatal(u, err)
	}
	before := u
	h.store.ops.syncDir = func(string) error { return errors.New("unknown directory sync") }
	_, err = h.store.updateFollowup(h.id, followupUpdate{ObjectRef: u.ObjectRef, UnitRef: u.Ref, Revision: u.Revision, TurnNumber: 1, Status: "in_progress", Summary: "核对保存结果"})
	if !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if _, err := svc.askFollowup(h.ctx, h.id, c); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal("unknown write not frozen", err)
	}
	h.store.ops = original
	reloadConversation(t, h)
	u, err = h.store.readFollowup(h.id, u.ObjectRef, u.Ref)
	if err != nil || u.Revision != before.Revision+1 || u.Status != "in_progress" {
		t.Fatal(u, err)
	}
}

func TestReferenceConversationDirectInstructionRevocationAndPendingQuestions(t *testing.T) {
	h, svc, units, sources := conversationHarness(t, 1)
	source := postConversationReply(t, h, "direct", "本条讨论的排期按方案 A 回复，恢复后继续", 0)
	c := followupInstructionChange{followupCommand: conversationCommand(units[0], 1), SourceSeq: source.Seq, MessageRefs: []string{sources[0].TargetRef},
		Intent: followupIntent{Operation: platform.OperationDiscussionReply, Body: "方案 A"}, Basis: "用户直接明确本次目标、正文和继续处理要求"}
	immediate, err := h.store.followupAnswerBasis(h.id, c)
	if err != nil || immediate.QuestionRef != "" || immediate.Source.Seq != source.Seq {
		t.Fatal(immediate, err)
	}
	u, err := h.store.saveFollowupInstruction(h.id, c)
	if err != nil || len(u.Questions) != 0 || len(u.Instructions) != 1 {
		t.Fatal(u, err)
	}
	// Direct instruction never creates a standing rule.
	if p, err := h.store.readPolicy(h.id); err != nil || len(p.Effective) != 0 {
		t.Fatal(p, err)
	}
	revocation := postConversationReply(t, h, "withdraw", "撤销刚才对本条讨论的排期答复指令", 0)
	u, err = h.store.finishFollowupInstruction(h.id, conversationCommand(u, 1), u.Instructions[0].Ref, 1, "revoked", "用户撤销本次回复", revocation.Seq)
	if err != nil || u.Instructions[0].Revocation.Text != "撤销刚才对本条讨论的排期答复指令" {
		t.Fatal(u, err)
	}
	c.followupCommand = conversationCommand(u, 1)
	if _, err := h.store.followupAnswerBasis(h.id, c); !errors.Is(err, ErrConflict) {
		t.Fatal("revoked instruction reused as raw human authority", err)
	}
	// Two distinct questions can share a native discussion; answering one must
	// preserve the other instead of making the whole object look finished.
	u = askConversation(t, h, svc, u, sources[0].TargetRef, "排期现在采用 A 吗？")
	u, err = svc.askFollowup(h.ctx, h.id, followupQuestionChange{followupCommand: conversationCommand(u, 1), Text: "还需要独立确认交付负责人是谁？"})
	if err != nil {
		t.Fatal(err)
	}
	postConversationReply(t, h, "first-only", "本次排期采用 A", u.Questions[0].Seq)
	u, err = svc.readFollowupConversation(h.ctx, h.id, conversationCommand(u, 1))
	if err != nil || u.Status != "waiting_user" || u.Next != u.Questions[1].Text {
		t.Fatal(u, err)
	}
	if _, err := h.store.updateFollowup(h.id, followupUpdate{ObjectRef: u.ObjectRef, UnitRef: u.Ref, Revision: u.Revision, TurnNumber: 1, Status: "completed", Summary: "还留一个问题", Evidence: "不得提前办结"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestReferenceConversationAnswerSaveUnknownAndCapacity(t *testing.T) {
	h, svc, units, sources := conversationHarness(t, 1)
	u := askConversation(t, h, svc, units[0], sources[0].TargetRef, "请确认本次答复")
	answer := postConversationReply(t, h, "save-unknown", "本次按 A", u.Questions[0].Seq)
	original := h.store.ops
	h.store.ops.syncDir = func(string) error { return errors.New("sync failure") }
	if _, err := svc.readFollowupConversation(h.ctx, h.id, conversationCommand(u, 1)); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if _, err := h.store.saveFollowupInstruction(h.id, followupInstructionChange{followupCommand: conversationCommand(u, 1), QuestionRef: u.Questions[0].Ref, SourceSeq: answer.Seq}); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal("continued after unknown save", err)
	}
	h.store.ops = original
	reloadConversation(t, h)
	inboxRunning(t, h, 2, 1)
	svc = referenceTestService(h)
	u, err := svc.readFollowupConversation(h.ctx, h.id, conversationCommand(u, 2))
	if err != nil || len(u.Questions[0].Answers) != 1 || u.Status != "pending" {
		t.Fatal(u, err)
	}
	// Fill the bounded answer set, then reject further growth without dropping
	// old evidence or silently consuming the new answer.
	for n := 1; n < 16; n++ {
		postConversationReply(t, h, fmt.Sprintf("answer-%d", n), "本次补充", u.Questions[0].Seq)
	}
	u, err = svc.readFollowupConversation(h.ctx, h.id, conversationCommand(u, 2))
	if err != nil || len(u.Questions[0].Answers) != 16 {
		t.Fatal(u, err)
	}
	postConversationReply(t, h, "too-many", "最后补充", u.Questions[0].Seq)
	if _, err := svc.readFollowupConversation(h.ctx, h.id, conversationCommand(u, 2)); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	saved, err := h.store.readFollowup(h.id, u.ObjectRef, u.Ref)
	if err != nil || !reflect.DeepEqual(saved, u) {
		t.Fatal("capacity lost existing evidence", saved, err)
	}
}

func TestReferenceConversationByteCapacityPreservesAnswers(t *testing.T) {
	h, svc, units, sources := conversationHarness(t, 1)
	u := askConversation(t, h, svc, units[0], sources[0].TargetRef, "保留完整答复证据？")
	body := strings.Repeat("x", maxTextBytes)
	for n := 0; n < 6; n++ {
		postConversationReply(t, h, fmt.Sprintf("large-%d", n), body, u.Questions[0].Seq)
	}
	u, err := svc.readFollowupConversation(h.ctx, h.id, conversationCommand(u, 1))
	if err != nil || len(u.Questions[0].Answers) != 6 {
		t.Fatal(len(u.Questions[0].Answers), err)
	}
	for n := 6; n < 9; n++ {
		postConversationReply(t, h, fmt.Sprintf("large-%d", n), body, u.Questions[0].Seq)
	}
	if _, err := svc.readFollowupConversation(h.ctx, h.id, conversationCommand(u, 1)); !errors.Is(err, ErrCapacity) {
		t.Fatal("oversized snapshot accepted", err)
	}
	saved, err := h.store.readFollowup(h.id, u.ObjectRef, u.Ref)
	if err != nil || !reflect.DeepEqual(saved, u) {
		t.Fatal("byte limit dropped old human evidence", err)
	}
}
