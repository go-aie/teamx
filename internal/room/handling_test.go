package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func handlingHarness(t *testing.T) (*inboxHarness, ActionChange) {
	t.Helper()
	h := newInboxHarness(t, InboxLimits{})
	inboxCall[InboxReceipt](t, h, h.receive("handle"))
	inboxRunning(t, h, 1, 1)
	p := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
	linkInboxPage(t, h, p)
	view, err := h.store.changePolicy(h.id, humanCaller, policyRule())
	if err != nil {
		t.Fatal(err)
	}
	r := h.receive("handle").Item.Source.Reference
	return h, ActionChange{MatterCommand: MatterCommand{Seq: 1, MatterID: "main", MatterVersion: 1, TurnNumber: 1}, ID: "reply-1",
		Target: ActionTarget{Tool: "gitlab", Provider: "gitlab", Instance: "https://gitlab.example.com", Account: "bot", ProjectID: r.ProjectID, Object: r.Object, Operation: "comment", URL: "https://gitlab.example.com/a/b/-/merge_requests/42"},
		Intent: "按照已核实文档回复使用方式", Verification: "GET MR42 and notes: no prior reply, docs checked; tools list: bot",
		Authority: ActionAuthority{PolicyRevision: view.Revision, RuleID: "usage-answers", RuleVersion: 1, Reason: "使用问题有当前文档依据，无交付承诺"}}
}

