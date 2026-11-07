package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestActionsStandaloneCLIAndAssociatedContract(t *testing.T) {
	m, room, svc, _, members := cliToolFixture(t, "exit 99\n")
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: members[0].AgentID, MembershipID: members[0].MembershipID}
	if err := svc.store.initReferenceFollowups(id); err != nil {
		t.Fatal(err)
	}
	post, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "forge-authority", Text: "请在这个 MR 留言：已收到。"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: post.Seq, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: post.Seq, OfferedUnreadSeq: post.Seq}}); err != nil {
		t.Fatal(err)
	}
	target := platform.TargetIdentity{IdentityScope: platform.IdentityScope{Provider: "forge-test", Instance: "https://forge.invalid"}, Containers: []platform.NativeIdentity{{Type: "Project", ID: platform.ExternalID{Namespace: "forge:project", Value: "1"}}}, Object: platform.NativeIdentity{Type: "MergeRequest", ID: platform.ExternalID{Namespace: "forge:object", Value: "42"}}}
	access := platform.AccessContext{RoomIdentity: id.RoomIdentity, AgentID: id.AgentID, MembershipID: id.MembershipID, Account: platform.AccountIdentity{IdentityScope: target.IdentityScope, ID: platform.ExternalID{Namespace: "forge:user", Value: "writer"}}, ConnectionID: strings.Repeat("a", 32), Generation: strings.Repeat("b", 32), ConnectionVersion: 1, CredentialVersion: 1}
	var calls atomic.Int32
	var uncertain atomic.Bool
	prepare := func(ctx context.Context, member MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
		if member != id || (old.Valid() && old.Account != access.Account) {
			return referenceActionSession{}, ErrIdentity
		}
		return referenceActionSession{Access: access, Check: func(ctx context.Context) error { return ctx.Err() }, Close: func() {}, Capabilities: platform.ReferenceCapabilities{Resolve: func(ctx context.Context, l platform.TargetLocator, a platform.AccessContext) (platform.TargetResolution, error) {
			return platform.TargetResolution{Status: platform.ResolutionResolved, Target: &target, Account: a.Account, ObjectVerified: true, Complete: true, CheckedAt: time.Now().UTC()}, nil
		}, Actions: map[platform.Operation]platform.ActionCapability{platform.OperationObjectComment: {Spec: platform.ActionSpec{Operation: platform.OperationObjectComment, TargetKind: "object", ObjectTypes: []string{"MergeRequest"}, InputSchema: json.RawMessage(`{"type":"object","required":["body"]}`), Retry: platform.RetryPolicy{MaxAttempts: 1}}, Inspect: func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
			if !sameNativeTarget(q.Target, target) {
				return platform.TargetActionInspection{}, ErrIdentity
			}
			return platform.TargetActionInspection{Target: q.Target, Account: a.Account}, nil
		}, Execute: func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
			calls.Add(1)
			status := "succeeded"
			if uncertain.Load() {
				status = "unknown"
			}
			return platform.TargetActionResult{Target: q.Target, Account: a.Account, Status: status, ResultID: platform.ExternalID{Namespace: "forge:comment", Value: "receipt-1"}, RetryClass: "never"}, nil
		}}}}}, nil
	}
	m.actionResolvers = map[string]actionTargetResolver{"forge-test": func(ctx context.Context, id MemberIdentity, l platform.TargetLocator) (platform.TargetResolution, referenceActionSession, error) {
		session, err := prepare(ctx, id, platform.AccessContext{})
		if err != nil {
			return platform.TargetResolution{}, session, err
		}
		r, err := session.Capabilities.Resolve(ctx, l, session.Access)
		return r, session, err
	}}
	m.referenceExecutors["forge-test"] = prepare
	server := httptest.NewServer(m)
	defer server.Close()
	cli := contractCLI(t, server.URL, svc, id)
	var resolved actionTargetView
	if err := cli([]string{"actions", "resolve", "--request", `{"provider":"forge-test","locator":{"url":"https://forge.invalid/mr/42"},"turn_number":1}`}, &resolved); err != nil || resolved.TargetRef == "" || len(resolved.Actions) != 1 {
		t.Fatal(resolved, err)
	}
	// Resolve does not create a unit or fabricate Inbox evidence.
	svc.store.mu.Lock()
	units, err := svc.store.followupUnitsLocked(id, "")
	svc.store.mu.Unlock()
	if err != nil || len(units) != 0 {
		t.Fatal(units, err)
	}

	policy, err := svc.store.readAgentPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	q := agentActionRequest{followupCommand: followupCommand{TurnNumber: 1}, TargetRef: resolved.TargetRef, ID: "standalone", PolicyToken: policy.PolicyToken, Authority: agentActionAuthority{Type: "human_reply", RoomSeq: post.Seq, Reason: "真人指令明确目标与正文"}, Operation: platform.OperationObjectComment, Input: CommentInput{Body: "已收到。"}}
	run := func(q agentActionRequest) (referenceActionReceipt, error) {
		t.Helper()
		body, _ := json.Marshal(q)
		var receipt referenceActionReceipt
		err := cli([]string{"actions", "execute", "--request", string(body)}, &receipt)
		return receipt, err
	}
	receipt, err := run(q)
	if err != nil || receipt.Status != "succeeded" || calls.Load() != 1 {
		t.Fatal(receipt, err, calls.Load())
	}
	var read actionQueryRecord
	if err := cli([]string{"actions", "read", "--id", q.ID}, &read); err != nil || read.Account != access.Account || read.Context != nil || read.Detail == nil || read.Detail.Basis.Source.Seq != post.Seq {
		t.Fatal(read, err)
	}
	if receipt, err = run(q); err != nil || receipt.Status != "succeeded" || calls.Load() != 1 {
		t.Fatal(receipt, err, calls.Load())
	}
	// Optional association uses the same member ledger and no duplicated unit action.
	svc.store.mu.Lock()
	stored, err := svc.store.saveTargetLocked(id, target)
	if err == nil {
		var u followupUnit
		u, err = svc.store.ensureUnitLocked(id, stored, 1)
		q.Context = &actionBusinessContext{u.ObjectRef, u.Ref, u.Revision}
	}
	svc.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	q.ID = "associated"
	if receipt, err = run(q); err != nil || receipt.Status != "succeeded" || calls.Load() != 2 {
		t.Fatal(receipt, err)
	}
	u, err := svc.store.readFollowup(id, q.Context.ObjectRef, q.Context.UnitRef)
	if err != nil || len(u.Actions) != 1 || u.Actions[0].Request.ID != q.ID {
		t.Fatal(u, err)
	}
	var disk followupUnit
	if err := readSnapshot(svc.store.unitPath(id, u.ObjectRef, u.Ref), &disk, followupBytes); err != nil || len(disk.Actions) != 0 {
		t.Fatal("unit contains a second ledger", disk, err)
	}
	// Reopen storage independently of the runtime; no automatic send recovery exists.
	if err := svc.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(svc.store.dir, id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	svc.store = reopened
	if receipt, err = run(q); err != nil || receipt.Status != "succeeded" || calls.Load() != 2 {
		t.Fatal(receipt, err, calls.Load())
	}
	q.Context = nil
	q.ID = "uncertain"
	q.Input.Body = "另一次明确任务"
	uncertain.Store(true)
	if receipt, err = run(q); err != nil || receipt.Status != "unknown" || calls.Load() != 3 {
		t.Fatal(receipt, err)
	}
	q.ID = "replacement"
	if _, err = run(q); err == nil || calls.Load() != 3 {
		t.Fatal("uncertain action replayed", err, calls.Load())
	}
	q.Operation = platform.OperationDiscussionReply
	if _, err = run(q); err == nil || calls.Load() != 3 {
		t.Fatal("wrong operation admitted", err)
	}
	var page struct {
		Items    []actionQueryRecord `json:"items"`
		Next     string              `json:"next"`
		Complete bool                `json:"complete"`
	}
	if err := cli([]string{"actions", "list", "--limit", "1"}, &page); err != nil || len(page.Items) != 1 || page.Next == "" || page.Complete {
		t.Fatal(page, err)
	}
	if err := cli([]string{"actions", "resolve", "--request", `{"provider":"gitlab","locator":{"url":"https://gitlab.invalid/g/p/-/merge_requests/42"},"turn_number":1}`}, &resolved); err != nil || resolved.Resolution.Status != platform.ResolutionUnsupported {
		t.Fatal("new production locator enabled", resolved, err)
	}
}

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
	access := platform.AccessContext{RoomIdentity: h.id.RoomIdentity, AgentID: h.id.AgentID, MembershipID: h.id.MembershipID, Account: platform.AccountIdentity{IdentityScope: target.IdentityScope, ID: platform.ExternalID{Namespace: "actor", Value: "writer"}}, ConnectionID: "connection", Generation: "generation", ConnectionVersion: 1, CredentialVersion: 1}
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

