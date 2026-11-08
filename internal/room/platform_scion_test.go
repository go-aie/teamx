package room

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/scion"
)

const scionToolFixture = `#!/usr/bin/env -S python3 -S
import os,sys,json,time
args=sys.argv[1:]
base=os.path.dirname(os.path.realpath(sys.argv[0]))
if '--help' in args or args==['--version']:
 assert 'SCION_CREDENTIALS_FILE' not in os.environ
 print('scn 0.1.13' if args==['--version'] else 'native help');sys.exit()
assert 'SCION_TOKEN' not in os.environ
assert 'MODEL_API_KEY' not in os.environ
assert os.environ['SCION_HUB']==os.environ['SCION_ENDPOINT']
path=os.environ['SCION_CREDENTIALS_FILE']
assert os.stat(path).st_mode & 0o777 == 0o600
subject=json.load(open(path))[os.environ['SCION_ENDPOINT']]
mode=open(base+'/mode').read()
if args[:2]==['--json','whoami']:
 if mode=='preflight':
  open(base+'/started','w').write(path)
  time.sleep(60)
 print(json.dumps({'schema':'scion.cli.whoami/v1','endpoint':os.environ['SCION_ENDPOINT'],'subject':subject}));sys.exit()
open(base+'/executed','a').write(json.dumps({'args':args,'cwd':os.getcwd(),'credentials':path})+'\n')
if mode=='block':
 open(base+'/started','w').write(path)
 time.sleep(60)
print('native-result');sys.exit(23)
`

func scionRoomFixture(t *testing.T) (*roomManager, roomSummary, *service, platformConnection, []MemberState, string) {
	t.Helper()
	m, room, svc, _, members := cliToolFixture(t, "exit 0")
	dir := t.TempDir()
	bin := filepath.Join(dir, "teamx-scion")
	if err := os.WriteFile(bin, []byte(scionToolFixture), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	p := &platform.CLIProfile{Version: 1, Brand: "scion", Profile: strings.Repeat("c", 32), CLIVersion: scion.CLIVersion, Executable: bin}
	scionWriteCredential(t, m, p.Profile, "alice")
	definition := scion.Definition(bin)
	definition.Skill = "skills/scion/SKILL.md"
	m.toolCatalog = append(m.toolCatalog, definition)
	c := platformTestSave(t, m, platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "Scion", Provider: "scion", URL: "https://scion.invalid", CLIProfile: p})
	return m, room, svc, c, members, dir
}

func scionWriteCredential(t *testing.T, m *roomManager, profile, subject string) string {
	t.Helper()
	dir := filepath.Join(m.store.dir, "scion-profiles", profile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credentials.json")
	data, _ := json.Marshal(map[string]string{"https://scion.invalid": subject})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func verifyScion(t *testing.T, m *roomManager, c platformConnection) platformConnection {
	t.Helper()
	c, err := m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: c.ID, Version: c.Version})
	if err != nil || c.Validation.Status != "valid" || c.Validation.UserID != 0 || c.Validation.Account.ID.Namespace != "scion:subject" {
		t.Fatal(c, err)
	}
	return c
}
