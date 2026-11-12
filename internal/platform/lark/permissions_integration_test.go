//go:build integration || e2e

package lark

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Exact read allowlist: any authorization or message write fails the process.
const permissionFixture = `#!/usr/bin/env -S python3 -S
import json,os,sys
base=os.path.dirname(os.path.realpath(sys.argv[0]));a=sys.argv[3:]
assert sys.argv[1:3]==['--profile','fixture']
mode=open(base+'/mode').read()
with open(base+'/calls','a') as f:f.write(json.dumps(a)+'\n')
if a==['--version']:print('lark-cli version 1.0.99');sys.exit()
if a==['config','show']:
 print(json.dumps({'profile':'fixture','brand':'feishu','appId':'cli_fixture','dpopMode':'required'}));sys.exit()
if a==['whoami','--as','user']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','identity':'user','available':True,'onBehalfOf':{'openId':'ou_reader'}}));sys.exit()
if a[:3]==['api','GET','/open-apis/authen/v1/user_info']:
 print(json.dumps({'ok':True,'identity':'user','data':{'open_id':'ou_reader','tenant_key':'tenant','name':'fixture'}}));sys.exit()
scopes='contact:user.basic_profile:readonly contact:user:search im:chat.members:read im:chat:read im:message im:message.group_msg:get_as_user im:message.p2p_msg:get_as_user im:message.send_as_user im:message:readonly offline_access'.split()
if a==['auth','scopes','--json']:
 if mode=='app-unknown':print('{}');sys.exit(1)
 app=[s for s in scopes if s!='offline_access' and (mode!='app-missing' or s!='im:chat.members:read')]
 print(json.dumps({'appId':'cli_other' if mode=='app-identity' else 'cli_fixture','tokenType':'user','userScopes':app}));sys.exit()
if a[:2]==['auth','check']:
 assert a[2]=='--scope' and a[4:]==['--json']
 requested=a[3].split()
 missing=['im:chat.members:read'] if mode=='user-missing' else []
 if mode=='user-unknown':print('{}');sys.exit(1)
 print(json.dumps({'ok':not missing,'granted':[s for s in requested if s not in missing],'missing':missing}));sys.exit(1 if missing else 0)
if a==['auth','status','--json']:
 user={'openId':'ou_reader','tokenStatus':'valid'}
 if mode!='no-refresh':user['refreshExpiresAt']='2099-01-01T00:00:00Z'
 if mode=='refresh-expired':user['refreshExpiresAt']='2020-01-01T00:00:00Z'
 if mode=='refresh-invalid':user['refreshExpiresAt']='invalid'
 print(json.dumps({'appId':'cli_fixture','identities':{'user':user}}));sys.exit()
raise Exception('not a permitted read')
`

func TestPermissionCheckReadOnlyAndIndependentDependencies(t *testing.T) {
	for _, mode := range []string{"ok", "app-missing", "user-missing", "app-unknown", "user-unknown", "no-refresh", "refresh-expired", "refresh-invalid", "app-identity"} {
		t.Run(mode, func(t *testing.T) {
			p, dir := authTestProfile(t)
			if err := os.WriteFile(p.Executable, []byte(permissionFixture), 0700); err != nil {
				t.Fatal(err)
			}
			setMode(t, dir, mode)
			result, err := NewClientFactory(0, 0, 0).CheckPermissions(context.Background(), p, permissionAccount())
			if mode == "app-identity" {
				if ErrorCode(err) != "identity_mismatch" {
					t.Fatal(result, err)
				}
				return
			}
			if err != nil || result.CheckedAt.IsZero() || result.Account != permissionAccount() {
				t.Fatal(result, err)
			}
			if runtime.GOOS == "darwin" && !result.AccountFixed {
				t.Fatal("fixture pin failed")
			}
			read := findCapability(t, result.Capabilities, "message.read")
			if mode == "app-unknown" || mode == "user-unknown" {
				if read.State != "unchecked" {
					t.Fatal(read)
				}
			} else if read.State != "ready" {
				t.Fatal(read)
			}
			if runtime.GOOS == "darwin" && (mode == "app-missing" || mode == "user-missing") {
				send := findCapability(t, result.Capabilities, "message.send")
				if send.State != "blocked" || findCapability(t, result.Capabilities, "message.reply").State != "ready" {
					t.Fatal(result)
				}
			}
			if mode == "no-refresh" || mode == "refresh-expired" {
				if result.Renewal.State != "missing" {
					t.Fatal(result.Renewal)
				}
			}
			if mode == "refresh-invalid" && result.Renewal.State != "unchecked" {
				t.Fatal(result.Renewal)
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if strings.Contains(string(calls), `"login"`) || strings.Contains(string(calls), `"POST"`) {
				t.Fatal("check wrote state")
			}
		})
	}
}
