package room

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func referenceDecisionRequest() (followupUnit, referenceActionRequest) {
	u := followupUnit{ObjectRef: referenceDigest("ref_", "object"), TargetRef: referenceDigest("ref_", "discussion"), Ref: referenceDigest("unit_", "discussion"), Revision: 2, Status: "pending"}
	q := referenceActionRequest{followupCommand: conversationCommand(u, 1), TargetRef: u.TargetRef, ID: "reply", PolicyToken: strings.Repeat("a", 64), Operation: platform.OperationDiscussionReply, Input: CommentInput{Body: "本次按方案 A 答复"}, Authority: referenceActionAuthority{RoomSeq: 1, Reason: "准确的人类依据"}}
	return u, q
}

func TestReferenceActionRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		change      func(*referenceActionRequest)
	}{
		{"object", "object_ref", func(q *referenceActionRequest) { q.ObjectRef = "bad" }},
		{"unit", "unit_ref", func(q *referenceActionRequest) { q.UnitRef = "bad" }},
		{"target", "target_ref", func(q *referenceActionRequest) { q.TargetRef = "bad" }},
		{"version", "unit_version", func(q *referenceActionRequest) { q.Revision = 0 }},
		{"turn", "turn_number", func(q *referenceActionRequest) { q.TurnNumber = 0 }},
		{"id", "id", func(q *referenceActionRequest) { q.ID = "" }},
		{"policy", "policy_token", func(q *referenceActionRequest) { q.PolicyToken = "bad" }},
		{"body", "input.body", func(q *referenceActionRequest) { q.Input.Body = "" }},
		{"reason", "authority.reason", func(q *referenceActionRequest) { q.Authority.Reason = "" }},
		{"constraints", "authority.constraints", func(q *referenceActionRequest) { q.Authority.Constraints = strings.Repeat("a", 4097) }},
		{"operation", "operation", func(q *referenceActionRequest) { q.Operation = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, q := referenceDecisionRequest()
			if err := referenceActionRequestError(q); err != nil {
				t.Fatal("valid control", err)
			}
			tc.change(&q)
			var diagnostic *requestDiagnostic
			err := referenceActionRequestError(q)
			if !errors.Is(err, ErrRequest) || !errors.As(err, &diagnostic) || diagnostic.Field != tc.field {
				t.Fatal(err)
			}
		})
	}
}

