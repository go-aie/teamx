package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Deliberately not a production provider. String tenant/account/message IDs,
// no project, IID, username, PAT, or discussions. It uses the production manager
// dispatch, delivery, CLI, Store and execute orchestration without core branches.
type referenceContractAdapter struct {
	mu                           sync.Mutex
	resolves, inspections, sends int
	requests                     []platform.TargetActionRequest
}

func contractSource(key, object string) platform.SourceRef {
	return platform.SourceRef{Provider: "contract", Instance: "contract://work", Tenant: platform.ExternalID{Namespace: "contract:tenant", Value: "tenant-blue"},
		Channel: "messages", SubscriptionID: "cccccccccccccccccccccccccccccccc", ItemKey: key, Object: "same display title",
		Reference: platform.NotificationReference{Version: 1, Scope: "tenant", RecipientID: platform.ExternalID{Namespace: "contract:account", Value: "account-alice"},
			Object: platform.ObjectReference{Type: "Conversation", ID: platform.ExternalID{Namespace: "contract:conversation", Value: object}},
			NoteID: platform.ExternalID{Namespace: "contract:message", Value: "message-" + key}}}
}

func contractAccess(id MemberIdentity, scope platform.IdentityScope) platform.AccessContext {
	a := platform.AccountIdentity{IdentityScope: scope, ID: platform.ExternalID{Namespace: "contract:account", Value: "account-alice"}}
	return platform.AccessContext{RoomIdentity: id.RoomIdentity, AgentID: id.AgentID, MembershipID: id.MembershipID,
		Account: a, Receiver: a, ConnectionID: "contract-connection", Generation: "contract-generation", ConnectionVersion: 1, CredentialVersion: 1}
}

func (a *referenceContractAdapter) capabilities() platform.ReferenceCapabilities {
	return platform.ReferenceCapabilities{
		Resolve: func(ctx context.Context, q platform.TargetLocator, access platform.AccessContext) (platform.TargetResolution, error) {
			a.mu.Lock()
			a.resolves++
			a.mu.Unlock()
			if ctx.Err() != nil {
				return platform.TargetResolution{}, ctx.Err()
			}
			if q.Source == nil || q.Source.Provider != access.Account.Provider || q.Source.Tenant != access.Account.Tenant || q.Source.Reference.RecipientID != access.Account.ID {
				return platform.TargetResolution{}, ErrIdentity
			}
			target := platform.TargetIdentity{IdentityScope: access.Account.IdentityScope, Object: platform.NativeIdentity{Type: q.Source.Reference.Object.Type, ID: q.Source.Reference.Object.ID}, Message: q.Source.Reference.NoteID}
			return platform.TargetResolution{Status: platform.ResolutionResolved, Target: &target, Account: access.Account, CheckedAt: time.Now().UTC(), ObjectVerified: true, MessageVerified: true, Complete: true}, nil
		},
		Actions: map[platform.Operation]platform.ActionCapability{platform.OperationObjectComment: {
			Spec: platform.ActionSpec{Operation: platform.OperationObjectComment, TargetKind: "object", ObjectTypes: []string{"Conversation"}, InputSchema: json.RawMessage(`{"type":"object","required":["body"],"properties":{"body":{"type":"string"}},"additionalProperties":false}`), Retry: platform.RetryPolicy{MaxAttempts: 1}},
			Inspect: func(ctx context.Context, q platform.TargetActionRequest, access platform.AccessContext) (platform.TargetActionInspection, error) {
				a.mu.Lock()
				a.inspections++
				a.mu.Unlock()
				return platform.TargetActionInspection{Target: q.Target, Account: access.Account}, ctx.Err()
			},
			Execute: func(ctx context.Context, q platform.TargetActionRequest, access platform.AccessContext) (platform.TargetActionResult, error) {
				a.mu.Lock()
				defer a.mu.Unlock()
				if ctx.Err() != nil {
					return platform.TargetActionResult{}, ctx.Err()
				}
				a.sends++
				a.requests = append(a.requests, q)
				return platform.TargetActionResult{Target: q.Target, Account: access.Account, Status: "succeeded", ResultID: platform.ExternalID{Namespace: "contract:message", Value: fmt.Sprintf("sent-%d", a.sends)}, RetryClass: "never"}, nil
			},
		}},
	}
}

