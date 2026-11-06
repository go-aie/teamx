package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"strings"
	"testing"
	"time"
)

func sampleMessage(id, chat, text string) Message {
	m := Message{ID: id, ChatID: chat, Type: "text", Created: "1001000", Updated: "1001000", Sender: Sender{ID: "ou_sender", IDType: "open_id", Type: "user"}}
	b, _ := json.Marshal(map[string]string{"text": text})
	m.Body.Content = string(b)
	return m
}
func sampleBinding() Binding {
	return Binding{ConnectionID: "connection", Generation: "generation", CredentialVersion: 1, Fingerprint: strings.Repeat("a", 64), Account: platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: Origin("feishu"), Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "account_tenant"}}, ID: platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_reader"}}}
}
func sampleState(t *testing.T) State {
	t.Helper()
	s, e := Configure(platform.RoomIdentity{RoomID: "room", RoomInstance: "instance"}, nil, sampleBinding(), Config{Enabled: true, ConnectionID: "connection", Chats: []string{"oc_chat"}, Target: notification.Target{Mode: "inbox", AgentID: "agent", MembershipID: "member"}, Private: true, GroupMentions: true, IntervalSeconds: 60}, time.Unix(1000, 0))
	if e != nil {
		t.Fatal(e)
	}
	return s
}

type fakeReader struct {
	chat     Chat
	pages    func(MessagesRequest) (MessagePage, error)
	messages map[string]Message
	calls    int
	requests []MessagesRequest
}

func (f *fakeReader) Chat(context.Context, platform.AccountIdentity, string) (Chat, error) {
	f.calls++
	return f.chat, nil
}
func (f *fakeReader) Messages(_ context.Context, _ platform.AccountIdentity, r MessagesRequest) (MessagePage, error) {
	f.calls++
	f.requests = append(f.requests, r)
	return f.pages(r)
}
func (f *fakeReader) Message(_ context.Context, _ platform.AccountIdentity, id string) (Message, error) {
	f.calls++
	if m, ok := f.messages[id]; ok {
		return m, nil
	}
	return Message{}, failure("permission_denied")
}

type stepHarness struct {
	s          State
	now        time.Time
	limits     notification.Limits
	failResult bool
	force      bool
	profile    *platform.CLIProfile
}

