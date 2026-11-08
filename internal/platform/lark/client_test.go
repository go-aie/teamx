package lark

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

// The executable fixture exercises argv, exit status, output limits, process
// cancellation and separate profile checks through the production transport.
const fixture = `#!/usr/bin/env -S python3 -S
import sys,json,os,time
base=os.path.dirname(os.path.realpath(sys.argv[0]))
mode=open(base+'/mode').read()
a=sys.argv[1:]
with open(base+'/calls','a') as f:f.write(json.dumps(a)+'\n')
assert a[:2]==['--profile','fixture']
a=a[2:]
if a==['--version']:
 print('lark-cli version '+('0.0.0' if mode=='version' else '1.0.99'));sys.exit()
switched=mode=='switch' and os.path.exists(base+'/read') or mode=='name_switch' and os.path.exists(base+'/names-read')
user='ou_other' if switched else 'ou_reader'
if a==['whoami','--as','user']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','identity':'bot' if mode=='bot' else 'user','available':mode!='logout','onBehalfOf':{'openId':user}}));sys.exit()
assert a[:2]==['api','GET'] or a[:2]==['api','POST']
assert a[-4:]==['--as','user','--format','json']
path=a[2]
if path=='/open-apis/authen/v1/user_info':
 print(json.dumps({'ok':True,'identity':'user','data':{'open_id':user,'tenant_key':'account_tenant','name':'中文显示名'}}));sys.exit()
if path=='/open-apis/contact/v3/users/basic_batch':
 assert a[1]=='POST' and json.loads(a[4])=={'user_id_type':'open_id'}
 ids=json.loads(a[6])['user_ids'];assert 0<len(ids)<=10 and len(ids)==len(set(ids))
 open(base+'/names-read','w').write('yes')
 if mode=='name_permission':
  print(json.dumps({'error':{'type':'authorization'}}),file=sys.stderr);sys.exit(1)
 if mode=='name_malformed':print('{');sys.exit()
 if mode=='name_timeout':time.sleep(60)
 users=[{'user_id':id,'name':'同名用户'} for id in reversed(ids)]
 if mode=='name_partial':users=users[:1]
 if mode=='name_wrong':users=[{'user_id':'ou_unrequested','name':'错误姓名'}]
 if mode=='name_duplicate':users=[users[0],users[0]]
 if mode=='name_invalid':users[0]['name']='bad\nname'
 print(json.dumps({'ok':True,'identity':'user','data':{'users':users}}));sys.exit()
assert a[1]=='GET'
open(base+'/read','w').write('yes')
if mode=='block':time.sleep(60)
if mode=='large':print('s'*((2<<20)+1));sys.exit()
if mode in ['auth','permission','rate','cursor','network','exit']:
 typ={'auth':'authentication','permission':'authorization','rate':'api','cursor':'api','network':'network','exit':'unknown'}[mode]
 print(json.dumps({'ok':False,'error':{'type':typ,'code':1020 if mode=='cursor' else 0,'subtype':'rate_limit' if mode=='rate' else 'unknown','message':'private-secret-body'}}),file=sys.stderr);sys.exit(1)
if mode=='malformed':print('{');sys.exit()
params=json.loads(a[4])
m={'message_id':'om_one','chat_id':'oc_chat','msg_type':'text','create_time':'100000','update_time':'100001','sender':{'id':'ou_sender','id_type':'open_id','sender_type':'user','tenant_key':'external_tenant'},'body':{'content':'{"text":"private-message"}'}}
if mode=='wrong_chat':m['chat_id']='oc_wrong'
if mode=='bad_time':m['create_time']='oops'
if mode=='same_time':
 second=dict(m);second['message_id']='om_two'
 items=[m,second]
elif mode=='duplicate':items=[m,m]
elif params.get('page_token')=='second':items=[]
else:items=[m]
if os.path.exists(base+'/sender-messages.json'):items=json.load(open(base+'/sender-messages.json'))
data={'items':items,'has_more':params.get('page_token')!='second','page_token':'second' if params.get('page_token')!='second' else ''}
if path.endswith('/om_one'):data={'items':[m]}
if mode=='incomplete':del data['has_more']
if mode=='loop':data['page_token']=params.get('page_token','second');data['has_more']=True
if mode=='null_items':data['items']=None
print(json.dumps({'ok':True,'identity':'bot' if mode=='response_bot' else 'user','data':data}))
`

func testClient(t *testing.T) (*Client, string, platform.AccountIdentity) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "lark-fixture")
	if err := os.WriteFile(bin, []byte(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := NewClientFactory(0, 0, 0).New(platform.CLIProfile{Version: 1, Executable: bin, Profile: "fixture", Brand: "feishu", CLIVersion: CLIVersion})
	if err != nil {
		t.Fatal(err)
	}
	c.requests = &requestBudget{}
	account := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: Origin("feishu"), Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "account_tenant"}}, ID: platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_reader"}}
	return c, dir, account
}
func setMode(t *testing.T, dir, mode string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0600); err != nil {
		t.Fatal(err)
	}
}
