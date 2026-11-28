package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/authn"
)

type hostedProviderFixture struct{}

func TestHostedDisplayNamesUseAuthenticatedUserAndPreserveIdentity(t *testing.T) {
	t.Setenv("USER", "teamx-service-account")
	s := hostedFixture(t, t.TempDir())
	for _, account := range []string{"alice", "bob"} {
		cookie, subject, space := hostedLogin(t, s, account)
		original, ok := s.access.users.user(subject.UserID)
		if !ok {
			t.Fatal("missing admitted user")
		}
		access := waitSpaceState(t, s, space, "setup_required").config.access
		svc, _ := testService(t)
		manager := testManager(t, t.TempDir(), nil)
		for _, displayName := range []string{"opencsg-" + account, "renamed-" + account, ""} {
			identity := original.Identity
			identity.DisplayName = displayName
			updated, err := s.access.users.reserve(identity, s.access.config.Deployment.Authentication)
			if err != nil || updated.User != original.User || updated.DefaultSpace != original.DefaultSpace {
				t.Fatal("display name update changed identity or Space", updated, err)
			}
			for name, next := range map[string]http.Handler{"room": svc, "managed": manager} {
				r := httptest.NewRequest(http.MethodGet, "https://teamx.example/v1/ui", nil)
				r.RemoteAddr = "127.0.0.1:1234"
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				access.wrap(next).ServeHTTP(w, r)
				var bootstrap map[string]any
				if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &bootstrap) != nil || bootstrap["operator_display_name"] != displayName {
					t.Fatalf("%s/%s display name: %d %s", account, name, w.Code, w.Body.String())
				}
				if bootstrap["space_id"] != space || name == "room" && bootstrap["operator"] != subject.UserID {
					t.Fatal("display name changed request identity", bootstrap)
				}
			}
			var saved hostedUserSnapshot
			if err := readIdentitySnapshot(filepath.Join(s.access.users.home, "users.json"), &saved); err != nil {
				t.Fatal(err)
			}
			for _, user := range saved.Users {
				if user.ID == subject.UserID && user.Identity.DisplayName != displayName {
					t.Fatal("display name update was not persisted")
				}
			}
		}
	}
}

func TestHostedOpenAdmissionKeepsOwnerIsolationAndDisable(t *testing.T) {
	s := hostedFixtureWithAdmission(t, t.TempDir(), externalAuthConfig{Provider: "fixture", AllowAllAuthenticated: true})
	a, alice, aSpace := hostedLogin(t, s, "previously-unlisted-a")
	b, bob, bSpace := hostedLogin(t, s, "previously-unlisted-b")
	if alice.UserID == bob.UserID || aSpace == bSpace {
		t.Fatal("open admission merged external identities")
	}
	for _, pair := range []struct {
		cookie  *http.Cookie
		foreign string
	}{{a, bSpace}, {b, aSpace}} {
		if w := hostedRequest(s, "/s/"+pair.foreign+"/v1/setup", nil, pair.cookie, ""); w.Code != 403 {
			t.Fatal("open admission exposed another owner's space", w.Code)
		}
	}
	for _, id := range []authn.Identity{
		{Provider: "test", Authority: "https://other.example", Subject: "another"},
		{Provider: "other", Authority: "https://hub.example", Subject: "another"},
		{Provider: "test", Authority: "https://hub.example"},
	} {
		if err := s.admitExternal(t.Context(), httptest.NewRecorder(), id); err == nil {
			t.Fatal("open admission accepted invalid or different-source identity")
		}
	}
	if _, err := s.access.users.setStatus(alice.UserID, "disabled"); err != nil {
		t.Fatal(err)
	}
	if w := hostedRequest(s, "/v1/access/session", nil, a, ""); w.Code != 401 && w.Code != 403 {
		t.Fatal("disabled open-registration user retained access", w.Code)
	}
	if err := s.admitExternal(t.Context(), httptest.NewRecorder(), authn.Identity{Provider: "test", Authority: "https://hub.example", Subject: "previously-unlisted-a"}); err == nil {
		t.Fatal("open admission reenabled a disabled user")
	}
	if w := hostedRequest(s, "/v1/access/session", nil, b, ""); w.Code != 200 {
		t.Fatal("other owner lost session", w.Code)
	}
}

func TestOpenCSGAdmissionMustBeExplicit(t *testing.T) {
	for _, open := range []bool{false, true} {
		_, err := (externalAuthConfig{Provider: "opencsg", Issuer: "https://iam.example", ClientID: "teamx-client", AllowAllAuthenticated: open}).provider()
		if (err == nil) != open {
			t.Fatal("unexpected empty-list admission", open, err)
		}
	}
}

