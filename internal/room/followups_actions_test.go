package room

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

type referenceExecuteFixture struct {
	h             *inboxHarness
	svc           *service
	q             referenceActionRequest
	prepare       referenceActionPreparer
	calls         atomic.Int32
	beforeInspect func()
	beforeExecute func()
	result        func(int) platform.TargetActionResult
}

func referenceExecuteTest(t *testing.T, kind string) *referenceExecuteFixture {
	t.Helper()
	h, svc, units, sources := conversationHarness(t, 1)
	u := units[0]
	if kind == "object" {
		var err error
		u, err = h.store.readFollowup(h.id, u.ObjectRef, referenceDigest("unit_", u.ObjectRef))
		if err != nil {
			t.Fatal(err)
		}
	}
	op := platform.OperationDiscussionReply
	if kind == "object" {
		op = platform.OperationObjectComment
	}
	var err error
	source := postConversationReply(t, h, "execute", "请按方案 A 回复这个问题", 0)
	authority := referenceActionAuthority{RoomSeq: source.Seq, Reason: "真实用户明确指向当前问题", MessageRefs: []string{sources[0].TargetRef}}
	p, err := h.store.readAgentPolicy(h.id)
	if err != nil {
		t.Fatal(err)
	}
	f := &referenceExecuteFixture{h: h, svc: svc, q: referenceActionRequest{followupCommand: conversationCommand(u, 1), TargetRef: u.TargetRef, ID: "reply", PolicyToken: p.PolicyToken, Authority: authority, Operation: op, Input: CommentInput{Body: "本次按方案 A 答复"}}}
	f.prepare = func(ctx context.Context, id MemberIdentity, access platform.AccessContext) (referenceActionSession, error) {
		spec := platform.ActionSpec{Operation: op, TargetKind: kind, InputSchema: []byte(`{"type":"object"}`), Retry: platform.RetryPolicy{MaxAttempts: 1}}
		if kind == "object" {
			spec.Retry = platform.RetryPolicy{MaxAttempts: 2, RetryUnknown: true}
		}
		cap := platform.ActionCapability{Spec: spec,
			Inspect: func(_ context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
				if f.beforeInspect != nil {
					f.beforeInspect()
				}
				return platform.TargetActionInspection{Target: q.Target, Account: a.Account, AccountLabel: "bot", ContainerLabel: "g/p", URL: "https://gitlab.example.com/g/p/-/merge_requests/42"}, nil
			},
			Execute: func(_ context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
				n := int(f.calls.Add(1))
				if f.beforeExecute != nil {
					f.beforeExecute()
				}
				if f.result != nil {
					return f.result(n), nil
				}
				return platform.TargetActionResult{Target: q.Target, Account: a.Account, Status: "succeeded", ResultID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "99"}, Message: "exact note", RetryClass: "never"}, nil
			},
		}
		return referenceActionSession{Access: access, Capabilities: platform.ReferenceCapabilities{Actions: map[platform.Operation]platform.ActionCapability{op: cap}}, Check: func(ctx context.Context) error { return ctx.Err() }, Close: func() {}}, nil
	}
	return f
}
func (f *referenceExecuteFixture) run() (referenceActionReceipt, error) {
	return f.svc.executeReferenceAction(f.h.ctx, f.h.id, f.q, f.prepare)
}
func (f *referenceExecuteFixture) unit(t *testing.T) followupUnit {
	t.Helper()
	u, err := f.h.store.readFollowup(f.h.id, f.q.ObjectRef, f.q.UnitRef)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
func (f *referenceExecuteFixture) outcome(status, retry string) platform.TargetActionResult {
	f.h.store.mu.Lock()
	defer f.h.store.mu.Unlock()
	t, _ := f.h.store.loadTargetLocked(f.h.id, f.q.ObjectRef, f.q.TargetRef)
	_, a, _ := gitlabReferenceFixture()
	a.RoomIdentity, a.AgentID, a.MembershipID = f.h.id.RoomIdentity, f.h.id.AgentID, f.h.id.MembershipID
	return platform.TargetActionResult{Target: t.Target, Account: a.Account, Status: status, Message: "test outcome", RetryClass: retry, ResultID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "99"}}
}

