package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

type replyContractReader struct{ messages map[string]lark.Message }

func (f *replyContractReader) Message(ctx context.Context, _ platform.AccountIdentity, id string) (lark.Message, error) {
	if err := ctx.Err(); err != nil {
		return lark.Message{}, err
	}
	m, ok := f.messages[id]
	if !ok {
		return m, errors.New("message unavailable")
	}
	return m, nil
}
func (*replyContractReader) Messages(context.Context, platform.AccountIdentity, lark.MessagesRequest) (lark.MessagePage, error) {
	return lark.MessagePage{}, ErrRequest
}
func (*replyContractReader) Chat(context.Context, platform.AccountIdentity, string) (lark.Chat, error) {
	return lark.Chat{ID: "oc_chat", Mode: "p2p", Name: "脱敏会话", TenantKey: "external_tenant"}, nil
}

type larkReplyContract struct {
	h       *larkHarness
	id      MemberIdentity
	cli     func([]string, any) error
	page    agentReferenceInboxPage
	reader  *replyContractReader
	post    PostReceipt
	calls   atomic.Int32
	unknown bool
}

func newLarkReplyContract(t *testing.T) *larkReplyContract {
	t.Helper()
	h := newLarkHarness(t)
	h.messages(2)
	// Two messages in the same native object exercise exact-message authority.
	fixtureData, err := os.ReadFile(filepath.Join(h.dir, "messages.json"))
	if err != nil {
		t.Fatal(err)
	}
	var captured []lark.Message
	if json.Unmarshal(fixtureData, &captured) != nil {
		t.Fatal("fixture messages")
	}
	captured[1].RootID, captured[1].ParentID = captured[0].ID, captured[0].ID
	fixtureData, _ = json.Marshal(captured)
	if err := os.WriteFile(filepath.Join(h.dir, "messages.json"), fixtureData, 0600); err != nil {
		t.Fatal(err)
	}
	h.start()
	for range 2 {
		if err := h.deliver(); err != nil {
			t.Fatal(err)
		}
	}
	f := &larkReplyContract{h: h, id: MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}, reader: &replyContractReader{messages: map[string]lark.Message{}}}
	data, err := os.ReadFile(filepath.Join(h.dir, "messages.json"))
	if err != nil {
		t.Fatal(err)
	}
	var messages []lark.Message
	if json.Unmarshal(data, &messages) != nil {
		t.Fatal("fixture messages")
	}
	for _, m := range messages {
		f.reader.messages[m.ID] = m
	}
	f.post, err = h.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: f.id.RoomIdentity, Caller: humanCaller, ClientMessageID: "lark-reply-authority", Text: "回复指定测试消息：已收到。"})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: f.id, TurnNumber: 1, LastOfferedUnreadSeq: f.post.Seq, LastOfferedInboxSeq: 2, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: f.post.Seq, OfferedUnreadSeq: f.post.Seq, InboxSnapshotSeq: 2}}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.m)
	t.Cleanup(server.Close)
	f.cli = contractCLI(t, server.URL, h.svc, f.id)
	if err = f.cli([]string{"inbox", "read"}, &f.page); err != nil || len(f.page.Entries) != 2 {
		t.Fatal(f.page, err)
	}
	caps := func() platform.ReferenceCapabilities {
		return lark.ReplyCapabilities(f.reader, func(ctx context.Context, id, body, key string) (string, error) {
			f.calls.Add(1)
			if f.unknown {
				return "", context.DeadlineExceeded
			}
			m := lark.Message{ID: "om_receipt", ChatID: "oc_chat", ParentID: id, RootID: id, Type: "text", Created: "101000", Sender: lark.Sender{ID: "ou_reader", IDType: "open_id", Type: "user", TenantKey: "account_tenant"}}
			b, _ := json.Marshal(map[string]string{"text": body})
			m.Body.Content = string(b)
			f.reader.messages[m.ID] = m
			return m.ID, nil
		}, "")
	}
	// Only this isolated test manager admits the unaccepted adapter. It retains
	// the real member capability/authority gates and shared CLI/HTTP/Store path.
	h.m.referenceAdapters["lark"] = caps
	h.m.referenceExecutors["lark"] = func(ctx context.Context, id MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
		h.m.mu.Lock()
		defer h.m.mu.Unlock()
		a, _, err := h.m.resolvePlatformAccountLocked(id.RoomIdentity, h.c.ID, "lark")
		if err != nil {
			return referenceActionSession{}, err
		}
		access := larkReferenceAccess(id, a, platform.SourceRef{})
		if old.Account.Valid() && old.Account != access.Account {
			return referenceActionSession{}, ErrIdentity
		}
		return referenceActionSession{Access: access, Capabilities: caps(), Close: func() {}, Check: func(ctx context.Context) error {
			h.m.mu.Lock()
			defer h.m.mu.Unlock()
			return h.m.checkLarkReferenceAccessLocked(ctx, toolsRequest{ServiceInfo: h.svc.info, Caller: agentCallerIdentity(id), Tool: "lark"}, access)
		}}, nil
	}
	h.m.actionResolvers["lark"] = func(ctx context.Context, id MemberIdentity, l platform.TargetLocator) (platform.TargetResolution, referenceActionSession, error) {
		session, err := h.m.referenceExecutors["lark"](ctx, id, platform.AccessContext{})
		if err != nil {
			return platform.TargetResolution{}, session, err
		}
		r, err := session.Capabilities.Resolve(ctx, l, session.Access)
		return r, session, err
	}
	bindCLITool(t, h.m, h.room.RoomIdentity, h.members[0], h.c, "lark", 0)
	return f
}
func (f *larkReplyContract) resolve(t *testing.T, ref string) actionTargetView {
	t.Helper()
	q := actionResolveRequest{Provider: "lark", Locator: platform.TargetLocator{TargetRef: ref}, TurnNumber: 1}
	b, _ := json.Marshal(q)
	var v actionTargetView
	if err := f.cli([]string{"actions", "resolve", "--request", string(b)}, &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func (f *larkReplyContract) grant(t *testing.T) {
	t.Helper()
	h := f.h
	h.m.mu.Lock()
	bindings, err := h.m.readToolBindingsLocked(f.id.RoomIdentity)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	r := managerHTTP(h.m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: h.m.epoch, RoomIdentity: f.id.RoomIdentity, Caller: humanCaller, Revision: bindings.Revision, Enabled: true, ConnectionVersion: h.c.Version, Binding: tool.Binding{AgentID: f.id.AgentID, MembershipID: f.id.MembershipID, Tool: "lark", ConnectionID: h.c.ID, ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: []tool.ActionGrant{{Operation: "message.reply", TargetKind: "message", ObjectType: "message"}}}}})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
}
func (f *larkReplyContract) request(t *testing.T, ref string) agentActionRequest {
	t.Helper()
	p, err := f.h.svc.store.readAgentPolicy(f.id)
	if err != nil {
		t.Fatal(err)
	}
	var input ActionInput
	if json.Unmarshal([]byte(`{"body":"已收到。","reply_mode":"chat"}`), &input) != nil {
		t.Fatal("input")
	}
	return agentActionRequest{followupCommand: followupCommand{TurnNumber: 1}, TargetRef: ref, ID: "lark-reply", PolicyToken: p.PolicyToken, Authority: agentActionAuthority{Type: "human_reply", RoomSeq: f.post.Seq, Reason: "本次准确目标和正文"}, Operation: platform.OperationMessageReply, Input: input}
}
func (f *larkReplyContract) run(q agentActionRequest) (referenceActionReceipt, error) {
	b, _ := json.Marshal(q)
	var r referenceActionReceipt
	err := f.cli([]string{"actions", "execute", "--request", string(b)}, &r)
	return r, err
}

