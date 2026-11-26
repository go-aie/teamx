package room

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/authn"
)

func TestCasdoorConfigurationKeepsCredentialsPrivateAndAdmissionExplicit(t *testing.T) {
	c := externalAuthConfig{Provider: "casdoor", Issuer: "https://casdoor.example", ClientID: "teamx-client", AllowAllAuthenticated: true}
	if _, err := c.provider(); err != nil {
		t.Fatal("public PKCE configuration rejected", err)
	}
	path := filepath.Join(t.TempDir(), "client-secret")
	if err := os.WriteFile(path, []byte("private-client-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c.ClientSecretFile = path
	provider, err := c.provider()
	if err != nil {
		t.Fatal("confidential client rejected", err)
	}
	jump, err := provider.LoginURL("https://teamx.example/v1/access/sso/callback?state=" + strings.Repeat("a", 64))
	if err != nil || strings.Contains(jump, "private-client-secret") || strings.Contains(jump, "client_secret") {
		t.Fatal("secret escaped into authorization URL", err)
	}
	for _, mutate := range []func(*externalAuthConfig){
		func(c *externalAuthConfig) { c.AllowAllAuthenticated = false },
		func(c *externalAuthConfig) { c.Issuer = "http://casdoor.example" },
		func(c *externalAuthConfig) { c.ClientID = "" },
		func(c *externalAuthConfig) { c.HubOrigin = "http://hub.example" },
		func(c *externalAuthConfig) { c.HubOrigin = "https://hub.example/untrusted-path" },
		func(c *externalAuthConfig) { c.ClientSecretFile = "relative-secret" },
		func(c *externalAuthConfig) { c.ClientSecretFile = filepath.Join(t.TempDir(), "missing") },
	} {
		invalid := c
		mutate(&invalid)
		if _, err := invalid.provider(); err == nil || strings.Contains(err.Error(), "private-client-secret") {
			t.Fatal("invalid configuration accepted or secret exposed", err)
		}
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.provider(); err == nil {
		t.Fatal("readable secret file accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "secret-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	c.ClientSecretFile = link
	if _, err := c.provider(); err == nil {
		t.Fatal("symlink secret accepted")
	}
}

// Protocol verification is covered by the Casdoor provider's TLS issuer tests.
// This substitute exercises authorization-code callbacks through the real
// host-owned browser transaction, user directory and persisted session store.
type codeProviderFixture struct{}

func (codeProviderFixture) LoginURL(callback string) (string, error) {
	u, err := url.Parse(callback)
	if err != nil {
		return "", err
	}
	state := u.Query().Get("state")
	u.RawQuery = ""
	return "https://casdoor.example/login/oauth/authorize?" + url.Values{"redirect_uri": {u.String()}, "state": {state}, "client_id": {"teamx-client"}, "response_type": {"code"}}.Encode(), nil
}

func (codeProviderFixture) Complete(_ context.Context, values url.Values) (authn.Identity, error) {
	return authn.Identity{Provider: "casdoor", Authority: "https://casdoor.example", Subject: values.Get("code"), DisplayName: values.Get("code"), ValidUntil: time.Now().Add(time.Hour)}, nil
}

func codeLogin(t *testing.T, s *spaceServer, account string) (*http.Cookie, userSubject, string) {
	t.Helper()
	start := hostedRequest(s, externalLoginPath, struct{}{}, nil, "")
	if start.Code != http.StatusSeeOther {
		t.Fatal("could not start code login", start.Code)
	}
	jump, _ := url.Parse(start.Header().Get("Location"))
	callback, _ := url.Parse(jump.Query().Get("redirect_uri"))
	callback.RawQuery = url.Values{"state": {jump.Query().Get("state")}, "code": {account}}.Encode()
	attempt := start.Result().Cookies()[0]
	// A valid code without this browser's cookie cannot reserve a user.
	if foreign := hostedRequest(s, callback.RequestURI(), nil, nil, ""); foreign.Header().Get("Location") != "/access?sso=failed" {
		t.Fatal("authorization code was not browser-bound")
	}
	result := hostedRequest(s, callback.RequestURI(), nil, attempt, "")
	if result.Header().Get("Location") != "/" {
		t.Fatal("authorization code callback failed")
	}
	var cookie *http.Cookie
	for _, c := range result.Result().Cookies() {
		if c.Name == s.access.cookieName() && c.Value != "" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no business session issued")
	}
	if replay := hostedRequest(s, callback.RequestURI(), nil, attempt, ""); replay.Header().Get("Location") != "/access?sso=failed" {
		t.Fatal("code callback was replayed")
	}
	subject, ok := s.access.browserSubject(&http.Request{Header: http.Header{"Cookie": {cookie.String()}}})
	if !ok {
		t.Fatal("business session not authorized")
	}
	return cookie, subject, s.access.defaultSpace(subject)
}

func TestCasdoorHostCodeLoginPersistsIsolationAndRequiresOwnModel(t *testing.T) {
	home := t.TempDir()
	c := externalAuthConfig{Provider: "casdoor", Issuer: "https://casdoor.example", ClientID: "teamx-client", AllowAllAuthenticated: true}
	s := hostedFixtureWithProvider(t, home, c, codeProviderFixture{})
	aliceCookie, alice, aliceSpace := codeLogin(t, s, "alice")
	bobCookie, bob, bobSpace := codeLogin(t, s, "bob")
	if alice.UserID == bob.UserID || aliceSpace == bobSpace || s.access.login.provision != nil {
		t.Fatal("direct OIDC mixed identities or enabled OpenCSG key retrieval")
	}
	for _, pair := range []struct {
		cookie *http.Cookie
		space  string
	}{{aliceCookie, bobSpace}, {bobCookie, aliceSpace}} {
		if w := hostedRequest(s, "/s/"+pair.space+"/v1/setup", nil, pair.cookie, ""); w.Code != http.StatusForbidden {
			t.Fatal("another owner's Space was exposed", w.Code)
		}
	}
	waitSpaceState(t, s, aliceSpace, "setup_required")
	if key, _, err := s.access.users.gatewayKey(alice.UserID); err != nil || key != "" {
		t.Fatal("direct OIDC acquired an unrelated model credential", err)
	}
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	s = hostedFixtureWithProvider(t, home, c, codeProviderFixture{})
	if w := hostedRequest(s, "/v1/access/session", nil, aliceCookie, ""); w.Code != http.StatusOK {
		t.Fatal("restart lost OIDC session", w.Code)
	}
	_, again, sameSpace := codeLogin(t, s, "alice")
	if again != alice || sameSpace != aliceSpace {
		t.Fatal("restart changed stable OIDC identity")
	}
	if _, err := s.access.users.setStatus(alice.UserID, "disabled"); err != nil {
		t.Fatal(err)
	}
	if w := hostedRequest(s, "/v1/access/session", nil, aliceCookie, ""); w.Code != 401 && w.Code != 403 {
		t.Fatal("disabled OIDC user retained access", w.Code)
	}
	if w := hostedRequest(s, "/v1/access/session", nil, bobCookie, ""); w.Code != 200 {
		t.Fatal("disabling one OIDC user affected the other", w.Code)
	}
	if _, err := s.access.users.reserve(authn.Identity{Provider: "opencsg", Authority: "https://hub.example", Subject: "alice"}, c); err == nil {
		t.Fatal("changed provider adopted an existing OIDC identity")
	}
}

func TestCasdoorIssuerAndClientChangesInvalidateSavedSessions(t *testing.T) {
	home := t.TempDir()
	c := externalAuthConfig{Provider: "casdoor", Issuer: "https://casdoor.example", ClientID: "teamx-client", AllowAllAuthenticated: true}
	s := hostedFixtureWithProvider(t, home, c, codeProviderFixture{})
	cookie, _, _ := codeLogin(t, s, "alice")
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	c.ClientID = "different-client"
	s = hostedFixtureWithProvider(t, home, c, codeProviderFixture{})
	if w := hostedRequest(s, "/v1/access/session", nil, cookie, ""); w.Code != 401 {
		t.Fatal("client change retained old session", w.Code)
	}
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	c.ClientID = "teamx-client"
	s = hostedFixtureWithProvider(t, home, c, codeProviderFixture{})
	if w := hostedRequest(s, "/v1/access/session", nil, cookie, ""); w.Code != 401 {
		t.Fatal("restoring old client resurrected revoked session", w.Code)
	}
	cookie, _, _ = codeLogin(t, s, "alice")
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	c.Issuer = "https://another.example"
	s = hostedFixtureWithProvider(t, home, c, codeProviderFixture{})
	if w := hostedRequest(s, "/v1/access/session", nil, cookie, ""); w.Code != 401 {
		t.Fatal("issuer change retained old session", w.Code)
	}
	data, err := os.ReadFile(s.access.sessionPath())
	var snapshot accessSessionSnapshot
	if err != nil || json.Unmarshal(data, &snapshot) != nil || len(snapshot.Sessions) != 0 {
		t.Fatal("invalidated sessions were not pruned durably", err)
	}
}