func TestReferenceActionUnitConflicts(t *testing.T) {
	for _, mode := range []string{"current", "target", "version", "completed", "archived", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			u, q := referenceDecisionRequest()
			switch mode {
			case "target":
				q.TargetRef = q.ObjectRef
			case "version":
				q.Revision++
			case "completed", "archived":
				u.Status = mode
			case "malformed":
				q.ID = ""
			}
			err := referenceActionUnitConflict(u, q)
			if mode == "current" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, ErrConflict) {
				t.Fatal(err)
			}
			if mode == "version" {
				var d *requestDiagnostic
				if !errors.As(err, &d) || d.Field != "unit_version" {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestReferenceActionSpecValidation(t *testing.T) {
	source, access, _ := gitlabReferenceFixture()
	target := platform.TargetIdentity{IdentityScope: access.Account.IdentityScope, Object: platform.NativeIdentity{Type: source.Reference.Object.Type, ID: source.Reference.Object.ID}}
	target.Discussion = platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: "thread"}
	for _, mode := range []string{"valid", "wrong-kind", "reply-retry", "unknown-retry", "wrong-operation", "invalid-schema"} {
		t.Run(mode, func(t *testing.T) {
			_, q := referenceDecisionRequest()
			spec := platform.ActionSpec{Operation: q.Operation, TargetKind: "discussion", InputSchema: []byte(`{"type":"object"}`), Retry: platform.RetryPolicy{MaxAttempts: 1}}
			switch mode {
			case "wrong-kind":
				spec.TargetKind = "object"
			case "reply-retry":
				spec.Retry.MaxAttempts = 2
			case "unknown-retry":
				spec.Retry.RetryUnknown = true
			case "wrong-operation":
				spec.Operation = platform.OperationObjectComment
			case "invalid-schema":
				spec.InputSchema = []byte(`{`)
			}
			if got := validReferenceSpec(spec, q, target); got != (mode == "valid") {
				t.Fatal(mode, got)
			}
		})
	}
}

// Only a legal member-scoped Room and policy storage are needed by this gate.
// No Agent turn, Actor, Inbox, target resolver or remote process is started.
func referenceAuthorityStore(t *testing.T) (*Store, MemberIdentity, RoomMessage) {
	t.Helper()
	state := historyRoom(1, "按方案 A 回复")
	state.Members = append(state.Members, MemberState{AgentID: "B", MembershipID: "generation-B", Runner: state.Members[0].Runner})
	store, err := OpenStore(t.TempDir(), state.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: state.Members[0].AgentID, MembershipID: state.Members[0].MembershipID}
	return store, id, state.Messages[0]
}

func TestReferenceActionInstructionAuthority(t *testing.T) {
	store, id, source := referenceAuthorityStore(t)
	view, err := store.readAgentPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"object", "discussion"} {
		for _, mode := range []string{"active", "changed-body", "revoked", "expired", "wrong-version", "wrong-target"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				u, q := referenceDecisionRequest()
				if kind == "object" {
					u.TargetRef = u.ObjectRef
					q.TargetRef = u.TargetRef
					q.Operation = platform.OperationObjectComment
				}
				u.MemberIdentity = id
				basis := followupReplyBasis{TargetRef: u.TargetRef, Intent: followupIntent{Operation: q.Operation, Body: q.Input.Body}, Source: source, Basis: "落实准确指令"}
				future := time.Now().Add(time.Hour)
				instruction := followupInstruction{Ref: instructionReference(u, basis), Version: 1, followupReplyBasis: basis, Status: "active", ExpiresAt: &future, UpdatedAt: time.Now()}
				q.PolicyToken = view.PolicyToken
				q.Authority = referenceActionAuthority{InstructionRef: instruction.Ref, InstructionVersion: 1, Reason: basis.Basis}
				switch mode {
				case "changed-body":
					q.Input.Body = "another body"
				case "revoked":
					instruction.Status = "revoked"
				case "expired":
					past := time.Now().Add(-time.Hour)
					instruction.ExpiresAt = &past
				case "wrong-version":
					q.Authority.InstructionVersion++
				case "wrong-target":
					instruction.TargetRef = referenceDigest("ref_", "foreign")
				}
				u.Instructions = []followupInstruction{instruction}
				store.mu.Lock()
				rule, got, changed, err := store.referenceAuthorityLocked(id, u, q, nil)
				store.mu.Unlock()
				if mode == "active" {
					if err != nil || rule != nil || changed != nil || got == nil || !reflect.DeepEqual(*got, basis) {
						t.Fatal(rule, got, changed, err)
					}
					return
				}
				want := ErrConflict
				if mode == "wrong-target" {
					want = ErrIdentity
				}
				if !errors.Is(err, want) || rule != nil || got != nil || changed != nil {
					t.Fatal(rule, got, changed, err)
				}
			})
		}
	}
}

func TestReferenceActionAnswerAuthority(t *testing.T) {
	store, id, source := referenceAuthorityStore(t)
	view, err := store.readAgentPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"exact", "exact-object", "ambiguous-answer", "other-source", "changed-body"} {
		t.Run(mode, func(t *testing.T) {
			u, q := referenceDecisionRequest()
			if mode == "exact-object" {
				u.TargetRef = u.ObjectRef
				q.TargetRef = u.TargetRef
				q.Operation = platform.OperationObjectComment
			}
			intent := followupIntent{Operation: q.Operation, Body: q.Input.Body}
			question := followupQuestion{Ref: referenceDigest("question_", "question"), TargetRef: u.TargetRef, Proposed: &intent, Answers: []followupAnswer{{Message: source}}}
			u.Questions = []followupQuestion{question}
			q.PolicyToken = view.PolicyToken
			q.Authority = referenceActionAuthority{QuestionRef: question.Ref, RoomSeq: source.Seq, Reason: "准确答复"}
			switch mode {
			case "ambiguous-answer":
				q.Authority.QuestionRef = referenceDigest("question_", "other")
			case "other-source":
				q.Authority.RoomSeq++
			case "changed-body":
				q.Input.Body = "changed"
			}
			store.mu.Lock()
			rule, basis, changed, err := store.referenceAuthorityLocked(id, u, q, nil)
			store.mu.Unlock()
			if mode == "exact" || mode == "exact-object" {
				if err != nil || rule != nil || changed != nil || basis == nil || basis.Source.Seq != source.Seq || basis.TargetRef != u.TargetRef {
					t.Fatal(rule, basis, changed, err)
				}
				return
			}
			want := ErrIdentity
			if mode == "changed-body" {
				want = ErrConflict
			}
			if !errors.Is(err, want) || basis != nil {
				t.Fatal(basis, err)
			}
		})
	}
}

