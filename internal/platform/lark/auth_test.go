package lark

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

const authFixture = `#!/usr/bin/env -S python3 -S
import sys,json,os,time,base64
base=os.path.dirname(os.path.realpath(sys.argv[0]));a=sys.argv[1:]
mode=open(base+'/mode').read()
assert 'LARKSUITE_CLI_PROFILE' not in os.environ
assert 'LARKSUITE_CLI_WORKSPACE' not in os.environ
with open(base+'/calls','a') as f:f.write(json.dumps(a)+'\n')
if a[:2]==['--profile','fixture']:a=a[2:]
if a==['--version']:
 print('lark-cli version '+('0.0.0' if mode=='version' else '1.0.99'));sys.exit()
if a==['profile','list']:
 print(json.dumps([{'name':'fixture','brand':'feishu','appId':'unsafe' if mode=='discovery-app-id' else 'cli_fixture','user':'测试用户','tokenStatus':'valid','private':'not projected'},{'name':'teamx-feishu','brand':'lark'}]));sys.exit()
if a==['config','show']:
 print(json.dumps({'profile':'fixture','brand':'lark' if mode=='brand' else 'feishu','appSecret':'****'}));sys.exit()
required='offline_access im:chat:read im:message:readonly im:message.p2p_msg:get_as_user im:message.group_msg:get_as_user contact:user.basic_profile:readonly im:message.send_as_user im:message im:chat.members:read contact:user:search'.split()
if a==['auth','scopes','--json']:
 if mode=='app-unknown':sys.exit(1)
 print(json.dumps({'appId':'cli_fixture','tokenType':'user','userScopes':[s for s in required if s!='offline_access' and not (mode=='app-missing' and s=='im:chat.members:read')]}));sys.exit()
if a[:2]==['auth','check']:
 missing=['im:chat:read'] if mode=='scopes' else []
 print(json.dumps({'ok':not missing,'granted':[s for s in a[a.index('--scope')+1].split() if s not in missing],'missing':missing}));sys.exit(1 if missing else 0)
if a==['auth','status','--json']:
 user={'openId':'ou_reader','tokenStatus':'valid'}
 if mode!='no-refresh':user['refreshExpiresAt']='2027-12-01T00:00:00Z'
 print(json.dumps({'appId':'cli_fixture','identities':{'user':user}}));sys.exit()
if a[:2]==['auth','login'] and '--no-wait' in a:
 assert '--scope' in a and '--recommend' not in a and '--domain' not in a
 scopes=a[a.index('--scope')+1].split()
 assert 'offline_access' in scopes and 'im:message:readonly' in scopes
 link='https://accounts.feishu.cn/oauth?code=A%2BB&x=1'
 if mode=='url':link='https://accounts.feishu.cn.attacker.invalid/oauth?token=private'
 print(json.dumps({'verification_url':link,'device_code':'private-device-code','expires_in':600}));sys.exit()
if a[:2]==['auth','qrcode']:
 assert a[2]=='https://accounts.feishu.cn/oauth?code=A%2BB&x=1'
 assert a[3:]==['--output','code.png']
 open('code.png','wb').write(base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j5X8AAAAASUVORK5CYII='))
 print('{}');sys.exit()
if a[:3]==['auth','login','--device-code']:
 assert a[3]=='private-device-code' and a[4:]==['--json']
 open(base+'/polling','w').write('yes')
 if mode=='block':time.sleep(60)
 print(json.dumps({'event':'authorization_complete','user_open_id':'ou_reader','user_name':'测试用户','missing':['im:chat:read'] if mode=='scopes' else [],'refresh_token_issued':mode!='no-refresh','expires_at':'2027-01-01T00:00:00Z'}))
 sys.exit(4 if mode=='scopes' else 0)
if a==['auth','logout','--json']:
 if mode=='logout-fail':
  print(json.dumps({'error':{'type':'authentication','message':'private-token'}}),file=sys.stderr);sys.exit(1)
 open(base+'/logged-out','w').write('yes')
 print(json.dumps({'ok':True,'loggedOut':True}));sys.exit()
if a==['whoami','--as','user']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','identity':'user','available':True,'onBehalfOf':{'openId':'ou_reader'}}));sys.exit()
if a[:3]==['api','GET','/open-apis/authen/v1/user_info']:
 assert a[-4:]==['--as','user','--format','json']
 print(json.dumps({'ok':True,'identity':'user','data':{'open_id':'ou_reader','tenant_key':'tenant','name':'测试用户'}}));sys.exit()
raise Exception('unexpected fixture argv')
`

func authTestProfile(t *testing.T) (platform.CLIProfile, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "lark-cli")
	if err := os.WriteFile(bin, []byte(authFixture), 0700); err != nil {
		t.Fatal(err)
	}
	setMode(t, dir, "ok")
	return platform.CLIProfile{Version: 1, Executable: bin, Profile: "fixture", Brand: "feishu", CLIVersion: CLIVersion}, dir
}