func TestLarkReplyCommonCLIThreeAuthorities(t *testing.T) {
	for _, authority := range []string{"human_reply", "rule", "instruction", "question"} {
		t.Run(authority, func(t *testing.T) {
			f := newLarkReplyContract(t)
			ref := f.page.Entries[0].Source.TargetRef
			v := f.resolve(t, ref)
			if v.TargetRef != ref || len(v.Actions) != 1 || v.Actions[0].Executable || v.Actions[0].Operation != platform.OperationMessageReply || v.Resolution.Account.ID.Value != "ou_reader" {
				t.Fatal(v)
			}
			q := f.request(t, ref)
			if _, err := f.run(q); err == nil || f.calls.Load() != 0 {
				t.Fatal("read binding granted write", err)
			}
			f.grant(t)
			v = f.resolve(t, ref)
			if !v.Actions[0].Executable {
				t.Fatal(v)
			}
			object := f.page.Followups[0].ObjectRef
			u, err := f.h.svc.store.readFollowup(f.id, object, referenceDigest("unit_", object))
			if err != nil {
				t.Fatal(err)
			}
			q.Context = &actionBusinessContext{u.ObjectRef, u.Ref, u.Revision}
			switch authority {
			case "rule":
				c := policyRule()
				c.Text = "指定测试消息可以回复已收到。"
				_, e := f.h.svc.store.changePolicy(f.id, humanCaller, c)
				if e != nil {
					t.Fatal(e)
				}
				q.Authority = agentActionAuthority{Type: "rule", ID: c.ID, Version: 1, Reason: "指定目标符合长期规则"}
				p, _ := f.h.svc.store.readAgentPolicy(f.id)
				q.PolicyToken = p.PolicyToken
			case "instruction":
				u, err = f.h.svc.store.saveFollowupInstruction(f.id, followupInstructionChange{followupCommand: conversationCommand(u, 1), MessageRefs: []string{ref}, SourceSeq: f.post.Seq, Intent: followupIntent{Operation: platform.OperationMessageReply, Body: q.Input.Body}, Basis: "真人明确指令"})
				if err != nil {
					t.Fatal(err)
				}
				i := u.Instructions[0]
				q.Authority = agentActionAuthority{Type: "instruction", ID: i.Ref, Version: i.Version, Reason: "按原消息和指令执行"}
			case "question":
				u, err = f.h.svc.askFollowup(context.Background(), f.id, followupQuestionChange{followupCommand: conversationCommand(u, 1), MessageRefs: []string{ref}, Text: "@operator 回复已收到，可以吗？", Proposed: &followupIntent{Operation: platform.OperationMessageReply, Body: q.Input.Body}})
				if err != nil {
					t.Fatal(err)
				}
				question := u.Questions[0]
				answer, e := f.h.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: f.id.RoomIdentity, Caller: humanCaller, ClientMessageID: "lark-answer", Text: "可以，按这个正文回复。", ReplyTo: question.Seq})
				if e != nil {
					t.Fatal(e)
				}
				u, err = f.h.svc.readFollowupConversation(context.Background(), f.id, conversationCommand(u, 1))
				if err != nil {
					t.Fatal(err)
				}
				q.Authority = agentActionAuthority{Type: "human_reply", RoomSeq: answer.Seq, QuestionRef: question.Ref, Reason: "真人明确回答了本次问题"}
			default:
				q.Authority.MessageRefs = []string{ref}
			}
			q.Context.UnitVersion = u.Revision
			r, err := f.run(q)
			if err != nil || r.Status != "succeeded" || f.calls.Load() != 1 {
				t.Fatal(r, err, f.calls.Load())
			}
			if r, err = f.run(q); err != nil || r.Status != "succeeded" || f.calls.Load() != 1 {
				t.Fatal("duplicate", r, err)
			}
			var read actionQueryRecord
			if err = f.cli([]string{"actions", "read", "--id", q.ID}, &read); err != nil || read.Detail == nil || read.Account.Provider != "lark" {
				t.Fatal(read, err)
			}
			u, err = f.h.svc.store.readFollowup(f.id, u.ObjectRef, u.Ref)
			if err != nil || len(u.Actions) != 1 || u.Actions[0].Request.TargetRef != ref {
				t.Fatal(u, err)
			}
			var disk followupUnit
			if err = readSnapshot(f.h.svc.store.unitPath(f.id, u.ObjectRef, u.Ref), &disk, followupBytes); err != nil || len(disk.Actions) != 0 {
				t.Fatal("duplicate action ledger", err)
			}
			// New IDs cannot redirect saved instruction/question authority to a sibling message.
			if authority != "rule" {
				other := f.resolve(t, f.page.Entries[1].Source.TargetRef)
				changed := q
				changed.ID = "redirect"
				changed.TargetRef = other.TargetRef
				changed.Context = &actionBusinessContext{u.ObjectRef, u.Ref, u.Revision}
				if _, e := f.run(changed); e == nil || f.calls.Load() != 1 {
					t.Fatal("authority retargeted", e)
				}
			}
			q.Input.Body = "changed"
			q.Input.raw = ""
			if _, err = f.run(q); err == nil || f.calls.Load() != 1 {
				t.Fatal("ID accepted changed input", err)
			}
		})
	}
}

