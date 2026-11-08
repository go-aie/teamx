package room

import (
	"reflect"
	"strings"
	"testing"
)

func TestAgentPolicyCLISaveReceiptAndRead(t *testing.T) {
	h, id, svc := policyTestService(t)
	cli := entryCLI(t, svc)
	source := roomCall[PostReceipt](t, h, h.post(humanCaller, "style", "以后简短中文"))
	q := agentPolicyChange{Type: "save", ID: "style", SourceSeq: source.Seq, TurnNumber: 1,
		Basis: "用户明确长期要求", Body: &agentPolicyBody{Text: "简短中文"}}
	var saved agentPolicyUpdateWireTest
	if err := cli(entryChange(t, "policy", "update", q), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Record.ID != q.ID || saved.Record.Version != 1 || saved.Record.Source.Seq != source.Seq ||
		saved.Record.Text != q.Body.Text || saved.Record.Conversation == nil || saved.Record.Conversation.Basis != q.Basis ||
		saved.Revision != 1 || saved.CurrentTurn != 1 || len(saved.Effective) != 1 || saved.PolicyToken == "" {
		t.Fatal("CLI lost the saved record or current context", saved)
	}
	var read map[string]any
	if err := cli([]string{"policy", "read"}, &read); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"record", "current", "history"} {
		if _, ok := read[field]; ok {
			t.Fatal("default Agent read leaked update or management fields", field, read)
		}
	}
	if read["policy_token"] != saved.PolicyToken {
		t.Fatal("read and update disagree on policy token", read)
	}
	before, err := h.store.readPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	q.ID, q.Revision, q.SourceSeq = "missing-source", 1, 999
	if err := cli(entryChange(t, "policy", "update", q), nil); err == nil ||
		!strings.Contains(err.Error(), "source_seq:") || strings.Contains(err.Error(), "snapshot identity mismatch") {
		t.Fatal("CLI lost the actionable source diagnostic", err)
	}
	assertPolicyUnchanged(t, h.store, id, before)
}

func TestAgentPolicyBranchesAndDetailRecovery(t *testing.T) {
	h, id, svc := policyTestService(t)
	cli := entryCLI(t, svc)
	seq := roomCall[PostReceipt](t, h, h.post(humanCaller, "habit", "这次简短中文回复")).Seq
	q := agentPolicyChange{Type: "suggest", ID: "style", SourceSeq: seq, TurnNumber: 1, Basis: "仅建议，不推断长期许可", Body: &agentPolicyBody{Text: "简短中文；保留必要限制"}}
	var suggestion agentPolicyUpdateWireTest
	if err := cli(entryChange(t, "policy", "update", q), &suggestion); err != nil || suggestion.Record.Version != 1 || len(suggestion.Effective) != 0 {
		t.Fatal(suggestion, err)
	}
	var sent PostReceipt
	if err := cli([]string{"policy", "publish", "--id", "style", "--version", "1", "--turn", "1"}, &sent); err != nil {
		t.Fatal(err)
	}
	confirmSeq := roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: "confirm-style", Text: "以后就按这个建议处理", ReplyTo: sent.Seq}).Seq
	confirm := agentPolicyChange{Type: "confirm", ID: "style", Version: 1, Revision: 1, SourceSeq: confirmSeq, TurnNumber: 1, Basis: "明确确认已公开的原建议", ProposalSeq: sent.Seq}
	var active agentPolicyUpdateWireTest
	if err := cli(entryChange(t, "policy", "update", confirm), &active); err != nil || len(active.Effective) != 1 || active.Record.Text != suggestion.Record.Text || active.Record.Conversation.Proposal.Seq != sent.Seq {
		t.Fatal(active, err)
	}
	revoke := agentPolicyChange{Type: "revoke", ID: "style", Version: 2, Revision: 2, SourceSeq: roomCall[PostReceipt](t, h, h.post(humanCaller, "revoke-style", "撤销这条长期偏好")).Seq, TurnNumber: 1, Basis: "准确撤销用户长期偏好"}
	var revoked agentPolicyUpdateWireTest
	if err := cli(entryChange(t, "policy", "update", revoke), &revoked); err != nil || revoked.Record.Text != active.Record.Text || revoked.Record.Status != "revoked" || len(revoked.Effective) != 0 {
		t.Fatal(revoked, err)
	}
	// Replaying a saved confirmation restores its historical body and original
	// receipt, even though the current record has since been revoked.
	var replay agentPolicyUpdateWireTest
	if err := cli(entryChange(t, "policy", "update", confirm), &replay); err != nil || !reflect.DeepEqual(replay.Record, active.Record) || replay.Revision != 3 || len(replay.Effective) != 0 {
		t.Fatal(replay, err)
	}
	stale := confirm
	stale.Revision = 3
	stale.SourceSeq = roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: "new-confirm", Text: "再次确认", ReplyTo: sent.Seq}).Seq
	if err := cli(entryChange(t, "policy", "update", stale), nil); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatal("stale suggestion activated", err)
	}
	var detail struct {
		Record   PolicyRevision
		History  []PolicyRevision
		Complete bool
	}
	if err := cli([]string{"policy", "read", "--id", "style", "--detail"}, &detail); err != nil || !detail.Complete || len(detail.History) != 3 || detail.History[0].Conversation.Message.Text != "这次简短中文回复" || detail.History[1].Conversation.Message.ReplyTo != sent.Seq {
		t.Fatal(detail, err)
	}
	raw, err := h.store.readPolicy(id)
	if err != nil || !reflect.DeepEqual(detail.History, raw.History) {
		t.Fatal("Agent cannot restore full policy provenance", err)
	}
	// Projection never changes the authoritative policy digest.
	view, err := h.store.readAgentPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	if view.PolicyToken != revoked.PolicyToken {
		t.Fatal("policy token changed during projection")
	}
}

type agentPolicyUpdateWireTest struct {
	policyContext
	Effective []agentPolicyRecord `json:"effective"`
	Record    agentPolicyRecord   `json:"record"`
}