func TestReferenceActionNaturalRuleStillEnforcesTargetScope(t *testing.T) {
	for _, field := range []string{"matching", "provider", "instance", "account", "project", "object-type"} {
		t.Run(field, func(t *testing.T) {
			f := referenceExecuteTest(t, "object")
			c := policyRule()
			switch field {
			case "provider":
				c.Scope.Provider = "another-platform"
			case "instance":
				c.Scope.Instance = "https://another.example.com"
			case "account":
				c.Scope.Account = "another-account"
			case "project":
				c.Scope.Project = "another-project"
			case "object-type":
				c.Scope.ObjectType = "another-type"
			}
			if _, err := f.h.store.changePolicy(f.h.id, humanCaller, c); err != nil {
				t.Fatal(err)
			}
			view, err := f.h.store.readAgentPolicy(f.h.id)
			if err != nil {
				t.Fatal(err)
			}
			f.q.PolicyToken = view.PolicyToken
			f.q.Authority = referenceActionAuthority{RuleID: c.ID, RuleVersion: 1, Reason: "已核实正文中的业务条件"}
			receipt, err := f.run()
			if field == "matching" {
				if err != nil || receipt.Status != "succeeded" || f.calls.Load() != 1 {
					t.Fatal(receipt, err)
				}
			} else if err == nil || f.calls.Load() != 0 {
				t.Fatal("scope mismatch dispatched", receipt, err)
			}
		})
	}
}
func TestReferenceActionThreeAuthorities(t *testing.T) {
	for _, kind := range []string{"object", "discussion"} {
		for _, authority := range []string{"human", "answer", "instruction", "rule"} {
			t.Run(kind+"/"+authority, func(t *testing.T) {
				f := referenceExecuteTest(t, kind)
				u := f.unit(t)
				if authority == "answer" {
					intent := followupIntent{Operation: f.q.Operation, Body: f.q.Input.Body, Constraints: "仅本次问题"}
					var err error
					u, err = f.svc.askFollowup(f.h.ctx, f.h.id, followupQuestionChange{followupCommand: conversationCommand(u, 1), MessageRefs: f.q.Authority.MessageRefs, Text: "是否按方案 A 回答本次问题？", Proposed: &intent})
					if err != nil {
						t.Fatal(err)
					}
					answer := postConversationReply(t, f.h, "answer", "可以，按方案 A 回复", u.Questions[0].Seq)
					u, err = f.svc.readFollowupConversation(f.h.ctx, f.h.id, conversationCommand(u, 1))
					if err != nil {
						t.Fatal(err)
					}
					f.q.Authority = referenceActionAuthority{RoomSeq: answer.Seq, QuestionRef: u.Questions[0].Ref, Constraints: intent.Constraints, Reason: "本次问题的准确答复"}
				}
				if authority == "instruction" {
					c := followupInstructionChange{followupCommand: conversationCommand(u, 1), SourceSeq: f.q.Authority.RoomSeq, MessageRefs: f.q.Authority.MessageRefs, Intent: followupIntent{Operation: f.q.Operation, Constraints: "仅当前问题"}, Basis: f.q.Authority.Reason}
					var err error
					u, err = f.h.store.saveFollowupInstruction(f.h.id, c)
					if err != nil {
						t.Fatal(err)
					}
					f.q.Authority = referenceActionAuthority{InstructionRef: u.Instructions[0].Ref, InstructionVersion: 1, Constraints: c.Intent.Constraints, Reason: "落实跨回合的准确指令"}
					roomCall[ClearReceipt](t, f.h.roomHarness, ClearMessages{RoomIdentity: f.h.id.RoomIdentity, Caller: humanCaller, ThroughSeq: c.SourceSeq})
				}
				if authority == "rule" {
					msg := postConversationReply(t, f.h, "rule", "以后这个项目的文档问题直接回复", 0)
					rule := conversationRule(msg.Seq)
					rule.Change.Scope.Account = "bot"
					view, err := f.svc.referencePolicyChange(f.h.ctx, f.h.id, agentCallerIdentity(f.h.id), rule)
					if err != nil {
						t.Fatal(err)
					}
					f.q.PolicyToken = view.PolicyToken
					f.q.Authority = referenceActionAuthority{RuleID: rule.Change.ID, RuleVersion: 1, Reason: "文档使用问题符合规则且不涉及排期"}
				}
				f.q.followupCommand = conversationCommand(u, 1)
				got, err := f.run()
				if err != nil || got.Status != "succeeded" || got.Attempts != 1 || f.calls.Load() != 1 {
					t.Fatal(got, err)
				}
				saved := f.unit(t)
				if saved.Status == "completed" || len(saved.Actions) != 1 || saved.Actions[0].Access.Account.ID.Value != "13" {
					t.Fatal(saved)
				}
				if authority == "instruction" && saved.Instructions[0].Status != "fulfilled" {
					t.Fatal(saved.Instructions)
				}
				f.q.TurnNumber, f.q.Revision, f.q.PolicyToken = 999, 999, "obsolete"
				repeated, err := f.run()
				if err != nil || repeated.Status != "succeeded" || f.calls.Load() != 1 {
					t.Fatal(repeated, err)
				}
				f.q.Input.Body = "other"
				if _, err := f.run(); !errors.Is(err, ErrConflict) {
					t.Fatal(err)
				}
			})
		}
	}
}
func TestReferenceActionUnknownRetryAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		kind, first, second, want string
		attempts                  int
		duplicate                 bool
	}{
		{"object", "unknown", "succeeded", "succeeded", 2, true}, {"object", "unknown", "not_applied", "unknown", 2, true}, {"object", "not_applied", "succeeded", "succeeded", 2, false}, {"discussion", "unknown", "succeeded", "unknown", 1, false},
	} {
		t.Run(tc.kind+tc.first+tc.second, func(t *testing.T) {
			f := referenceExecuteTest(t, tc.kind)
			f.result = func(n int) platform.TargetActionResult {
				if n == 1 {
					return f.outcome(tc.first, "transient")
				}
				return f.outcome(tc.second, "never")
			}
			got, err := f.run()
			if err != nil || got.Status != tc.want || got.Attempts != tc.attempts || got.PossibleDuplicate != tc.duplicate {
				t.Fatal(got, err)
			}
			if tc.want == "unknown" {
				u := f.unit(t)
				if _, err := f.h.store.updateFollowup(f.h.id, followupUpdate{ObjectRef: u.ObjectRef, UnitRef: u.Ref, Revision: u.Revision, TurnNumber: 1, Status: "completed", Summary: "done", Evidence: "sent"}); !errors.Is(err, ErrConflict) {
					t.Fatal("unknown completed", err)
				}
				page, err := f.h.store.listFollowups(f.h.id, followupQuery{Limit: 25})
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, item := range page.Items {
					if item.Ref == u.Ref && len(item.Actions) == 1 {
						found = true
					}
				}
				if !found {
					t.Fatal(page)
				}
				f.q.ID = "new-id"
				f.q.Revision = u.Revision
				if _, err := f.run(); !errors.Is(err, ErrConflict) {
					t.Fatal("unknown resent with new id", err)
				}
				f.q.ID = "reply"
			}
			reloadConversation(t, f.h)
			f.svc = referenceTestService(f.h)
			f.q.TurnNumber = 99
			f.q.Revision = 99
			got, err = f.run()
			if err != nil || got.Status != tc.want || int(f.calls.Load()) != tc.attempts {
				t.Fatal(got, err)
			}
		})
	}
}
func TestReferenceActionRefusals(t *testing.T) {
	for _, mode := range []string{"target", "version", "expired-turn", "account", "unsupported", "wrong-kind", "reply-retry", "wrong-inspection", "fake-human", "ambiguous-answer", "changed-body", "revoked", "expired", "policy"} {
		t.Run(mode, func(t *testing.T) {
			f := referenceExecuteTest(t, "discussion")
			switch mode {
			case "target":
				f.q.TargetRef = f.q.ObjectRef
			case "version":
				f.q.Revision++
			case "expired-turn":
				f.q.TurnNumber++
			case "fake-human":
				f.q.Authority.RoomSeq = 999
			case "ambiguous-answer":
				f.q.Authority.QuestionRef = referenceDigest("question_", "other")
			case "policy":
				f.q.PolicyToken = "obsolete-token" + f.q.PolicyToken[:50]
			case "changed-body", "revoked", "expired":
				u := f.unit(t)
				expiry := time.Now().Add(time.Hour)
				c := followupInstructionChange{followupCommand: conversationCommand(u, 1), SourceSeq: f.q.Authority.RoomSeq, MessageRefs: f.q.Authority.MessageRefs, Intent: followupIntent{Operation: f.q.Operation, Body: f.q.Input.Body}, Basis: f.q.Authority.Reason, ExpiresAt: &expiry}
				var err error
				u, err = f.h.store.saveFollowupInstruction(f.h.id, c)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "revoked" {
					m := postConversationReply(t, f.h, "revoke", "撤销本次回复", 0)
					u, err = f.h.store.finishFollowupInstruction(f.h.id, conversationCommand(u, 1), u.Instructions[0].Ref, 1, "revoked", "用户撤销", m.Seq)
					if err != nil {
						t.Fatal(err)
					}
				}
				if mode == "expired" {
					u.Instructions[0].ExpiresAt = new(time.Time)
					*u.Instructions[0].ExpiresAt = time.Now().Add(-time.Second)
					f.h.store.mu.Lock()
					err = f.h.store.saveFollowupUnitLocked(f.h.id, u)
					f.h.store.mu.Unlock()
					if err != nil {
						t.Fatal(err)
					}
				}
				f.q.followupCommand = conversationCommand(u, 1)
				f.q.Authority = referenceActionAuthority{InstructionRef: u.Instructions[0].Ref, InstructionVersion: 1, Reason: "落实准确指令"}
				if mode == "changed-body" {
					f.q.Input.Body = "another body"
				}
			default:
				original := f.prepare
				f.prepare = func(ctx context.Context, id MemberIdentity, a platform.AccessContext) (referenceActionSession, error) {
					s, err := original(ctx, id, a)
					c := s.Capabilities.Actions[f.q.Operation]
					switch mode {
					case "account":
						s.Access.Account.ID.Value = "999"
					case "unsupported":
						delete(s.Capabilities.Actions, f.q.Operation)
					case "wrong-kind":
						c.Spec.TargetKind = "object"
					case "reply-retry":
						c.Spec.Retry.MaxAttempts = 2
					case "wrong-inspection":
						old := c.Inspect
						c.Inspect = func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
							i, e := old(ctx, q, a)
							i.Target.Discussion.Value = "other"
							return i, e
						}
					}
					if mode != "unsupported" {
						s.Capabilities.Actions[f.q.Operation] = c
					}
					return s, err
				}
			}
			got, err := f.run()
			if mode == "policy" {
				if err != nil || got.Status != "policy_changed" {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("accepted", got)
			}
			if f.calls.Load() != 0 {
				t.Fatal("refusal dispatched")
			}
		})
	}
}
func TestReferenceActionIntentPersistenceAndProcessing(t *testing.T) {
	f := referenceExecuteTest(t, "discussion")
	gate := make(chan struct{})
	entered := make(chan struct{})
	f.beforeExecute = func() { close(entered); <-gate }
	done := make(chan error, 1)
	go func() { _, err := f.run(); done <- err }()
	<-entered
	got, err := f.run()
	if err != nil || got.Status != "processing" || f.calls.Load() != 1 {
		t.Fatal(got, err)
	}
	u := f.unit(t)
	if u.Actions[0].Status != "unknown" || u.Actions[0].Attempts != 1 || u.Actions[0].Basis.Source.Text == "" {
		t.Fatal(u)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestReferenceActionPersistenceFailures(t *testing.T) {
	for _, mode := range []string{"intent", "attempt", "result", "unknown-intent"} {
		t.Run(mode, func(t *testing.T) {
			f := referenceExecuteTest(t, "discussion")
			ops := f.h.store.ops
			fail := func(string, string) (snapshotFile, error) { return nil, errors.New("injected save failure") }
			if mode == "intent" {
				f.h.store.ops.createTemp = fail
			}
			if mode == "unknown-intent" {
				f.h.store.ops.syncDir = func(string) error { return errors.New("commit unknown") }
			}
			if mode == "attempt" {
				n := 0
				f.h.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
					n++
					if n == 2 {
						return fail(dir, pattern)
					}
					return ops.createTemp(dir, pattern)
				}
			}
			if mode == "result" {
				f.beforeExecute = func() { f.h.store.mu.Lock(); f.h.store.ops.createTemp = fail; f.h.store.mu.Unlock() }
			}
			_, err := f.run()
			if mode == "attempt" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("save failure hidden")
			}
			want := int32(0)
			if mode == "result" {
				want = 1
			}
			if f.calls.Load() != want {
				t.Fatal(f.calls.Load())
			}
			f.h.store.ops = ops
			if mode == "unknown-intent" || mode == "result" {
				reloadConversation(t, f.h)
				f.svc = referenceTestService(f.h)
				got, err := f.run()
				if err != nil || got.Status != "unknown" || f.calls.Load() != want {
					t.Fatal(got, err)
				}
			}
		})
	}
}
func TestReferenceActionUnknownSurvivesLaterStop(t *testing.T) {
	f := referenceExecuteTest(t, "object")
	f.result = func(int) platform.TargetActionResult { return f.outcome("unknown", "transient") }
	inspections := 0
	original := f.prepare
	f.prepare = func(ctx context.Context, id MemberIdentity, a platform.AccessContext) (referenceActionSession, error) {
		s, e := original(ctx, id, a)
		old := s.Check
		s.Check = func(ctx context.Context) error {
			inspections++
			if f.calls.Load() > 0 {
				return ErrConflict
			}
			return old(ctx)
		}
		return s, e
	}
	got, err := f.run()
	if err != nil || got.Status != "unknown" || got.Attempts != 1 || f.calls.Load() != 1 {
		t.Fatal(got, err, inspections)
	}
	// No legacy business files were written.
	if _, err := os.Stat(f.h.store.followupRoot(f.h.id) + "/../matters"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

func TestReferenceActionRechecksAuthorityBeforeRetry(t *testing.T) {
	for _, mode := range []string{"rule", "instruction", "turn", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := referenceExecuteTest(t, "object")
			u := f.unit(t)
			var rule PolicyChange
			if mode == "rule" {
				msg := postConversationReply(t, f.h, "standing", "以后文档问题直接回复", 0)
				q := conversationRule(msg.Seq)
				q.Change.Scope.Account = "bot"
				v, err := f.svc.referencePolicyChange(f.h.ctx, f.h.id, agentCallerIdentity(f.h.id), q)
				if err != nil {
					t.Fatal(err)
				}
				rule = q.Change
				f.q.PolicyToken = v.PolicyToken
				f.q.Authority = referenceActionAuthority{RuleID: q.Change.ID, RuleVersion: 1, Reason: "符合文档规则"}
			}
			if mode == "instruction" {
				c := followupInstructionChange{followupCommand: conversationCommand(u, 1), SourceSeq: f.q.Authority.RoomSeq, Intent: followupIntent{Operation: f.q.Operation, Body: f.q.Input.Body}, Basis: f.q.Authority.Reason}
				var err error
				u, err = f.h.store.saveFollowupInstruction(f.h.id, c)
				if err != nil {
					t.Fatal(err)
				}
				f.q.followupCommand = conversationCommand(u, 1)
				f.q.Authority = referenceActionAuthority{InstructionRef: u.Instructions[0].Ref, InstructionVersion: 1, Reason: "准确单次指令"}
			}
			ctx, cancel := context.WithCancel(f.h.ctx)
			defer cancel()
			f.result = func(n int) platform.TargetActionResult {
				if n > 1 {
					t.Error("invalid second attempt")
				}
				switch mode {
				case "rule":
					msg := postConversationReply(t, f.h, "revoke", "撤销自动回复规则", 0)
					rule.Version, rule.Revision, rule.SourceSeq, rule.Status = 1, 1, msg.Seq, "revoked"
					_, err := f.svc.referencePolicyChange(f.h.ctx, f.h.id, agentCallerIdentity(f.h.id), conversationPolicyChange{Change: rule, Mode: "explicit_long_term", Basis: "明确撤销"})
					if err != nil {
						t.Fatal(err)
					}
				case "instruction":
					u := f.unit(t)
					msg := postConversationReply(t, f.h, "revoke", "撤销这次回复", 0)
					if _, err := f.h.store.finishFollowupInstruction(f.h.id, conversationCommand(u, 1), u.Instructions[0].Ref, 1, "revoked", "用户明确撤销", msg.Seq); err != nil {
						t.Fatal(err)
					}
				case "turn":
					inboxRunning(t, f.h, 2, 1)
				case "cancel":
					cancel()
				}
				return f.outcome("unknown", "transient")
			}
			got, err := f.svc.executeReferenceAction(ctx, f.h.id, f.q, f.prepare)
			if err != nil || got.Status != "unknown" || got.Attempts != 1 || f.calls.Load() != 1 {
				t.Fatal(got, err)
			}
			if mode == "rule" && got.Policy == nil {
				t.Fatal("missing changed policy view")
			}
		})
	}
}

