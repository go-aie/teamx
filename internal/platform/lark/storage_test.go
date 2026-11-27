package lark

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The same production setup/auth/read/pin methods run against a synthetic CLI.
// All mutable state follows the injected pair, including refresh/DPoP markers.
const scopedCLIFixture = `#!/usr/bin/env -S python3 -S
import sys,os,json,base64
args=sys.argv[1:];profile=''
if args[:1]==['--profile']:profile,args=args[1],args[2:]
config=os.environ['LARKSUITE_CLI_CONFIG_DIR'];data=os.environ['LARKSUITE_CLI_DATA_DIR']
assert os.path.isabs(config) and os.path.isabs(data)
assert not any(k in os.environ for k in ['LARKSUITE_CLI_PROFILE','LARKSUITE_CLI_USER_ACCESS_TOKEN','LARKSUITE_CLI_WORKSPACE'])
cfg=config+'/config.json';token=data+'/credential';dpop=data+'/dpop'
def app():
 a=json.load(open(cfg))['apps'][0]
 assert a['name']==profile
 return a
def output(v):print(json.dumps(v))
if args==['--version']:print('lark-cli version 1.0.99')
elif args==['profile','list']:
 if not os.path.exists(cfg):output([])
 else:
  a=json.load(open(cfg))['apps'][0];output([{'name':a['name'],'appId':a['appId'],'brand':'feishu','active':True,'user':'reader' if os.path.exists(token) else ''}])
elif args[:3]==['config','init','--new']:
 name=args[args.index('--name')+1]
 json.dump({'currentApp':'cli_fixture','apps':[{'name':name,'appId':'cli_fixture','brand':'feishu','dpopMode':'required','appSecret':{'source':'keychain','id':'appsecret:cli_fixture'},'users':[{'userOpenId':'ou_reader'}]}]},open(cfg,'w'))
 print('https://open.feishu.cn/page/cli?user_code=TEST&lpv=1.0.99&ocv=1.0.99&from=cli',file=sys.stderr)
 output({'appId':'cli_fixture','brand':'feishu'})
elif args==['config','show']:
 a=app();output({'profile':profile,'appId':a['appId'],'brand':'feishu','dpopMode':'required'})
elif args[:2]==['auth','qrcode']:
 open('code.png','wb').write(base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j5X8AAAAASUVORK5CYII='));output({})
elif args[:3]==['api','GET','/open-apis/bot/v3/info']:output({'ok':True,'identity':'bot','data':{'app_name':'private app'}})
elif args==['auth','scopes','--json']:output({'appId':'cli_fixture','scopes':__SCOPES__})
elif args[:2]==['auth','check']:output({'ok':True,'granted':args[args.index('--scope')+1].split(),'missing':[]})
elif args[:2]==['auth','login'] and '--no-wait' in args:
 app();output({'verification_url':'https://accounts.feishu.cn/oauth?user_code=TEST','device_code':'synthetic-code','expires_in':60})
elif args[:3]==['auth','login','--device-code']:
 open(token,'w').write('synthetic-private-token');open(dpop,'w').write('same-dpop-key')
 output({'event':'authorization_complete','user_open_id':'ou_reader','missing':[],'refresh_token_issued':True,'expires_at':'2027-12-01T00:00:00Z'})
elif args==['auth','status','--json']:output({'appId':'cli_fixture','identities':{'user':{'openId':'ou_reader','tokenStatus':'valid','refreshExpiresAt':'2027-12-01T00:00:00Z'}}})
elif args==['auth','logout','--json']:
 app()
 if os.path.exists(token):os.remove(token)
 output({'ok':True,'loggedOut':True})
elif args==['whoami','--as','user']:
 a=app();output({'profile':profile,'appId':'cli_fixture','brand':'feishu','identity':'user','available':os.path.exists(token),'onBehalfOf':{'openId':a['users'][0]['userOpenId']}})
elif args[:3]==['api','GET','/open-apis/authen/v1/user_info']:
 a=app();assert os.path.exists(token) and open(dpop).read()=='same-dpop-key'
 output({'ok':True,'identity':'user','data':{'open_id':a['users'][0]['userOpenId'],'tenant_key':'tenant','name':'reader'}})
else:raise Exception('unexpected command')
`