func TestReferenceActionRuleAuthority(t *testing.T) {
	store, id, _ := referenceAuthorityStore(t)
	c := policyRule()
	if _, err := store.changePolicy(id, humanCaller, c); err != nil {
		t.Fatal(err)
	}
	view, err := store.readAgentPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"object", "discussion"} {
		t.Run(kind, func(t *testing.T) {
			u, q := referenceDecisionRequest()
			if kind == "object" {
				u.TargetRef = u.ObjectRef
				q.Operation = platform.OperationObjectComment
				q.TargetRef = u.TargetRef
			}
			q.PolicyToken = view.PolicyToken
			q.Authority = referenceActionAuthority{RuleID: c.ID, RuleVersion: 1, Reason: "Agent 声明规则适用"}
			store.mu.Lock()
			rule, basis, changed, err := store.referenceAuthorityLocked(id, u, q, nil)
			store.mu.Unlock()
			if err != nil || rule == nil || rule.ID != c.ID || rule.Version != 1 || basis != nil || changed != nil {
				t.Fatal(rule, basis, changed, err)
			}
		})
	}
}

func TestReferenceActionRuleAuthorityLifecycle(t *testing.T) {
	for _, mode := range []string{"suggested", "expired", "revoked", "old-version", "old-token", "foreign-member"} {
		t.Run(mode, func(t *testing.T) {
			store, id, _ := referenceAuthorityStore(t)
			c := policyRule()
			owner := id
			switch mode {
			case "suggested":
				c.Status = "suggested"
			case "expired":
				past := time.Now().Add(-time.Hour)
				c.ExpiresAt = &past
			case "foreign-member":
				owner.AgentID = "B"
				owner.MembershipID = "generation-B"
			}
			if _, err := store.changePolicy(owner, humanCaller, c); err != nil {
				t.Fatal(err)
			}
			old, err := store.readAgentPolicy(id)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "revoked" || mode == "old-version" || mode == "old-token" {
				c.Version, c.Text = 1, "核实文档后再回复"
				if mode == "revoked" {
					c.Status = "revoked"
				}
				if _, err := store.changePolicy(owner, humanCaller, c); err != nil {
					t.Fatal(err)
				}
			}
			current, err := store.readAgentPolicy(id)
			if err != nil {
				t.Fatal(err)
			}
			u, q := referenceDecisionRequest()
			q.PolicyToken = current.PolicyToken
			if mode == "old-token" {
				q.PolicyToken = old.PolicyToken
			}
			q.Authority = referenceActionAuthority{RuleID: c.ID, RuleVersion: 1, Reason: "声明适用"}
			store.mu.Lock()
			rule, basis, changed, err := store.referenceAuthorityLocked(id, u, q, nil)
			store.mu.Unlock()
			if rule != nil || basis != nil {
				t.Fatal("ineligible authority", rule, basis)
			}
			if mode == "old-token" {
				current.CurrentTurn = q.TurnNumber
				if err != nil || changed == nil || !reflect.DeepEqual(*changed, current) {
					t.Fatal(changed, err)
				}
				return
			}
			if !errors.Is(err, ErrConflict) || changed != nil {
				t.Fatal(changed, err)
			}
		})
	}
}