func (a *referenceContractAdapter) install(m *roomManager, svc *service) {
	m.referenceAdapters["contract"] = func() platform.ReferenceCapabilities { return a.capabilities() }
	m.referenceResolvers["contract"] = func(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		access := contractAccess(id, platform.IdentityScope{Provider: source.Provider, Instance: source.Instance, Tenant: source.Tenant})
		caps, err := m.referenceCapabilities("contract")
		if err != nil {
			return platform.TargetResolution{}, access, err
		}
		r, err := caps.Resolve(ctx, platform.TargetLocator{Source: &source}, access)
		return r, access, err
	}
	m.referenceExecutors["contract"] = func(ctx context.Context, id MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
		caps, err := m.referenceCapabilities("contract")
		return referenceActionSession{Access: contractAccess(id, old.Account.IdentityScope), Capabilities: caps, Check: func(ctx context.Context) error { return ctx.Err() }, Close: func() {}}, err
	}
	m.bindPlatformReferences(svc)
}

func contractDeliver(t *testing.T, m *roomManager, svc *service, id MemberIdentity, source platform.SourceRef, text string) {
	t.Helper()
	item := notification.Item{Key: source.ItemKey, DeliveryID: "contract-" + source.ItemKey, CreatedAt: time.Now().UTC(), Order: source.ItemKey, Source: source, Text: text,
		Route: notification.Route{Version: 1, Target: inboxTarget(id)}}
	m.mu.Lock()
	err := m.commitPlatformNotificationLocked(context.Background(), notification.Metadata{Key: notification.Key{Owner: id.RoomIdentity, Source: notification.SourceID{Provider: source.Provider, Channel: source.Channel}, SubscriptionID: source.SubscriptionID}}, item)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

func contractCLI(t *testing.T, endpoint string, svc *service, id MemberIdentity) func([]string, any) error {
	t.Helper()
	env := cliToolEnv(endpoint, svc, MemberState{AgentID: id.AgentID, MembershipID: id.MembershipID})
	return func(args []string, out any) error {
		var stdout, stderr bytes.Buffer
		err := runCLI(context.Background(), args, &stdout, &stderr, env)
		if err == nil && out != nil {
			err = json.Unmarshal(stdout.Bytes(), out)
		}
		return err
	}
}

func referenceExecuteArgs(t *testing.T, q referenceActionRequest) []string {
	args := entryChange(t, "actions", "execute", q)
	args[2] = "--request"
	return args
}

func TestReferenceCrossPlatformManagedChain(t *testing.T) {
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

func mustContractJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestReferenceCrossPlatformIdentityAndFailure(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	a := &referenceContractAdapter{}
	sources := []platform.SourceRef{contractSource("a", "same"), contractSource("b", "same"), contractSource("c", "same"), contractSource("d", "same"), contractSource("denied", "same")}
	sources[1].Tenant.Value = "tenant-green"
	sources[2].Provider = "another-provider"
	sources[3].Instance = "contract://another"
	for _, source := range sources {
		receiveReference(t, h, source.ItemKey, source)
	}
	inboxRunning(t, h, 1, uint64(len(sources)))
	resolve := func(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		access := contractAccess(id, platform.IdentityScope{Provider: source.Provider, Instance: source.Instance, Tenant: source.Tenant})
		if source.ItemKey == "denied" {
			return platform.TargetResolution{Status: platform.ResolutionUnavailable, ReasonCode: "permission_denied", CheckedAt: time.Now().UTC()}, access, nil
		}
		r, e := a.capabilities().Resolve(ctx, platform.TargetLocator{Source: &source}, access)
		return r, access, e
	}
	page, err := referenceTestService(h).readReferenceInbox(h.ctx, h.id, 1, 25, resolve)
	if err != nil || len(page.Followups) != 4 {
		t.Fatal(page, err)
	}
	refs := map[string]bool{}
	for _, u := range page.Followups {
		if refs[u.ObjectRef] {
			t.Fatal("different namespaces merged")
		}
		refs[u.ObjectRef] = true
	}
	inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: page.Batch.Receipt})
	list, err := h.store.listFollowups(h.id, followupQuery{Limit: 25})
	if err != nil || list.Total != 5 {
		t.Fatal(list, err)
	}
	blocked := list.Items[0]
	found := false
	for _, u := range list.Items {
		if u.Kind == "unresolved_source" {
			blocked = u
			found = true
		}
	}
	if !found || blocked.ReasonCode != "permission_denied" || blocked.ObjectRef != "" {
		t.Fatal("consumed failure disappeared", list)
	}
	if _, err := h.store.ensureDiscussionFollowup(h.id, 5, 1, blocked.Ref); err == nil {
		t.Fatal("unresolved source became write target")
	}
}

