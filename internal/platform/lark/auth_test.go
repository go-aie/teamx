package lark

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

const authFixture = `#!/usr/bin/env python3
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
 print(json.dumps([{'name':'fixture','brand':'feishu','user':'测试用户','tokenStatus':'valid','private':'not projected'},{'name':'teamx-feishu','brand':'lark'}]));sys.exit()
if a==['config','show']:
 print(json.dumps({'profile':'fixture','brand':'lark' if mode=='brand' else 'feishu','appSecret':'****'}));sys.exit()
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

func TestDiscoverProfilesWithoutChangingDefaultOrExposingSecrets(t *testing.T) {
	p, dir := authTestProfile(t)
	t.Setenv("LARKSUITE_CLI_PROFILE", "wrong-profile")
	t.Setenv("LARKSUITE_CLI_WORKSPACE", "wrong-workspace")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, executable := range []string{p.Executable, ""} {
		d, err := Discover(context.Background(), executable, "feishu")
		if err != nil || d.Executable != p.Executable || len(d.Profiles) != 1 || d.Profiles[0].Name != "fixture" {
			t.Fatal(d, err)
		}
		if d.SetupProfile != "teamx-feishu-2" {
			t.Fatal("setup would overwrite an existing profile")
		}
		data, _ := json.Marshal(d)
		if strings.Contains(string(data), "private") || strings.Contains(string(data), "other") {
			t.Fatal("discovery exposed fields outside selected platform")
		}
	}
	setMode(t, dir, "version")
	if _, err := Discover(context.Background(), p.Executable, "feishu"); ErrorCode(err) != "cli_version_mismatch" {
		t.Fatal(err)
	}
}

func TestLoginSplitFlowVerifiesIdentityAndReportsRenewal(t *testing.T) {
	for _, mode := range []string{"ok", "no-refresh"} {
		t.Run(mode, func(t *testing.T) {
			p, dir := authTestProfile(t)
			setMode(t, dir, mode)
			ready := 0
			result, err := Login(context.Background(), p, func(link, qr string) {
				ready++
				if link != "https://accounts.feishu.cn/oauth?code=A%2BB&x=1" || !strings.HasPrefix(qr, "data:image/png;base64,") {
					t.Error("link changed or QR missing")
				}
			})
			if err != nil || ready != 1 || result.Validation.Status != "valid" || result.Validation.Username != "测试用户" || result.RenewalUnavailable != (mode == "no-refresh") || result.ExpiresAt.IsZero() {
				t.Fatal(result, err)
			}
		})
	}
}

func TestLoginRejectsWrongPlatformUnsafeLinksAndMissingScopes(t *testing.T) {
	for _, tc := range []struct{ mode, code string }{{"brand", "identity_mismatch"}, {"url", "invalid_response"}, {"scopes", "permission_denied"}, {"version", "cli_version_mismatch"}} {
		t.Run(tc.mode, func(t *testing.T) {
			p, dir := authTestProfile(t)
			setMode(t, dir, tc.mode)
			_, err := Login(context.Background(), p, func(string, string) {})
			if ErrorCode(err) != tc.code || strings.Contains(err.Error(), "private") {
				t.Fatal(err)
			}
		})
	}
}

func TestLoginCancellationSettlesProcessAndReleasesReaderGate(t *testing.T) {
	p, dir := authTestProfile(t)
	setMode(t, dir, "block")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := Login(ctx, p, func(string, string) { close(ready) }); done <- err }()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("login did not publish ready link")
	}
	c, _ := New(p)
	readCtx, readCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer readCancel()
	if _, err := c.Verify(readCtx); ErrorCode(err) != "timeout" {
		t.Fatal("read bypassed auth profile gate", err)
	}
	cancel()
	select {
	case err := <-done:
		if ErrorCode(err) != "cancelled" {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("auth process not settled")
	}
	if _, err := c.Verify(context.Background()); err != nil {
		t.Fatal("profile gate leaked after cancellation", err)
	}
}

func TestLogoutReportsCLIErrorWithoutLeakingCredentials(t *testing.T) {
	p, dir := authTestProfile(t)
	setMode(t, dir, "logout-fail")
	if err := Logout(context.Background(), p); ErrorCode(err) != "credential_rejected" || strings.Contains(err.Error(), "private-token") {
		t.Fatal(err)
	}
	setMode(t, dir, "ok")
	if err := Logout(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "logged-out")); err != nil {
		t.Fatal("logout was not executed")
	}
}

// Explicit read-only acceptance; never creates, switches or logs out a profile.
func TestLarkDiscoveryReal(t *testing.T) {
	if os.Getenv("TEAMX_LARK_DISCOVERY_REAL") != "1" {
		t.Skip("set TEAMX_LARK_DISCOVERY_REAL=1 for local CLI discovery")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	result, err := Discover(ctx, "", "feishu")
	if err != nil || result.Executable == "" {
		t.Fatal("real CLI discovery failed", ErrorCode(err))
	}
	t.Logf("detected pinned CLI and %d Feishu profile(s)", len(result.Profiles))
}
