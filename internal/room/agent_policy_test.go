package room

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Policy tests need a running member and saved Room evidence. No Inbox,
// platform binding, target resolution or model session is involved.
func policyTestService(t *testing.T) (*roomHarness, MemberIdentity, *service) {
	t.Helper()
	state := historyRoom(1, "维护长期规则")
	state.Members = append(state.Members, MemberState{AgentID: "B", MembershipID: "generation-B", Runner: state.Members[0].Runner})
	h := newRoomHarness(t, state, nil)
	id := h.memberID("A", "generation-A")
	if err := h.store.initReferenceFollowups(id); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id,
		TurnNumber: 1, LastOfferedUnreadSeq: 1,
		Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: 1, OfferedUnreadSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	svc := &service{store: h.store, room: h.addr,
		info: ServiceInfo{RoomIdentity: h.id, ServiceEpoch: "policy-test"}, requests: make(chan struct{}, 32)}
	return h, id, svc
}

func assertPolicyUnchanged(t *testing.T, store *Store, id MemberIdentity, before PolicyView) {
	t.Helper()
	after, err := store.readPolicy(id)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("policy changed: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestAgentPolicyRulesCoexistAndRecover(t *testing.T) {
	h, id, _ := policyTestService(t)
	for i, text := range []string{"AgenticHub 产品负责人是 huayi", "AgenticHub 的这类报错优先建议找 hongwei 和 luojun"} {
		source := roomCall[PostReceipt](t, h, h.post(humanCaller, text, "记住："+text))
		q := agentPolicyChange{Type: "save", ID: []string{"owner", "contacts"}[i], Revision: uint64(i), SourceSeq: source.Seq, TurnNumber: 1, Basis: "用户明确长期要求", Body: &agentPolicyBody{Text: text}}
		result, err := h.store.changeAgentPolicy(id, agentCallerIdentity(id), q)
		if err != nil || result.Record.Text != text || result.Record.Source.Text != "记住："+text || len(result.Effective) != i+1 {
			t.Fatal(result, err)
		}
		assertNaturalPolicyJSON(t, result.Record)
	}
	before, err := h.store.readPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	h.reload(t, true)
	assertPolicyUnchanged(t, h.store, id, before)
}

func TestAgentPolicyInvalidSourceKeepsState(t *testing.T) {
	h, id, _ := policyTestService(t)
	q := agentPolicyChange{Type: "save", ID: "style", SourceSeq: 999, TurnNumber: 1,
		Basis: "真实来源", Body: &agentPolicyBody{Text: "简短中文"}}
	before, err := h.store.readPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.store.changeAgentPolicy(id, agentCallerIdentity(id), q)
	var diagnostic *requestDiagnostic
	if !errors.Is(err, ErrIdentity) || !errors.As(err, &diagnostic) || diagnostic.Field != "change.source_seq" {
		t.Fatal("invalid source lost its identity classification or field", err)
	}
	assertPolicyUnchanged(t, h.store, id, before)
}

func TestAgentPolicySuggestionCannotReplaceActiveRule(t *testing.T) {
	h, id, _ := policyTestService(t)
	source := roomCall[PostReceipt](t, h, h.post(humanCaller, "style", "以后用简短中文"))
	q := agentPolicyChange{Type: "save", ID: "style", SourceSeq: source.Seq, TurnNumber: 1,
		Basis: "用户明确长期要求", Body: &agentPolicyBody{Text: "简短中文"}}
	if _, err := h.store.changeAgentPolicy(id, agentCallerIdentity(id), q); err != nil {
		t.Fatal(err)
	}
	before, err := h.store.readPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	source = roomCall[PostReceipt](t, h, h.post(humanCaller, "habit", "这次也简短"))
	q.Type, q.Version, q.Revision, q.SourceSeq = "suggest", 1, 1, source.Seq
	_, err = h.store.changeAgentPolicy(id, agentCallerIdentity(id), q)
	var diagnostic *requestDiagnostic
	if !errors.Is(err, ErrRequest) || !errors.As(err, &diagnostic) || diagnostic.Field != "mode" ||
		!strings.Contains(diagnostic.Message, "suggest can only create or amend a suggested record") {
		t.Fatal(err)
	}
	assertPolicyUnchanged(t, h.store, id, before)
}

func TestAgentPolicySaveCorrectRevokePreservesEvidence(t *testing.T) {
	h, id, _ := policyTestService(t)
	q := agentPolicyChange{Type: "save", ID: "error-contacts", TurnNumber: 1, Basis: "上下文明确指向 AgenticHub 报错处理约定", Body: &agentPolicyBody{Text: "AgenticHub 的这类报错，优先建议找 hongwei 和 luojun"}}
	var priorToken string
	for i, human := range []string{"记住，AgenticHub 这类报错优先找 hongwei 和 luojun", "改一下，这类报错先建议找 hongwei，联系前问我", "撤销这条报错处理约定"} {
		q.SourceSeq = roomCall[PostReceipt](t, h, h.post(humanCaller, fmt.Sprintf("rule-maintenance-%d", i), human)).Seq
		q.Version, q.Revision = uint64(i), uint64(i)
		if i == 1 {
			q.Body = &agentPolicyBody{Text: "AgenticHub 的这类报错先建议找 hongwei，联系前问我"}
			q.Basis = "用户明确纠正原处理约定并增加联系前询问的限制"
		}
		if i == 2 {
			q.Type, q.Body, q.Basis = "revoke", nil, "用户明确撤销这条规则"
		}
		got, err := h.store.changeAgentPolicy(id, agentCallerIdentity(id), q)
		if err != nil {
			t.Fatal(err)
		}
		view, err := h.store.readAgentPolicy(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Record.Version != uint64(i+1) || got.Record.Source.Text != human || got.Record.Conversation == nil || got.Record.Conversation.Basis != q.Basis || view.PolicyToken == priorToken {
			t.Fatal("receipt lost exact version, human evidence, interpretation or updated token", got)
		}
		if i < 2 && (got.Record.Status != "active" || len(got.Effective) != 1) || i == 2 && (got.Record.Status != "revoked" || len(got.Effective) != 0) {
			t.Fatal(got)
		}
		priorToken = view.PolicyToken
	}
	detail, err := h.store.readPolicy(id)
	if err != nil || len(detail.History) != 3 || len(detail.Current) != 1 ||
		detail.Current[0].Version != 3 || detail.Current[0].Status != "revoked" ||
		!strings.Contains(detail.Current[0].Text, "联系前问我") {
		t.Fatal("revoke did not preserve the corrected body", detail, err)
	}
	h.reload(t, true)
	assertPolicyUnchanged(t, h.store, id, detail)
}
