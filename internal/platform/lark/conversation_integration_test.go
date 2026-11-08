//go:build integration || e2e

package lark

import (
	"os"
	"strings"
	"testing"
)

func TestNativeConversationRelationship(t *testing.T) {
	parallelClientTest(t)
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
