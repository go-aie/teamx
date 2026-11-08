package room

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Native CLI fixture; the manager, pinned account, settings, CLI protocol,
// Inspect/Execute and authoritative ledger all use the production implementation.
func selfSendHarness(t *testing.T) *larkHarness {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("fixed-account user dispatch requires macOS")
	}
	h := newLarkHarness(t)
	script := strings.Replace(l2Fixture, "path=a[2]", `if a==['config','show']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','dpopMode':'required'}));sys.exit()
if a==['auth','scopes','--json']:
 print(json.dumps({'appId':'cli_fixture','userScopes':['im:message.send_as_user','im:message'],'tokenType':'user'}));sys.exit()
if a[:2]==['auth','check']:
 print(json.dumps({'ok':True,'granted':['im:message.send_as_user','im:message'],'missing':[]}));sys.exit()
if a[:3]==['api','POST','/open-apis/im/v1/messages']:
 assert os.environ.get('LARKSUITE_CLI_CONFIG_DIR')
 assert json.loads(a[4])=={'receive_id_type':'open_id'}
 payload=json.loads(a[6]);assert payload['receive_id']=='ou_reader'
 with open(base+'/posts','a') as f:f.write(payload['uuid']+'\n')
 if os.path.exists(base+'/unknown'):sys.exit(1)
 m={'message_id':'om_receipt','chat_id':'oc_chat','msg_type':'text','create_time':'100000','body':{'content':payload['content']},'sender':{'id':'ou_reader','id_type':'open_id','sender_type':'user','tenant_key':'account_tenant'}}
 json.dump([m],open(base+'/messages.json','w'))
 print(json.dumps({'ok':True,'identity':'user','data':{'message_id':'om_receipt'}}));sys.exit()
path=a[2]`, 1)
	script = strings.Replace(script, "{'member_id':'ou_reader','member_id_type':'open_id','tenant_key':'account_tenant'},{'member_id':'ou_sender','member_id_type':'open_id','tenant_key':'external_tenant'}", "{'member_id':'ou_reader','member_id_type':'open_id','tenant_key':'account_tenant'}", 1)
	script = strings.ReplaceAll(script, "'tenant_key':'external_tenant'", "'tenant_key':'account_tenant'")
	if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return h
}
