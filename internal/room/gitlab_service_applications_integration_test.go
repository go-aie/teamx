//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlaboauth"
	"github.com/go-aie/teamx/internal/platform/registry"
)

func hostedGitLabServer(t *testing.T, home string) *spaceServer {
	t.Helper()
	s := hostedFixture(t, home)
	s.access.config.Deployment.GitLab.Enabled = true
	if err := s.openGitLabApplications(); err != nil {
		t.Fatal(err)
	}
	return s
}
func hostedGitLabReady(t *testing.T, s *spaceServer, id string, cookie *http.Cookie) *roomManager {
	t.Helper()
	waitSpaceState(t, s, id, "setup_required")
	body := setupModelInput{RequestID: "managed-model", ExpectedVersion: 1, ModelParameters: modelTestEntry("managed-model").ModelParameters, APIKey: "fixture-key"}
	w := hostedRequest(s, spaceRoute(id)+"/v1/setup/model", body, cookie, "")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	return waitSpaceState(t, s, id, "ready").manager
}
func hostedGitLabTransport(user int64) http.RoundTripper {
	return workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		body := "{}"
		switch r.URL.Path {
		case "/oauth/token":
			body = fmt.Sprintf(`{"access_token":"access-%d","refresh_token":"refresh-%d","token_type":"bearer","expires_in":600}`, user, user)
		case "/oauth/token/info":
			body = fmt.Sprintf(`{"resource_owner_id":%d,"scope":["api"],"application":{"uid":"managed-client"}}`, user)
		case "/api/v4/user":
			body = fmt.Sprintf(`{"id":%d,"username":"user%d"}`, user, user)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
}
func hostedGitLabCallback(s *spaceServer, auth gitlabAuthView, browser, oauth *http.Cookie) *httptest.ResponseRecorder {
	jump, _ := url.Parse(auth.URL)
	q := url.Values{"state": {jump.Query().Get("state")}, "code": {"fixture-code"}}
	r := httptest.NewRequest("GET", "https://teamx.example"+gitlabCallbackPath+"?"+q.Encode(), nil)
	r.RemoteAddr = "127.0.0.1:1234"
	if browser != nil {
		r.AddCookie(browser)
	}
	if oauth != nil {
		r.AddCookie(oauth)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestHostedGitLabApplicationRoutingAdministrationAndRestart(t *testing.T) {
	home := t.TempDir()
	s := hostedGitLabServer(t, home)
	admin, err := readAdminToken(adminTokenPath(s.access.config))
	if err != nil {
		t.Fatal(err)
	}
	app := gitlaboauth.Application{OAuthApplication: registry.OAuthApplication{ID: strings.Repeat("d", 32), Source: gitlaboauth.ServiceSource, Origin: "https://gitlab.example", OwnerID: 999, ClientID: "managed-client", RedirectURI: "https://teamx.example" + gitlabCallbackPath, Scopes: []string{"api"}}, Name: "Company GitLab", Secret: "control-plane-secret", Status: "active"}
	if w := hostedRequest(s, "/v1/deployment/gitlab-applications", map[string]any{"application": app, "version": 0}, nil, admin); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	alice, a, defaultA := hostedLogin(t, s, "alice")
	bob, b, defaultB := hostedLogin(t, s, "bob")
	if w := hostedRequest(s, "/v1/deployment/gitlab-applications", nil, alice, ""); w.Code != 403 {
		t.Fatal("user read control catalog")
	}
	extra, _ := newOpaqueID()
	if w := hostedRequest(s, "/v1/spaces", spaceChangeInput{RequestID: extra, Name: "Work"}, alice, ""); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if extra == defaultA {
		t.Fatal("non-default target missing")
	}
	managers := []*roomManager{hostedGitLabReady(t, s, extra, alice), hostedGitLabReady(t, s, defaultB, bob)}
	auths := make([]gitlabAuthView, 2)
	cookies := make([]*http.Cookie, 2)
	for i, m := range managers {
		m.mu.Lock()
		m.platformHTTP = hostedGitLabTransport(int64(201 + i))
		m.mu.Unlock()
		user, cookie, id := []userSubject{a, b}[i], []*http.Cookie{alice, bob}[i], []string{extra, defaultB}[i]
		directory := hostedRequest(s, spaceRoute(id)+gitlabCookiePath+"applications?service_epoch="+m.epoch+"&caller_id="+user.UserID, nil, cookie, "")
		if directory.Code != 200 || strings.Contains(directory.Body.String(), "control-plane-secret") {
			t.Fatal(directory.Code, directory.Body.String())
		}
		req := gitlabAuthRequest{ServiceEpoch: m.epoch, Caller: Caller{Kind: "human", ID: user.UserID}, ApplicationID: app.ID, Origin: app.Origin, Name: "My GitLab", Scopes: []string{"api"}}
		w := hostedRequest(s, spaceRoute(id)+gitlabCookiePath+"login", req, cookie, "")
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &auths[i]) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		cookies[i] = w.Result().Cookies()[0]
		if cookies[i].Path != "/" {
			t.Fatal("shared callback cannot receive browser binding", cookies[i].Path)
		}
	}
	if w := hostedGitLabCallback(s, auths[0], bob, cookies[0]); w.Code != 403 {
		t.Fatal("cross-user global callback accepted")
	}
	for i, v := range auths {
		w := hostedGitLabCallback(s, v, []*http.Cookie{alice, bob}[i], cookies[i])
		if w.Code != 303 || w.Header().Get("Location") != spaceRoute([]string{extra, defaultB}[i])+gitlabCompletePath {
			t.Fatal("callback lost original Space", w.Code, w.Header().Get("Location"), w.Body.String())
		}
		f := mustSnapshot(t, managers[i])
		if len(f.OAuthCredentials) != 1 || f.OAuthCredentials[0].OwnerUserID != []userSubject{a, b}[i].UserID {
			t.Fatal("grant committed to wrong owner")
		}
	}
	inventory := hostedRequest(s, "/v1/deployment/gitlab-applications", nil, nil, admin)
	if inventory.Code != 200 || !strings.Contains(inventory.Body.String(), extra) || !strings.Contains(inventory.Body.String(), defaultB) || strings.Contains(inventory.Body.String(), "control-plane-secret") {
		t.Fatal("missing or unsafe impact inventory", inventory.Code, inventory.Body.String())
	}
	// Normal restart keeps the catalog, exact grants and browser subjects.
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	restarted := hostedGitLabServer(t, home)
	for _, record := range restarted.directory.Spaces {
		if record.Status == "active" {
			restarted.start(record, nil, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8088})
		}
	}
	for i, id := range []string{extra, defaultB} {
		m := waitSpaceState(t, restarted, id, "ready").manager
		f := mustSnapshot(t, m)
		if len(f.OAuthCredentials) != 1 || f.OAuthApplications[0].ClientSecret != "" {
			t.Fatal("restart lost reference")
		}
		if w := hostedRequest(restarted, spaceRoute(id)+"/v1/rooms", nil, []*http.Cookie{alice, bob}[i], ""); w.Code != 200 {
			t.Fatal("restart lost user admission", w.Code)
		}
	}
	// Disabling blocks old grants without claiming to revoke their remote tokens.
	app.Version = 1
	app.Status = "disabled"
	app.Secret = ""
	if w := hostedRequest(restarted, "/v1/deployment/gitlab-applications", map[string]any{"application": app, "version": 1}, nil, admin); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	m := waitSpaceState(t, restarted, extra, "ready").manager
	_, account := savedServiceGrant(t, &gitlabLoginHarness{h: &gitlabSetupHarness{m: m}})
	// Source permission is checked before token use, even with a stored active grant.
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := m.acquireGitLabToken(ctx, RoomIdentity{}, account, time.Second); err == nil {
		t.Fatal("disabled service app admitted token")
	}
}

func TestHostedGitLabUserRevokedDuringExchangeCannotCommitGrant(t *testing.T) {
	s := hostedGitLabServer(t, t.TempDir())
	app := gitlaboauth.Application{OAuthApplication: registry.OAuthApplication{ID: strings.Repeat("e", 32), Source: gitlaboauth.ServiceSource, Origin: "https://gitlab.example", OwnerID: 999, ClientID: "managed-client", RedirectURI: "https://teamx.example" + gitlabCallbackPath, Scopes: []string{"api"}}, Name: "Company", Secret: "service-secret", Status: "active"}
	if _, err := s.gitlabApplications.Update(t.Context(), app, 0); err != nil {
		t.Fatal(err)
	}
	cookie, user, space := hostedLogin(t, s, "alice")
	m := hostedGitLabReady(t, s, space, cookie)
	entered, release := make(chan struct{}), make(chan struct{})
	base := hostedGitLabTransport(201)
	m.mu.Lock()
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/oauth/token" {
			close(entered)
			<-release
		}
		return base.RoundTrip(r)
	})
	m.mu.Unlock()
	req := gitlabAuthRequest{ServiceEpoch: m.epoch, Caller: Caller{Kind: "human", ID: user.UserID}, ApplicationID: app.ID, Origin: app.Origin, Name: "GitLab", Scopes: []string{"api"}}
	w := hostedRequest(s, spaceRoute(space)+gitlabCookiePath+"login", req, cookie, "")
	var auth gitlabAuthView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &auth) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	callback := make(chan *httptest.ResponseRecorder, 1)
	go func() { callback <- hostedGitLabCallback(s, auth, cookie, w.Result().Cookies()[0]) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("exchange not admitted")
	}
	// Simulate interruption after the durable user fence, before runtime closure.
	if _, err := s.access.users.setStatus(user.UserID, "disabled"); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case <-callback:
	case <-time.After(time.Second):
		t.Fatal("exchange did not settle")
	}
	file := mustSnapshot(t, m)
	if len(file.OAuthCredentials) != 0 {
		t.Fatal("revoked user activated a late grant")
	}
	m.mu.Lock()
	status := m.gitlabAuthSessions[auth.SessionID].Status
	m.mu.Unlock()
	if status != "failed" {
		t.Fatal("revocation result not reflected", status)
	}
}