func TestLarkReplyUnknownMixedTargetsAndRestart(t *testing.T) {
	f := newLarkReplyContract(t)
	f.grant(t)
	v := f.resolve(t, f.page.Entries[0].Source.TargetRef)
	q := f.request(t, v.TargetRef)
	wrong := q
	wrong.Operation = platform.OperationObjectComment
	if _, err := f.run(wrong); err == nil || f.calls.Load() != 0 {
		t.Fatal("GitLab operation used for Lark", err)
	}
	wrongResolve, _ := json.Marshal(actionResolveRequest{Provider: "gitlab", TurnNumber: 1, Locator: platform.TargetLocator{TargetRef: q.TargetRef}})
	if err := f.cli([]string{"actions", "resolve", "--request", string(wrongResolve)}, &v); err == nil {
		t.Fatal("provider mismatch accepted")
	}
	server := httptest.NewServer(f.h.m)
	defer server.Close()
	otherID := f.id
	otherID.AgentID = f.h.members[1].AgentID
	otherID.MembershipID = f.h.members[1].MembershipID
	otherCLI := contractCLI(t, server.URL, f.h.svc, otherID)
	b, _ := json.Marshal(q)
	if err := otherCLI([]string{"actions", "execute", "--request", string(b)}, new(referenceActionReceipt)); err == nil || f.calls.Load() != 0 {
		t.Fatal("cross-member action", err)
	}
	f.unknown = true
	r, err := f.run(q)
	if err != nil || r.Status != "unknown" || r.Attempts != 1 || f.calls.Load() != 1 {
		t.Fatal(r, err)
	}
	if err = f.h.svc.store.Close(); err != nil {
		t.Fatal(err)
	}
	f.h.svc.store, err = OpenStore(f.h.svc.store.dir, f.id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if r, err = f.run(q); err != nil || r.Status != "unknown" || f.calls.Load() != 1 {
		t.Fatal("restart replay", r, err)
	}
	q.ID = "replacement"
	if _, err = f.run(q); err == nil || f.calls.Load() != 1 {
		t.Fatal("unknown replayed with new ID", err)
	}
}

func TestLarkReplyProductionRegistrationRequiresSavedGrant(t *testing.T) {
	h := newLarkHarness(t)
	caps, err := h.m.referenceCapabilities("lark")
	if err != nil || len(caps.Actions) != 3 || caps.Actions[platform.OperationMessageReply].Spec.DisabledReason != "" || h.m.referenceExecutors["lark"] == nil || h.m.actionResolvers["lark"] == nil {
		t.Fatal(caps, err)
	}
	f := bindCLITool(t, h.m, h.room.RoomIdentity, h.members[0], h.c, "lark", 0)
	h.m.mu.Lock()
	selected, e := h.m.selectionsLocked(f, agentCaller(h.members[0].AgentID, h.members[0].MembershipID))
	h.m.mu.Unlock()
	if e != nil || len(selected) != 1 || selected[0].ActionAuthorization != nil {
		t.Fatal("read-only binding silently gained grants", selected, e)
	}
	r := managerHTTP(h.m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: h.m.epoch, RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, Revision: f.Revision, Enabled: true, ConnectionVersion: h.c.Version, Binding: tool.Binding{AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID, Tool: "lark", ConnectionID: h.c.ID, ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: []tool.ActionGrant{{Operation: "message.reply", TargetKind: "message", ObjectType: "message"}}}}})
	if r.Code != 200 {
		t.Fatal("accepted reply grant rejected", r.Code, r.Body.String())
	}
}

