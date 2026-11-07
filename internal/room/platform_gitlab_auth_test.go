package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type gitlabLoginHarness struct {
	h              *gitlabSetupHarness
	req            gitlabAuthRequest
	posts, revokes atomic.Int32
	user           atomic.Int64
	before         func(*http.Request)
	revokeStatus   atomic.Int32
}

func newGitlabLoginHarness(t *testing.T) *gitlabLoginHarness {
	t.Helper()
	h := newGitlabSetupHarness(t)
	init := gitlabSetupHTTP(t, h, "prepare", h.req)
	g := &gitlabLoginHarness{h: h, req: gitlabAuthRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: h.req.ID, Version: h.req.Version, ApplicationID: init.ApplicationID, Affected: []gitlabAuthConnection{{ID: h.req.ID, Version: h.req.Version}}}}
	g.user.Store(101)
	g.revokeStatus.Store(200)
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		status := 200
		body := ""
		switch r.URL.Path {
		case "/oauth/token":
			g.posts.Add(1)
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("client_secret") != "private-client-secret" || r.Form.Get("redirect_uri") != h.m.config.endpoint+gitlabCallbackPath || len(r.Form.Get("code_verifier")) != 43 {
				t.Error("invalid confidential PKCE exchange")
			}
			if g.before != nil {
				g.before(r)
			}
			body = `{"access_token":"candidate-access","refresh_token":"candidate-refresh","token_type":"bearer","expires_in":600}`
		case "/oauth/token/info":
			body = fmt.Sprintf(`{"resource_owner_id":%d,"scope":["api"],"application":{"uid":"private-client-id"}}`, g.user.Load())
		case "/api/v4/user":
			body = fmt.Sprintf(`{"id":%d,"username":"tester"}`, g.user.Load())
		case "/oauth/revoke":
			g.revokes.Add(1)
			status = int(g.revokeStatus.Load())
			body = `{}`
		default:
			return nil, errors.New("unexpected auth endpoint")
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	return g
}
func gitlabAuthHTTP(m *roomManager, action string, req gitlabAuthRequest, cookie *http.Cookie) *httptest.ResponseRecorder {
	data, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, m.config.endpoint+gitlabCookiePath+action, bytes.NewReader(data))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w
}
func (g *gitlabLoginHarness) login(t *testing.T) (gitlabAuthView, *http.Cookie) {
	t.Helper()
	w := gitlabAuthHTTP(g.h.m, "login", g.req, nil)
	var v gitlabAuthView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("missing browser cookie")
	}
	for _, secret := range []string{"private-client-secret", "candidate-refresh"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("secret exposed")
		}
	}
	return v, cookies[0]
}
func (g *gitlabLoginHarness) callback(v gitlabAuthView, c *http.Cookie, change func(url.Values)) *httptest.ResponseRecorder {
	u, _ := url.Parse(v.URL)
	q := url.Values{"state": {u.Query().Get("state")}, "code": {"authorization-fixture"}}
	if change != nil {
		change(q)
	}
	r := httptest.NewRequest("GET", g.h.m.config.endpoint+gitlabCallbackPath+"?"+q.Encode(), nil)
	r.Header.Set("Origin", g.h.server.URL)
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	g.h.m.ServeHTTP(w, r)
	return w
}
func (g *gitlabLoginHarness) status(t *testing.T, v gitlabAuthView, c *http.Cookie) gitlabAuthView {
	t.Helper()
	w := gitlabAuthHTTP(g.h.m, "status", gitlabAuthRequest{ServiceEpoch: g.h.m.epoch, Caller: humanCaller, SessionID: v.SessionID}, c)
	var result gitlabAuthView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return result
}
func TestGitLabAuthPKCEAndAtomicAuthorization(t *testing.T) {
	g := newGitlabLoginHarness(t)
	v, cookie := g.login(t)
	m := g.h.m
	m.mu.Lock()
	s := m.gitlabAuthSessions[v.SessionID]
	hash := sha256.Sum256([]byte(s.verifier))
	verifier := s.verifier
	m.mu.Unlock()
	u, _ := url.Parse(v.URL)
	if u.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(hash[:]) || u.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("PKCE challenge")
	}
	data, _ := os.ReadFile(m.connectionsPathForTest())
	if strings.Contains(string(data), verifier) || strings.Contains(string(data), u.Query().Get("state")) {
		t.Fatal("persisted session secrets")
	}
	before := platformTestList(t, m)[0]
	if before.CredentialConfigured {
		t.Fatal("early replacement")
	}
	w := g.callback(v, cookie, nil)
	if w.Code != 303 || w.Header().Get("Location") != gitlabCompletePath || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unsafe callback response", w.Code)
	}
	result := g.status(t, v, cookie)
	if result.Status != "succeeded" || len(result.Connections) != 1 {
		t.Fatal(result.Status, result.ErrorCode)
	}
	after := platformTestList(t, m)[0]
	if after.ID != before.ID || after.Version != before.Version+1 || after.CredentialVersion != before.CredentialVersion+1 || after.CredentialKind != "oauth" {
		t.Fatal("conversion versions")
	}
	if g.callback(v, cookie, nil).Code == 303 || g.posts.Load() != 1 {
		t.Fatal("callback replay")
	}
	if g.revokes.Load() != 0 {
		t.Fatal("adopted token revoked")
	}
}
func TestGitLabAuthRejectsStateBrowserOperatorAndEpoch(t *testing.T) {
	g := newGitlabLoginHarness(t)
	v, c := g.login(t)
	if g.callback(v, c, func(q url.Values) { q.Set("state", "wrong") }).Code != 403 {
		t.Fatal("wrong state")
	}
	if g.callback(v, nil, nil).Code != 403 {
		t.Fatal("missing cookie")
	}
	wrong := *c
	wrong.Value = "wrong"
	if g.callback(v, &wrong, nil).Code != 403 {
		t.Fatal("wrong browser")
	}
	for _, mutate := range []func(*gitlabAuthRequest){func(r *gitlabAuthRequest) { r.Caller.ID = "someone" }, func(r *gitlabAuthRequest) { r.ServiceEpoch = "old" }} {
		req := gitlabAuthRequest{ServiceEpoch: g.h.m.epoch, Caller: humanCaller, SessionID: v.SessionID}
		mutate(&req)
		if gitlabAuthHTTP(g.h.m, "status", req, c).Code != 403 {
			t.Fatal("session ownership")
		}
	}
	if g.posts.Load() != 0 {
		t.Fatal("invalid callback exchanged code")
	}
	if g.callback(v, c, nil).Code != 303 || g.posts.Load() != 1 {
		t.Fatal("valid callback was consumed by wrong browser")
	}
}
func TestGitLabAuthCancellationExpiryAndRestart(t *testing.T) {
	for _, mode := range []string{"cancel", "expire", "restart", "denied", "version"} {
		t.Run(mode, func(t *testing.T) {
			g := newGitlabLoginHarness(t)
			v, c := g.login(t)
			switch mode {
			case "cancel":
				gitlabAuthHTTP(g.h.m, "cancel", gitlabAuthRequest{ServiceEpoch: g.h.m.epoch, Caller: humanCaller, SessionID: v.SessionID}, c)
			case "expire":
				g.h.m.mu.Lock()
				s := g.h.m.gitlabAuthSessions[v.SessionID]
				s.cancel()
				g.h.m.finishGitLabAuthLocked(s, "expired", "")
				g.h.m.mu.Unlock()
			case "restart":
				dir := g.h.m.store.dir
				if err := g.h.m.close(); err != nil {
					t.Fatal(err)
				}
				g.h.m = testManager(t, dir, nil)
				g.h.m.config.endpoint = "http://127.0.0.1:8088"
			case "denied":
				w := g.callback(v, c, func(q url.Values) { q.Del("code"); q.Set("error", "access_denied") })
				if w.Code != 303 {
					t.Fatal(w.Code)
				}
			case "version":
				item := platformTestList(t, g.h.m)[0]
				platformTestSave(t, g.h.m, platformConnectionRequest{ServiceEpoch: g.h.m.epoch, Caller: humanCaller, ID: item.ID, Version: item.Version, Name: "renamed", Provider: item.Provider, URL: item.URL})
			}
			_ = g.callback(v, c, nil)
			if g.posts.Load() != 0 {
				t.Fatal("invalid session reached exchange")
			}
			if platformTestList(t, g.h.m)[0].CredentialConfigured {
				t.Fatal("invalid callback changed connection")
			}
		})
	}
}
func TestGitLabAuthAccountMismatchAndCommitFailure(t *testing.T) {
	for _, mode := range []string{"account", "save", "commit_unknown", "changed_during_exchange", "cancel_during_exchange", "revoke_unknown"} {
		t.Run(mode, func(t *testing.T) {
			g := newGitlabLoginHarness(t)
			v, c := g.login(t)
			original := g.h.m.store.ops
			switch mode {
			case "account", "revoke_unknown":
				g.user.Store(202)
				if mode == "revoke_unknown" {
					g.revokeStatus.Store(500)
				}
			case "save":
				g.before = func(*http.Request) {
					g.h.m.store.ops.rename = func(string, string) error { return errors.New("save failed") }
				}
			case "commit_unknown":
				g.before = func(*http.Request) { g.h.m.store.ops.syncDir = func(string) error { return errors.New("sync failed") } }
			case "changed_during_exchange":
				g.before = func(*http.Request) {
					item := platformTestList(t, g.h.m)[0]
					platformTestSave(t, g.h.m, platformConnectionRequest{ServiceEpoch: g.h.m.epoch, Caller: humanCaller, ID: item.ID, Version: item.Version, Name: "changed", Provider: item.Provider, URL: item.URL})
				}
			case "cancel_during_exchange":
				g.before = func(*http.Request) {
					gitlabAuthHTTP(g.h.m, "cancel", gitlabAuthRequest{ServiceEpoch: g.h.m.epoch, Caller: humanCaller, SessionID: v.SessionID}, c)
				}
			}
			g.callback(v, c, nil)
			g.h.m.store.ops = original
			result := g.status(t, v, c)
			if result.Status == "succeeded" {
				t.Fatal("unconfirmed commit reported successful")
			}
			if mode != "commit_unknown" && platformTestList(t, g.h.m)[0].CredentialConfigured {
				t.Fatal("unconfirmed authorization adopted")
			}
			if mode == "account" && (g.revokes.Load() != 1 || result.Cleanup != "revoked") {
				t.Fatal("candidate cleanup missing", result.Cleanup)
			}
			if mode == "commit_unknown" && g.revokes.Load() != 0 {
				t.Fatal("possibly committed candidate revoked")
			}
			if mode == "revoke_unknown" {
				file, err := g.h.m.connections.Load()
				if err != nil || len(file.OAuthCleanup) != 1 || file.OAuthCleanup[0].State != "unknown" {
					t.Fatal("lost revocation evidence", err)
				}
			}
		})
	}
}
func TestGitLabAuthSharedLogoutNoFallbackAndRelogin(t *testing.T) {
	g := newGitlabLoginHarness(t)
	v, c := g.login(t)
	g.callback(v, c, nil)
	m := g.h.m
	m.mu.Lock()
	file, _ := m.connections.Load()
	second := file.Connections[0]
	second.ID = strings.Repeat("d", 32)
	second.Name = "shared"
	file.Connections = append(file.Connections, second)
	if err := m.store.save(m.connectionsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	first := file.Connections[0]
	old, _ := connection.ResolveAuthorization(file, first)
	m.mu.Unlock()
	lease, err := m.acquireGitLabToken(context.Background(), RoomIdentity{}, old, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	g.req.Version = first.Version
	g.req.Affected = []gitlabAuthConnection{{ID: first.ID, Version: first.Version}, {ID: second.ID, Version: second.Version}}
	bad := g.req
	bad.Affected = bad.Affected[:1]
	if gitlabAuthHTTP(m, "logout", bad, nil).Code != 409 {
		t.Fatal("incomplete shared impact accepted")
	}
	w := gitlabAuthHTTP(m, "logout", g.req, nil)
	var out gitlabAuthView
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	cookie := w.Result().Cookies()[0]
	select {
	case <-lease.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("logout did not cancel calls")
	}
	if _, err = m.acquireGitLabToken(context.Background(), RoomIdentity{}, old, time.Second); err == nil {
		t.Fatal("new token admission after local logout")
	}
	if g.revokes.Load() != 0 {
		t.Fatal("revoke before settlement")
	}
	_ = m.finishGitLabToken(lease)
	m.mu.Lock()
	done := m.gitlabAuthSessions[out.SessionID].done
	m.mu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("logout stuck")
	}
	result := g.status(t, out, cookie)
	if result.Status != "logged_out" || g.revokes.Load() != 1 {
		t.Fatal(result.Status, result.ErrorCode)
	}
	file, err = m.connections.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range file.Connections {
		if r.CredentialKind != "oauth" || r.CredentialConfigured || r.Generation != first.Generation {
			t.Fatal("lost binding or admitted unleased access")
		}
		if _, _, err := connection.ResolveAccount(file, r); err == nil {
			t.Fatal("logged-out account admitted")
		}
	}
	// Retry logging in uses the original app and connection IDs with all references.
	g.req.Version = file.Connections[0].Version
	for i := range g.req.Affected {
		g.req.Affected[i].Version = file.Connections[i].Version
	}
	next, nextCookie := g.login(t)
	g.callback(next, nextCookie, nil)
	if result := g.status(t, next, nextCookie); result.Status != "succeeded" || len(result.Connections) != 2 {
		t.Fatal("relogin failed", result.Status, result.ErrorCode)
	}
}
func TestGitLabAuthCanonicalHTTPBrowserCookieCallback(t *testing.T) {
	g := newGitlabLoginHarness(t)
	m := g.h.m
	server := httptest.NewServer(m)
	defer server.Close()
	m.config.endpoint = server.URL
	m.mu.Lock()
	file, _ := m.connections.Load()
	file.OAuthApplications[0].RedirectURI = server.URL + gitlabCallbackPath

	if err := m.store.save(m.connectionsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	data, _ := json.Marshal(g.req)
	response, err := browser.Post(server.URL+gitlabCookiePath+"login", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	var v gitlabAuthView
	_ = json.NewDecoder(response.Body).Decode(&v)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("browser start", response.StatusCode)
	}
	u, _ := url.Parse(v.URL)
	callback := server.URL + gitlabCallbackPath + "?" + url.Values{"code": {"authorization-fixture"}, "state": {u.Query().Get("state")}}.Encode()
	response, err = browser.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 303 || response.Header.Get("Location") != gitlabCompletePath {
		t.Fatal("browser callback", response.StatusCode)
	}
	response, err = browser.Get(server.URL + gitlabCompletePath)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if bytes.Contains(body, []byte("authorization-fixture")) {
		t.Fatal("code leaked to complete page")
	}
	// The exemption is only for the callback: a GitLab Origin cannot call management.
	r := httptest.NewRequest("POST", server.URL+gitlabCookiePath+"cancel", strings.NewReader(`{}`))
	r.Header.Set("Origin", g.h.server.URL)
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("management origin guard bypassed")
	}
}

func TestGitLabAuthImportedClientOwnershipAndNewConnection(t *testing.T) {
	for _, owned := range []bool{true, false} {
		t.Run(fmt.Sprint(owned), func(t *testing.T) {
			g := newGitlabLoginHarness(t)
			old := g.h.m
			m := testManager(t, t.TempDir(), nil)
			m.config.endpoint = old.config.endpoint
			imported, err := m.connections.ImportOAuthClient(connection.OAuthClientImport{Origin: g.h.server.URL, ClientID: "private-client-id", ClientSecret: "private-client-secret", RedirectURI: m.config.endpoint + gitlabCallbackPath, Scopes: []string{"api"}, ActorID: humanCaller.ID})
			if err != nil {
				t.Fatal(err)
			}
			original := old.platformHTTP
			m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/api/v4/user/applications" {
					return original.RoundTrip(r)
				}
				if r.Header.Get("Authorization") != "Bearer candidate-access" || r.Header.Get("PRIVATE-TOKEN") != "" {
					t.Error("owner check did not use candidate OAuth")
				}
				body := `[]`
				if owned {
					body = fmt.Sprintf(`[{"id":41,"application_id":"private-client-id","application_name":"TeamX fixture","callback_url":%q,"confidential":true}]`, m.config.endpoint+gitlabCallbackPath)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			g.h.m = m
			g.req = gitlabAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Name: "new OAuth", Origin: g.h.server.URL, ClientImportID: imported}
			v, c := g.login(t)
			g.callback(v, c, nil)
			result := g.status(t, v, c)
			file, err := m.connections.Load()
			if err != nil {
				t.Fatal(err)
			}
			if owned {
				if result.Status != "succeeded" || len(file.Connections) != 1 || len(file.OAuthClientImports) != 0 || file.OAuthApplications[0].OwnerID != 101 || result.ConnectionID != file.Connections[0].ID {
					t.Fatal("owner not promoted", result.Status, result.ErrorCode)
				}
			} else {
				if result.Status == "succeeded" || len(file.Connections) != 0 || len(file.OAuthClientImports) != 1 {
					t.Fatal("foreign app promoted")
				}
			}
		})
	}
}
func TestGitLabAuthAdditionalConnectionSharesGrantAtomically(t *testing.T) {
	g := newGitlabLoginHarness(t)
	v, c := g.login(t)
	g.callback(v, c, nil)
	old := platformTestList(t, g.h.m)[0]
	g.req.ID = ""
	g.req.Version = 0
	g.req.Name = "second account view"
	g.req.Origin = old.URL
	g.req.Affected = []gitlabAuthConnection{{ID: old.ID, Version: old.Version}}
	next, cookie := g.login(t)
	g.callback(next, cookie, nil)
	result := g.status(t, next, cookie)
	if result.Status != "succeeded" || len(result.Connections) != 2 || result.ConnectionID == old.ID {
		t.Fatal("new shared connection not committed", result.Status, result.ErrorCode)
	}
	file, err := g.h.m.connections.Load()
	if err != nil || len(file.OAuthCredentials) != 1 || file.Connections[0].CredentialID != file.Connections[1].CredentialID {
		t.Fatal("grant not shared", err)
	}
	if g.revokes.Load() != 0 || len(file.OAuthCleanup) != 0 {
		t.Fatal("old grant revoked before instance acceptance")
	}
}
func TestGitLabAuthShutdownCancelsAndSettlesExchange(t *testing.T) {
	g := newGitlabLoginHarness(t)
	v, c := g.login(t)
	started := make(chan struct{})
	g.before = func(r *http.Request) { close(started); <-r.Context().Done() }
	done := make(chan struct{})
	go func() { g.callback(v, c, nil); close(done) }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := g.h.m.shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("callback escaped shutdown")
	}
}
func TestGitLabAuthLogoutRevocationUncertaintySurvivesRestart(t *testing.T) {
	g := newGitlabLoginHarness(t)
	v, c := g.login(t)
	g.callback(v, c, nil)
	item := platformTestList(t, g.h.m)[0]
	g.req.Version = item.Version
	g.req.Affected[0].Version = item.Version
	g.revokeStatus.Store(500)
	w := gitlabAuthHTTP(g.h.m, "logout", g.req, nil)
	var logout gitlabAuthView
	_ = json.Unmarshal(w.Body.Bytes(), &logout)
	g.h.m.mu.Lock()
	done := g.h.m.gitlabAuthSessions[logout.SessionID].done
	g.h.m.mu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("logout stuck")
	}
	dir := g.h.m.store.dir
	if err := g.h.m.close(); err != nil {
		t.Fatal(err)
	}
	m := testManager(t, dir, nil)
	item = platformTestList(t, m)[0]
	if item.AuthorizationStatus != "logged_out" || item.RevocationStatus != "unconfirmed" || item.CredentialConfigured {
		t.Fatal("restart restored disconnected account")
	}
	if g.revokes.Load() != 1 {
		t.Fatal("revocation replay")
	}
}

