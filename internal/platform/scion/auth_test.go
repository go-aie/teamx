package scion

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
func TestScionPATLoginDiscoveryAndIsolation(t *testing.T) {
	t.Setenv("SCION_TOKEN", "must-not-inherit")
	t.Setenv("SCION_CONFIG_FILE", "global-config")
	t.Setenv("MODEL_API_KEY", "must-not-inherit")
	for _, name := range []string{"scn", "teamx-scion"} {
		t.Run(name, func(t *testing.T) {
			exe := authCLI(t, name)
			root := t.TempDir()
			id := strings.Repeat("a", 32)
			empty, err := Discover(context.Background(), root, origin, exe)
			if err != nil || len(empty.Profiles) != 0 || empty.Executable != exe {
				t.Fatal(empty, err)
			}
			p, v, err := Login(context.Background(), root, origin, exe, id, platform.NewSecret("private-pat-alice"))
			if err != nil || v.Username != "alice" || v.Account.ID.Value != "alice" || p.Profile != id {
				t.Fatal(v, err)
			}
			found, err := Discover(context.Background(), root, origin, exe)
			b, _ := json.Marshal(found)
			if err != nil || len(found.Profiles) != 1 || found.Profiles[0].Username != "alice" || strings.Contains(string(b), "private-pat") || strings.Contains(string(b), "credentials.json") {
				t.Fatal(found, err)
			}
			path := filepath.Join(root, "scion-profiles", id, "credentials.json")
			info, e := os.Stat(path)
			if e != nil || info.Mode().Perm() != 0600 {
				t.Fatal("private credential permissions")
			}
			before, _ := os.ReadFile(path)
			if _, _, err := Login(context.Background(), root, origin, exe, id, platform.NewSecret("different-pat")); err == nil {
				t.Fatal("overwrote existing profile")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("existing credentials changed")
			}
			argv, _ := os.ReadFile(filepath.Join(filepath.Dir(exe), "argv"))
			if strings.Contains(string(argv), "private-pat") {
				t.Fatal("PAT in argv")
			}
		})
	}
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
func TestScionPATFailuresAndCancelledUnpublishedProfile(t *testing.T) {
	for _, mode := range []string{"reject", "mismatch", "version", "block"} {
		t.Run(mode, func(t *testing.T) {
			exe := authCLI(t, "scn")
			root := t.TempDir()
			id := strings.Repeat("b", 32)
			os.WriteFile(filepath.Join(filepath.Dir(exe), "mode"), []byte(mode), 0600)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, _, err := Login(ctx, root, origin, exe, id, platform.NewSecret("private-pat-alice"))
				done <- err
			}()
			if mode == "block" {
				started := filepath.Join(filepath.Dir(exe), "started")
				deadline := time.Now().Add(4 * time.Second)
				for {
					if _, e := os.Stat(started); e == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("login not started")
					}
					time.Sleep(5 * time.Millisecond)
				}
				found, err := Discover(context.Background(), root, origin, exe)
				if err != nil || len(found.Profiles) != 0 {
					t.Fatal("unfinished profile was discoverable", found, err)
				}
				cancel()
			}
			select {
			case err := <-done:
				if err == nil || strings.Contains(err.Error(), "private-pat") {
					t.Fatal("unsafe login result", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("login not settled")
			}
			if _, err := os.Stat(filepath.Join(root, "scion-profiles", id)); !os.IsNotExist(err) {
				t.Fatal("failed profile not removed")
			}
		})
	}
}
func TestScionDiscoveryRejectsUnsafeRootAndExplicitExecutable(t *testing.T) {
	exe := authCLI(t, "scn")
	for _, mode := range []string{"symlink", "permissions"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "scion-profiles")
			if mode == "symlink" {
				os.Symlink(t.TempDir(), path)
			} else {
				os.Mkdir(path, 0755)
			}
			if _, err := Discover(context.Background(), root, origin, exe); err == nil {
				t.Fatal("unsafe root accepted")
			}
			if _, _, err := Login(context.Background(), root, origin, exe, strings.Repeat("a", 32), platform.NewSecret("private-pat-alice")); err == nil {
				t.Fatal("login accepted unsafe root")
			}
		})
	}
	if _, err := ResolveCLI(context.Background(), "scn"); err == nil {
		t.Fatal("relative explicit path accepted")
	}
	t.Setenv("TEAMX_SCION_BIN", "/missing/explicit-wrapper")
	t.Setenv("TEAMX_SCN_BIN", exe)
	if _, err := ResolveCLI(context.Background(), ""); err == nil {
		t.Fatal("explicit wrapper override fell back")
	}
}

func TestScionAutomaticCLIResolution(t *testing.T) {
	exe := authCLI(t, "scn")
	t.Setenv("TEAMX_SCION_BIN", exe)
	resolved, err := ResolveCLI(context.Background(), "")
	if err != nil || resolved != exe {
		t.Fatal("configured CLI not discovered", resolved, err)
	}
	t.Setenv("TEAMX_SCION_BIN", "")
	t.Setenv("TEAMX_SCN_BIN", exe)
	t.Setenv("PATH", "/usr/bin:/bin")
	resolved, err = ResolveCLI(context.Background(), "")
	if err != nil || resolved != exe {
		t.Fatal("native fallback not discovered", resolved, err)
	}
}
