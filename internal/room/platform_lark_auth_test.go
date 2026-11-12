package room

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

const roomAuthCommands = `
if a==['config','show']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu'}));sys.exit()
if a==['auth','scopes','--json']:
 print(json.dumps({'appId':'cli_fixture','tokenType':'user','userScopes':'im:chat:read im:message:readonly im:message.p2p_msg:get_as_user im:message.group_msg:get_as_user contact:user.basic_profile:readonly im:message.send_as_user im:message im:chat.members:read contact:user:search'.split()}));sys.exit()
if a[:2]==['auth','check']:
 print(json.dumps({'ok':True,'granted':a[a.index('--scope')+1].split(),'missing':[]}));sys.exit()
if a==['auth','status','--json']:
 print(json.dumps({'appId':'cli_fixture','identities':{'user':{'openId':'ou_reader','tokenStatus':'valid','refreshExpiresAt':'2027-12-01T00:00:00Z'}}}));sys.exit()
if a[:2]==['auth','login'] and '--no-wait' in a:
 open(base+'/auth-started','w').write('yes')
 print(json.dumps({'verification_url':'https://accounts.feishu.cn/oauth?code=fixture','device_code':'private-device-code','expires_in':600}));sys.exit()
if a[:2]==['auth','qrcode']:
 import base64
 open('code.png','wb').write(base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j5X8AAAAASUVORK5CYII='))
 print('{}');sys.exit()
if a[:3]==['auth','login','--device-code']:
 open(base+'/auth-polling','w').write('yes')
 while not os.path.exists(base+'/auth-release'):time.sleep(0.02)
 print(json.dumps({'event':'authorization_complete','user_open_id':'ou_reader','missing':[],'refresh_token_issued':True,'expires_at':'2027-01-01T00:00:00Z'}));sys.exit()
if a==['auth','logout','--json']:
 open(base+'/auth-logout','w').write('yes')
 print(json.dumps({'ok':True,'loggedOut':True}));sys.exit()
`

func roomAuthProfile(t *testing.T) (platform.CLIProfile, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "lark-cli")
	script := strings.Replace(larkFixture, "if a==['--version']:", roomAuthCommands+"\nif a==['--version']:", 1)
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	return platform.CLIProfile{Version: 1, Executable: bin, Profile: "fixture", Brand: "feishu", CLIVersion: lark.CLIVersion}, dir
}

func authHTTP(t *testing.T, m *roomManager, action string, req larkAuthRequest) larkAuthView {
	t.Helper()
	r := managerHTTP(m, "/v1/platforms/lark/"+action, req)
	var view larkAuthView
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &view) != nil {
		t.Fatal(action, r.Code, r.Body.String())
	}
	if strings.Contains(r.Body.String(), "private-device-code") || strings.Contains(r.Body.String(), "data:image") {
		t.Fatal("device code or inline image escaped to browser")
	}
	return view
}

func authWait(t *testing.T, m *roomManager, req larkAuthRequest, status string) larkAuthView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		view := authHTTP(t, m, "status", req)
		if view.Status == status {
			return view
		}
		if view.Status == "failed" {
			t.Fatal("auth failed", view.ErrorCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("auth did not reach", status)
	return larkAuthView{}
}

func authImpact(t *testing.T, m *roomManager, profile string) []larkAuthPlatform {
	t.Helper()
	var result []larkAuthPlatform
	for _, c := range platformTestList(t, m) {
		if c.CLIProfile == nil || c.CLIProfile.Profile != profile {
			continue
		}
		ack := larkAuthPlatform{ID: c.ID, Version: c.Version}
		for _, ref := range c.References {
			ack.ReferencedRooms = append(ack.ReferencedRooms, ref.RoomIdentity)
		}
		result = append(result, ack)
	}
	return result
}
