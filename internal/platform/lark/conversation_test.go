package lark

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type incompleteConversationReader struct{ *fakeReader }

func (f incompleteConversationReader) NotificationChat(_ context.Context, _ platform.AccountIdentity, id string) (Chat, error) {
	if id == "oc_group" {
		return Chat{ID: id, Mode: "group"}, nil
	}
	return Chat{ID: id, Mode: "p2p"}, nil // No native relationship proof.
}

func TestUnverifiedP2PWaitsWithoutBlockingOtherSourceChats(t *testing.T) {
	h, f, _ := newStepHarness(t)
	h.s.Config.Chats = append(h.s.Config.Chats, "oc_group")
	h.s.Config.GroupAll = true
	f.pages = func(q MessagesRequest) (MessagePage, error) {
		if q.ChatID != "oc_group" {
			t.Fatal("unverified private body read")
		}
		return MessagePage{Items: []Message{sampleMessage("om_business", q.ChatID, "business")}}, nil
	}
	r := (&Source{Reader: func(QueryScope) (MessageReader, error) { return incompleteConversationReader{f}, nil }}).NewRunner(h)
	for range 3 {
		r.Step(t.Context())
	}
	if h.s.QueryPaused() || h.s.Chats["oc_chat"].ErrorCode != "conversation_unverified" || len(h.s.Inbox) != 1 || len(h.s.PendingItems()) != 1 {
		t.Fatal("incomplete peer evidence leaked or blocked other chats", h.s.ErrorCode, h.s.Chats, len(h.s.Inbox))
	}
}

func TestNativeConversationRelationship(t *testing.T) {
	c, dir, a := testClient(t)
	// Exercise the production CLI transport, including complete membership,
	// exact batch mapping and before/after identity checks. Only reads are allowed.
	script := strings.Replace(fixture, "assert a[1]=='GET'", `
if path.startswith('/open-apis/im/v1/chats/') and not path.endswith('/members'):
 data={'chat_mode':'group' if mode=='group' else 'p2p','tenant_key':'other' if mode=='wrong_chat_tenant' else 'account_tenant'}
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
if path.endswith('/members'):
 items=[{'member_id':user,'member_id_type':'open_id','tenant_key':'account_tenant'}]
 if mode=='peer':items.append({'member_id':'ou_peer','member_id_type':'open_id','tenant_key':'external_tenant'})
 if mode=='duplicate':items.append(items[0])
 if mode=='missing_self':items[0]['member_id']='ou_peer'
 if mode=='wrong_type':items[0]['member_id_type']='union_id'
 if mode=='wrong_tenant':items[0]['tenant_key']='other'
 if mode=='empty':items=[]
 if mode=='identity_switch':open(base+'/read','w').write('yes')
 data={'items':items,'has_more':mode=='more'}
 if mode=='missing_flag':del data['has_more']
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
if path=='/open-apis/im/v1/chat_p2p/batch_query':
 assert a[1]=='POST' and json.loads(a[4])=={'chatter_id_type':'open_id'}
 assert json.loads(a[6])=={'chatter_ids':['ou_peer' if mode=='peer' else 'ou_reader']}
 chats=[{'chat_id':'oc_other' if mode=='wrong_mapping' else 'oc_chat'}]
 if mode=='multiple_mapping':chats.append({'chat_id':'oc_chat'})
 data={'p2p_chats':chats}
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
assert a[1]=='GET'`, 1)
	script = strings.Replace(script, "switched=mode=='switch'", "switched=mode in ['switch','identity_switch']", 1)
	if err := os.WriteFile(c.profile.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ok", "peer", "group", "more", "missing_flag", "duplicate", "missing_self", "wrong_type", "wrong_tenant", "wrong_chat_tenant", "empty", "wrong_mapping", "multiple_mapping", "identity_switch"} {
		t.Run(mode, func(t *testing.T) {
			setMode(t, dir, mode)
			chat, err := c.NotificationChat(t.Context(), a, "oc_chat")
			want := map[string]string{"ok": "self", "peer": "peer", "group": "group"}[mode]
			if want == "" {
				if err == nil {
					t.Fatal("incomplete or mismatched relationship admitted", chat)
				}
				return
			}
			if err != nil || chat.Relation.Kind != want || !chat.Relation.ValidFor(a, "oc_chat") {
				t.Fatal(chat, err)
			}
			other := a
			other.ID.Namespace = "lark:open_id:cli_other"
			if chat.Relation.ValidFor(other, "oc_chat") || chat.Relation.ValidFor(a, "oc_other") {
				t.Fatal("relationship escaped native scope")
			}
			self, e := c.SelfChat(t.Context(), a)
			if mode == "ok" {
				if e != nil || self.Relation.Kind != "self" {
					t.Fatal("native self discovery", self, e)
				}
			} else if e == nil {
				t.Fatal("non-self accepted by self discovery", self)
			}
		})
	}
}

