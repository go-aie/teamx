package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlaboauth"
	"github.com/go-aie/teamx/internal/platform/registry"
)

type serviceGitLabFixture struct {
	catalog *gitlaboauth.Catalog
	app     gitlaboauth.Application
	users   []*gitlabLoginHarness
	saved   gitlaboauth.Snapshot
}

func managedGitLabFixture(t *testing.T, scopes ...[]string) *serviceGitLabFixture {
	t.Helper()
	f := &serviceGitLabFixture{saved: gitlaboauth.Snapshot{Version: 1, Applications: []gitlaboauth.Application{}}}
	for range 2 {
		f.users = append(f.users, newGitlabLoginHarness(t))
	}
	a := registry.OAuthApplication{Source: gitlaboauth.ServiceSource, ID: strings.Repeat("b", 32), OwnerID: 999, Origin: f.users[0].h.req.Origin, ClientID: "private-client-id", RedirectURI: f.users[0].h.m.config.endpoint + gitlabCallbackPath, Scopes: []string{"api"}}
	if len(scopes) == 1 {
		a.Scopes = scopes[0]
	}
	// The pending connection stores its origin in the registry, not the request.
	file, err := f.users[0].h.m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	a.Origin = file.OAuthApplications[0].Origin
	f.catalog, err = gitlaboauth.Open(gitlaboauth.Store{Load: func() (gitlaboauth.Snapshot, error) { return f.saved, nil }, Save: func(s gitlaboauth.Snapshot) error { f.saved = s; return nil }, OtherGrant: func(app string, user int64, _ string) (bool, error) {
		for _, g := range f.users {
			data, err := os.ReadFile(g.h.m.platformsPathForTest())
			if err != nil {
				return true, err
			}
			var snapshot registry.Snapshot
			if json.Unmarshal(data, &snapshot) != nil {
				return true, ErrInvalid
			}
			for _, c := range snapshot.OAuthCredentials {
				if c.ApplicationID == app && c.UserID == user && c.State != "logged_out" {
					return true, nil
				}
			}
		}
		return false, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.app, err = f.catalog.Update(t.Context(), gitlaboauth.Application{OAuthApplication: a, Name: "Company", Status: "active", Secret: "private-client-secret"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, g := range f.users {
		m := g.h.m
		file, err := m.platforms.Load()
		if err != nil {
			t.Fatal(err)
		}
		file.OAuthApplications = nil
		file.Platforms[0].URL = a.Origin
		if err := m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
			t.Fatal(err)
		}
		user := []string{"alice", "bob"}[i]
		space := strings.Repeat([]string{"a", "c"}[i], 32)
		g.req.Caller = Caller{Kind: "human", ID: user}
		g.req.ApplicationID = a.ID
		g.req.Scopes = []string{"api"}
		m.config.gitlabApplications = f.catalog.ForSpace(user, space)
		m.platforms.SetOAuthOwner(user, space)
		g.user.Store(int64(201 + i))
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := f.catalog.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return f
}
func serviceGitLabStatus(t *testing.T, g *gitlabLoginHarness, v gitlabAuthView, cookie *http.Cookie) gitlabAuthView {
	t.Helper()
	w := gitlabAuthHTTP(g.h.m, "status", gitlabAuthRequest{ServiceEpoch: g.h.m.epoch, Caller: g.req.Caller, SessionID: v.SessionID}, cookie)
	var out gitlabAuthView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return out
}
func savedServiceGrant(t *testing.T, g *gitlabLoginHarness) (registry.Snapshot, registry.Account) {
	t.Helper()
	m := g.h.m
	m.mu.Lock()
	defer m.mu.Unlock()
	file, err := m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	account, err := registry.ResolveAuthorization(file, file.Platforms[0])
	if err != nil {
		t.Fatal(err)
	}
	return file, account
}
func TestServiceGitLabTwoNonOwnersConcurrentAuthorizationAndPrivateGrants(t *testing.T) {
	f := managedGitLabFixture(t)
	a, b := f.users[0], f.users[1]
	av, ac := a.login(t)
	bv, bc := b.login(t)
	if a.callback(av, bc, nil).Code != 403 || b.callback(av, ac, nil).Code != 403 {
		t.Fatal("cross browser or Space callback accepted")
	}
	var wg sync.WaitGroup
	for i, g := range f.users {
		wg.Go(func() {
			v, c := []gitlabAuthView{av, bv}[i], []*http.Cookie{ac, bc}[i]
			if w := g.callback(v, c, nil); w.Code != 303 {
				t.Error(w.Code, w.Body.String())
			}
		})
	}
	wg.Wait()
	for i, g := range f.users {
		v, c := []gitlabAuthView{av, bv}[i], []*http.Cookie{ac, bc}[i]
		if result := serviceGitLabStatus(t, g, v, c); result.Status != "succeeded" {
			t.Fatal(result.Status, result.ErrorCode)
		}
		file, _ := savedServiceGrant(t, g)
		grant := file.OAuthCredentials[0]
		if grant.UserID != int64(201+i) || grant.OwnerUserID != g.req.Caller.ID || grant.SpaceID == "" || grant.OperationID != v.SessionID {
			t.Fatal("grant owner lost")
		}
		data, _ := os.ReadFile(g.h.m.platformsPathForTest())
		if strings.Contains(string(data), "private-client-secret") || file.OAuthApplications[0].ClientSecret != "" {
			t.Fatal("service secret copied to Space")
		}
		if g.callback(v, c, nil).Code == 303 {
			t.Fatal("callback replay")
		}
	}
	// Disconnecting Alice never changes Bob's grant or the service application.
	before, _ := json.Marshal(mustSnapshot(t, b.h.m))
	file, _ := savedServiceGrant(t, a)
	req := a.req
	req.Version = file.Platforms[0].Version
	req.Affected = []gitlabAuthPlatform{{ID: req.ID, Version: req.Version}}
	w := gitlabAuthHTTP(a.h.m, "logout", req, nil)
	var logout gitlabAuthView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &logout) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	a.h.m.mu.Lock()
	done := a.h.m.gitlabAuthSessions[logout.SessionID].done
	a.h.m.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("logout did not settle")
	}
	after, _ := json.Marshal(mustSnapshot(t, b.h.m))
	if string(before) != string(after) || len(f.catalog.Public()) != 1 {
		t.Fatal("logout modified another owner")
	}
	if a.revokes.Load() != 1 {
		t.Fatal("independent user's remote revocation missing", a.revokes.Load())
	}
}
func mustSnapshot(t *testing.T, m *roomManager) registry.Snapshot {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	f, e := m.platforms.Load()
	if e != nil {
		t.Fatal(e)
	}
	return f
}

func TestServiceGitLabRefreshAndRotationFenceLateResult(t *testing.T) {
	f := managedGitLabFixture(t)
	g := f.users[0]
	v, c := g.login(t)
	g.callback(v, c, nil)
	_, account := savedServiceGrant(t, g)
	entered, release, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	g.h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Path {
		case "/oauth/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_secret") != "private-client-secret" {
				t.Error("refresh bypassed service application")
			}
			close(entered)
			<-r.Context().Done()
			close(canceled)
			<-release
			body = `{"access_token":"new-access","refresh_token":"new-refresh","token_type":"bearer","expires_in":600}`
		case "/oauth/token/info":
			body = `{"resource_owner_id":201,"scope":["api"],"application":{"uid":"private-client-id"}}`
		case "/api/v4/user":
			body = `{"id":201,"username":"tester"}`
		default:
			return nil, errors.New("unexpected request")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	result := make(chan error, 1)
	go func() { result <- g.h.m.refreshGitLabToken(t.Context(), account, nil) }()
	<-entered
	rotated := make(chan error, 1)
	go func() {
		app := f.app
		app.Secret = "new-control-secret"
		_, err := f.catalog.Update(t.Context(), app, app.Version)
		rotated <- err
	}()
	// Application change must cancel and retain the in-flight operation until it settles.
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("rotation did not cancel refresh")
	}
	select {
	case <-rotated:
		t.Fatal("rotation returned before refresh settled")
	default:
	}
	close(release)
	if err := <-result; err == nil {
		t.Fatal("old refresh committed after rotation")
	}
	if err := <-rotated; err != nil {
		t.Fatal(err)
	}
	file := mustSnapshot(t, g.h.m)
	if file.OAuthCredentials[0].State == "active" {
		t.Fatal("stale refresh became usable")
	}
	// Crash/restart reads the same unusable evidence; it cannot replay a refresh.
	if file.OAuthCredentials[0].RefreshAttemptID == "" {
		t.Fatal("lost destructive refresh receipt")
	}
}
func TestServiceGitLabApplicationManagementAndUnsupportedScopeRejected(t *testing.T) {
	f := managedGitLabFixture(t)
	g := f.users[0]
	m := g.h.m
	req := g.h.req
	req.Caller = g.req.Caller
	req.Client = &gitlabClientImport{ClientID: "injected", ClientSecret: "injected", RedirectURI: m.config.endpoint + gitlabCallbackPath, Scopes: []string{"api"}}
	if w := managerHTTP(m, gitlabCookiePath+"initialize", req); w.Code == 200 {
		t.Fatal("user changed service application")
	}
	bad := g.req
	bad.Scopes = []string{"api", "write_repository"}
	if w := gitlabAuthHTTP(m, "login", bad, nil); w.Code == 200 {
		t.Fatal("unsupported repository scope granted")
	}
	bad = g.req
	bad.ApplicationID = strings.Repeat("f", 32)
	if w := gitlabAuthHTTP(m, "login", bad, nil); w.Code == 200 {
		t.Fatal("foreign app accepted")
	}
}

func TestServiceGitLabRefreshKeepsOtherGrantAndRotationJoinsAPICall(t *testing.T) {
	f := managedGitLabFixture(t)
	for _, g := range f.users {
		v, c := g.login(t)
		g.callback(v, c, nil)
		if serviceGitLabStatus(t, g, v, c).Status != "succeeded" {
			t.Fatal("login failed")
		}
	}
	g := f.users[0]
	before, _ := json.Marshal(mustSnapshot(t, f.users[1].h.m))
	_, account := savedServiceGrant(t, g)
	original := g.h.m.platformHTTP
	g.h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/oauth/token" {
			return original.RoundTrip(r)
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_secret") != "private-client-secret" {
			t.Error("wrong refresh source")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"access_token":"fresh-access","refresh_token":"fresh-refresh","token_type":"bearer","expires_in":600}`)), Request: r}, nil
	})
	if err := g.h.m.refreshGitLabToken(t.Context(), account, nil); err != nil {
		t.Fatal(err)
	}
	file, account := savedServiceGrant(t, g)
	if file.OAuthCredentials[0].TokenRevision != 2 || file.OAuthCredentials[0].RefreshToken != "fresh-refresh" {
		t.Fatal("refresh not committed")
	}
	after, _ := json.Marshal(mustSnapshot(t, f.users[1].h.m))
	if string(before) != string(after) {
		t.Fatal("refresh changed another user")
	}
	lease, err := g.h.m.acquireGitLabToken(t.Context(), RoomIdentity{}, account, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		app := f.app
		app.Secret = "rotation-two"
		_, err := f.catalog.Update(t.Context(), app, app.Version)
		done <- err
	}()
	select {
	case <-lease.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("in-flight API not canceled")
	}
	select {
	case <-done:
		t.Fatal("returned before API response settled")
	default:
	}
	if err := g.h.m.finishGitLabToken(lease); err == nil {
		t.Fatal("old API result admitted")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	file = mustSnapshot(t, g.h.m)
	if file.OAuthCredentials[0].State != "active" || file.OAuthCredentials[0].RefreshToken != "fresh-refresh" {
		t.Fatal("rotation rewrote grant")
	}
}
func TestServiceGitLabCopiedSpaceGrantAndDisabledCompositionRejected(t *testing.T) {
	f := managedGitLabFixture(t)
	g := f.users[0]
	v, c := g.login(t)
	g.callback(v, c, nil)
	file := mustSnapshot(t, g.h.m)
	other := f.users[1].h.m
	// A misplaced snapshot cannot be adopted simply because its application is shared.
	if err := other.store.save(other.platformsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := other.platforms.Load(); err == nil {
		t.Fatal("copied grant adopted by another Space")
	}
	m := testManager(t, t.TempDir(), nil)
	m.config.endpoint = "http://127.0.0.1:8088"
	m.config.gitlabApplications = gitlaboauth.Disabled{}
	if w := managerHTTP(m, gitlabCookiePath+"prepare", gitlabSetupRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Origin: "https://gitlab.example"}); w.Code == 200 {
		t.Fatal("unselected GitLab entry open")
	}
	req := platformTestRequest(m)
	if _, err := m.updatePlatform(req, false); err == nil {
		t.Fatal("private application fallback admitted")
	}
}

func TestServiceGitLabUserScopeChangeDoesNotReplaceOtherGrant(t *testing.T) {
	f := managedGitLabFixture(t, []string{"api", "write_repository"})
	for _, g := range f.users {
		v, c := g.login(t)
		g.callback(v, c, nil)
		if serviceGitLabStatus(t, g, v, c).Status != "succeeded" {
			t.Fatal("initial login failed")
		}
	}
	before, _ := json.Marshal(mustSnapshot(t, f.users[1].h.m))
	g := f.users[0]
	file := mustSnapshot(t, g.h.m)
	g.req.Version = file.Platforms[0].Version
	g.req.Affected = []gitlabAuthPlatform{{ID: g.req.ID, Version: g.req.Version}}
	g.req.Scopes = []string{"api", "write_repository"}
	original := g.h.m.platformHTTP
	g.h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/oauth/token/info" {
			return original.RoundTrip(r)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"resource_owner_id":201,"scope":["api","write_repository"],"application":{"uid":"private-client-id"}}`)), Request: r}, nil
	})
	v, c := g.login(t)
	g.callback(v, c, nil)
	if status := serviceGitLabStatus(t, g, v, c); status.Status != "succeeded" {
		t.Fatal(status.Status, status.ErrorCode)
	}
	file = mustSnapshot(t, g.h.m)
	if len(file.OAuthCredentials[0].Scopes) != 2 || file.OAuthCredentials[0].GrantGeneration != 2 {
		t.Fatal("user scope change not committed")
	}
	after, _ := json.Marshal(mustSnapshot(t, f.users[1].h.m))
	if string(before) != string(after) {
		t.Fatal("scope change replaced another user's grant")
	}
}