func (h *stepHarness) Read(context.Context) (notification.State, notification.Scope, error) {
	q := QueryScope{Binding: h.s.Binding, ConnectionVersion: 1, Profile: platform.CLIProfile{Version: 1, Executable: "/fixture/lark-cli", Profile: "fixture", Brand: "feishu", CLIVersion: CLIVersion}}
	if h.profile != nil {
		q.Profile = *h.profile
	}
	b, _ := json.Marshal(q)
	return h.s.Clone(), notification.Scope{Data: b, Revision: "one"}, nil
}
func (h *stepHarness) Commit(s notification.State) error {
	n := s.(State)
	if e := n.Validate(n.RoomIdentity); e != nil {
		return e
	}
	if h.failResult && len(n.Observed) > 0 {
		return errors.New("save failure")
	}
	h.s = n.Clone()
	return nil
}
func (h *stepHarness) Now() time.Time                       { return h.now }
func (h *stepHarness) Jitter(d time.Duration) time.Duration { return d }
func (h *stepHarness) Limits() notification.Limits          { return h.limits }
func (h *stepHarness) ConsumeForce() bool                   { b := h.force; h.force = false; return b }
func newStepHarness(t *testing.T) (*stepHarness, *fakeReader, notification.Runner) {
	h := &stepHarness{s: sampleState(t), now: time.Unix(1010, 0), limits: notification.DefaultLimits()}
	f := &fakeReader{chat: Chat{ID: "oc_chat", Mode: "p2p", TenantKey: "chat_tenant"}}
	s := &Source{Reader: func(QueryScope) (MessageReader, error) { return f, nil }}
	return h, f, s.NewRunner(h)
}
func TestMessagesFilteringAndContent(t *testing.T) {
	c := Config{Private: true, GroupMentions: true}
	m := sampleMessage("om_one", "oc_chat", "hello")
	for _, test := range []struct {
		name            string
		edit            func(*Message)
		mode            string
		all, bots, want bool
	}{
		{"private", func(*Message) {}, "p2p", false, false, true},
		{"self", func(m *Message) { m.Sender.ID = "ou_reader" }, "p2p", false, false, false},
		{"system", func(m *Message) { m.Type = "system" }, "p2p", false, false, false},
		{"bot_off", func(m *Message) { m.Sender.Type = "app" }, "p2p", false, false, false},
		{"bot_on", func(m *Message) { m.Sender.Type = "app" }, "p2p", false, true, true},
		{"name_not_identity", func(m *Message) { m.Mentions = []Mention{{ID: "ou_other", IDType: "open_id", Name: "reader"}} }, "group", false, false, false},
		{"at_me", func(m *Message) { m.Mentions = []Mention{{ID: "ou_reader", IDType: "open_id"}} }, "group", false, false, true},
		{"native_all", func(m *Message) { m.Mentions = nil; m.Body.Content = `{"text":"@_all fixture"}` }, "group", true, false, true},
		{"native_all_off", func(m *Message) { m.Mentions = nil; m.Body.Content = `{"text":"@_all fixture"}` }, "group", false, false, false},
		{"all_off", func(m *Message) { m.Mentions = []Mention{{ID: "all"}} }, "group", false, false, false},
		{"all_on", func(m *Message) { m.Mentions = []Mention{{ID: "all"}} }, "group", true, false, true},
		{"rich_all", func(m *Message) {
			m.Type = "post"
			m.Body.Content = `{"zh_cn":{"title":"标题","content":[[{"tag":"at","user_id":"all"},{"tag":"text","text":"hello"}]]}}`
		}, "group", true, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			v := m
			test.edit(&v)
			cfg := c
			cfg.AtAll = test.all
			cfg.Bots = test.bots
			if Matches(v, Chat{Mode: test.mode}, "ou_reader", cfg) != test.want {
				t.Fatal("filter mismatch")
			}
		})
	}
	m.Type = "post"
	m.Body.Content = `{"zh_cn":{"title":"标题","content":[[{"tag":"text","text":"正文"},{"tag":"img","image_key":"img_fixture"}]]}}`
	if text := m.Content(); !strings.Contains(text, "正文") || !strings.Contains(text, "img_fixture") {
		t.Fatal(text)
	}
	m.Type = "file"
	m.Body.Content = `{"file_name":"fixture.pdf","file_key":"file_fixture"}`
	if text := m.Content(); !strings.Contains(text, "file_fixture") || !strings.Contains(text, "未读取") {
		t.Fatal(text)
	}
}
func TestMessagesPageRecoveryAtomicCheckpointAndRevision(t *testing.T) {
	h, f, r := newStepHarness(t)
	a := sampleMessage("om_one", "oc_chat", "one")
	b := sampleMessage("om_two", "oc_chat", "two")
	f.pages = func(q MessagesRequest) (MessagePage, error) {
		if q.PageToken == "" {
			return MessagePage{Items: []Message{a}, HasMore: true, PageToken: "second"}, nil
		}
		return MessagePage{Items: []Message{b}}, nil
	}
	r.Step(context.Background())
	if f.calls != 1 || len(h.s.Chats) != 1 {
		t.Fatal("metadata not a single step")
	}
	h.failResult = true
	r.Step(context.Background())
	if len(h.s.Observed) != 0 || h.s.Chats["oc_chat"].Completed != 0 || h.s.Chats["oc_chat"].Scan.Token != "" {
		t.Fatal("failed save advanced")
	}
	h.failResult = false
	r.Step(context.Background())
	p := h.s.Chats["oc_chat"]
	if p.Scan.Token != "second" || p.Completed != 0 || len(h.s.Inbox) != 1 {
		t.Fatal("page not checkpointed")
	}
	// Restart from the source codec, then expire cursor and restart the same window.
	data, _ := json.Marshal(h.s)
	decoded, e := (&Source{}).Decode(data, h.s.RoomIdentity)
	if e != nil {
		t.Fatal(e)
	}
	h.s = decoded.(State)
	expired := false
	f.pages = func(q MessagesRequest) (MessagePage, error) {
		if q.PageToken == "second" && !expired {
			expired = true
			return MessagePage{}, failure("cursor_expired")
		}
		if q.PageToken == "" {
			return MessagePage{Items: []Message{a}, HasMore: true, PageToken: "second"}, nil
		}
		return MessagePage{Items: []Message{b}}, nil
	}
	r = (&Source{Reader: func(QueryScope) (MessageReader, error) { return f, nil }}).NewRunner(h)
	r.Step(context.Background())
	if h.s.Chats["oc_chat"].Scan.Token != "" || len(h.s.Inbox) != 1 {
		t.Fatal("expiry lost progress")
	}
	h.now = h.now.Add(time.Minute)
	r.Step(context.Background())
	r.Step(context.Background())
	if len(h.s.Inbox) != 2 || h.s.Chats["oc_chat"].Scan != nil || h.s.Chats["oc_chat"].Completed != p.Scan.End {
		t.Fatal("same-time dedup/recovery failed")
	}
	// Target switch does not replay old revisions or reroute an edited message.
	cfg := h.s.Config
	cfg.Target.MembershipID = "replacement"
	next, e := Configure(h.s.RoomIdentity, &h.s, h.s.Binding, cfg, h.now)
	if e != nil {
		t.Fatal(e)
	}
	h.s = next
	a.Updated = "1001001"
	a.Body.Content = `{"text":"edited"}`
	f.pages = func(MessagesRequest) (MessagePage, error) { return MessagePage{Items: []Message{a, b}}, nil }
	h.now = h.now.Add(time.Minute)
	r = (&Source{Reader: func(QueryScope) (MessageReader, error) { return f, nil }}).NewRunner(h)
	r.Step(context.Background())
	if len(h.s.Inbox) != 3 {
		t.Fatal("revision not appended")
	}
	for _, entry := range h.s.Inbox {
		if entry.Item.Route.Target.MembershipID != "member" {
			t.Fatal("old message rerouted")
		}
	}
}
func TestMessagesCapacityAndAuthenticationPreserveWindow(t *testing.T) {
	for _, code := range []string{"capacity", "identity_mismatch", "network_error", "invalid_response"} {
		t.Run(code, func(t *testing.T) {
			h, f, r := newStepHarness(t)
			r.Step(context.Background())
			f.pages = func(MessagesRequest) (MessagePage, error) {
				if code == "capacity" {
					return MessagePage{Items: []Message{sampleMessage("om_one", "oc_chat", "one"), sampleMessage("om_two", "oc_chat", "two")}}, nil
				}
				return MessagePage{}, failure(code)
			}
			if code == "capacity" {
				h.limits.Inbox = 1
			}
			r.Step(context.Background())
			if len(h.s.Inbox) != 0 || len(h.s.Observed) != 0 || h.s.Chats["oc_chat"].Completed != 0 || h.s.Chats["oc_chat"].Scan == nil {
				t.Fatal("failure advanced window")
			}
			if h.s.ErrorCode != code {
				t.Fatal(h.s.ErrorCode)
			}
		})
	}
}
func TestMessageReferenceResolutionNativeGroupingAndReads(t *testing.T) {
	s := sampleState(t)
	m := sampleMessage("om_one", "oc_chat", "正文")
	root := sampleMessage("om_root", "oc_chat", "根消息")
	m.RootID = root.ID
	m.ParentID = root.ID
	m.ThreadID = "omt_thread"
	chat := Chat{ID: m.ChatID, Mode: "group", TenantKey: "external_tenant"}
	f := &fakeReader{chat: chat, messages: map[string]Message{m.ID: m, root.ID: root}}
	source := sourceRef(s, m, RevisionKey(s.Binding, m), chat)
	access := platform.AccessContext{RoomIdentity: s.RoomIdentity, AgentID: "agent", MembershipID: "member", Account: s.Binding.Account, Receiver: s.Binding.Account, ConnectionID: "connection", Generation: "generation", ConnectionVersion: 1, CredentialVersion: 1}
	r, e := ResolveTarget(context.Background(), platform.TargetLocator{Source: &source}, access, f)
	if e != nil || r.Status != platform.ResolutionResolved || r.Target.Object.ID.Value != root.ID || r.Target.Tenant == access.Account.Tenant || !r.DiscussionVerified {
		t.Fatal(r, e)
	}
	limits := platform.ReadLimits{Items: 2, Bytes: 8192, BodyBytes: 4096}
	page, e := ReadTarget(context.Background(), platform.ReadTargetRequest{Target: *r.Target, Scope: "content", Limits: limits}, access, f)
	if e != nil || len(page.Contents) != 1 || page.Contents[0].Body != "正文" || !page.Complete {
		t.Fatal(page, e)
	}
	delete(f.messages, root.ID)
	r, e = ResolveTarget(context.Background(), platform.TargetLocator{Source: &source}, access, f)
	if e != nil || r.Status != platform.ResolutionPartial || r.Target.Object.ID.Value != m.ID || r.Complete {
		t.Fatal(r, e)
	}
	m.RootID = ""
	m.ParentID = ""
	m.ThreadID = ""
	f.messages[m.ID] = m
	r, _ = ResolveTarget(context.Background(), platform.TargetLocator{Source: &source}, access, f)
	other := sampleMessage("om_other", "oc_chat", "independent")
	f.messages[other.ID] = other
	src := sourceRef(s, other, "key", chat)
	r2, _ := ResolveTarget(context.Background(), platform.TargetLocator{Source: &src}, access, f)
	if r.Target.Object == r2.Target.Object {
		t.Fatal("group became a followup object")
	}
	if caps := Capabilities(f); caps.Resolve == nil || caps.Read == nil || len(caps.Actions) != 0 {
		t.Fatal("wrong capabilities")
	}
	f.pages = func(MessagesRequest) (MessagePage, error) {
		return MessagePage{Items: []Message{m, other}, HasMore: true, PageToken: "next"}, nil
	}
	page, e = ReadTarget(context.Background(), platform.ReadTargetRequest{Target: *r.Target, Scope: "context", Limits: limits}, access, f)
	if e != nil || page.Next == nil || page.Complete {
		t.Fatal(page, e)
	}
	access.ConnectionVersion++
	if _, e = ReadTarget(context.Background(), platform.ReadTargetRequest{Target: *r.Target, Scope: "context", Limits: limits, Cursor: page.Next}, access, f); e == nil {
		t.Fatal("cursor crossed account revision")
	}
}
func TestMessageInvalidPageAndLongBody(t *testing.T) {
	s := sampleState(t)
	s.Chats["oc_chat"] = ChatProgress{Chat: Chat{ID: "oc_chat", Mode: "p2p"}, Scan: &Scan{Start: 1000, End: 1010}}
	for _, mode := range []string{"wrong_chat", "time", "duplicate", "loop"} {
		m := sampleMessage("om_one", "oc_chat", "one")
		p := MessagePage{Items: []Message{m}}
		switch mode {
		case "wrong_chat":
			p.Items[0].ChatID = "oc_other"
		case "time":
			p.Items[0].Created = "990000"
		case "duplicate":
			p.Items = append(p.Items, m)
		case "loop":
			p.HasMore = true
		}
		if _, e := acceptPage(s, "oc_chat", p, notification.DefaultLimits()); e == nil {
			t.Fatal(mode)
		}
	}
	m := sampleMessage("om_long", "oc_chat", strings.Repeat("中文", 6000))
	n, e := acceptPage(s, "oc_chat", MessagePage{Items: []Message{m}}, notification.DefaultLimits())
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range n.Inbox {
		if len(v.Item.Text) > 16<<10 || !strings.Contains(v.Item.Text, "已截断") || v.Message.Body.Content != m.Body.Content {
			t.Fatal("long original lost or invalid delivery")
		}
	}
	_ = fmt.Sprint(n)
}