func TestActionsResolveExistingGitLabFollowup(t *testing.T) {
	for _, discussion := range []bool{false, true} {
		t.Run(map[bool]string{false: "object", true: "discussion"}[discussion], func(t *testing.T) {
			f := managedActionTest(t, discussion)
			server := httptest.NewServer(f.m)
			defer server.Close()
			cli := contractCLI(t, server.URL, f.svc, f.id)
			body, _ := json.Marshal(actionResolveRequest{Locator: platform.TargetLocator{TargetRef: f.action.TargetRef}, TurnNumber: 1})
			var target actionTargetView
			if err := cli([]string{"actions", "resolve", "--request", string(body)}, &target); err != nil || target.TargetRef != f.action.TargetRef || len(target.Actions) != 1 || !target.Actions[0].Executable {
				t.Fatal(target, err)
			}
			q := agentActionFixture(f.action)
			q.TargetRef = target.TargetRef
			q.Context = &actionBusinessContext{q.ObjectRef, q.UnitRef, q.Revision}
			q.ObjectRef, q.UnitRef, q.Revision = "", "", 0
			body, _ = json.Marshal(q)
			var receipt referenceActionReceipt
			if err := cli([]string{"actions", "execute", "--request", string(body)}, &receipt); err != nil || receipt.Status != "succeeded" || f.posts.Load() != 1 {
				t.Fatal(receipt, err, f.posts.Load())
			}
			assertActionCallsSettled(t, f.m)
		})
	}
}