func TestGitLabAuthBrowser(t *testing.T) {
	if os.Getenv("TEAMX_G3_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	g := newGitlabLoginHarness(t)
	m := g.h.m
	server := httptest.NewServer(m)
	defer server.Close()
	m.config.endpoint = server.URL
	m.mu.Lock()
	file, _ := m.connections.Load()
	file.OAuthApplications[0].RedirectURI = server.URL + gitlabCallbackPath

	if err := m.store.save(m.connectionsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	original := m.platformHTTP
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/.well-known/") || r.URL.Path == "/api/v4/user/applications" {
			return g.h.server.Client().Transport.RoundTrip(r)
		}
		return original.RoundTrip(r)
	})
	m.mu.Unlock()
	g.h.mu.Lock()
	if g.h.remote == nil {
		g.h.remote = map[string]any{}
	}
	g.h.remote["callback_url"] = server.URL + gitlabCallbackPath
	g.h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/gitlab_oauth_browser.cjs", server.URL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func TestGitLabAuthReferenceRecheckAndNotificationProgress(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			g := newGitlabLoginHarness(t)
			transport := g.h.m.platformHTTP
			seed, _ := g.h.m.connections.Load()
			app := seed.OAuthApplications[0]
			h, c := newIndependentEventHarness(t)
			m := h.m
			m.config.endpoint = g.h.m.config.endpoint
			m.platformHTTP = transport
			m.mu.Lock()
			file, _ := m.connections.Load()
			file.Version = connection.OAuthSnapshotVersion
			file.InstallationID = seed.InstallationID
			app.Origin = c.URL
			file.OAuthApplications = []connection.OAuthApplication{app}
			file.OAuthCredentials[0].ApplicationID = app.ID
			file.OAuthCredentials[0].Scopes = app.Scopes
			evidence := platform.AuthorizationEvidence{Origin: app.Origin, ApplicationID: app.ID, ClientID: app.ClientID, CredentialID: file.OAuthCredentials[0].ID, UserID: 101, Generation: file.OAuthCredentials[0].GrantGeneration, Scopes: "api"}
			evidenceData, _ := json.Marshal(evidence)
			file.Connections[0].IdentityBinding.CredentialFingerprint = fmt.Sprintf("%x", sha256.Sum256(evidenceData))
			if err := m.store.save(m.connectionsPathForTest(), file, 2<<20); err != nil {
				t.Fatal(err)
			}
			m.mu.Unlock()
			before := h.state()
			g.h.m = m
			g.req = gitlabAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: c.ID, Version: c.Version, ApplicationID: app.ID, Affected: []gitlabAuthConnection{{ID: c.ID, Version: c.Version, ReferencedRooms: []RoomIdentity{h.room.RoomIdentity}}}}
			v, cookie := g.login(t)
			if changed {
				// A second Room starts referencing this connection while the browser is
				// away. It was not in the operator's frozen impact acknowledgement.
				other := managerCreate(t, m, "new-reference")
				cfg := before.Config
				if _, err := m.configureEventsSubscription(context.Background(), notification.Key{Owner: other.RoomIdentity, Source: gitlab.EventsSourceID}, 0, cfg); err != nil {
					t.Fatal(err)
				}
			}
			g.callback(v, cookie, nil)
			result := g.status(t, v, cookie)
			after := h.state()
			if after.SubscriptionID != before.SubscriptionID || after.Baseline != before.Baseline || after.Watermark != before.Watermark || !reflect.DeepEqual(after.Inbox, before.Inbox) || !reflect.DeepEqual(after.Observed, before.Observed) {
				t.Fatal("conversion reset notification progress")
			}
			if changed {
				if result.Status == "succeeded" || g.posts.Load() != 0 || platformTestList(t, m)[0].CredentialVersion != c.CredentialVersion {
					t.Fatal("stale reference acknowledgement adopted")
				}
			} else {
				if result.Status != "succeeded" || !after.QueryPaused() {
					t.Fatal("conversion did not preserve paused notification", result.Status)
				}
			}
		})
	}
}

