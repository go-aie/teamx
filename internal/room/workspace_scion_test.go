package room

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/scion"
)

const scionWorkspaceFixture = `#!/usr/bin/env -S python3 -S
import os,sys,json,time
args=sys.argv[1:];root=os.path.dirname(os.path.realpath(sys.argv[0]))
assert 'SCION_TOKEN' not in os.environ
assert 'MODEL_API_KEY' not in os.environ
if args==['--version']:print('scn 0.1.13');sys.exit()
origin=os.environ['SCION_ENDPOINT'];assert os.environ['SCION_HUB']==origin
path=os.environ['SCION_CREDENTIALS_FILE'];subject=json.load(open(path))[origin]
if args[:2]==['--json','whoami']:
 print(json.dumps({'schema':'scion.cli.whoami/v1','endpoint':origin,'subject':subject}));sys.exit()
mode=open(root+'/mode').read()
open(root+'/calls','a').write(json.dumps(args)+'\n')
if args[:3]==['--json','repo','info']:
 print(json.dumps({'schema':'scion.cli.repo/v1','name':'wrong/repo' if mode=='wrong' else args[3],'default_branch':'main'}));sys.exit()
if args[-2:]==['branch','ls']:
 print(json.dumps({'schema':'scion.cli.branches/v1','repo':'OpenCSG/teamx-demo','branches':[] if mode=='empty' else [{'name':'main','changeset':'c1'+'a'*64}]}));sys.exit()
assert args[0]=='clone'
assert os.path.exists(args[2]+'.clone-pending')
assert args[3:]==['--ref','main']
if mode=='block':
 open(root+'/started','w').write(path);time.sleep(60)
countfile=root+'/clones';count=int(open(countfile).read())+1 if os.path.exists(countfile) else 1
open(countfile,'w').write(str(count))
if mode=='partial' and count==2:sys.exit(4)
os.makedirs(args[2]+'/.scion',exist_ok=True)
json.dump({'format':1,'endpoint':origin,'repo':'OpenCSG/teamx-demo','ref':'main','base':'c1'+'a'*64,'head':None},open(args[2]+'/.scion/state.json','w'))
json.dump({'format':1,'files':{}},open(args[2]+'/.scion/dirstate.json','w'))
open(args[2]+'/README.md','w').write('baseline')
`

func scionWorkspaceManager(t *testing.T) (*roomManager, roomSummary, managedPlatform, string) {
	t.Helper()
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	room := managerCreate(t, m, "scion-workspace")
	openRoomOK(t, m, room, "open")
	dir := t.TempDir()
	bin := filepath.Join(dir, "teamx-scion")
	if os.WriteFile(bin, []byte(scionWorkspaceFixture), 0700) != nil || os.WriteFile(filepath.Join(dir, "mode"), []byte("ok"), 0600) != nil {
		t.Fatal("fixture")
	}
	p := &platform.CLIProfile{Version: 1, Brand: "scion", Profile: strings.Repeat("c", 32), CLIVersion: scion.CLIVersion, Executable: bin}
	scionWriteCredential(t, m, p.Profile, "alice")
	c := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "Scion Workspace", Provider: "scion", URL: "https://scion.invalid", CLIProfile: p})
	c = verifyScion(t, m, c)
	return m, room, c, dir
}
func scionWorkspaceRequest(m *roomManager, room roomSummary, c managedPlatform) workspaceApplyRequest {
	id, _ := newOpaqueID()
	return workspaceApplyRequest{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, OperationID: id, ExpectedVersion: 1, PlatformVersion: c.Version, Target: workspaceConfig{Repository: &workspaceRepository{Kind: "scion", URL: c.URL + "/OpenCSG/teamx-demo"}, Binding: &workspaceBinding{Provider: "scion", URL: c.URL, Project: "OpenCSG/teamx-demo", PlatformID: c.ID}}}
}
