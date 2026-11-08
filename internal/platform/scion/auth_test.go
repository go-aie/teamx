package scion

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const authFixture = `#!/usr/bin/env -S python3 -S
import sys,os,json,time
from pathlib import Path
a=sys.argv[1:];root=Path(__file__).parent
mode=(root/'mode').read_text()
assert 'SCION_TOKEN' not in os.environ
assert 'MODEL_API_KEY' not in os.environ
if a==['--version']:
 print('scn 0.2.0' if mode=='version' else 'scn 0.1.13');sys.exit()
assert os.environ['SCION_HUB']==os.environ['SCION_ENDPOINT']
assert os.environ['SCION_CREDENTIALS']=='file'
assert os.environ['SCION_NO_DAEMON']=='1'
assert os.environ['SCION_CONFIG_FILE']!='global-config'
f=Path(os.environ['SCION_CREDENTIALS_FILE']);origin=os.environ['SCION_ENDPOINT']
if a==['--json','login',origin,'--with-token']:
 token=sys.stdin.read()
 with open(root/'argv','a') as log:log.write(json.dumps(a)+'\n')
 if mode=='reject':print(token,file=sys.stderr);sys.exit(5)
 f.write_text(json.dumps({origin:token}));f.chmod(0o600)
 if mode=='block':
  (root/'started').write_text(str(f));time.sleep(60)
 print(json.dumps({'schema':'scion.cli.login/v1','endpoint':origin,'subject':'bob' if mode=='mismatch' else 'alice','stored_in':str(f)}));sys.exit()
assert a==['--json','whoami',origin]
token=json.loads(f.read_text())[origin]
assert token=='private-pat-alice'
print(json.dumps({'schema':'scion.cli.whoami/v1','endpoint':origin,'subject':'alice'}))
`

func authCLI(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if os.WriteFile(p, []byte(authFixture), 0700) != nil || os.WriteFile(filepath.Join(dir, "mode"), []byte("ok"), 0600) != nil {
		t.Fatal("fixture")
	}
	return p
}

func TestScionLogoutRemovesOnlySelectedManagedProfile(t *testing.T) {
	root, p, path := profileFixture(t)
	if err := os.WriteFile(path, []byte(`{"https://scion.invalid":"private-alice-pat"}`), 0600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(root, "scion-profiles", strings.Repeat("b", 32))
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	otherFile := filepath.Join(other, "credentials.json")
	if err := os.WriteFile(otherFile, []byte("unrelated-login"), 0600); err != nil {
		t.Fatal(err)
	}
	// Local logout works even when the CLI executable is no longer installed.
	if err := os.Remove(p.Executable); err != nil {
		t.Fatal(err)
	}
	if err := Logout(root, origin, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("selected login survived", err)
	}
	if data, err := os.ReadFile(otherFile); err != nil || string(data) != "unrelated-login" {
		t.Fatal("unrelated login changed", err)
	}
	if err := Logout(root, origin, p); err != nil {
		t.Fatal("local cleanup is not idempotent", err)
	}
}

func TestScionLogoutRejectsUnsafeTargets(t *testing.T) {
	for _, mode := range []string{"parent-symlink", "profile-symlink", "credential-symlink", "permissions", "wrong-origin", "multiple-origins", "invalid-profile"} {
		t.Run(mode, func(t *testing.T) {
			root, p, path := profileFixture(t)
			if mode != "multiple-origins" {
				if err := os.WriteFile(path, []byte(`{"https://scion.invalid":"private-alice-pat"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			targetOrigin := origin
			switch mode {
			case "parent-symlink", "profile-symlink":
				dir := filepath.Dir(path)
				if mode == "parent-symlink" {
					dir = filepath.Dir(dir)
				}
				if err := os.Rename(dir, dir+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dir+"-original", dir); err != nil {
					t.Fatal(err)
				}
			case "credential-symlink":
				if err := os.Rename(path, path+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+"-original", path); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "wrong-origin":
				targetOrigin = "https://other.invalid"
			case "invalid-profile":
				p.Profile = "../escape"
			}
			if err := Logout(root, targetOrigin, p); err == nil {
				t.Fatal("unsafe logout accepted")
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatal("rejected logout removed credentials", err)
			}
		})
	}
}
