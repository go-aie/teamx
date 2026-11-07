package room

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/scion"
)

const scionAuthCLI = `#!/usr/bin/env python3
import os,sys,json,time
from pathlib import Path
a=sys.argv[1:];root=Path(__file__).parent
assert 'SCION_TOKEN' not in os.environ
if a==['--version']:print('scn 0.1.13');sys.exit()
f=Path(os.environ['SCION_CREDENTIALS_FILE']);origin=os.environ['SCION_ENDPOINT']
assert os.environ['SCION_HUB']==origin
if a==['--json','login',origin,'--with-token']:
 token=sys.stdin.read()
 with open(root/'calls','a') as log:log.write(json.dumps(a)+'\n')
 if token=='rejected-pat':print(token,file=sys.stderr);sys.exit(5)
 f.write_text(json.dumps({origin:token}));f.chmod(0o600)
 if (root/'mode').read_text()=='block':
  (root/'started').write_text(str(f));time.sleep(60)
 print(json.dumps({'schema':'scion.cli.login/v1','endpoint':origin,'subject':token.split('-')[0]}));sys.exit()
assert a==['--json','whoami',origin]
subject=json.loads(f.read_text())[origin].split('-')[0]
print(json.dumps({'schema':'scion.cli.whoami/v1','endpoint':origin,'subject':subject}))
`