func (hostedProviderFixture) LoginURL(callback string) (string, error) {
	return fixtureLoginURL(callback)
}
func (hostedProviderFixture) Complete(_ context.Context, values url.Values) (authn.Identity, error) {
	if values.Get("code") == "fail" {
		return authn.Identity{}, errors.New("offline")
	}
	return authn.Identity{Provider: "test", Authority: "https://hub.example", Subject: values.Get("code"), DisplayName: "same display name", ValidUntil: time.Now().Add(time.Hour)}, nil
}

func hostedFixture(t *testing.T, home string) *spaceServer {
	t.Helper()
	return hostedFixtureWithAdmission(t, home, externalAuthConfig{Provider: "fixture", AllowedSubjects: []string{"alice", "bob"}})
}

func hostedFixtureWithAdmission(t *testing.T, home string, admission externalAuthConfig) *spaceServer {
	return hostedFixtureWithProvider(t, home, admission, hostedProviderFixture{})
}

func hostedFixtureWithProvider(t *testing.T, home string, admission externalAuthConfig, provider authn.Provider) *spaceServer {
	t.Helper()
	c := freshAccessConfig(t, home)
	source := teamDefaultsPath(c.path)
	if _, err := os.Stat(source); os.IsNotExist(err) {
		if err := initializeTeamTemplates(resetSeedForTest(t), source); err != nil {
			t.Fatal(err)
		}
	}
	c.Deployment.Mode = "multi-user"
	c.Deployment.Authentication = admission
	c.Server.PublicBaseURL = "https://teamx.example"
	c.Runtime = testTeamDefinition(t).Runtime
	if err := prepareFirstAccess(c); err != nil {
		t.Fatal(err)
	}
	a, err := newServerAccess(c, "http://127.0.0.1:8088")
	if err != nil {
		t.Fatal(err)
	}
	factories := personalDeployment(controlledActivationFactory(nil, nil)).factories
	factories.authentication = func(externalAuthConfig) (authn.Provider, error) { return provider, nil }
	config := serviceConfig{teamsSource: source, access: a, settings: newSystemSettings(c), platformCatalog: defaultPlatformCatalog(), maxTurns: 7, modelProbe: successfulModelProbe, endpoint: a.internal}
	s, err := newHostedSpaceServer(t.Context(), c, config, factories, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.closeWithTimeout(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func hostedRequest(s *spaceServer, path string, body any, cookie *http.Cookie, token string) *httptest.ResponseRecorder {
	if setup, ok := body.(setupModelInput); ok && setup.RuntimeEpoch == "" {
		response := hostedRequest(s, strings.TrimSuffix(path, "/model"), nil, cookie, token)
		var state struct {
			RuntimeEpoch string `json:"runtime_epoch"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &state)
		setup.RuntimeEpoch = state.RuntimeEpoch
		body = setup
	}
	method := http.MethodGet
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
		method = http.MethodPost
	}
	r := httptest.NewRequest(method, "https://teamx.example"+path, reader)
	r.RemoteAddr = "127.0.0.1:1234"
	if body != nil && token == "" {
		r.Header.Set("Origin", "https://teamx.example")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func hostedLogin(t *testing.T, s *spaceServer, account string) (*http.Cookie, userSubject, string) {
	t.Helper()
	start := hostedRequest(s, externalLoginPath, struct{}{}, nil, "")
	if start.Code != 303 {
		t.Fatalf("start login %d %s", start.Code, start.Body.String())
	}
	jump, _ := url.Parse(start.Header().Get("Location"))
	callback, _ := url.Parse(fixtureCallbackURL(jump))
	values := callback.Query()
	values.Set("code", account)
	callback.RawQuery = values.Encode()
	w := hostedRequest(s, callback.RequestURI(), nil, start.Result().Cookies()[0], "")
	if w.Header().Get("Location") != "/" {
		t.Fatalf("callback failed: %d %s", w.Code, w.Header().Get("Location"))
	}
	var cookie *http.Cookie
	for _, item := range w.Result().Cookies() {
		if item.Name == s.access.cookieName() {
			cookie = item
		}
	}
	if cookie == nil {
		t.Fatal("missing local session")
	}
	session := hostedRequest(s, "/v1/access/session", nil, cookie, "")
	var subject struct {
		UserID  string `json:"user_id"`
		Version uint64 `json:"auth_version"`
		Space   string `json:"space_id"`
	}
	if session.Code != 200 || json.Unmarshal(session.Body.Bytes(), &subject) != nil || subject.UserID == "" {
		t.Fatalf("invalid local session %d %s", session.Code, session.Body.String())
	}
	return cookie, userSubject{subject.UserID, subject.Version}, subject.Space
}

func TestHostedUsersTwoOwnersTwoSpacesAndRevocation(t *testing.T) {
	s := hostedFixture(t, t.TempDir())
	if s.claims.sharedExternal {
		t.Fatal("hosted deployment enabled public directory sharing")
	}
	a, alice, aDefault := hostedLogin(t, s, "alice")
	b, bob, bDefault := hostedLogin(t, s, "bob")
	if alice.UserID == bob.UserID || aDefault == bDefault {
		t.Fatal("different identities merged")
	}
	var extra []string
	for i, cookie := range []*http.Cookie{a, b} {
		id, _ := newOpaqueID()
		extra = append(extra, id)
		w := hostedRequest(s, "/v1/spaces", spaceChangeInput{RequestID: id, Name: "extra"}, cookie, "")
		if w.Code != 202 {
			t.Fatalf("create %d %s", w.Code, w.Body.String())
		}
		w = hostedRequest(s, "/v1/spaces", nil, cookie, "")
		var view struct {
			Spaces []struct {
				SpaceRecord
				Capabilities map[string]bool `json:"capabilities"`
			} `json:"spaces"`
			Default string `json:"default_space_id"`
		}
		if json.Unmarshal(w.Body.Bytes(), &view) != nil || len(view.Spaces) != 2 {
			t.Fatalf("directory: %s", w.Body.String())
		}
		owner := []userSubject{alice, bob}[i]
		for _, record := range view.Spaces {
			if record.Capabilities["space_management"] {
				t.Fatal("hosted Space management must stay hidden")
			}
			if record.OwnerUserID != owner.UserID {
				t.Fatal("directory leaked another owner")
			}
		}
	}
	aRuntime := waitSpaceState(t, s, aDefault, "setup_required")
	_ = waitSpaceState(t, s, bDefault, "setup_required")
	for _, target := range []string{bDefault, extra[1]} {
		if w := hostedRequest(s, "/s/"+target+"/v1/setup", nil, a, ""); w.Code != 403 {
			t.Fatalf("cross-owner business %d", w.Code)
		}
		if w := hostedRequest(s, "/v1/spaces/"+target, spaceChangeInput{Name: "hijack", ExpectedVersion: 1}, a, ""); w.Code != 403 {
			t.Fatalf("cross-owner rename %d", w.Code)
		}
		if w := hostedRequest(s, "/v1/spaces/"+target+"/archive", spaceChangeInput{ExpectedVersion: 1}, a, ""); w.Code != 403 {
			t.Fatalf("cross-owner archive %d", w.Code)
		}
	}
	// Global administration never doubles as a business bearer credential.
	admin, err := readAdminToken(adminTokenPath(s.access.config))
	if err != nil {
		t.Fatal(err)
	}
	if w := hostedRequest(s, "/v1/spaces", nil, nil, admin); w.Code != 401 {
		t.Fatalf("admin business escalation %d", w.Code)
	}
	if w := hostedRequest(s, "/v1/deployment/users", nil, a, ""); w.Code != 403 {
		t.Fatal("browser gained deployment administration")
	}
	if w := hostedRequest(s, "/v1/access/settings", nil, a, ""); w.Code != 403 {
		t.Fatal("ordinary user gained deployment settings")
	}
	// A private execution binding and an existing browser stream share the same
	// durable user fence and are revoked before runtime settlement.
	execution, _, err := aRuntime.config.access.issueExecution(executionBinding{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "https://teamx.example/v1/updates", nil)
	r.AddCookie(a)
	r = r.WithContext(context.WithValue(r.Context(), accessContextKey{}, aRuntime.config.access))
	r, err = aRuntime.config.access.bindSpaceRequest(r, alice)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := aRuntime.config.access.watchUIUpdates(r)
	if err != nil {
		t.Fatal(err)
	}
	w := hostedRequest(s, "/v1/deployment/users", map[string]string{"user_id": alice.UserID, "status": "disabled"}, nil, admin)
	if w.Code != 200 {
		t.Fatalf("disable %d %s", w.Code, w.Body.String())
	}
	select {
	case <-lease.done:
	default:
		t.Fatal("old event stream retained")
	}
	if w := hostedRequest(s, "/v1/access/session", nil, a, ""); w.Code != 401 {
		t.Fatalf("old browser retained %d", w.Code)
	}
	if _, err := aRuntime.config.access.authorizeSpace(alice, aDefault); err == nil {
		t.Fatal("old execution subject retained")
	}
	aRuntime.config.access.mu.Lock()
	_, exists := aRuntime.config.access.executions[tokenDigest(execution)]
	aRuntime.config.access.mu.Unlock()
	if exists {
		t.Fatal("old execution credential retained")
	}
	if w := hostedRequest(s, "/v1/access/session", nil, b, ""); w.Code != 200 {
		t.Fatal("disable affected another owner")
	}
	s.mu.Lock()
	for _, record := range s.directory.Spaces {
		if record.OwnerUserID == alice.UserID && record.Status != "suspended" {
			t.Error("disable acknowledged before settlement")
		}
	}
	s.mu.Unlock()
}

func TestHostedLoginFormKeepsOriginAndCallbackOmitsReferrer(t *testing.T) {
	s := hostedFixture(t, t.TempDir())
	page := hostedRequest(s, "/access", nil, nil, "")
	if page.Code != http.StatusOK || page.Header().Get("Referrer-Policy") != "same-origin" || !strings.Contains(page.Body.String(), `id="sso-login-form"`) {
		t.Fatal("login document would strip form Origin", page.Code, page.Header())
	}
	callback := hostedRequest(s, externalCallbackPath+"?state=invalid", nil, nil, "")
	if callback.Code != http.StatusSeeOther || callback.Header().Get("Referrer-Policy") != "no-referrer" || callback.Header().Get("Location") != "/access?sso=failed" {
		t.Fatal("sensitive callback did not clear its URL without a referrer", callback.Code, callback.Header())
	}
}

func TestHostedRegistrationRecoveryStableIdentityAndSessions(t *testing.T) {
	home := t.TempDir()
	s := hostedFixture(t, home)
	cookie, subject, space := hostedLogin(t, s, "alice")
	_, again, same := hostedLogin(t, s, "alice")
	if again != subject || same != space {
		t.Fatal("repeat login changed identity")
	}
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	restored := hostedFixture(t, home)
	if w := hostedRequest(restored, "/v1/access/session", nil, cookie, ""); w.Code != 200 {
		t.Fatalf("restart lost session %d %s", w.Code, w.Body.String())
	}
	_, after, afterSpace := hostedLogin(t, restored, "alice")
	if after != subject || afterSpace != space {
		t.Fatal("restart reallocated identity")
	}
	// A reserved user survives interruption before publishing its default Space.
	entry, err := restored.access.users.reserve(authn.Identity{Provider: "test", Authority: "https://hub.example", Subject: "bob"}, externalAuthConfig{AllowedSubjects: []string{"bob"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	recovered := hostedFixture(t, home)
	_, bob, bSpace := hostedLogin(t, recovered, "bob")
	if bob.UserID != entry.ID || bSpace != entry.DefaultSpace.ID {
		t.Fatal("partial registration changed identity")
	}
}

func TestHostedConcurrentAdmissionAndDeniedSubject(t *testing.T) {
	s := hostedFixture(t, t.TempDir())
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			w := httptest.NewRecorder()
			if err := s.admitExternal(t.Context(), w, authn.Identity{Provider: "test", Authority: "https://hub.example", Subject: "alice"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	s.access.users.mu.Lock()
	count := len(s.access.users.saved.Users)
	s.access.users.mu.Unlock()
	if count != 1 {
		t.Fatal("concurrent callbacks created multiple users")
	}
	if err := s.admitExternal(t.Context(), httptest.NewRecorder(), authn.Identity{Provider: "test", Authority: "https://hub.example", Subject: "unlisted"}); err == nil {
		t.Fatal("unlisted subject admitted")
	}
	if err := s.admitExternal(t.Context(), httptest.NewRecorder(), authn.Identity{Provider: "test", Authority: "https://other.example", Subject: "alice"}); err == nil {
		t.Fatal("provider change silently rebound local identity")
	}
}

func TestHostedRejectsCrossModeBeforeRegistration(t *testing.T) {
	home := t.TempDir()
	personal := freshAccessConfig(t, home)
	if err := prepareFirstAccess(personal); err != nil {
		t.Fatal(err)
	}
	personal = freshAccessConfig(t, home)
	if _, err := newServerAccess(personal, "http://127.0.0.1:8088"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(home, "users.json"))
	personal.Deployment.Mode = "multi-user"
	if _, err := registerHostedUsers(personal, "new"); err == nil {
		t.Fatal("personal root adopted")
	}
	after, _ := os.ReadFile(filepath.Join(home, "users.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("foreign root changed")
	}
	s := hostedFixture(t, t.TempDir())
	other := s.access.config
	other.Deployment.Mode = "personal"
	if _, err := newServerAccess(other, "http://127.0.0.1:8088"); err == nil || !strings.Contains(err.Error(), "deployment mode") {
		t.Fatal("hosted root adopted", err)
	}
}

func TestPersonalStartupIgnoresExternalAuthentication(t *testing.T) {
	c, _ := testAccessConfig(t, t.TempDir())
	c.Deployment.Authentication = externalAuthConfig{Provider: "invalid", Issuer: "file:///untrusted", HubOrigin: "http://not-a-trusted-site"}
	if _, err := newServerAccess(c, "http://127.0.0.1:8088"); err != nil {
		t.Fatal("personal deployment read external authentication configuration", err)
	}
}

func TestHostedDisableInterruptedAfterDurableFence(t *testing.T) {
	home := t.TempDir()
	s := hostedFixture(t, home)
	old, subject, space := hostedLogin(t, s, "alice")
	if _, err := s.access.users.setStatus(subject.UserID, "disabled"); err != nil {
		t.Fatal(err)
	}
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	restarted := hostedFixture(t, home)
	if record := spaceRecordForTest(t, restarted, space); record.Status != "suspended" {
		t.Fatal("crash recovered disabled owner into active business")
	}
	if w := hostedRequest(restarted, "/v1/access/session", nil, old, ""); w.Code != 401 {
		t.Fatal("disabled cookie restored")
	}
	if _, err := restarted.access.users.setStatus(subject.UserID, "active"); err != nil {
		t.Fatal(err)
	}
	_, next, nextSpace := hostedLogin(t, restarted, "alice")
	if next.UserID != subject.UserID || next.AuthVersion != subject.AuthVersion+2 || nextSpace != space {
		t.Fatal("enable lost stable identity or authentication generation")
	}
	if w := hostedRequest(restarted, "/v1/access/session", nil, old, ""); w.Code != 401 {
		t.Fatal("enable resurrected old cookie")
	}
}

func TestHostedUnknownIdentityCommitFencesUntilRestart(t *testing.T) {
	home := t.TempDir()
	s := hostedFixture(t, home)
	base := s.access.users.writer.ops
	s.access.users.writer.ops.rename = func(from, to string) error {
		if err := base.rename(from, to); err != nil {
			return err
		}
		return errors.New("lost publication receipt")
	}
	err := s.admitExternal(t.Context(), httptest.NewRecorder(), authn.Identity{Provider: "test", Authority: "https://hub.example", Subject: "alice"})
	if !errors.Is(err, ErrCommitUnknown) {
		t.Fatal("missing unknown commit", err)
	}
	if err := s.admitExternal(t.Context(), httptest.NewRecorder(), authn.Identity{Provider: "test", Authority: "https://hub.example", Subject: "alice"}); err == nil {
		t.Fatal("uncertain identity granted a session")
	}
	var published hostedUserSnapshot
	if err := readIdentitySnapshot(filepath.Join(home, "users.json"), &published); err != nil || len(published.Users) != 1 {
		t.Fatal("lost identity evidence", err)
	}
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	restarted := hostedFixture(t, home)
	_, subject, space := hostedLogin(t, restarted, "alice")
	if subject.UserID != published.Users[0].ID || space != published.Users[0].DefaultSpace.ID {
		t.Fatal("unknown publication reallocated identity")
	}
}

func TestHostedDisableRetainsUnsettledRuntime(t *testing.T) {
	s := hostedFixture(t, t.TempDir())
	entry, err := s.access.users.reserve(authn.Identity{Provider: "test", Authority: "https://hub.example", Subject: "alice"}, externalAuthConfig{AllowedSubjects: []string{"alice"}})
	if err != nil {
		t.Fatal(err)
	}
	// Model a retained external operation owner which ignores cancellation.
	done := make(chan struct{})
	stuck := &spaceServerEntry{done: done, cancel: func() {}}
	s.mu.Lock()
	s.directory.Spaces = append(s.directory.Spaces, entry.DefaultSpace)
	s.entries[entry.DefaultSpace.ID] = stuck
	s.mu.Unlock()
	if _, err := s.access.users.setStatus(entry.ID, "disabled"); err != nil {
		t.Fatal(err)
	}
	s.fenceUser(entry.ID)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := s.settleUser(ctx, entry.ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unsettled operation acknowledged", err)
	}
	s.mu.Lock()
	retained := s.entries[entry.DefaultSpace.ID]
	record := s.directory.Spaces[0]
	s.mu.Unlock()
	if retained != stuck || record.Status != "archiving" || s.userSettled(entry.ID) {
		t.Fatal("unsettled owner or evidence released")
	}
	close(done)
	if err := s.settleUser(t.Context(), entry.ID); err != nil {
		t.Fatal(err)
	}
}
