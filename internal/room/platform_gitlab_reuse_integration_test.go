//go:build integration || e2e

package room

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestGitLabReuseCompatibleApplicationSelection(t *testing.T) {
	parallelFixtureTest(t)
	g := newGitlabLoginHarness(t)
	m := g.h.m
	req := gitlabSetupRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Origin: g.h.server.URL}
	view := gitlabSetupHTTP(t, g.h, "prepare", req)
	if view.Status != "ready" || view.ApplicationID != g.req.ApplicationID || len(view.Affected) != 0 {
		t.Fatal("existing app was not reused independently of owner")
	}
	m.mu.Lock()
	f, _ := m.platforms.Load()
	app := f.OAuthApplications[0]
	app.ID, app.ClientID = strings.Repeat("c", 32), "second-client"
	f.OAuthApplications = append(f.OAuthApplications, app)
	err := m.store.save(m.platformsPathForTest(), f, 2<<20)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	view = gitlabSetupHTTP(t, g.h, "prepare", req)
	if view.Status != "application_selection_required" || view.ApplicationID != "" || len(view.Applications) != 2 {
		t.Fatal("ambiguous selection picked first")
	}
	req.ApplicationID = app.ID
	view = gitlabSetupHTTP(t, g.h, "prepare", req)
	if view.Status != "ready" || view.ApplicationID != app.ID {
		t.Fatal("explicit selection ignored")
	}
	req.Scopes = []string{"api", "write_repository"}
	req.ApplicationID = ""
	view = gitlabSetupHTTP(t, g.h, "prepare", req)
	if view.Status != "application_incompatible" || len(view.Applications[0].MissingScopes) != 1 {
		t.Fatal("scope deficit hidden")
	}
	req.Scopes = nil
	m.mu.Lock()
	f, _ = m.platforms.Load()
	for i := range f.OAuthApplications {
		f.OAuthApplications[i].RedirectURI = "http://127.0.0.1:9999/v1/platforms/gitlab/callback"
	}
	err = m.store.save(m.platformsPathForTest(), f, 2<<20)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	view = gitlabSetupHTTP(t, g.h, "prepare", req)
	if view.Status != "application_incompatible" || view.Applications[0].CallbackCompatible {
		t.Fatal("wrong callback reused")
	}
}
func TestGitLabReuseTwoUsersLoginReauthorizationAndLogoutIsolation(t *testing.T) {
	parallelFixtureTest(t)
	g := newGitlabLoginHarness(t)
	m := g.h.m
	v, cookie := g.login(t)
	g.callback(v, cookie, nil)
	before, err := m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	first := before.Platforms[0]
	// Normal reuse does not require an application-owner login or a second import.
	g.req = gitlabAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Name: "Second account", Origin: first.URL, ApplicationID: before.OAuthApplications[0].ID}
	g.user.Store(202)
	v, cookie = g.login(t)
	g.callback(v, cookie, nil)
	status := g.status(t, v, cookie)
	after, err := m.platforms.Load()
	if err != nil || status.Status != "succeeded" || len(after.OAuthApplications) != 1 || len(after.OAuthCredentials) != 2 || !reflect.DeepEqual(after.Platforms[0], first) || !reflect.DeepEqual(after.OAuthCredentials[0], before.OAuthCredentials[0]) || after.OAuthApplications[0].OwnerID != 101 {
		t.Fatal("second login changed first account", status.Status, status.ErrorCode, err)
	}
	second := after.Platforms[1]
	secondGrant := after.OAuthCredentials[1]
	req := gitlabSetupRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: second.ID, Version: second.Version}
	impact := gitlabSetupHTTP(t, g.h, "prepare", req)
	if len(impact.Affected) != 1 || impact.Affected[0].ID != second.ID {
		t.Fatal("reauthorization affected application owner")
	}
	g.req = gitlabAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: second.ID, Version: second.Version, ApplicationID: after.OAuthApplications[0].ID, Affected: impact.Affected}
	g.user.Store(101)
	v, cookie = g.login(t)
	g.callback(v, cookie, nil)
	if s := g.status(t, v, cookie); s.Status != "failed" {
		t.Fatal("reauthorization changed account", s.Status)
	}
	unchanged, _ := m.platforms.Load()
	if !reflect.DeepEqual(unchanged.OAuthCredentials[1], secondGrant) || g.revokes.Load() != 0 {
		t.Fatal("mismatch adopted or remotely revoked another grant")
	}
	g.user.Store(202)
	v, cookie = g.login(t)
	g.callback(v, cookie, nil)
	if s := g.status(t, v, cookie); s.Status != "succeeded" {
		t.Fatal("non-owner reauthorization failed", s.ErrorCode)
	}
	current, _ := m.platforms.Load()
	second = current.Platforms[1]
	if !reflect.DeepEqual(current.Platforms[0], first) || !reflect.DeepEqual(current.OAuthCredentials[0], before.OAuthCredentials[0]) {
		t.Fatal("reauthorization modified owner")
	}
	g.req.Version = second.Version
	g.req.Affected[0].Version = second.Version
	w := gitlabAuthHTTP(m, "logout", g.req, nil)
	var out gitlabAuthView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	m.mu.Lock()
	session := m.gitlabAuthSessions[out.SessionID]
	m.mu.Unlock()
	select {
	case <-session.done:
	case <-t.Context().Done():
		t.Fatal("logout did not finish")
	}
	current, err = m.platforms.Load()
	if err != nil || current.OAuthCredentials[1].State != "logged_out" || !reflect.DeepEqual(current.Platforms[0], first) || !reflect.DeepEqual(current.OAuthCredentials[0], before.OAuthCredentials[0]) || g.revokes.Load() != 0 {
		t.Fatal("logout crossed accounts", err)
	}
}
func TestGitLabSharedApplicationReplacementFencesAndDisconnectsAllAccounts(t *testing.T) {
	parallelFixtureTest(t)
	for _, targetIndex := range []int{0, 1} {
		t.Run(fmt.Sprint(targetIndex), func(t *testing.T) {
			g := newGitlabLoginHarness(t)
			m := g.h.m
			v, cookie := g.login(t)
			g.callback(v, cookie, nil)
			before, _ := m.platforms.Load()
			first := before.Platforms[0]
			g.req = gitlabAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Name: "Second", Origin: first.URL, ApplicationID: before.OAuthApplications[0].ID}
			g.user.Store(202)
			v, cookie = g.login(t)
			g.callback(v, cookie, nil)
			before, _ = m.platforms.Load()
			app := before.OAuthApplications[0]
			first = before.Platforms[targetIndex]
			setup := gitlabSetupRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: first.ID, Version: first.Version, Client: &gitlabClientImport{ClientID: app.ClientID, ClientSecret: "rotated-secret", RedirectURI: app.RedirectURI, Scopes: app.Scopes}}
			initialized := gitlabSetupHTTP(t, g.h, "initialize", setup)
			if len(initialized.Affected) != 2 {
				t.Fatal("application edit hid second account")
			}
			g.req = gitlabAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: first.ID, Version: first.Version, ClientImportID: initialized.ClientImportID, Affected: initialized.Affected}
			partial := g.req
			partial.Affected = partial.Affected[:1]
			if w := gitlabAuthHTTP(m, "login", partial, nil); w.Code != 409 {
				t.Fatal("partial application impact accepted", w.Code)
			}
			m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
				body := `{}`
				switch r.URL.Path {
				case "/oauth/token":
					_ = r.ParseForm()
					if r.Form.Get("client_secret") != "rotated-secret" {
						t.Error("wrong app secret")
					}
					body = `{"access_token":"rotated-access","refresh_token":"rotated-refresh","token_type":"bearer","expires_in":600}`
				case "/oauth/token/info":
					body = fmt.Sprintf(`{"resource_owner_id":101,"scope":["api"],"application":{"uid":%q}}`, app.ClientID)
				case "/api/v4/user":
					body = `{"id":101,"username":"owner"}`
				case "/api/v4/user/applications":
					body = fmt.Sprintf(`[{"id":55,"application_name":"TeamX","application_id":%q,"callback_url":%q,"confidential":true}]`, app.ClientID, app.RedirectURI)
				default:
					t.Error("unexpected application management call", r.URL.Path)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			v, cookie = g.login(t)
			g.callback(v, cookie, nil)
			s := g.status(t, v, cookie)
			after, err := m.platforms.Load()
			if err != nil || s.Status != "succeeded" || len(s.Platforms) != 2 || after.OAuthApplications[0].ClientSecret != "rotated-secret" || after.OAuthCredentials[1].State != "logged_out" || after.Platforms[1].CredentialID != before.Platforms[1].CredentialID || after.Platforms[1].Generation != before.Platforms[1].Generation || after.Platforms[1].CredentialConfigured {
				t.Fatal("application replacement did not preserve and isolate accounts", s.Status, s.ErrorCode, err)
			}
			if after.OAuthCredentials[1].UserID != 202 || after.Platforms[1].Validation.UserID != 202 {
				t.Fatal("owner's grant replaced second user")
			}
		})
	}
}
func TestGitLabOnboardingFreezesTargetBeforeLoginAndAppliesAfterCallback(t *testing.T) {
	parallelFixtureTest(t)
	g := newGitlabLoginHarness(t)
	m := g.h.m
	// Use a real Room with a fixed member, and an as-yet uncreated GitLab platform.
	m2, room, _, _, members := cliToolFixture(t, "exit 0")
	// The Room service and platform authentication share the same manager.
	source, _ := g.h.m.platforms.Load()
	g.h.m = m2
	m = m2
	m.config.endpoint = "http://127.0.0.1:8088"
	old, _ := g.h.m.platforms.Load()
	source.OAuthApplications[0].Origin = g.h.server.URL
	source.OAuthApplications[0].ID, _ = newOpaqueID()
	old.OAuthApplications = append(old.OAuthApplications, source.OAuthApplications[0])
	m.mu.Lock()
	err := m.store.save(m.platformsPathForTest(), old, 2<<20)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// Install a transport whose identity belongs to the second account.
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		body := `{}`
		switch r.URL.Path {
		case "/oauth/token":
			body = `{"access_token":"new-access","refresh_token":"new-refresh","token_type":"bearer","expires_in":600}`
		case "/oauth/token/info":
			body = `{"resource_owner_id":202,"scope":["api"],"application":{"uid":"private-client-id"}}`
		case "/api/v4/user":
			body = `{"id":202,"username":"second"}`
		default:
			t.Error("unexpected endpoint", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	req := onboardingRequest(t, m, room, members[0], managedPlatform{})
	req.PlatformID, req.Provider, req.Origin = "", "gitlab", g.h.server.URL
	op := onboardingHTTP(t, m, "prepare", req)
	g.req = gitlabAuthRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Name: "second account", Origin: req.Origin, ApplicationID: source.OAuthApplications[0].ID, OnboardingID: op.ID}
	v, cookie := g.login(t)
	if v.PlatformID != op.PlatformID {
		t.Fatal("login changed reserved platform")
	}
	g.callback(v, cookie, nil)
	req.platformOnboardingSpec = platformOnboardingSpec{}
	done := onboardingHTTP(t, m, "status", req)
	if done.Connection.Status != "connected" || done.Notifications.Status != "applied" || done.Tool.Status != "applied" {
		t.Fatal(done.Connection, done.Notifications, done.Tool)
	}
}

func TestGitLabCandidateIdentityMismatchCleanupReceipts(t *testing.T) {
	parallelFixtureTest(t)
	for _, remoteStatus := range []int{200, 500} {
		t.Run(fmt.Sprint(remoteStatus), func(t *testing.T) {
			g := newGitlabLoginHarness(t)
			g.revokeStatus.Store(int32(remoteStatus))
			transport := g.h.m.platformHTTP
			g.h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path == "/api/v4/user" {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":202,"username":"inconsistent-user"}`)), Request: r}, nil
				}
				return transport.RoundTrip(r)
			})
			v, cookie := g.login(t)
			g.callback(v, cookie, nil)
			status := g.status(t, v, cookie)
			if status.Status != "failed" || g.revokes.Load() != 1 {
				t.Fatal("candidate identity mismatch was adopted or not cleaned", status.Status, status.Cleanup)
			}
			snapshot, err := g.h.m.platforms.Load()
			if err != nil || len(snapshot.OAuthCredentials) != 0 || len(snapshot.OAuthCleanup) != 1 {
				t.Fatal("cleanup evidence lost", err)
			}
			want := "revoked"
			if remoteStatus == 500 {
				want = "unknown"
			}
			if snapshot.OAuthCleanup[0].State != want {
				t.Fatal("unconfirmed cleanup reported confirmed", snapshot.OAuthCleanup[0].State)
			}
		})
	}
}
