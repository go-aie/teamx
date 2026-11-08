package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
