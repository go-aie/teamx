package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestActionsLedgerFailureNeverReplays(t *testing.T) {
	for _, mode := range []string{"intent", "attempt", "result", "projection"} {
		t.Run(mode, func(t *testing.T) {
			f := referenceExecuteTest(t, "object")
			ops := f.h.store.ops
			records := 0
			f.h.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
				if filepath.Base(dir) == "records" {
					records++
					if (mode == "intent" && records == 1) || (mode == "attempt" && records == 2) || (mode == "result" && records == 3) {
						return nil, errors.New("injected ledger failure")
					}
				}
				if mode == "projection" && filepath.Base(dir) == "units" {
					return nil, errors.New("projection failure")
				}
				return ops.createTemp(dir, pattern)
			}
			_, err := f.run()
			if mode != "attempt" && err == nil {
				t.Fatal("failure hidden")
			}
			want := int32(0)
			if mode == "result" {
				want = 1
			}
			if f.calls.Load() != want {
				t.Fatal(f.calls.Load())
			}
			f.h.store.ops = ops
			if mode != "intent" {
				reloadConversation(t, f.h)
				f.svc = referenceTestService(f.h)
				r, err := f.run()
				if err != nil || f.calls.Load() != want || (r.Status != "unknown" && r.Status != "not_applied") {
					t.Fatal(r, err, f.calls.Load())
				}
			}
		})
	}
}

func TestActionsLegacyReceiptAndSchemaPreflight(t *testing.T) {
	f := referenceExecuteTest(t, "object")
	if _, err := f.run(); err != nil {
		t.Fatal(err)
	}
	f.h.store.mu.Lock()
	actions, err := f.h.store.loadActionsLocked(f.h.id)
	if err != nil {
		t.Fatal(err)
	}
	u, err := f.h.store.loadUnitLocked(f.h.id, f.q.ObjectRef, f.q.UnitRef)
	if err != nil {
		t.Fatal(err)
	}
	a := actions[0]
	a.Ledger = false
	u.Actions = []referenceAction{a}
	if err := f.h.store.saveFollowupUnitLocked(f.h.id, u); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.h.store.actionPath(f.h.id, f.q.ID)); err != nil {
		t.Fatal(err)
	}
	f.h.store.mu.Unlock()
	reloadConversation(t, f.h)
	f.svc = referenceTestService(f.h)
	r, err := f.run()
	if err != nil || r.Status != "succeeded" || f.calls.Load() != 1 {
		t.Fatal(r, err)
	}
}

func TestActionsAdapterInputAndMessageTargetAreGeneric(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	source := postConversationReply(t, h, "structured-task", "按已确认的结构化输入处理准确目标", 0)
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: h.id, TurnNumber: 1, LastOfferedUnreadSeq: source.Seq, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: source.Seq, OfferedUnreadSeq: source.Seq}}); err != nil {
		t.Fatal(err)
	}
	svc := referenceTestService(h)
	target := platform.TargetIdentity{IdentityScope: platform.IdentityScope{Provider: "structured-test", Instance: "native:test"}, Object: platform.NativeIdentity{Type: "WorkObject", ID: platform.ExternalID{Namespace: "object", Value: "42"}}, Message: platform.ExternalID{Namespace: "message", Value: "1"}}
	access := platform.AccessContext{RoomIdentity: h.id.RoomIdentity, AgentID: h.id.AgentID, MembershipID: h.id.MembershipID, Account: platform.AccountIdentity{IdentityScope: target.IdentityScope, ID: platform.ExternalID{Namespace: "actor", Value: "writer"}}, PlatformID: "connection", Generation: "generation", PlatformVersion: 1, CredentialVersion: 1}
	resolution := platform.TargetResolution{Status: platform.ResolutionResolved, Target: &target, Account: access.Account, ObjectVerified: true, MessageVerified: true, Complete: true, CheckedAt: time.Now().UTC()}
	ref, err := h.store.saveActionTarget(h.id, resolution, access)
	if err != nil {
		t.Fatal(err)
	}
	var input CommentInput
	if err := json.Unmarshal([]byte(`{"labels":["reviewed"],"approved":true}`), &input); err != nil {
		t.Fatal(err)
	}
	policy, err := h.store.readAgentPolicy(h.id)
	if err != nil {
		t.Fatal(err)
	}
	q := referenceActionRequest{followupCommand: followupCommand{TurnNumber: 1}, TargetRef: ref, ID: "structured", PolicyToken: policy.PolicyToken, Authority: referenceActionAuthority{RoomSeq: source.Seq, Reason: "实际 Room 指令"}, Operation: platform.OperationMessageReply, Input: input}
	count := 0
	prepare := func(context.Context, MemberIdentity, platform.AccessContext) (referenceActionSession, error) {
		return referenceActionSession{Access: access, Check: func(ctx context.Context) error { return ctx.Err() }, Close: func() {}, Capabilities: platform.ReferenceCapabilities{Actions: map[platform.Operation]platform.ActionCapability{q.Operation: {Spec: platform.ActionSpec{Operation: q.Operation, TargetKind: "message", InputSchema: json.RawMessage(`{"type":"object"}`), Retry: platform.RetryPolicy{MaxAttempts: 1}}, Inspect: func(_ context.Context, request platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
			if string(request.Input) != `{"approved":true,"labels":["reviewed"]}` {
				return platform.TargetActionInspection{}, ErrRequest
			}
			return platform.TargetActionInspection{Target: request.Target, Account: a.Account}, nil
		}, Execute: func(_ context.Context, request platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
			count++
			if len(request.IdempotencyKey) != 32 {
				t.Fatal("missing service idempotency key")
			}
			return platform.TargetActionResult{Target: request.Target, Account: a.Account, Status: "succeeded", ResultID: platform.ExternalID{Namespace: "receipt", Value: "1"}, RetryClass: "never"}, nil
		}}}}}, nil
	}
	r, err := svc.executeReferenceAction(context.Background(), h.id, q, prepare)
	if err != nil || r.Status != "succeeded" || count != 1 {
		t.Fatal(r, err, count)
	}
	reloadConversation(t, h)
	svc = referenceTestService(h)
	r, err = svc.executeReferenceAction(context.Background(), h.id, q, prepare)
	if err != nil || r.Status != "succeeded" || count != 1 {
		t.Fatal(r, err, count)
	}
}
