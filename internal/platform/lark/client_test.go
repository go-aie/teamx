package lark

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// The executable fixture exercises argv, exit status, output limits, process
// cancellation and separate profile checks through the production transport.
const fixture = `#!/usr/bin/env python3
import sys,json,os,time
base=os.path.dirname(os.path.realpath(sys.argv[0]))
mode=open(base+'/mode').read()
a=sys.argv[1:]
with open(base+'/calls','a') as f:f.write(json.dumps(a)+'\n')
assert a[:2]==['--profile','fixture']
a=a[2:]
if a==['--version']:
 print('lark-cli version '+('0.0.0' if mode=='version' else '1.0.99'));sys.exit()
switched=mode=='switch' and os.path.exists(base+'/read')
user='ou_other' if switched else 'ou_reader'
if a==['whoami','--as','user']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','identity':'bot' if mode=='bot' else 'user','available':mode!='logout','onBehalfOf':{'openId':user}}));sys.exit()
assert a[:2]==['api','GET'] and a[-4:]==['--as','user','--format','json']
path=a[2]
if path=='/open-apis/authen/v1/user_info':
 print(json.dumps({'ok':True,'identity':'user','data':{'open_id':user,'tenant_key':'account_tenant','name':'中文显示名'}}));sys.exit()
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
	c, err := New(platform.CLIProfile{Version: 1, Executable: bin, Profile: "fixture", Brand: "feishu", CLIVersion: CLIVersion})
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
func TestUserIdentityAndSinglePageReads(t *testing.T) {
	c, dir, account := testClient(t)
	v, err := c.Verify(context.Background())
	if err != nil || v.Account != account || v.Username != "中文显示名" || v.UserID != 0 {
		t.Fatal(v, err)
	}
	req := MessagesRequest{ChatID: "oc_chat", Start: 99, End: 101, PageSize: 2}
	page, err := c.Messages(context.Background(), account, req)
	if err != nil || len(page.Items) != 1 || !page.HasMore || page.PageToken != "second" {
		t.Fatal(page, err)
	}
	if page.Items[0].Text() != "private-message" || page.Items[0].Sender.TenantKey == account.Tenant.Value {
		t.Fatal("lost message or distinct target tenant")
	}
	req.PageToken = page.PageToken
	page, err = c.Messages(context.Background(), account, req)
	if err != nil || page.HasMore || len(page.Items) != 0 {
		t.Fatal(page, err)
	}
	message, err := c.Message(context.Background(), account, "om_one")
	if err != nil || message.ID != "om_one" {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	remotePages := 0
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		var args []string
		if json.Unmarshal([]byte(line), &args) != nil {
			t.Fatal("invalid fixture log")
		}
		if strings.Contains(line, "--page-all") || strings.Contains(line, "search") {
			t.Fatal("hidden extra query")
		}
		if len(args) > 4 && args[4] == "/open-apis/im/v1/messages" {
			remotePages++
		}
	}
	if remotePages != 2 {
		t.Fatal("expected one remote page per read", remotePages)
	}
	setMode(t, dir, "same_time")
	req.PageToken = ""
	page, err = c.Messages(context.Background(), account, req)
	if err != nil || len(page.Items) != 2 {
		t.Fatal("dropped equal timestamp", err)
	}
}
func TestReadFailuresDiscardCandidate(t *testing.T) {
	cases := map[string]string{"switch": "identity_mismatch", "bot": "credential_unavailable", "logout": "credential_unavailable", "version": "cli_version_mismatch", "auth": "credential_rejected", "permission": "permission_denied", "rate": "rate_limited", "cursor": "cursor_expired", "network": "network_error", "exit": "query_failed", "malformed": "invalid_response", "wrong_chat": "invalid_response", "duplicate": "invalid_response", "bad_time": "invalid_response", "incomplete": "invalid_response", "null_items": "invalid_response", "response_bot": "invalid_response", "large": "output_limit", "loop": "invalid_response"}
	for mode, code := range cases {
		t.Run(mode, func(t *testing.T) {
			c, dir, a := testClient(t)
			setMode(t, dir, mode)
			req := MessagesRequest{ChatID: "oc_chat", Start: 99, End: 101, PageSize: 2}
			if mode == "loop" {
				req.PageToken = "second"
			}
			page, err := c.Messages(context.Background(), a, req)
			if ErrorCode(err) != code || page.Items != nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("%s: page escaped or error category: %v", mode, err)
			}
		})
	}
}
func TestCancellationAndProfileGate(t *testing.T) {
	c, dir, a := testClient(t)
	setMode(t, dir, "block")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Messages(ctx, a, MessagesRequest{ChatID: "oc_chat", Start: 99, End: 101, PageSize: 2})
		done <- err
	}()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "read")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("read did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	other, _ := New(c.profile)
	wait, cancelWait := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancelWait()
	if _, err := other.Verify(wait); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("profile gate not cancellable", err)
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("process did not settle")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancel too slow")
	}
	setMode(t, dir, "ok")
	if _, err := c.Verify(context.Background()); err != nil {
		t.Fatal("profile gate not released", err)
	}
}
