//go:build integration || e2e

package lark

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

// Fixed CLI read fixtures exercise native response contracts, without giving a
// test process any valid real target, credential or write endpoint.
func TestPinnedSendTargetNativeEvidence(t *testing.T) {
	parallelClientTest(t)
	if runtime.GOOS != "darwin" {
		t.Skip("macOS pinning")
	}
	p, dir := authTestProfile(t)
	script := strings.Replace(pinnedFixture, "raise Exception('unexpected operation')", `
assert a[0:2]!=['api','POST'] or a[2]=='/open-apis/im/v1/chat_p2p/batch_query', 'unexpected write'
data=None
if a[:3]==['api','POST','/open-apis/im/v1/chat_p2p/batch_query']:
 assert json.loads(a[4])=={'chatter_id_type':'open_id'}
 assert json.loads(a[6])=={'chatter_ids':['ou_peer']}
 data={'p2p_chats':[{'chat_id':'oc_private'}]}
if a[:2]==['api','GET'] and a[2].endswith('/members'):
 items=[{'member_id':user,'member_id_type':'open_id','tenant_key':'tenant','name':'账号'}]
 if 'private' in a[2]:items.append({'member_id':'ou_peer' if mode!='wrong_peer' else 'ou_impostor','member_id_type':'open_id','tenant_key':'tenant' if mode!='cross_tenant' else 'other','name':'同名用户'})
 if mode=='not_joined':items=[]
 data={'items':items,'has_more':mode=='incomplete_members'}
 if mode=='page_cycle':data={'items':[],'has_more':True,'page_token':'repeat'}
assert not (a[:2]==['api','GET'] and a[2].endswith('/moderation')), 'redundant moderation request'
if a[:2]==['api','GET'] and a[2] in ['/open-apis/im/v1/chats/oc_private','/open-apis/im/v1/chats/oc_group']:
 data={'chat_mode':'p2p' if 'private' in a[2] else 'group','tenant_key':'tenant','name':'脱敏会话','chat_status':'normal','external':False,'group_message_type':'chat','moderation_permission':'all_members'}
 if mode=='muted':data['moderation_permission']='moderator_list'
 if mode=='missing_policy':data.pop('moderation_permission')
 if mode=='unknown_policy':data['moderation_permission']='unknown'
 if mode=='topic':data['chat_mode']='topic'
 if mode=='external':data['external']=True
 if mode=='dissolved':data['chat_status']='dissolved'
 if mode=='thread_group':data['group_message_type']='thread'
if a[:2]==['contact','+search-user']:
 assert '--query' in a and '--has-chatted' in a
 data={'users':[{'open_id':'ou_peer','localized_name':'同名','is_cross_tenant':False},{'open_id':'ou_other','localized_name':'同名','is_cross_tenant':False}],'has_more':True}
if a[:2]==['im','+chat-search']:
 assert '--disable-search-by-user' in a
 data={'chats':[{'chat_id':'oc_group','chat_mode':'DEFAULT','name':'测试群'}],'has_more':False}
if data is not None:
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
raise Exception('unexpected operation')`, 1)
	if err := os.WriteFile(p.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: Origin("feishu"), Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "tenant"}}, ID: platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_reader"}}
	pin, err := NewClientFactory(0, 0, 0).Pin(context.Background(), p, a)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	for _, mode := range []string{"ok", "wrong_peer", "cross_tenant", "incomplete_members"} {
		setMode(t, dir, mode)
		chat, _, err := pin.client.DirectUser(context.Background(), a, "ou_peer")
		if mode == "ok" {
			if err != nil || chat.ID != "oc_private" {
				t.Fatal(chat, err)
			}
		} else if err == nil {
			t.Fatal(mode, "unverified peer accepted")
		}
	}
	for _, mode := range []string{"ok", "not_joined", "page_cycle", "muted", "missing_policy", "unknown_policy", "topic", "external", "dissolved", "thread_group"} {
		setMode(t, dir, mode)
		chat, err := pin.client.SendGroup(context.Background(), a, "oc_group")
		if mode == "ok" {
			if err != nil || chat.ID != "oc_group" {
				t.Fatal(chat, err)
			}
		} else if err == nil {
			t.Fatal(mode, "unverified group accepted")
		}
	}
	setMode(t, dir, "ok")
	for _, kind := range []string{"user", "chat"} {
		candidates, more, err := pin.client.FindSendTargets(context.Background(), a, platform.TargetQuery{ObjectType: kind, Text: "同名"})
		if err != nil || len(candidates) == 0 || more != (kind == "user") {
			t.Fatal(candidates, more, err)
		}
	}
}