func TestServiceGitLabSharedNativeAccountDisconnectKeepsOtherSpaceGrant(t *testing.T) {
	f := managedGitLabFixture(t)
	f.users[1].user.Store(201)
	for _, g := range f.users {
		v, c := g.login(t)
		g.callback(v, c, nil)
		if serviceGitLabStatus(t, g, v, c).Status != "succeeded" {
			t.Fatal("login failed")
		}
	}
	g := f.users[0]
	file := mustSnapshot(t, g.h.m)
	req := g.req
	req.Version = file.Platforms[0].Version
	req.Affected = []gitlabAuthPlatform{{ID: req.ID, Version: req.Version}}
	w := gitlabAuthHTTP(g.h.m, "logout", req, nil)
	var auth gitlabAuthView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &auth) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	g.h.m.mu.Lock()
	done := g.h.m.gitlabAuthSessions[auth.SessionID].done
	g.h.m.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("logout not settled")
	}
	if g.revokes.Load() != 0 {
		t.Fatal("remote revoke endangered shared native grant")
	}
	file = mustSnapshot(t, g.h.m)
	if file.OAuthCredentials[0].State != "logged_out" || len(file.OAuthCleanup) != 1 || file.OAuthCleanup[0].State == "revoked" {
		t.Fatal("uncertain remote revocation falsely confirmed")
	}
	if mustSnapshot(t, f.users[1].h.m).OAuthCredentials[0].State != "active" {
		t.Fatal("sharing Space disconnected")
	}
}
func TestServiceGitLabDisableCancelsWaitingAuthorizationBeforeCallback(t *testing.T) {
	f := managedGitLabFixture(t)
	g := f.users[0]
	v, c := g.login(t)
	app := f.app
	app.Status = "disabled"
	if _, err := f.catalog.Update(t.Context(), app, app.Version); err != nil {
		t.Fatal(err)
	}
	if w := g.callback(v, c, nil); w.Code == 303 || g.posts.Load() != 0 {
		t.Fatal("disabled application exchanged an old code")
	}
	if serviceGitLabStatus(t, g, v, c).Status != "cancelled" {
		t.Fatal("waiting operation not settled")
	}
}
