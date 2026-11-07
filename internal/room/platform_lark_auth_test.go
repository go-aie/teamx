package room

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

const roomAuthCommands = `
if a==['config','show']:
 print(json.dumps({'profile':'fixture','brand':'feishu'}));sys.exit()
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
	r := managerHTTP(m, "/v1/platform/connections/lark/"+action, req)
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

func authImpact(t *testing.T, m *roomManager, profile string) []larkAuthConnection {
	t.Helper()
	var result []larkAuthConnection
	for _, c := range platformTestList(t, m) {
		if c.CLIProfile == nil || c.CLIProfile.Profile != profile {
			continue
		}
		ack := larkAuthConnection{ID: c.ID, Version: c.Version}
		for _, ref := range c.References {
			ack.ReferencedRooms = append(ack.ReferencedRooms, ref.RoomIdentity)
		}
		result = append(result, ack)
	}
	return result
}

func TestLarkAuthHTTPGuardsCancellationAndShutdown(t *testing.T) {
	m := testManager(t, t.TempDir(), nil)
	p, dir := roomAuthProfile(t)
	req := larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Profile: &p}
	for _, bad := range []larkAuthRequest{{ServiceEpoch: "stale", Caller: humanCaller, Profile: &p}, {ServiceEpoch: m.epoch, Caller: agentCaller("assistant", "member"), Profile: &p}} {
		if r := managerHTTP(m, "/v1/platform/connections/lark/login", bad); r.Code == 200 {
			t.Fatal("authentication admitted non-operator")
		}
	}
	if r := managerHTTP(m, "/v1/platform/connections/lark/unknown", req); r.Code != 404 {
		t.Fatal(r.Code)
	}
	if r := managerHTTP(m, "/v1/platform/connections/lark/login", nil); r.Code != 405 {
		t.Fatal(r.Code)
	}
	start := authHTTP(t, m, "login", req)
	status := larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, SessionID: start.SessionID}
	waiting := authWait(t, m, status, "waiting")
	if waiting.URL == "" || !strings.HasPrefix(waiting.QRCodeURL, "/v1/platform/connections/lark/qrcode?") || waiting.QRCode != "" {
		t.Fatal("authorization link or QR missing")
	}
	qr := managerHTTP(m, waiting.QRCodeURL, nil)
	if qr.Code != 200 || qr.Header().Get("Content-Type") != "image/png" || qr.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(qr.Body.String(), "\x89PNG") {
		t.Fatal("same-origin QR image not served")
	}
	page := managerHTTP(m, "/", nil)
	if !strings.Contains(page.Header().Get("Content-Security-Policy"), "img-src 'self';") {
		t.Fatal("image fix changed the existing content security policy")
	}
	save := platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("e", 32), Name: "auth in progress", Provider: "lark", URL: lark.Origin("feishu"), CLIProfile: &p, SnapshotVersion: 2}
	if r := managerHTTP(m, "/v1/platform/connections/save", save); r.Code != 409 {
		t.Fatal("saved a connection while its profile was authenticating", r.Code)
	}
	borrowed, err := url.Parse(waiting.QRCodeURL)
	if err != nil {
		t.Fatal(err)
	}
	query := borrowed.Query()
	query.Set("caller_id", "someone-else")
	borrowed.RawQuery = query.Encode()
	if r := managerHTTP(m, borrowed.String(), nil); r.Code == 200 {
		t.Fatal("QR borrowed by another operator")
	}
	other := status
	other.Caller.ID = "another-operator"
	if r := managerHTTP(m, "/v1/platform/connections/lark/status", other); r.Code == 200 {
		t.Fatal("flow borrowed by another operator")
	}
	if r := managerHTTP(m, "/v1/platform/connections/lark/login", req); r.Code != 409 {
		t.Fatal("overlapping auth allowed", r.Code)
	}
	view := authHTTP(t, m, "cancel", status)
	if view.Status != "cancelled" || view.URL != "" || view.QRCode != "" {
		t.Fatal("cancel did not settle or clear auth material")
	}
	if r := managerHTTP(m, waiting.QRCodeURL, nil); r.Code == 200 {
		t.Fatal("cancelled QR still readable")
	}
	start = authHTTP(t, m, "login", req)
	status.SessionID = start.SessionID
	authWait(t, m, status, "waiting")
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	s := m.larkAuthSessions[start.SessionID]
	m.mu.Unlock()
	select {
	case <-s.done:
	default:
		t.Fatal("shutdown leaked auth process")
	}
	if data, err := os.ReadFile(filepath.Join(m.store.dir, "platform-connections.json")); err == nil && strings.Contains(string(data), start.SessionID) {
		t.Fatal("auth session persisted")
	}
	if _, err := os.Stat(filepath.Join(dir, "auth-logout")); err == nil {
		t.Fatal("cancelling login performed logout")
	}
}

func TestLarkLogoutAcknowledgesAllSharedConnectionsAndPreservesNotificationProgress(t *testing.T) {
	h := newLarkHarness(t)
	h.start()
	before := h.state()
	script := strings.Replace(l2Fixture, "import sys,json,os", "import sys,json,os,time", 1)
	script = strings.Replace(script, "if a==['--version']:", roomAuthCommands+"\nif a==['--version']:", 1)
	if err := os.WriteFile(h.c.CLIProfile.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	second := platformTestSave(t, h.m, platformConnectionRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "shared profile", Provider: "lark", URL: h.c.URL, CLIProfile: h.c.CLIProfile})
	if _, err := h.m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: second.ID, Version: second.Version}); err != nil {
		t.Fatal(err)
	}
	req := larkAuthRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, Profile: h.c.CLIProfile}
	if r := managerHTTP(h.m, "/v1/platform/connections/lark/logout", req); r.Code != 409 {
		t.Fatal("unacknowledged shared profile mutation", r.Code)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "auth-logout")); err == nil {
		t.Fatal("rejected request touched CLI credentials")
	}
	req.Affected = authImpact(t, h.m, "fixture")
	stale := req
	stale.Affected = append([]larkAuthConnection(nil), req.Affected...)
	stale.Affected[0].Version++
	if r := managerHTTP(h.m, "/v1/platform/connections/lark/logout", stale); r.Code != 409 {
		t.Fatal("stale impact snapshot allowed", r.Code)
	}
	view := authHTTP(t, h.m, "logout", req)
	if len(view.Connections) != 2 {
		t.Fatal("shared connection omitted")
	}
	authWait(t, h.m, larkAuthRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, SessionID: view.SessionID}, "succeeded")
	for _, c := range platformTestList(t, h.m) {
		if c.Provider != "lark" {
			continue
		}
		if c.Validation.Status != "failed" || c.Validation.Account.Valid() {
			t.Fatal("logout left verified identity available")
		}
	}
	after := h.state()
	if !after.QueryPaused() || after.SubscriptionVersion <= before.SubscriptionVersion || !after.Baseline.Equal(before.Baseline) || !reflect.DeepEqual(after.Chats, before.Chats) || !reflect.DeepEqual(after.Inbox, before.Inbox) || !reflect.DeepEqual(after.Accepted, before.Accepted) {
		t.Fatal("logout did not pause or changed notification progress")
	}
}

func TestLarkLoginCompletesWithoutSavingConnectionOrLeakingFlow(t *testing.T) {
	m := testManager(t, t.TempDir(), nil)
	p, dir := roomAuthProfile(t)
	start := authHTTP(t, m, "login", larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Profile: &p})
	req := larkAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, SessionID: start.SessionID}
	authWait(t, m, req, "waiting")
	if err := os.WriteFile(filepath.Join(dir, "auth-release"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	view := authWait(t, m, req, "succeeded")
	if view.Username != "中文用户" || view.URL != "" || view.QRCode != "" || len(platformTestList(t, m)) != 0 {
		t.Fatal("login did not verify or saved a connection implicitly")
	}
	r := httptest.NewRequest("POST", "/v1/platform/connections/lark/status", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("auth responses may be cached")
	}
}