func TestSelfConversationSourceBacklogRestartAndScope(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(map[bool]string{false: "specified", true: "all"}[all], func(t *testing.T) {
			h, f, _ := newStepHarness(t)
			chat := Chat{ID: "oc_chat", Mode: "p2p", TenantKey: h.s.Binding.Account.Tenant.Value}
			h.s.Chats[chat.ID] = ChatProgress{Chat: chat, Scan: &Scan{Start: 1000, End: 1010}}
			m := sampleMessage("om_manual", chat.ID, "自聊中手写的待办")
			m.Sender.ID = h.s.Binding.Account.ID.Value
			var err error
			h.s, err = acceptPage(h.s, chat.ID, MessagePage{Items: []Message{m}}, h.limits)
			if err != nil || len(h.s.Inbox) != 1 || len(h.s.PendingItems()) != 0 {
				t.Fatal("legacy backlog leaked before verification", err)
			}
			if all {
				h.s.Config.ScopeMode = "all"
				h.s.Config.Chats = nil
				h.s.Directory = &DirectoryProgress{LastFull: h.now, RetryAt: h.now.Add(time.Hour)}
			}
			chat.Relation = ConversationRelation{Account: h.s.Binding.Account, ChatID: chat.ID, Kind: "self"}
			f.chat = chat
			f.pages = func(MessagesRequest) (MessagePage, error) {
				t.Fatal("self bodies were automatically scanned")
				return MessagePage{}, nil
			}
			source := &Source{Reader: func(QueryScope) (MessageReader, error) { return f, nil }}
			r := source.NewRunner(h)
			for range 3 {
				r.Step(t.Context())
			}
			if len(h.s.Inbox) != 1 || len(h.s.Observed) != 1 || len(h.s.PendingItems()) != 0 || h.s.Metadata().Pending != 0 || h.s.ExcludedItems() != 1 {
				t.Fatal("self history lost or delivered")
			}
			m.Updated = "1002000"
			m.Sender.Type = "app" // Receipt order/author is irrelevant.
			p := h.s.Chats[chat.ID]
			p.Scan = &Scan{Start: 1000, End: 1010}
			h.s.Chats[chat.ID] = p
			h.s, err = acceptPage(h.s, chat.ID, MessagePage{Items: []Message{m}}, h.limits)
			if err != nil || len(h.s.Inbox) != 1 {
				t.Fatal("accepted-history route bypassed self exclusion", err)
			}
			raw, _ := json.Marshal(h.s)
			state, err := source.Decode(raw, h.s.RoomIdentity)
			if err != nil || len(state.PendingItems()) != 0 {
				t.Fatal("restart bypassed exclusion", err)
			}
			h.s = state.(State)
			h.now = h.now.Add(time.Minute)
			source.NewRunner(h).Step(t.Context())
		})
	}
}

func TestSourceVerifiesFrozenOutOfScopeP2PBacklog(t *testing.T) {
	h, f, _ := newStepHarness(t)
	m := sampleMessage("om_old", "oc_old", "history")
	h.s.Chats[m.ChatID] = ChatProgress{Chat: Chat{ID: m.ChatID, Mode: "p2p", TenantKey: h.s.Binding.Account.Tenant.Value}, Scan: &Scan{Start: 1000, End: 1010}}
	var err error
	h.s, err = acceptPage(h.s, m.ChatID, MessagePage{Items: []Message{m}}, notification.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	h.s.Config.Chats = []string{"oc_new"}
	f.chat = Chat{ID: m.ChatID, Mode: "p2p", TenantKey: h.s.Binding.Account.Tenant.Value, Relation: ConversationRelation{Account: h.s.Binding.Account, ChatID: m.ChatID, Kind: "self"}}
	// Put the configured chat on cooldown so the frozen route is selected first.
	h.s.Chats["oc_new"] = ChatProgress{Chat: Chat{ID: "oc_new", Mode: "group"}, RetryAt: h.now.Add(time.Hour)}
	r := (&Source{Reader: func(QueryScope) (MessageReader, error) { return f, nil }}).NewRunner(h)
	r.Step(context.Background())
	if h.s.Chats[m.ChatID].Chat.Relation.Kind != "self" || len(h.s.PendingItems()) != 0 {
		t.Fatal("scope switch bypassed old backlog check")
	}
	if ids := h.s.ChatIDs(); len(ids) != 1 || ids[0] != "oc_new" {
		t.Fatal("verification widened subscription", ids)
	}
}