func TestReferenceCrossPlatformRuleKeepsNativeExecutionChecks(t *testing.T) {
	for _, mode := range []string{"exact", "tenant", "account-namespace", "account-id", "native-id", "containers"} {
		t.Run(mode, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			initReferenceTest(t, h)
			a := &referenceContractAdapter{}
			source := contractSource("rule", "receipt-conversation")
			receiveReference(t, h, source.ItemKey, source)
			inboxRunning(t, h, 1, 1)
			svc := referenceTestService(h)
			access := contractAccess(h.id, platform.IdentityScope{Provider: source.Provider, Instance: source.Instance, Tenant: source.Tenant})
			page, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, func(ctx context.Context, _ MemberIdentity, s platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
				r, e := a.capabilities().Resolve(ctx, platform.TargetLocator{Source: &s}, access)
				return r, access, e
			})
			if err != nil || len(page.Followups) != 1 {
				t.Fatal(page, err)
			}
			u, err := h.store.readFollowup(h.id, page.Followups[0].ObjectRef, page.Followups[0].Ref)
			if err != nil {
				t.Fatal(err)
			}
			human := postConversationReply(t, h, "standing", "以后当前租户、当前账号的无父容器 Conversation 验收收件任务直接回复“R6 已收到”，其他任务不适用", 0)
			change := conversationPolicyChange{Change: PolicyChange{ID: "native-rule", Status: "active", Text: "仅验收收件任务，核对账号和目标后评论准确正文 R6 已收到；所有其他任务不适用", SourceSeq: human.Seq, TurnNumber: 1}, Mode: "explicit_long_term", Basis: "用户明确的准确租户账号范围长期指令"}
			view, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), change)
			if err != nil {
				t.Fatal(err)
			}
			q := referenceActionRequest{followupCommand: conversationCommand(u, 1), TargetRef: u.TargetRef, ID: "native-rule-action", PolicyToken: view.PolicyToken, Authority: referenceActionAuthority{RuleID: "native-rule", RuleVersion: 1, Reason: "本次为准确范围的验收收件任务"}, Operation: platform.OperationObjectComment, Input: CommentInput{Body: "R6 已收到"}}
			prepare := func(ctx context.Context, _ MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
				caps := a.capabilities()
				cap := caps.Actions[platform.OperationObjectComment]
				inspect := cap.Inspect
				cap.Inspect = func(ctx context.Context, q platform.TargetActionRequest, access platform.AccessContext) (platform.TargetActionInspection, error) {
					i, err := inspect(ctx, q, access)
					switch mode {
					case "tenant":
						i.Account.Tenant.Value = "other-tenant"
					case "account-namespace":
						i.Account.ID.Namespace = "another:account"
					case "account-id":
						i.Account.ID.Value = "another-account"
					case "native-id":
						i.Target.Object.ID.Value = "another-object"
					case "containers":
						i.Target.Containers = []platform.NativeIdentity{{Type: "Workspace", ID: platform.ExternalID{Namespace: "contract:workspace", Value: "other"}}}
					}
					return i, err
				}
				caps.Actions[platform.OperationObjectComment] = cap
				return referenceActionSession{Access: old, Capabilities: caps, Check: func(ctx context.Context) error { return ctx.Err() }, Close: func() {}}, nil
			}
			got, err := svc.executeReferenceAction(h.ctx, h.id, q, prepare)
			if mode == "exact" {
				if err != nil || got.Status != "succeeded" || a.sends != 1 {
					t.Fatal(got, err)
				}
			} else if err == nil || a.sends != 0 {
				t.Fatal("mismatched native identity dispatched", got, err)
			}
		})
	}
}
