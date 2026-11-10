package room

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
)

const l2Fixture = `#!/usr/bin/env -S python3 -S
import sys,json,os
base=os.path.dirname(os.path.realpath(sys.argv[0]));a=sys.argv[3:]
if a==['--version']:print('lark-cli version 1.0.99');sys.exit()
if a==['whoami','--as','user']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','identity':'user','available':True,'onBehalfOf':{'openId':'ou_reader'}}));sys.exit()
if a[:2]==['im','+chat-list']:
 data=json.load(open(base+'/directory.json'))
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
path=a[2]
if path=='/open-apis/authen/v1/user_info':data={'open_id':'ou_reader','tenant_key':'account_tenant','name':'测试用户'}
elif path=='/open-apis/contact/v3/users/basic_batch':
 assert a[1]=='POST' and json.loads(a[4])=={'user_id_type':'open_id'}
 data={'users':[{'user_id':id,'name':'测试发送者'} for id in json.loads(a[6])['user_ids']]}
elif path=='/open-apis/im/v1/chat_p2p/batch_query':
 data={'p2p_chats':[{'chat_id':'oc_chat'}]}
elif path.endswith('/members'):
 data={'items':[{'member_id':'ou_reader','member_id_type':'open_id','tenant_key':'account_tenant'},{'member_id':'ou_sender','member_id_type':'open_id','tenant_key':'external_tenant'}],'has_more':False}
elif path.startswith('/open-apis/im/v1/chats/'):data={'chat_mode':'p2p','name':'脱敏会话','tenant_key':'external_tenant'}
else:
 messages=json.load(open(base+'/messages.json'))
 if path.startswith('/open-apis/im/v1/messages/'):data={'items':[m for m in messages if m['message_id']==path.split('/')[-1]]}
 else:
  params=json.loads(a[4]);messages=[m for m in messages if m['chat_id']==params['container_id'] and int(params['start_time'])<=int(m['create_time'])//1000<=int(params['end_time'])]
  offset=int(params.get('page_token','0'));size=params['page_size'];page=messages[offset:offset+size];more=len(messages)>offset+size
  data={'items':page,'has_more':more,'page_token':str(offset+size) if more else ''}
print(json.dumps({'ok':True,'identity':'user','data':data}))
`

type larkHarness struct {
	t       *testing.T
	m       *roomManager
	room    roomSummary
	svc     *service
	members []MemberState
	c       managedPlatform
	dir     string
	clock   *eventTestClock
}

func newLarkHarness(t *testing.T) *larkHarness {
	t.Helper()
	m, r, svc, _, members := cliToolFixture(t, "exit 0")
	m.toolCatalog = append(m.toolCatalog, tool.Definition{Name: "lark", Provider: "lark", InternalOnly: true})
	m.bindPlatformReferences(svc)
	dir := t.TempDir()
	bin := filepath.Join(dir, "lark-cli")
	if e := os.WriteFile(bin, []byte(l2Fixture), 0700); e != nil {
		t.Fatal(e)
	}
	c := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("c", 32), Name: "飞书", Provider: "lark", URL: lark.Origin("feishu"), CLIProfile: &platform.CLIProfile{Version: 1, Executable: bin, Profile: "fixture", Brand: "feishu", CLIVersion: lark.CLIVersion}})
	c, e := m.verifyPlatform(context.Background(), managedPlatformRequest{ID: c.ID, Version: c.Version})
	if e != nil || c.Validation.Status != "valid" {
		t.Fatal(e)
	}
	clock := &eventTestClock{now: time.Unix(99, 0).UTC()}
	m.notifications.Clock = clock.get
	h := &larkHarness{t, m, r, svc, members, c, dir, clock}
	h.messages(1)
	return h
}
func (h *larkHarness) messages(count int) {
	h.t.Helper()
	ms := []lark.Message{}
	for i := 0; i < count; i++ {
		m := lark.Message{ID: fmt.Sprintf("om_%03d", i), ChatID: "oc_chat", Type: "text", Created: "100000", Updated: "100000", Sender: lark.Sender{ID: "ou_sender", IDType: "open_id", Type: "user"}}
		m.Body.Content = `{"text":"请确认这条脱敏测试消息"}`
		ms = append(ms, m)
	}
	b, _ := json.Marshal(ms)
	if e := os.WriteFile(filepath.Join(h.dir, "messages.json"), b, 0600); e != nil {
		h.t.Fatal(e)
	}
}
func (h *larkHarness) state() lark.State {
	h.t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	s, e := h.m.currentLarkLocked(h.room.RoomIdentity)
	if e != nil || s == nil {
		h.t.Fatal(e)
	}
	return *s
}
func (h *larkHarness) configure(c lark.Config) lark.State {
	h.t.Helper()
	h.m.mu.Lock()
	s, _ := h.m.currentLarkLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	key := notification.Key{Owner: h.room.RoomIdentity, Source: lark.MessagesSourceID}
	v := uint64(0)
	if s != nil {
		key = s.Metadata().Key
		v = s.SubscriptionVersion
	}
	next, e := h.m.configureLarkSubscription(context.Background(), key, v, c)
	if e != nil {
		h.t.Fatal(e)
	}
	return next
}
func (h *larkHarness) start() {
	h.t.Helper()
	member := h.members[0]
	h.configure(lark.Config{Enabled: true, PlatformID: h.c.ID, Private: lark.PrivateConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_chat"}}, Target: notification.Target{Mode: "inbox", AgentID: member.AgentID, MembershipID: member.MembershipID}})
	h.clock.add(3 * time.Second)
	s := h.state()
	h.m.mu.Lock()
	r := h.m.notifications.NewRunnerLocked(s.Metadata().Key)
	h.m.mu.Unlock()
	r.Step(context.Background())
	r.Step(context.Background())
}
func (h *larkHarness) deliver() error {
	s := h.state()
	return h.m.notifications.Deliver(context.Background(), s.Metadata().Key, s.SubscriptionVersion)
}
