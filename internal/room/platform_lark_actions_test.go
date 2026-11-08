package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

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