func scionAuthProgram(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "scn")
	if os.WriteFile(exe, []byte(scionAuthCLI), 0700) != nil || os.WriteFile(filepath.Join(dir, "mode"), []byte("ok"), 0600) != nil {
		t.Fatal("fixture")
	}
	return exe
}
func scionAuthBody(m *roomManager, extra map[string]any) map[string]any {
	result := map[string]any{"service_epoch": m.epoch, "caller": humanCaller}
	for k, v := range extra {
		result[k] = v
	}
	return result
}
func awaitScionAuth(t *testing.T, m *roomManager, id string) scionAuthView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r := managerHTTP(m, "/v1/platform/connections/scion/status", scionAuthBody(m, map[string]any{"session_id": id}))
		var view scionAuthView
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &view) != nil {
			t.Fatal(r.Code, r.Body.String())
		}
		if view.Status != "starting" {
			return view
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("authentication did not settle")
	return scionAuthView{}
}
func startScionAuth(t *testing.T, m *roomManager, exe, id, pat string) {
	t.Helper()
	r := managerHTTP(m, "/v1/platform/connections/scion/login", scionAuthBody(m, map[string]any{"session_id": id, "url": "https://scion.invalid", "executable": exe, "pat": pat}))
	if r.Code != 200 || strings.Contains(r.Body.String(), pat) {
		t.Fatal("unsafe login start", r.Code, r.Body.String())
	}
}
func TestScionAuthDiscoveryLoginSaveAndNoSecretResponses(t *testing.T) {
	m := testManager(t, t.TempDir(), nil)
	exe := scionAuthProgram(t)
	id := strings.Repeat("a", 32)
	discover := func() scion.Discovery {
		t.Helper()
		r := managerHTTP(m, "/v1/platform/connections/scion/discover", scionAuthBody(m, map[string]any{"url": "https://scion.invalid", "executable": exe}))
		var d scion.Discovery
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &d) != nil || strings.Contains(r.Body.String(), "alice-pat") {
			t.Fatal(r.Code, r.Body.String())
		}
		return d
	}
	if len(discover().Profiles) != 0 {
		t.Fatal("unexpected global login discovery")
	}
	startScionAuth(t, m, exe, id, "alice-pat")
	view := awaitScionAuth(t, m, id)
	if view.Status != "succeeded" || view.Username != "alice" || view.Profile == nil || view.Profile.Profile == id {
		t.Fatal("invalid account result", view)
	}
	profiles := discover().Profiles
	if len(profiles) != 1 || profiles[0].Username != "alice" || profiles[0].Profile != view.Profile.Profile {
		t.Fatal(profiles)
	}
	c := platformTestSave(t, m, platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("c", 32), Name: "Scion", Provider: "scion", URL: "https://scion.invalid", CLIProfile: view.Profile})
	c = verifyScion(t, m, c)
	oldProfile := *c.CLIProfile
	before, _ := os.ReadFile(filepath.Join(m.store.dir, "scion-profiles", oldProfile.Profile, "credentials.json"))
	startScionAuth(t, m, exe, strings.Repeat("b", 32), "bob-pat")
	replacement := awaitScionAuth(t, m, strings.Repeat("b", 32))
	if replacement.Status != "succeeded" || replacement.Profile.Profile == oldProfile.Profile {
		t.Fatal("replacement mutated old profile")
	}
	after, _ := os.ReadFile(filepath.Join(m.store.dir, "scion-profiles", oldProfile.Profile, "credentials.json"))
	if string(before) != string(after) {
		t.Fatal("in-place credential mutation")
	}
	unchanged := platformTestList(t, m)
	if len(unchanged) != 1 || unchanged[0].Version != c.Version || unchanged[0].Validation.Username != "alice" {
		t.Fatal("login silently changed saved connection")
	}
	r := managerHTTP(m, "/v1/platform/connections/scion/login", scionAuthBody(m, map[string]any{"session_id": id, "url": "https://scion.invalid", "executable": exe, "pat": "alice-pat"}))
	if r.Code != 409 {
		t.Fatal("duplicate login ID was replayed")
	}
	calls, _ := os.ReadFile(filepath.Join(filepath.Dir(exe), "calls"))
	if len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != 2 || strings.Contains(string(calls), "-pat") {
		t.Fatal("secret in argv or duplicate login")
	}
	disk, _ := os.ReadFile(filepath.Join(m.store.dir, "platform-connections.json"))
	if strings.Contains(string(disk), "alice-pat") || strings.Contains(string(disk), "bob-pat") || strings.Contains(string(disk), id) {
		t.Fatal("auth secret/session in connection snapshot")
	}
	for _, bad := range []map[string]any{{"caller": agentCaller("assistant", "member"), "session_id": id}, {"caller": Caller{Author: Author{Kind: "human", ID: "other"}}, "session_id": id}, {"service_epoch": "stale", "session_id": id}} {
		if r := managerHTTP(m, "/v1/platform/connections/scion/status", scionAuthBody(m, bad)); r.Code == 200 {
			t.Fatal("borrowed authentication accepted")
		}
	}
	raw, _ := json.Marshal(scionAuthRequest{PAT: scionPAT("do-not-print")})
	if strings.Contains(string(raw), "do-not-print") || strings.Contains(fmt.Sprintf("%+v", scionAuthRequest{PAT: scionPAT("do-not-print")}), "do-not-print") {
		t.Fatal("request diagnostics expose PAT")
	}
}
func TestScionAuthFailureCancelAndShutdownSettleCLI(t *testing.T) {
	for _, action := range []string{"reject", "cancel", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			m := testManager(t, t.TempDir(), nil)
			exe := scionAuthProgram(t)
			id := strings.Repeat("a", 32)
			pat := "rejected-pat"
			if action != "reject" {
				pat = "alice-pat"
				os.WriteFile(filepath.Join(filepath.Dir(exe), "mode"), []byte("block"), 0600)
			}
			startScionAuth(t, m, exe, id, pat)
			if action == "reject" {
				v := awaitScionAuth(t, m, id)
				if v.Status != "failed" || v.Profile != nil || strings.Contains(v.ErrorCode, pat) {
					t.Fatal(v)
				}
			} else {
				deadline := time.Now().Add(4 * time.Second)
				for {
					if _, e := os.Stat(filepath.Join(filepath.Dir(exe), "started")); e == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("CLI not started")
					}
					time.Sleep(5 * time.Millisecond)
				}
				if action == "cancel" {
					r := managerHTTP(m, "/v1/platform/connections/scion/cancel", scionAuthBody(m, map[string]any{"session_id": id}))
					if r.Code != 200 || !strings.Contains(r.Body.String(), "cancelled") {
						t.Fatal(r.Code, r.Body.String())
					}
				} else {
					if err := m.close(); err != nil {
						t.Fatal(err)
					}
				}
			}
			entries, _ := os.ReadDir(filepath.Join(m.store.dir, "scion-profiles"))
			if len(entries) != 0 {
				t.Fatal("failed or cancelled profile survived", entries)
			}
			m.mu.Lock()
			done := m.scionAuthSessions[id].done
			m.mu.Unlock()
			select {
			case <-done:
			default:
				t.Fatal("CLI remains owned after cancellation")
			}
		})
	}
}
func TestScionAuthHTTPInputGuards(t *testing.T) {
	m := testManager(t, t.TempDir(), nil)
	exe := scionAuthProgram(t)
	base := map[string]any{"session_id": strings.Repeat("a", 32), "url": "https://scion.invalid", "executable": exe, "pat": "alice-pat"}
	for _, bad := range []map[string]any{{"caller": agentCaller("assistant", "member")}, {"service_epoch": "stale"}, {"url": "https://name:secret@scion.invalid"}, {"url": "http://scion.invalid"}, {"session_id": "../escape"}, {"pat": "bad\ntoken"}, {"profile": "caller-chosen"}} {
		q := map[string]any{}
		for k, v := range base {
			q[k] = v
		}
		for k, v := range bad {
			q[k] = v
		}
		if r := managerHTTP(m, "/v1/platform/connections/scion/login", scionAuthBody(m, q)); r.Code == 200 {
			t.Fatal("invalid authentication input admitted")
		}
	}
	if r := managerHTTP(m, "/v1/platform/connections/scion/login", nil); r.Code != 405 {
		t.Fatal(r.Code)
	}
	if r := managerHTTP(m, "/v1/platform/connections/scion/unknown", scionAuthBody(m, nil)); r.Code != 404 {
		t.Fatal(r.Code)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "calls")); !os.IsNotExist(err) {
		t.Fatal("rejected request started CLI")
	}
}
