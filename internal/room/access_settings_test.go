package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func anonymousAccessFixture(t *testing.T) (*serverAccess, http.Handler) {
	t.Helper()
	c := freshAccessConfig(t, t.TempDir())
	if err := prepareFirstAccess(c); err != nil {
		t.Fatal(err)
	}
	c = freshAccessConfig(t, filepath.Dir(c.path))
	if c.Access.LoginEnabled == nil || *c.Access.LoginEnabled {
		t.Fatal("new home requires login")
	}
	if _, err := os.Stat(adminTokenPath(c)); !os.IsNotExist(err) {
		t.Fatal("anonymous install created credential", err)
	}
	a, err := newServerAccess(c, "http://127.0.0.1:8088")
	if err != nil {
		t.Fatal(err)
	}
	a.document = newSystemSettings(c).configDocument
	return a, a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
}

func settingsBody(a *serverAccess, enabled, remote bool, token, current, public string) string {
	b, _ := json.Marshal(accessSettingsInput{Caller: Caller{Author: Author{Kind: "human", ID: a.operator}}, ExpectedVersion: a.version, LoginEnabled: enabled, RemoteEnabled: remote, Token: token, CurrentToken: current, PublicBaseURL: public})
	return string(b)
}

func TestAnonymousAccessRetainsOriginIdentityAndAgentChecks(t *testing.T) {
	a, h := anonymousAccessFixture(t)
	if w := accessRequest(h, "GET", "/v1/access/session", "", "", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"login_enabled":false`) {
		t.Fatal(w.Code, w.Body.String())
	}
	body := `{"caller":{"kind":"human","id":"` + a.operator + `"}}`
	if w := accessRequest(h, "POST", "/v1/send", "", a.internal, body, nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	for _, origin := range []string{"", "https://evil.invalid"} {
		if w := accessRequest(h, "POST", "/v1/send", "", origin, body, nil); w.Code != 403 {
			t.Fatal("cross-site write accepted", w.Code)
		}
	}
	for _, peer := range []string{"192.168.1.2:4000", "8.8.8.8:4000"} {
		r := httptest.NewRequest("GET", a.internal+"/v1/ui", nil)
		r.RemoteAddr = peer
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("remote anonymous accepted", peer, w.Code)
		}
	}
	for _, host := range []string{"evil.invalid:8088", "192.168.1.10:8088"} {
		r := httptest.NewRequest("GET", a.internal+"/v1/ui", nil)
		r.RemoteAddr = "127.0.0.1:4000"
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("forged host accepted", host, w.Code)
		}
	}
	agent := `{"caller":{"kind":"agent","id":"agent","membership_id":"member"}}`
	if w := accessRequest(h, "POST", "/v1/read", "", a.internal, agent, nil); w.Code != 403 {
		t.Fatal("uncredentialed agent accepted", w.Code)
	}
	if w := accessRequest(h, "GET", "/v1/ui", "invalid-credential-with-sufficient-length", "", "", nil); w.Code != 401 {
		t.Fatal("invalid bearer became anonymous", w.Code)
	}
	r := httptest.NewRequest("GET", "http://localhost:8088/v1/ui", nil)
	r.RemoteAddr = "127.0.0.1:4000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("localhost rejected", w.Code)
	}
}

func TestAccessSettingsEnableRotateDisableAndSharedConfig(t *testing.T) {
	a, h := anonymousAccessFixture(t)
	token := "remember these four words"
	w := accessRequest(h, "POST", "/v1/access/settings", "", a.internal, settingsBody(a, true, false, token, "", ""), nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = accessRequest(h, "GET", "/v1/ui", "", "", "", nil); w.Code != 401 {
		t.Fatal("enable did not protect routes", w.Code)
	}
	cookie := accessLogin(t, h, token)
	if w = accessRequest(h, "POST", "/v1/access/settings/generate", "", a.internal, settingsBody(a, true, false, "", "", ""), nil); w.Code != 401 {
		t.Fatal("unauthenticated candidate generated", w.Code)
	}
	if w = accessRequest(h, "POST", "/v1/access/settings/generate", "", a.internal, settingsBody(a, true, false, "", "", ""), cookie); w.Code != 200 || strings.Contains(w.Body.String(), token) {
		t.Fatal("candidate requires old token or exposes it", w.Code, w.Body.String())
	}
	if w = accessRequest(h, "POST", "/v1/access/settings/reveal", "", a.internal, settingsBody(a, true, false, "", token, ""), cookie); w.Code != 404 {
		t.Fatal("obsolete reveal endpoint remains available", w.Code)
	}
	if w = accessRequest(h, "POST", "/v1/access/settings", "", a.internal, settingsBody(a, false, false, "", "", ""), cookie); w.Code != 403 {
		t.Fatal("save no longer verifies original token", w.Code)
	}
	execution, revoke, err := a.issueExecution(executionBinding{Caller: Caller{Author: Author{Kind: "agent", ID: "agent"}, MembershipID: "member"}})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	next := "another memorable long phrase"
	if w = accessRequest(h, "POST", "/v1/access/settings", "", a.internal, settingsBody(a, true, false, next, token, ""), cookie); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, bearer := range []string{token, ""} {
		if w = accessRequest(h, "GET", "/v1/ui", bearer, "", "", cookie); w.Code != 401 {
			t.Fatal("old access survived rotation", w.Code)
		}
	}
	// Token changes retain independent Agent credentials and bindings.
	body := `{"caller":{"kind":"agent","id":"agent","membership_id":"member"}}`
	if w = accessRequest(h, "POST", "/v1/read", execution, "", body, nil); w.Code != 200 {
		t.Fatal("rotation revoked Agent", w.Code, w.Body.String())
	}
	cookie = accessLogin(t, h, next)
	if w = accessRequest(h, "POST", "/v1/access/settings", "", a.internal, settingsBody(a, false, false, "", next, ""), cookie); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = accessRequest(h, "GET", "/v1/ui", "", "", "", nil); w.Code != 200 {
		t.Fatal("disable did not open local access", w.Code)
	}
	c := freshAccessConfig(t, filepath.Dir(a.config.path))
	restarted, err := newServerAccess(c, a.internal)
	if err != nil || !restarted.disabled || restarted.operator != a.operator {
		t.Fatal("restart lost access state", err)
	}
	// Settings/model writers share the same durable config document.
	settings := &systemSettings{configDocument: a.document, current: defaultSystemSettings().current}
	if _, err := settings.save("UTC", 1); err != nil {
		t.Fatal("access write broke settings writer", err)
	}
	c = freshAccessConfig(t, filepath.Dir(c.path))
	if *c.Server.TimeZone != "UTC" || *c.Access.LoginEnabled {
		t.Fatal("settings overwrite access")
	}
}

func TestAccessRemoteRequiresLoginHTTPSAndRestartBeforeDisable(t *testing.T) {
	a, h := anonymousAccessFixture(t)
	const token = "four memorable random words"
	for _, input := range []struct {
		login  bool
		public string
	}{{false, "https://assistant.example.com"}, {true, "http://192.168.1.10:8088"}, {true, ""}} {
		if w := accessRequest(h, "POST", "/v1/access/settings", "", a.internal, settingsBody(a, input.login, true, token, "", input.public), nil); w.Code != 400 {
			t.Fatal("unsafe remote accepted", w.Code, w.Body.String())
		}
	}
	w := accessRequest(h, "POST", "/v1/access/settings", "", a.internal, settingsBody(a, true, true, token, "", "https://assistant.example.com"), nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"restart_required":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	c := freshAccessConfig(t, filepath.Dir(a.config.path))
	if *c.Server.Listen != "127.0.0.1:8088" {
		t.Fatal("proxy listener must stay local", *c.Server.Listen)
	}
	if err := validateAccessListen(c, *c.Server.Listen); err != nil {
		t.Fatal(err)
	}
	fresh, err := newServerAccess(c, a.internal)
	if err != nil {
		t.Fatal(err)
	}
	fresh.document = newSystemSettings(c).configDocument
	if fresh.settingsView().RestartRequired {
		t.Fatal("restart still pending")
	}
	handler := fresh.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	// Internal Bearer keeps working; local-origin browsers cannot bypass HTTPS.
	if w = accessRequest(handler, "GET", "/v1/ui", token, "", "", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w = accessRequest(handler, "GET", "/v1/ui", "", "", "", nil); w.Code != 403 {
		t.Fatal("remote browser bypassed origin", w.Code)
	}
	if w = accessRequest(handler, "POST", "/v1/access/settings", token, "", settingsBody(fresh, false, false, "", token, ""), nil); w.Code != 400 {
		t.Fatal("active remote lost authentication", w.Code)
	}
	if w = accessRequest(handler, "POST", "/v1/access/settings", token, "", settingsBody(fresh, true, false, "", token, ""), nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = accessRequest(handler, "POST", "/v1/access/settings", token, "", settingsBody(fresh, false, false, "", token, ""), nil); w.Code != 400 {
		t.Fatal("pending remote shutdown disabled auth", w.Code)
	}
	c = freshAccessConfig(t, filepath.Dir(c.path))
	if *c.Server.Listen != "127.0.0.1:8088" {
		t.Fatal("local listen not restored")
	}
	if err := validateAccessListen(c, "0.0.0.0:8088"); err == nil {
		t.Fatal("local mode accepted wildcard")
	}
}

func TestAnonymousCLIUsesFixedOperator(t *testing.T) {
	a, _ := anonymousAccessFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(ServiceInfo{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, ServiceEpoch: "epoch"})
		})).ServeHTTP(w, r)
	}))
	defer server.Close()
	a.internal, a.public = server.URL, server.URL
	client := cliClient{endpoint: server.URL, http: server.Client()}
	id, err := client.identity(context.Background(), "room", func(string) string { return "" })
	if err != nil || id.Caller.ID != a.operator {
		t.Fatal(id, err)
	}
	body := apiRequest{ServiceInfo: id.ServiceInfo, Caller: id.Caller}
	var out ServiceInfo
	if err = client.request(context.Background(), "/v1/read", body, &out); err != nil {
		t.Fatal("anonymous CLI write failed", err)
	}
}

func TestAccessSettingsConflictsKeepOriginalCredentialAndVersionAcrossRestart(t *testing.T) {
	a, h := anonymousAccessFixture(t)
	const token = "existing-strong-access-token"
	w := accessRequest(h, "POST", "/v1/access/settings", "", a.internal, settingsBody(a, true, false, token, "", ""), nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	c := freshAccessConfig(t, filepath.Dir(a.config.path))
	fresh, err := newServerAccess(c, a.internal)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.version != a.version {
		t.Fatal("access version reset on restart", fresh.version, a.version)
	}
	cookie := accessLogin(t, h, token)
	oldVersion := a.version
	// An external edit must prevent publishing either a new credential or memory.
	if err := os.WriteFile(c.path, append(c.source, []byte("\n# external edit\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	w = accessRequest(h, "POST", "/v1/access/settings", "", a.internal, settingsBody(a, true, false, "replacement-strong-access-token", token, ""), cookie)
	if w.Code != 409 {
		t.Fatal("external edit overwritten", w.Code, w.Body.String())
	}
	if a.version != oldVersion {
		t.Fatal("failed write advanced version")
	}
	if w = accessRequest(h, "GET", "/v1/ui", token, "", "", nil); w.Code != 200 {
		t.Fatal("failed write revoked original credential", w.Code)
	}
	c = freshAccessConfig(t, filepath.Dir(c.path))
	active, err := readAdminToken(adminTokenPath(c))
	if err != nil || active != token {
		t.Fatal("failed write replaced disk credential", err)
	}
}

func TestLegacyAccessWithoutLoginSwitchRemainsProtected(t *testing.T) {
	c, token := testAccessConfig(t, t.TempDir())
	c.Access.LoginEnabled = nil
	a, err := newServerAccess(c, "http://127.0.0.1:8088")
	if err != nil {
		t.Fatal(err)
	}
	handler := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	if w := accessRequest(handler, "GET", "/v1/ui", "", "", "", nil); w.Code != 401 {
		t.Fatal("legacy access downgraded", w.Code)
	}
	if w := accessRequest(handler, "GET", "/v1/ui", token, "", "", nil); w.Code != 200 {
		t.Fatal("legacy token rejected", w.Code)
	}
}

func TestAccessWildcardServerKeepsInternalEndpoint(t *testing.T) {
	home := t.TempDir()
	c, token := testAccessConfig(t, home)
	data := append(c.source, []byte("\n[server]\nlisten='0.0.0.0:0'\nremote_enabled=true\npublic_base_url='https://assistant.example.com'\ntrusted_proxies=['127.0.0.1']\n")...)
	if err := os.WriteFile(c.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "teams"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ready := make(chan serverTestReady, 1)
	go func() {
		done <- runServer(ctx, []string{"--config", c.path}, io.Discard, io.Discard, serverOptions{ready: func(value any) error {
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			var info serverTestReady
			if err = json.Unmarshal(data, &info); err != nil {
				return err
			}
			ready <- info
			return nil
		}})
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(35 * time.Second):
			t.Error("server shutdown timeout")
		}
	}()
	var info serverTestReady
	select {
	case info = <-ready:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("server startup timeout")
	}
	if !strings.HasPrefix(info.Endpoint, "http://127.0.0.1:") {
		t.Fatal("wildcard advertised to CLI", info.Endpoint)
	}
	client := cliClient{endpoint: info.Endpoint, http: &http.Client{Timeout: time.Second}, token: token}
	var settings accessSettingsView
	if err := client.request(context.Background(), "/v1/access/settings", nil, &settings); err != nil || !settings.RemoteActive || !settings.LoginEnabled || settings.RestartRequired {
		t.Fatal(settings, err)
	}
	var health map[string]any
	if err := client.request(context.Background(), "/healthz", nil, &health); err != nil {
		t.Fatal("local access through wildcard failed", err)
	}
}

func modeSettingsBody(a *serverAccess, mode, port, token, current string, login bool) string {
	b, _ := json.Marshal(accessSettingsInput{Caller: Caller{Author: Author{Kind: "human", ID: a.operator}}, ExpectedVersion: a.version, LoginEnabled: login, AccessMode: mode, Port: port, Token: token, CurrentToken: current})
	return string(b)
}

func TestLANSettingsPersistPortAndRequireRestartBeforeDisablingLogin(t *testing.T) {
	a, h := anonymousAccessFixture(t)
	const token = "memorable-local-network-token"
	for _, input := range []struct {
		mode, port string
		login      bool
	}{{accessLAN, "8088", false}, {"unknown", "8088", true}, {accessLAN, "65536", true}, {accessLAN, "0", true}, {accessLAN, "oops", true}} {
		w := accessRequest(h, "POST", "/v1/access/settings", "", a.internal, modeSettingsBody(a, input.mode, input.port, token, "", input.login), nil)
		if w.Code != 400 {
			t.Fatal("invalid LAN settings accepted", input, w.Code, w.Body.String())
		}
	}
	w := accessRequest(h, "POST", "/v1/access/settings", "", a.internal, modeSettingsBody(a, accessLAN, "8090", token, "", true), nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var view accessSettingsView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || view.AccessMode != accessLAN || view.ActiveMode != accessLocal || !view.RestartRequired || view.RemoteActive || view.Port != "8090" {
		t.Fatal("pending state incorrect", view, err)
	}
	c := freshAccessConfig(t, filepath.Dir(a.config.path))
	if *c.Server.Listen != "0.0.0.0:8090" || c.Server.PublicBaseURL != "" || len(c.Server.TrustedProxies) != 0 || !remoteAccessEnabled(c) {
		t.Fatal("LAN configuration lost", c.Server)
	}
	if err := validateAccessListen(c, *c.Server.Listen); err != nil {
		t.Fatal(err)
	}
	fresh, err := newServerAccess(c, "http://127.0.0.1:8090")
	if err != nil {
		t.Fatal(err)
	}
	fresh.document = newSystemSettings(c).configDocument
	if view = fresh.settingsView(); view.RestartRequired || !view.RemoteActive || view.ActiveMode != accessLAN {
		t.Fatal("restart did not activate LAN", view)
	}
	h = fresh.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", fresh.internal+"/v1/access/settings", strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:4000"
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, mode := range []string{accessLAN, accessLocal} {
		w = post(modeSettingsBody(fresh, mode, "", "", token, false))
		if w.Code != 400 {
			t.Fatal("active LAN lost login", w.Code, w.Body.String())
		}
	}
	w = post(modeSettingsBody(fresh, accessLocal, "", "", token, true))
	if w.Code != 200 || !fresh.settingsView().RemoteActive || !fresh.settingsView().RestartRequired {
		t.Fatal("LAN shutdown state incorrect", w.Code, w.Body.String())
	}
	w = post(modeSettingsBody(fresh, accessLocal, "", "", token, false))
	if w.Code != 400 {
		t.Fatal("pending shutdown disabled login", w.Code)
	}
	c = freshAccessConfig(t, filepath.Dir(c.path))
	if *c.Server.Listen != "127.0.0.1:8090" {
		t.Fatal("local listener not restored", *c.Server.Listen)
	}
	restarted, err := newServerAccess(c, "http://127.0.0.1:8090")
	if err != nil || restarted.settingsView().RemoteActive || restarted.settingsView().RestartRequired {
		t.Fatal("local restart state incorrect", err)
	}
}

func TestLANOriginRequiresPrivatePeerServerIPAndSameOrigin(t *testing.T) {
	c, token := testAccessConfig(t, t.TempDir())
	c.Server.AccessMode = accessLAN
	listen := "0.0.0.0:8088"
	c.Server.Listen = &listen
	a, err := newServerAccess(c, "http://127.0.0.1:8088")
	if err != nil {
		t.Fatal(err)
	}
	a.lanHosts = []string{"192.168.1.10", "fd00::10"}
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	request := func(method, route, host, peer, origin, body string, cookie *http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+host+route, strings.NewReader(body))
		r.RemoteAddr = peer
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	const host, peer, origin = "192.168.1.10:8088", "192.168.1.2:4000", "http://192.168.1.10:8088"
	if w := request("GET", "/v1/ui", host, peer, "", "", nil, nil); w.Code != 401 {
		t.Fatal("LAN private API exposed", w.Code)
	}
	w := request("POST", "/v1/access/login", host, peer, origin, `{"token":"`+token+`"}`, nil, nil)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatal("LAN login rejected", w.Code, w.Body.String())
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Name != "teamx-lan-session" || !cookie.HttpOnly || cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("wrong LAN cookie", cookie)
	}
	body := `{"caller":{"kind":"human","id":"` + a.operator + `"}}`
	if w = request("POST", "/v1/send", host, peer, origin, body, cookie, nil); w.Code != 200 {
		t.Fatal("same-origin LAN write rejected", w.Code, w.Body.String())
	}
	for _, input := range []struct {
		host, peer, origin string
		headers            map[string]string
	}{
		{"evil.invalid:8088", peer, origin, nil},
		{"192.168.1.99:8088", peer, origin, nil},
		{"192.168.1.10:8090", peer, origin, nil},
		{host, "8.8.8.8:4000", origin, nil},
		{"127.0.0.1:8088", "8.8.8.8:4000", "http://127.0.0.1:8088", nil},
		{host, peer, "https://evil.invalid", nil},
		{host, peer, "", nil},
		{host, peer, origin, map[string]string{"X-Forwarded-Proto": "https"}},
		{host, peer, origin, map[string]string{"Forwarded": "proto=https"}},
		{host, peer, origin, map[string]string{"X-Forwarded-Host": host}},
	} {
		w = request("POST", "/v1/send", input.host, input.peer, input.origin, body, cookie, input.headers)
		if w.Code != 403 {
			t.Fatal("invalid LAN request accepted", input, w.Code, w.Body.String())
		}
	}
	if w = request("GET", "/v1/ui", "[fd00::10]:8088", "[fd00::2]:4000", "", "", nil, map[string]string{"Authorization": "Bearer " + token}); w.Code != 200 {
		t.Fatal("private IPv6 rejected", w.Code)
	}
	if w = accessRequest(h, "GET", "/v1/ui", token, "", "", nil); w.Code != 200 {
		t.Fatal("LAN broke internal CLI endpoint", w.Code)
	}
}

func TestManagedLANAccessKeepsInternalControlEndpoint(t *testing.T) {
	c, _ := testAccessConfig(t, t.TempDir())
	c.Server.AccessMode = accessLAN
	if err := validateManagedAccess(c, "0.0.0.0:8088"); err != nil {
		t.Fatal(err)
	}
	if err := validateManagedAccess(c, "127.0.0.1:8088"); err == nil {
		t.Fatal("LAN mode accepted loopback-only listener")
	}
	c.Server.AccessMode = accessProxy
	if err := validateManagedAccess(c, "0.0.0.0:8088"); err == nil {
		t.Fatal("managed proxy mode exposed backend")
	}
	if got := internalAccessEndpoint("0.0.0.0:8088"); got != "http://127.0.0.1:8088" {
		t.Fatal(got)
	}
}