func TestLarkReplyProductionResolvesCapturedMessageWithReadOnlyBinding(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS pinned backend")
	}
	h := newLarkHarness(t)
	h.start()
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	if err := h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 1, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.m)
	defer server.Close()
	cli := contractCLI(t, server.URL, h.svc, id)
	var inbox agentReferenceInboxPage
	if err := cli([]string{"inbox", "read"}, &inbox); err != nil {
		t.Fatal(err)
	}
	// Extend only the local CLI fixture to support isolated fixed configuration.
	script := strings.Replace(l2Fixture, "path=a[2]", `if a==['config','show']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','dpopMode':'required'}));sys.exit()
assert a[1]=='GET' or a[2]=='/open-apis/contact/v3/users/basic_batch', 'unexpected write'
path=a[2]`, 1)
	if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	bindCLITool(t, h.m, h.room.RoomIdentity, h.members[0], h.c, "lark", 0)
	q := actionResolveRequest{Provider: "lark", TurnNumber: 1, Locator: platform.TargetLocator{TargetRef: inbox.Entries[0].Source.TargetRef}}
	b, _ := json.Marshal(q)
	var v actionTargetView
	if err := cli([]string{"actions", "resolve", "--request", string(b)}, &v); err != nil || v.TargetRef != q.Locator.TargetRef || len(v.Actions) != 1 || v.Actions[0].Executable || v.Actions[0].DisabledReason != "Member action capability is disabled on the current binding." {
		t.Fatal(v, err)
	}
	h.m.mu.Lock()
	active := len(h.m.platformCalls)
	h.m.mu.Unlock()
	if active != 0 {
		t.Fatal("unsettled managed resolve", active)
	}
	for _, mode := range []string{"cancel", "unbind"} {
		// Force bootstrap from captured evidence, including its error cleanup.
		_ = os.Remove(h.svc.store.actionTargetPath(id, q.Locator.TargetRef))
		blocking := strings.Replace(script, "path=a[2]", `path=a[2]
if a[1]=='GET' and path.startswith('/open-apis/im/v1/messages/'):
 import time
 open(base+'/pin-root','w').write(os.environ['LARKSUITE_CLI_CONFIG_DIR'])
 open(base+'/blocked','w').write('ready')
 time.sleep(60)`, 1)
		if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte(blocking), 0700); err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(filepath.Join(h.dir, "blocked"))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := h.m.resolveActionTarget(ctx, apiRequest{ServiceInfo: h.svc.info, Caller: agentCallerIdentity(id)}, q)
			done <- err
		}()
		deadline := time.Now().Add(8 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(h.dir, "blocked")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatal("managed CLI did not reach read")
			}
			time.Sleep(5 * time.Millisecond)
		}
		if mode == "cancel" {
			cancel()
		} else {
			r := managerHTTP(h.m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: h.m.epoch, RoomIdentity: id.RoomIdentity, Caller: humanCaller, Revision: 1, Enabled: false, Binding: tool.Binding{AgentID: id.AgentID, MembershipID: id.MembershipID, Tool: "lark"}})
			if r.Code != 200 {
				cancel()
				t.Fatal(r.Code, r.Body.String())
			}
		}
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("cancelled read admitted")
			}
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("managed CLI did not settle")
		}
		cancel()
		assertActionCallsSettled(t, h.m)
		pinRoot, err := os.ReadFile(filepath.Join(h.dir, "pin-root"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(string(pinRoot)); !os.IsNotExist(err) {
			t.Fatal("pinned context leaked", err)
		}
	}
}