func mustAction(t *testing.T, h *inboxHarness, c ActionChange, prepare bool) MatterRecord {
	t.Helper()
	r, err := h.store.changeAction(h.id, c, prepare)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestHandlingActionIntentFencingAndRecovery(t *testing.T) {
	h, c := handlingHarness(t)
	r := mustAction(t, h, c, true)
	if r.Matters[0].Status != "unknown" || r.Matters[0].Actions[0].Source.Source.Text != policyRule().SourceText {
		t.Fatal(r)
	}
	for _, id := range []string{c.ID} {
		bad := c
		bad.MatterVersion, bad.ID = 2, id
		if _, err := h.store.changeAction(h.id, bad, true); !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if _, err := h.store.updateMatter(h.id, MatterUpdate{Seq: 1, ID: "main", Version: 2, TurnNumber: 1, Status: "completed", Summary: "read", Evidence: "read notice"}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	h.reload(t, true)
	inboxRunning(t, h, 2, 1)
	result := ActionChange{MatterCommand: c.MatterCommand, ID: c.ID, Version: 1, Status: "succeeded", Evidence: "GET notes: exact body and note 99 found"}
	result.MatterVersion = 2
	if _, err := h.store.changeAction(h.id, result, false); !errors.Is(err, ErrConflict) {
		t.Fatal("old turn accepted", err)
	}
	result.TurnNumber = 2
	if _, err := h.store.changeAction(h.id, result, false); !errors.Is(err, ErrRequest) {
		t.Fatal("recovery skipped query", err)
	}
	result.Reconciliation = "GET notes for exact MR42, bot and intended body; found note99"
	r = mustAction(t, h, result, false)
	if r.Matters[0].Status == "completed" || r.Matters[0].Actions[0].TurnNumber != 1 || r.Matters[0].Actions[0].LastTurn != 2 {
		t.Fatal(r)
	}
	if _, err := h.store.changeAction(h.id, result, false); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	r, err := h.store.updateMatter(h.id, MatterUpdate{Seq: 1, ID: "main", Version: 3, TurnNumber: 2, Status: "waiting_followup", Summary: "已回复", Next: "等待对方确认", Evidence: "note99"})
	if err != nil || r.Matters[0].Actions[0].Status != "succeeded" {
		t.Fatal(r, err)
	}
}

func TestHandlingFreshAuthorityAndAccurateTargets(t *testing.T) {
	h, c := handlingHarness(t)
	for _, mutate := range []func(*ActionChange){
		func(c *ActionChange) { c.Authority.PolicyRevision = 0 },
		func(c *ActionChange) { c.Authority.RuleVersion = 2 },
		func(c *ActionChange) { c.Target.ProjectID.Value = "999" },
		func(c *ActionChange) { c.Target.Object.ID.Value = "42" },
		func(c *ActionChange) { c.Target.Instance = "https://elsewhere.example.com" },
		func(c *ActionChange) { c.Target.Account = "other-account" },
		func(c *ActionChange) { c.Target.URL = "https://elsewhere.example.com/a/b" },
		func(c *ActionChange) { c.Authority.RoomSeq = 1 },
	} {
		bad := c
		mutate(&bad)
		if _, err := h.store.changeAction(h.id, bad, true); err == nil {
			t.Fatal("bad preflight accepted", bad)
		}
	}
	rule := policyRule()
	rule.Version, rule.Revision, rule.Status = 1, 1, "revoked"
	if _, err := h.store.changePolicy(h.id, humanCaller, rule); err != nil {
		t.Fatal(err)
	}
	c.Authority.PolicyRevision = 2
	if _, err := h.store.changeAction(h.id, c, true); !errors.Is(err, ErrConflict) {
		t.Fatal("revoked rule accepted", err)
	}
	decision := PolicyChange{ID: "one-off", Kind: "decision", Status: "active", Text: "仅本次按文档回复", Scope: PolicyScope{Description: "MR42 main"}, Actions: "回复使用说明", SourceText: "本次按文档回复", Seq: 1, MatterID: "main", MatterVersion: 1}
	if _, err := h.store.changeDecision(h.id, humanCaller, decision); err != nil {
		t.Fatal(err)
	}
	c.MatterVersion = 2
	c.Authority = ActionAuthority{PolicyRevision: 2, DecisionID: "one-off", DecisionVersion: 1, Reason: "当前事项单次明确回复，无扩大范围"}
	mustAction(t, h, c, true)
	p, err := h.store.readPolicy(h.id)
	if err != nil || len(p.Effective) != 0 {
		t.Fatal("one-off leaked to standing rules", p, err)
	}
}

func TestHandlingCommunicationCorrelatesHumanAndCoversProgress(t *testing.T) {
	h, c := handlingHarness(t)
	if matterNoticeID(h.id, 1, "a-b", "c") == matterNoticeID(h.id, 1, "a", "b-c") {
		t.Fatal("ambiguous notice identity")
	}
	svc := &service{store: h.store, room: h.roomHarness.addr}
	nc := NoticeChange{MatterCommand: c.MatterCommand, ID: "important", Kind: "result", Text: "重要核实结果"}
	r, err := svc.notifyMatter(h.ctx, h.id, nc)
	if err != nil {
		t.Fatal(err)
	}
	nc.MatterVersion, nc.ID, nc.Text = r.Matters[0].Version, "repeat-different-wording", "同一结果改写"
	if _, err := svc.notifyMatter(h.ctx, h.id, nc); !errors.Is(err, ErrConflict) {
		t.Fatal("unchanged progress re-reported", err)
	}
	nc.ID, nc.Kind, nc.Text = "ask", "question", "@"+humanOperator()+" 需要选择日期 A 或 B。"
	r, err = svc.notifyMatter(h.ctx, h.id, nc)
	if err != nil {
		t.Fatal(err)
	}
	question := r.Matters[0].Notices[1]
	agent := roomCall[PostReceipt](t, h.roomHarness, h.post(agentCaller("B", "generation-B"), "third-party", "A"))
	otherQuestion := roomCall[PostReceipt](t, h.roomHarness, h.post(agentCaller("B", "generation-B"), "other-question", "另一个问题"))
	wrong := h.post(humanCaller, "wrong-answer", "A")
	wrong.ReplyTo = otherQuestion.Seq
	wrongReceipt := roomCall[PostReceipt](t, h.roomHarness, wrong)
	link := MatterReplyChange{MatterCommand: nc.MatterCommand, QuestionID: question.ID, Basis: "attempt to correlate"}
	link.MatterVersion = r.Matters[0].Version
	for _, seq := range []uint64{agent.Seq, wrongReceipt.Seq} {
		link.SourceSeq = seq
		if _, err := h.store.linkMatterReply(h.id, link); !errors.Is(err, ErrIdentity) {
			t.Fatal("unrelated answer accepted", err)
		}
	}
	answer := h.post(humanCaller, "vague", "好")
	answer.ReplyTo = question.Seq
	ack := roomCall[PostReceipt](t, h.roomHarness, answer)
	link.SourceSeq = ack.Seq
	r, err = h.store.linkMatterReply(h.id, link)
	if err != nil {
		t.Fatal(err)
	}
	if r.Matters[0].Decisions != nil || r.Matters[0].Notices[1].Answers[0].Message.Text != "好" {
		t.Fatal("vague answer became effective decision", r)
	}
	// Human changes while a prepared public message is in flight must survive
	// receipt installation; the receipt only updates its own frozen record.
	nc.ID, nc.Kind, nc.Text, nc.MatterVersion = "new-result", "result", "新事实结果", r.Matters[0].Version
	r, prepared, err := h.store.prepareMatterNotice(h.id, nc)
	if err != nil {
		t.Fatal(err)
	}
	decision := PolicyChange{ID: "restrict", Kind: "decision", Status: "active", Text: "任何日期承诺先询问", Scope: PolicyScope{Description: "本事项"}, Actions: "仅准备草稿", SourceText: "仅准备草稿", Seq: 1, MatterID: "main", MatterVersion: r.Matters[0].Version}
	r, err = h.store.changeDecision(h.id, humanCaller, decision)
	if err != nil {
		t.Fatal(err)
	}
	receipt := roomCall[PostReceipt](t, h.roomHarness, PostMessage{RoomIdentity: h.id.RoomIdentity, Caller: agentCaller(h.id.AgentID, h.id.MembershipID), ClientMessageID: prepared.ClientMessageID, Text: prepared.Text, MatterNotice: true})
	r, err = h.store.finishMatterNotice(h.id, nc, prepared, receipt)
	if err != nil || r.Matters[0].Decisions.History[0].Text != "任何日期承诺先询问" {
		t.Fatal(r, err)
	}
}

func TestHandlingUncertaintyRequiresQueryAndDurableIntent(t *testing.T) {
	h, c := handlingHarness(t)
	mustAction(t, h, c, true)
	result := ActionChange{MatterCommand: c.MatterCommand, ID: c.ID, Version: 1, Status: "unknown", Evidence: "tool timed out"}
	result.MatterVersion = 2
	mustAction(t, h, result, false)
	result.MatterVersion, result.Version, result.Status, result.Evidence = 3, 2, "succeeded", "success guessed"
	if _, err := h.store.changeAction(h.id, result, false); !errors.Is(err, ErrRequest) {
		t.Fatal("unknown overwritten without query", err)
	}
	result.Status, result.Evidence, result.Reconciliation = "not_applied", "not applied", "complete GET notes shows no intended body; no retry authorized"
	mustAction(t, h, result, false)
	// An uncertain durable intent must freeze writes before any tool could run.
	h2, c2 := handlingHarness(t)
	h2.store.ops.syncDir = func(string) error { return errors.New("injected fsync") }
	if _, err := h2.store.changeAction(h2.id, c2, true); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if _, err := h2.store.changeAction(h2.id, c2, true); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	h2.reload(t, true)
	page, err := h2.store.listMatters(h2.id, "", 0, 25)
	if err != nil || page.Records[0].Matters[0].Actions[0].Status != "unknown" {
		t.Fatal(page, err)
	}
}

func TestHandlingExplicitTaskEvidenceSurvivesClear(t *testing.T) {
	h, c := handlingHarness(t)
	msg := roomCall[PostReceipt](t, h.roomHarness, h.post(humanCaller, "exact-task", "本次允许在 MR42 分别回复 A 和 B；禁止其他写入。"))
	c.Authority = ActionAuthority{PolicyRevision: 1, RoomSeq: msg.Seq, Reason: "本次明确允许的第一条回复 A"}
	r := mustAction(t, h, c, true)
	result := ActionChange{MatterCommand: c.MatterCommand, ID: c.ID, Version: 1, Status: "succeeded", Evidence: "GET notes confirms A"}
	result.MatterVersion = r.Matters[0].Version
	r = mustAction(t, h, result, false)
	roomCall[ClearReceipt](t, h.roomHarness, ClearMessages{RoomIdentity: h.id.RoomIdentity, Caller: humanCaller, ThroughSeq: msg.Seq})
	h.reload(t, true)
	c.ID, c.Intent, c.MatterVersion, c.Authority.Reason = "reply-B", "回复 B", r.Matters[0].Version, "原任务准确允许的第二条回复 B，重新核实当前限制"
	r = mustAction(t, h, c, true)
	if r.Matters[0].Actions[1].Source.Source.Text != "本次允许在 MR42 分别回复 A 和 B；禁止其他写入。" {
		t.Fatal("lost explicit task provenance")
	}
}

func TestHandlingNoticeCommitGapClearAndReply(t *testing.T) {
	h, c := handlingHarness(t)
	nc := NoticeChange{MatterCommand: c.MatterCommand, ID: "date-question", Kind: "question", Text: "@" + humanOperator() + " MR42 需要确定日期，请选择 A 或 B。"}
	r, n, err := h.store.prepareMatterNotice(h.id, nc)
	if err != nil {
		t.Fatal(err)
	}
	post := PostMessage{RoomIdentity: h.id.RoomIdentity, Caller: agentCaller(h.id.AgentID, h.id.MembershipID), ClientMessageID: n.ClientMessageID, Text: n.Text, MatterNotice: true}
	receipt := roomCall[PostReceipt](t, h.roomHarness, post)
	// Crash gap: Room committed, no member receipt. Clear before recovery.
	roomCall[ClearReceipt](t, h.roomHarness, ClearMessages{RoomIdentity: h.id.RoomIdentity, Caller: humanCaller, ThroughSeq: receipt.Seq})
	h.reload(t, true)
	room, err := NewRoom(h.store)
	if err != nil {
		t.Fatal(err)
	}
	h.roomHarness.addr, err = h.rt.Spawn(room)
	if err != nil {
		t.Fatal(err)
	}
	nc.MatterVersion = r.Matters[0].Version
	svc := &service{store: h.store, room: h.roomHarness.addr}
	r, err = svc.notifyMatter(h.ctx, h.id, nc)
	if err != nil || r.Matters[0].Notices[0].Seq != receipt.Seq {
		t.Fatal(r, err)
	}
	state, err := h.store.LoadRoom()
	if err != nil || len(state.Messages) != 0 || state.LatestSeq != receipt.Seq {
		t.Fatal("resent after clear", state, err)
	}
	// Repeating unchanged questions with a different ID is rejected.
	nc.MatterVersion, nc.ID = r.Matters[0].Version, "different-id"
	if _, err := svc.notifyMatter(h.ctx, h.id, nc); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	answer := roomCall[PostReceipt](t, h.roomHarness, h.post(humanCaller, "answer", "本次允许回复 A；以后仍先问日期"))
	link := MatterReplyChange{MatterCommand: nc.MatterCommand, QuestionID: n.ID, SourceSeq: answer.Seq, Basis: "用户明确指出本次 MR42 日期问题"}
	r, err = h.store.linkMatterReply(h.id, link)
	if err != nil || r.Matters[0].Status != "pending" || r.Matters[0].Notices[0].Answers[0].Message.Text != "本次允许回复 A；以后仍先问日期" {
		t.Fatal(r, err)
	}
	roomCall[ClearReceipt](t, h.roomHarness, ClearMessages{RoomIdentity: h.id.RoomIdentity, Caller: humanCaller, ThroughSeq: answer.Seq})
	c.MatterVersion = r.Matters[0].Version
	c.Authority = ActionAuthority{PolicyRevision: 1, RoomSeq: answer.Seq, Reason: "仅本次回复 A，后续日期仍须询问"}
	r = mustAction(t, h, c, true)
	if r.Matters[0].Actions[0].Source.Source.Text != "本次允许回复 A；以后仍先问日期" {
		t.Fatal(r)
	}
	p, err := h.store.readPolicy(h.id)
	if err != nil || p.Revision != 1 {
		t.Fatal("reply promoted to policy", p, err)
	}
}

func TestHandlingRetiredAgentCommandsAndGuidance(t *testing.T) {
	h, c := handlingHarness(t)
	svc := &service{info: ServiceInfo{RoomIdentity: h.id.RoomIdentity, ServiceEpoch: "test-service"}, store: h.store, room: h.roomHarness.addr, requests: make(chan struct{}, 32)}
	for _, path := range []string{"/v1/actions/prepare", "/v1/actions/record"} {
		req := apiRequest{ServiceInfo: svc.info, Caller: agentCaller(h.id.AgentID, h.id.MembershipID), Action: &c}
		body, _ := json.Marshal(req)
		w := httptest.NewRecorder()
		svc.ServeHTTP(w, httptest.NewRequest("POST", path, bytes.NewReader(body)))
		if w.Code != 403 {
			t.Fatal(w.Code, w.Body)
		}
	}
	text := dshNotification(UnreadChanged{})
	for _, s := range []string{"actions execute", "matters notify", "matters reply", "successful reply is distinct", "at most twice"} {
		if !strings.Contains(text, s) {
			t.Fatal(fmt.Sprintf("missing %q", s))
		}
	}
	for _, s := range []string{"actions prepare", "actions record"} {
		if strings.Contains(text, s) || strings.Contains(handlingHelp, s) {
			t.Fatal("retired command exposed", s)
		}
	}
}