func TestGitLabAuthLogoutUnknownRefreshDoesNotClaimRemoteRevocation(t *testing.T) {
	g := newGitlabLoginHarness(t)
	v, c := g.login(t)
	g.callback(v, c, nil)
	m := g.h.m
	m.mu.Lock()
	file, _ := m.connections.Load()
	file.OAuthCredentials[0].State = "refresh_unknown"
	file.OAuthCredentials[0].RefreshAttemptID = strings.Repeat("f", 32)
	if err := m.store.save(m.connectionsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()
	g.req.Version = file.Connections[0].Version
	g.req.Affected[0].Version = g.req.Version
	w := gitlabAuthHTTP(m, "logout", g.req, nil)
	var out gitlabAuthView
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	m.mu.Lock()
	done := m.gitlabAuthSessions[out.SessionID].done
	m.mu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("logout stuck")
	}
	result := g.status(t, out, w.Result().Cookies()[0])
	if result.Status != "disconnected" || platformTestList(t, m)[0].RevocationStatus != "unconfirmed" {
		t.Fatal("old token receipt incorrectly proves unknown refresh cleanup")
	}
}
func TestGitLabAuthIgnoresForwardedCallbackHost(t *testing.T) {
	g := newGitlabLoginHarness(t)
	data, _ := json.Marshal(g.req)
	r := httptest.NewRequest("POST", "http://localhost:8088"+gitlabCookiePath+"login", bytes.NewReader(data))
	r.Header.Set("Forwarded", "host=127.0.0.1:8088;proto=http")
	r.Header.Set("X-Forwarded-Host", "127.0.0.1:8088")
	w := httptest.NewRecorder()
	g.h.m.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("proxy headers changed canonical browser origin")
	}
}