func TestLarkReplyMixedGitlabDispatch(t *testing.T) {
	f := newLarkReplyContract(t)
	f.grant(t)
	larkView := f.resolve(t, f.page.Entries[0].Source.TargetRef)
	var c platformConnection
	for _, candidate := range platformTestList(t, f.h.m) {
		if candidate.Provider == "gitlab" {
			c = candidate
		}
	}
	if c.ID == "" {
		t.Fatal("missing isolated GitLab connection")
	}
	bindCLITool(t, f.h.m, f.id.RoomIdentity, f.h.members[0], c, "gitlab", 2)
	remote := &managedActionFixture{action: referenceActionRequest{Operation: platform.OperationObjectComment}}
	managedReferenceHTTP(t, f.h.m, remote.serve)
	f.h.m.mu.Lock()
	account, _, err := f.h.m.resolvePlatformAccountLocked(f.id.RoomIdentity, c.ID, "gitlab")
	f.h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	access := gitlabReferenceAccess(f.id, account, account.Identity.ID)
	target := platform.TargetIdentity{IdentityScope: access.Account.IdentityScope, Containers: []platform.NativeIdentity{{Type: "Project", ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "123"}}}, Object: platform.NativeIdentity{Type: "MergeRequest", ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "9001"}}}
	alias := target
	alias.Object.ID = platform.ExternalID{Namespace: "gitlab:rest:iid", Value: "42"}
	ref, err := f.h.svc.store.saveActionTarget(f.id, platform.TargetResolution{Status: platform.ResolutionResolved, Target: &target, Aliases: []platform.TargetIdentity{alias}, Account: access.Account, Complete: true, ObjectVerified: true, CheckedAt: time.Now().UTC()}, access)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(actionResolveRequest{Provider: "gitlab", TurnNumber: 1, Locator: platform.TargetLocator{TargetRef: ref}})
	var gitlabView actionTargetView
	if err = f.cli([]string{"actions", "resolve", "--request", string(b)}, &gitlabView); err != nil || len(gitlabView.Actions) != 1 || gitlabView.Actions[0].Operation != platform.OperationObjectComment || gitlabView.Resolution.Account.Provider != "gitlab" {
		t.Fatal(gitlabView, err)
	}
	q := f.request(t, larkView.TargetRef)
	wrong := q
	wrong.TargetRef = ref
	if _, err = f.run(wrong); err == nil || f.calls.Load() != 0 || remote.posts.Load() != 0 {
		t.Fatal("message.reply reached GitLab", err)
	}
	q.ID = "gitlab-comment"
	q.TargetRef = ref
	q.Operation = platform.OperationObjectComment
	q.Input = CommentInput{Body: "exact reply"}
	r, err := f.run(q)
	if err != nil || r.Status != "succeeded" || r.Result == nil || r.Result.Account.Provider != "gitlab" || remote.posts.Load() != 1 || f.calls.Load() != 0 {
		t.Fatal(r, err)
	}
	q = f.request(t, larkView.TargetRef)
	r, err = f.run(q)
	if err != nil || r.Status != "succeeded" || r.Result == nil || r.Result.Account.Provider != "lark" || f.calls.Load() != 1 || remote.posts.Load() != 1 {
		t.Fatal(r, err)
	}
}