func TestReferenceActionUsesLaterVerifiedAlias(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	for _, key := range []string{"first", "later"} {
		receiveReference(t, h, key, referenceTestSource(key))
	}
	inboxRunning(t, h, 1, 2)
	svc := referenceTestService(h)
	page, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, func(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		r, a, e := referenceTestResolver(ctx, id, source)
		if source.ItemKey == "first" {
			r.Aliases = nil
		}
		return r, a, e
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := h.store.readFollowup(h.id, page.Followups[0].ObjectRef, page.Followups[0].Ref)
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.store.readAgentPolicy(h.id)
	if err != nil {
		t.Fatal(err)
	}
	q := referenceActionRequest{followupCommand: conversationCommand(u, 1), TargetRef: u.TargetRef, ID: "aliases", PolicyToken: p.PolicyToken, Authority: referenceActionAuthority{Reason: "准确用户指令"}, Operation: platform.OperationObjectComment, Input: CommentInput{Body: "reply"}}
	request, _, err := h.store.referenceActionEvidence(h.id, q)
	if err != nil || len(request.Aliases) != 1 || request.Aliases[0].Object.ID.Namespace != "gitlab:rest:iid" {
		t.Fatal(request, err)
	}
}
func TestReferenceActionKeepsOtherUnansweredQuestion(t *testing.T) {
	f := referenceExecuteTest(t, "discussion")
	u := f.unit(t)
	u, err := f.svc.askFollowup(f.h.ctx, f.h.id, followupQuestionChange{followupCommand: conversationCommand(u, 1), Text: "另一个排期问题何时交付？"})
	if err != nil {
		t.Fatal(err)
	}
	f.q.followupCommand = conversationCommand(u, 1)
	result, err := f.run()
	if err != nil || result.Status != "succeeded" {
		t.Fatal(result, err)
	}
	u = f.unit(t)
	if u.Status != "waiting_user" || len(u.Questions[0].Answers) != 0 {
		t.Fatal(u)
	}
}
