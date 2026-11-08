package room

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const scionAuthCLI = `#!/usr/bin/env -S python3 -S
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
