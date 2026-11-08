//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func TestReferenceCrossPlatformManagedChain(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	r := managerCreate(t, m, "reference-contract")
	svc := eventLiveService(t, &eventHarness{m: m, room: r})
	state, err := svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: r.RoomIdentity, AgentID: state.Members[0].AgentID, MembershipID: state.Members[0].MembershipID}
	adapter := &referenceContractAdapter{}
	adapter.install(m, svc)
	server := httptest.NewServer(m)
	defer server.Close()
	cli := contractCLI(t, server.URL, svc, id)
	for i := 1; i <= 3; i++ {
		contractDeliver(t, m, svc, id, contractSource(fmt.Sprint(i), "conversation-blue"), "本次需要确认后回复")
	}
	if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 3, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 3}}); err != nil {
		t.Fatal(err)
	}
	var page agentReferenceInboxPage
	if err := cli([]string{"inbox", "read"}, &page); err != nil || len(page.Followups) != 1 || len(page.Entries) != 3 {
		t.Fatal(page, err)
	}
	var detail struct {
		Unit    agentFollowupUnit     `json:"unit"`
		Target  agentStoredTarget     `json:"target"`
		Actions []platform.ActionSpec `json:"registered_actions"`
	}
	summary := page.Followups[0]
	read := []string{"matters", "read", "--object", summary.ObjectRef, "--unit", summary.Ref, "--turn", "1"}
	if err := cli(read, &detail); err != nil || len(detail.Target.Target.Containers) != 0 || len(detail.Actions) != 1 || detail.Target.Target.Message.Valid() {
		t.Fatal(detail, err)
	}
	u := followupFixture(detail.Unit)
	q := followupQuestionChange{followupCommand: conversationCommand(u, 1), Text: "@human 本次回复准确正文：已收到。可以吗？", MessageRefs: []string{page.Entries[0].Source.TargetRef}, Proposed: &followupIntent{Operation: platform.OperationObjectComment, Body: "已收到"}}
	var notified agentFollowupReceipt
	if err := cli(entryChange(t, "matters", "notify", q), &notified); err != nil {
		t.Fatal(err)
	}
	answer, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "contract-answer", ReplyTo: notified.Question.Seq, Text: "可以，就这样回复"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cli(read, &detail); err != nil || len(detail.Unit.Questions[0].Answers) != 1 {
		t.Fatal(detail, err)
	}
	u = followupFixture(detail.Unit)
	policy, err := svc.store.readAgentPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	action := referenceActionRequest{followupCommand: conversationCommand(u, 1), TargetRef: u.TargetRef, ID: "contract-answer", PolicyToken: policy.PolicyToken,
		Authority: referenceActionAuthority{RoomSeq: answer.Seq, QuestionRef: u.Questions[0].Ref, Reason: "用户明确回复本问题"}, Operation: platform.OperationObjectComment, Input: CommentInput{Body: "已收到"}}
	var receipt referenceActionReceipt
	wrongAuthority := action
	wrongAuthority.Authority.RoomSeq = u.Questions[0].Seq
	if err := cli(referenceExecuteArgs(t, wrongAuthority), nil); err == nil {
		t.Fatal("Agent question accepted as human action authority")
	}
	if err := cli(referenceExecuteArgs(t, action), &receipt); err != nil || receipt.Status != "succeeded" || receipt.Attempts != 1 || receipt.Result.Account.ID.Value != "account-alice" {
		t.Fatal(receipt, err)
	}
	if err := cli(referenceExecuteArgs(t, action), &receipt); err != nil {
		t.Fatal(err)
	}
	// Unsupported discussions cannot silently become an object comment.
	action.ID, action.Operation, action.Revision = "unsupported-reply", platform.OperationDiscussionReply, receipt.UnitVersion
	if err := cli(referenceExecuteArgs(t, action), nil); err == nil {
		t.Fatal("unsupported reply accepted")
	}
	wrong := id
	wrong.MembershipID = state.Members[1].MembershipID
	if err := contractCLI(t, server.URL, svc, wrong)(read, nil); err == nil {
		t.Fatal("cross-member reference accepted")
	}
	adapter.mu.Lock()
	calls, sends := adapter.resolves, adapter.sends
	adapter.mu.Unlock()
	if calls != 3 || sends != 1 {
		t.Fatal("unexpected adapter calls", calls, sends)
	}
	if err := cli(read, &detail); err != nil {
		t.Fatal(err)
	}
	before, err := svc.store.readFollowup(id, u.ObjectRef, u.Ref)
	if err != nil {
		t.Fatal(err)
	}
	// Close/reopen Store and service: identity and original human evidence survive.
	if err := svc.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	delete(m.runtimes, r.RoomIdentity)
	m.mu.Unlock()
	restored := eventLiveService(t, &eventHarness{m: m, room: r})
	adapter.install(m, restored)
	cli = contractCLI(t, server.URL, restored, id)
	// Human detail reads don't start a Session or require a running turn.
	req := apiRequest{ServiceInfo: restored.info, Caller: humanCaller, AgentID: id.AgentID, MembershipID: id.MembershipID, Followup: &followupRequest{followupCommand: conversationCommand(before, 1)}}
	got, err := restored.memberFollowups(context.Background(), req, "/v1/matters/read")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	var humanDetail followupReadResult
	if json.Unmarshal(b, &humanDetail) != nil || !reflect.DeepEqual(before, humanDetail.Unit) {
		t.Fatal("recovery changed evidence")
	}
	if detail.Unit.Questions[0].Answers[0].Message.Text != "可以，就这样回复" {
		t.Fatal("human evidence lost")
	}
	// Same ID returns the receipt in a new service without inspect or send.
	action.Operation, action.ID = platform.OperationObjectComment, "contract-answer"
	if err := cli(referenceExecuteArgs(t, action), &receipt); err != nil || receipt.Status != "succeeded" {
		t.Fatal(receipt, err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.sends != 1 || adapter.resolves != calls {
		t.Fatal("recovery replayed adapter")
	}
	t.Logf("3 notifications -> 1 object, resolves=%d sends=%d questions=1 repeated_questions=0 required_UI=0 list_bytes=%d detail_bytes=%d", calls, adapter.sends, len(mustContractJSON(page.Followups)), len(b))
}