func TestScopedLarkLifecycleKeepsCredentialsAndPinInOriginalContext(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "lark-cli")
	scopes, _ := json.Marshal(DefaultScopes())
	if err := os.WriteFile(bin, []byte(strings.ReplaceAll(scopedCLIFixture, "__SCOPES__", string(scopes))), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LARKSUITE_CLI_CONFIG_DIR", "/must-not-inherit/config")
	t.Setenv("LARKSUITE_CLI_DATA_DIR", "/must-not-inherit/data")
	t.Setenv("LARKSUITE_CLI_USER_ACCESS_TOKEN", "must-not-inherit")
	factories := make([]*ClientFactory, 2)
	profiles := make([]SetupResult, 2)
	for i := range factories {
		f, err := NewClientFactory(0, 0, 0).WithStorage(filepath.Join(dir, string(rune('a'+i))), bin)
		if err != nil {
			t.Fatal(err)
		}
		tmp, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		// Assert every CLI child inherits the selected temporary root. Includes
		// init/QR, login, normal reads and pinning, not just Process.Run.
		if err := os.Chmod(tmp, 0700); err != nil {
			t.Fatal(err)
		}
		f, err = f.WithTemporaryRoot(tmp)
		if err != nil {
			t.Fatal(err)
		}
		clientForTemp := &Client{tempRoot: tmp}
		if env := clientForTemp.command(t.Context(), "", "--version").Env; !slices.Contains(env, "TMPDIR="+tmp) {
			t.Fatal("CLI inherited global temporary root")
		}
		factories[i] = f
		result, err := f.ConfigureApplication(t.Context(), "", "feishu", func(SetupProgress) {})
		if err != nil || !result.Found {
			t.Fatal(result, err)
		}
		profiles[i] = result
		login, err := f.Login(t.Context(), result.Profile, func(string, string) {})
		if err != nil || !login.Validation.Account.Valid() {
			t.Fatal(login, err)
		}
		client, err := f.New(result.Profile)
		if err != nil || client.dataDir == client.configDir {
			t.Fatal("storage pair missing", err)
		}
		if f.SupportsPin() {
			pin, err := f.Pin(t.Context(), result.Profile, login.Validation.Account)
			if err != nil {
				t.Fatal(err)
			}
			if pin.client.dataDir != client.dataDir || pin.client.configDir == client.configDir {
				t.Fatal("pin cloned or replaced credential state")
			}
			if _, err := pin.Verify(t.Context()); err != nil {
				t.Fatal(err)
			}
			pin.Close()
			if _, err := os.Stat(client.dataDir); err != nil {
				t.Fatal("pin removed credentials", err)
			}
		}
		// Reconstructing the factory preserves the original credential/DPoP files.
		reopened, err := NewClientFactory(0, 0, 0).WithStorage(f.storage.root, bin)
		if err != nil {
			t.Fatal(err)
		}
		if c, err := reopened.New(result.Profile); err != nil {
			t.Fatal(err)
		} else if _, err := c.Verify(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	// Upgrade only the runtime program. The old executable is removed; the
	// original profile, credentials and request ownership remain intact.
	nextBin := filepath.Join(dir, "lark-cli-new")
	body, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nextBin, body, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	for i, old := range factories {
		metadataPath := filepath.Join(old.storage.directory(profiles[i].Profile.Profile), "profile.json")
		before, err := os.ReadFile(metadataPath)
		if err != nil {
			t.Fatal(err)
		}
		f, err := old.WithStorage(old.storage.root, nextBin)
		if err != nil {
			t.Fatal(err)
		}
		factories[i] = f
		c, err := f.New(profiles[i].Profile)
		if err != nil {
			t.Fatal(err)
		}
		if c.executable != nextBin || c.profile != profiles[i].Profile || c.requests != old.requests || c.gates != old.gates {
			t.Fatal("upgrade changed connection or queue ownership")
		}
		v, err := c.Verify(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.CheckPermissions(t.Context(), profiles[i].Profile, v.Account); err != nil {
			t.Fatal(err)
		}
		discovery, err := f.Discover(t.Context(), "", "feishu")
		if err != nil || len(discovery.Profiles) != 1 || discovery.Profiles[0].CLIProfile == nil || *discovery.Profiles[0].CLIProfile != profiles[i].Profile {
			t.Fatal("upgrade discovery changed saved reference", err)
		}
		if f.SupportsPin() {
			pin, err := f.Pin(t.Context(), profiles[i].Profile, v.Account)
			if err != nil {
				t.Fatal(err)
			}
			if pin.client.executable != nextBin || pin.client.dataDir != c.dataDir {
				t.Fatal("pin lost runtime binding or credentials")
			}
			if _, err := pin.Verify(t.Context()); err != nil {
				t.Fatal(err)
			}
			pin.Close()
		}
		after, err := os.ReadFile(metadataPath)
		if err != nil || string(before) != string(after) {
			t.Fatal("upgrade rewrote metadata", err)
		}
		tampered := profiles[i].Profile
		tampered.Executable = nextBin
		if _, err := f.New(tampered); ErrorCode(err) != "invalid_cli_storage" {
			t.Fatal("one-sided profile rewrite admitted", err)
		}
		if err := f.storage.provision(tampered); ErrorCode(err) != "profile_conflict" {
			t.Fatal(err)
		}
		tampered.Profile = "unapproved"
		tampered.Executable = bin
		if err := f.storage.provision(tampered); ErrorCode(err) != "invalid_cli_storage" {
			t.Fatal("old executable admitted for new profile", err)
		}
	}
	if _, err := factories[1].New(profiles[0].Profile); err == nil {
		t.Fatal("profile from another Space admitted")
	}
	if err := factories[0].Logout(t.Context(), profiles[0].Profile); err != nil {
		t.Fatal(err)
	}
	a, _ := factories[0].New(profiles[0].Profile)
	if _, err := a.Verify(t.Context()); err == nil {
		t.Fatal("logout selected another account")
	}
	b, _ := factories[1].New(profiles[1].Profile)
	if _, err := b.Verify(t.Context()); err != nil {
		t.Fatal("logout affected other Space", err)
	}
	if err := os.Rename(b.dataDir, b.dataDir+".missing"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Verify(t.Context()); ErrorCode(err) != "invalid_cli_storage" {
		t.Fatal("missing data root fell back", err)
	}
	if _, err := factories[1].New(profiles[1].Profile); err == nil {
		t.Fatal("missing context recreated")
	}
	if err := os.Symlink(a.dataDir, b.dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Verify(t.Context()); ErrorCode(err) != "invalid_cli_storage" {
		t.Fatal("linked data root admitted", err)
	}
	if _, err := factories[1].New(profiles[1].Profile); err == nil {
		t.Fatal("linked context admitted")
	}
	link := filepath.Join(dir, "linked-context")
	if err := os.Symlink(factories[0].storage.root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := factories[0].WithStorage(link, bin); err == nil {
		t.Fatal("linked storage root admitted")
	}
	if _, err := factories[0].Discover(context.Background(), "/unapproved/program", "feishu"); err == nil {
		t.Fatal("caller executable admitted")
	}
}