func TestMessageRevisionUsesNormalizedNativeContent(t *testing.T) {
	b := sampleBinding()
	m := sampleMessage("om_one", "oc_chat", "one")
	m.Body.Content = `{"text":"one","extra":1}`
	key := RevisionKey(b, m)
	m.Body.Content = `{ "extra": 1, "text": "one" }`
	m.IsUpdated = true
	if RevisionKey(b, m) != key {
		t.Fatal("formatting created a duplicate revision")
	}
	m.Updated = "1001001"
	if RevisionKey(b, m) == key {
		t.Fatal("native revision time was lost")
	}
}

func TestMessagesSlicePageBudgetKeepsContinuation(t *testing.T) {
	h, f, r := newStepHarness(t)
	h.limits.SlicePages = 2
	f.pages = func(q MessagesRequest) (MessagePage, error) {
		if q.PageToken == "" {
			return MessagePage{Items: []Message{sampleMessage("om_one", "oc_chat", "one")}, HasMore: true, PageToken: "next"}, nil
		}
		return MessagePage{Items: []Message{}}, nil
	}
	r.Step(context.Background())
	r.Step(context.Background())
	before := f.calls
	r.Step(context.Background())
	p := h.s.Chats["oc_chat"]
	if f.calls != before || p.Completed != 0 || p.Scan == nil || p.Scan.Token != "next" || !h.s.RetryAt.After(h.now) {
		t.Fatal("slice budget lost or queried beyond continuation")
	}
	r.Step(context.Background())
	if f.calls != before {
		t.Fatal("budget backoff bypassed")
	}
	h.now = h.now.Add(time.Minute)
	r.Step(context.Background())
	if h.s.Chats["oc_chat"].Scan != nil || h.s.Chats["oc_chat"].Completed == 0 || len(h.s.Inbox) != 1 {
		t.Fatal("budget continuation failed")
	}
}
