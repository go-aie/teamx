package lark

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-aie/teamx/internal/platform"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

const pinnedFixture = `#!/usr/bin/env python3
import sys,os,json
base=os.path.dirname(os.path.realpath(sys.argv[0]));a=sys.argv[3:]
assert sys.argv[1:3]==['--profile','fixture']
root=os.environ.get('LARKSUITE_CLI_CONFIG_DIR','')
mode=open(base+'/mode').read()
if a==['config','show']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','dpopMode':'required'}));sys.exit()
if a==['--version']:print('lark-cli version 1.0.99');sys.exit()
assert root and root!=base
cfg=json.load(open(root+'/config.json'));app=cfg['apps'][0]
assert app['appSecret']=={'source':'keychain','id':'appsecret:cli_fixture'}
assert len(app['users'])==1 and app['dpopMode']=='required'
user=app['users'][0]['userOpenId']
assert user=='ou_reader'
assert 'LARKSUITE_CLI_PROFILE' not in os.environ
assert 'LARKSUITE_CLI_WORKSPACE' not in os.environ
assert 'LARKSUITE_CLI_USER_ACCESS_TOKEN' not in os.environ
if mode=='revoked':print('{}');sys.exit(1)
if a==['whoami','--as','user']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','identity':'user','available':True,'onBehalfOf':{'openId':user}}));sys.exit()
if a[:3]==['api','GET','/open-apis/authen/v1/user_info']:
 print(json.dumps({'ok':True,'identity':'user','data':{'open_id':user,'tenant_key':'tenant','name':'fixed user'}}));sys.exit()
if a==['auth','scopes','--json']:
 print(json.dumps({'appId':'cli_fixture','scopes':['im:message.send_as_user','im:message']}));sys.exit()
if a[:2]==['auth','check']:
 if mode=='missing':print(json.dumps({'ok':False,'granted':['im:message'],'missing':['im:message.send_as_user']}));sys.exit(1)
 print(json.dumps({'ok':True,'granted':['im:message.send_as_user','im:message'],'missing':[]}));sys.exit()
raise Exception('unexpected operation')
`

func TestPinnedAccountIsolatesExternalProfileAndFailsOnRevocation(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS pinning contract")
	}
	p, dir := authTestProfile(t)
	if err := os.WriteFile(p.Executable, []byte(pinnedFixture), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LARKSUITE_CLI_CONFIG_DIR", dir)
	t.Setenv("LARKSUITE_CLI_PROFILE", "other-user")
	t.Setenv("LARKSUITE_CLI_WORKSPACE", "other-workspace")
	t.Setenv("LARKSUITE_CLI_USER_ACCESS_TOKEN", "must-not-inherit")
	account := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: Origin("feishu"), Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "tenant"}}, ID: platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_reader"}}
	pin, err := Pin(context.Background(), p, account)
	if err != nil {
		t.Fatal(err)
	}
	root := pin.client.configDir
	if info, err := os.Stat(root); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("private directory", info, err)
	}
	content, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "token") || strings.Contains(string(content), "must-not-inherit") {
		t.Fatal("credential escaped")
	}
	// Changing ordinary profile metadata does not affect the admitted config.
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"apps":[{"appId":"cli_other","users":[{"userOpenId":"ou_other"}]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ok", "refresh", "missing"} {
		setMode(t, dir, mode)
		v, err := pin.Verify(context.Background())
		if err != nil || v.Account != account {
			t.Fatal(mode, v, err)
		}
		permission, err := pin.SendPermission(context.Background())
		if err != nil || !permission.AccountFixed || !permission.AdapterImplemented || permission.ReplyEnabled != (mode != "missing") || permission.GroupSendEnabled != permission.ReplyEnabled || permission.SelfSendEnabled != permission.ReplyEnabled || permission.UserSendEnabled || (mode == "missing" && permission.UserScopes != "missing") {
			t.Fatal(permission, err)
		}
	}
	setMode(t, dir, "revoked")
	if _, err := pin.Verify(context.Background()); err == nil {
		t.Fatal("revocation silently selected another account")
	}
	pin.Close()
	pin.Close()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("pinned state retained", err)
	}
}

func TestSelfSendPinnedQueueCancellationAndFinalGuard(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS pinning contract")
	}
	profile, dir := authTestProfile(t)
	script := strings.Replace(pinnedFixture, "raise Exception('unexpected operation')", `if a[:3]==['api','POST','/open-apis/im/v1/messages']:
 with open(base+'/posts','a') as f:f.write('post\n')
 print(json.dumps({'ok':True,'identity':'user','data':{'message_id':'om_receipt'}}));sys.exit()
raise Exception('unexpected operation')`, 1)
	if err := os.WriteFile(profile.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	account := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: Origin("feishu"), Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "tenant"}}, ID: platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_reader"}}
	pins := make([]*Pinned, 2)
	for i := range pins {
		pin, err := Pin(t.Context(), profile, account)
		if err != nil {
			t.Fatal(err)
		}
		pins[i] = pin
		defer pin.Close()
	}
	key := account.Instance + "|" + account.ID.Namespace + "|" + account.ID.Value
	ctx := context.WithValue(t.Context(), accountContextKey{}, key)
	release, err := selfSendRequests.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	target := platform.TargetIdentity{IdentityScope: account.IdentityScope, Object: platform.NativeIdentity{Type: "user", ID: account.ID}, Containers: []platform.NativeIdentity{{Type: "chat", ID: platform.ExternalID{Namespace: "lark:chat_id:cli_fixture", Value: "oc_self"}}}}
	cancelled, cancel := context.WithCancel(ctx)
	done := make(chan error, 2)
	go func() {
		_, e := pins[0].SendText(cancelled, target, "cancelled", strings.Repeat("a", 32), func(context.Context) error { return nil })
		done <- e
	}()
	awaitQueue(t, selfSendRequests, key, 1)
	var allowed atomic.Bool
	allowed.Store(true)
	go func() {
		_, e := pins[1].SendText(ctx, target, "revoked while waiting", strings.Repeat("b", 32), func(context.Context) error {
			if !allowed.Load() {
				return errors.New("binding revoked")
			}
			return nil
		})
		done <- e
	}()
	awaitQueue(t, selfSendRequests, key, 2)
	cancel()
	allowed.Store(false)
	release()
	for range 2 {
		if e := <-done; !errors.Is(e, ErrSendNotSent) {
			t.Fatal("cancelled/revoked queue entry was dispatched", e)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "posts")); !os.IsNotExist(err) {
		t.Fatal("queue revocation reached POST", err)
	}
	// Cancelled waiters release capacity and cannot strand the shared account.
	if id, err := pins[0].SendText(ctx, target, "new authorized task", strings.Repeat("c", 32), func(context.Context) error { return nil }); err != nil || id != "om_receipt" {
		t.Fatal(id, err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "posts")); err != nil || strings.TrimSpace(string(b)) != "post" {
		t.Fatal("unexpected native dispatches", string(b), err)
	}
}

func TestIncrementalLoginNeverChangesReaderDefaults(t *testing.T) {
	p, dir := authTestProfile(t)
	if _, err := LoginSending(context.Background(), p, func(string, string) {}); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	var argv []string
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		_ = json.Unmarshal([]byte(line), &argv)
		for i, a := range argv {
			if a == "--scope" {
				scope := argv[i+1]
				if !strings.Contains(scope, SendScopes) || !strings.Contains(scope, readerScopes) {
					t.Fatal(scope)
				}
			}
		}
	}
	if strings.Contains(readerScopes, "send_as_user") {
		t.Fatal("read login enlarged")
	}
}
