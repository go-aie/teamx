// Package opencsg implements direct authorization-code login. The host still
// owns browser binding, admission, local identities and sessions.
package opencsg

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-aie/teamx/internal/authn"
	"golang.org/x/oauth2"
)

var ErrAuthentication = errors.New("OpenCSG authentication failed")

const attemptLifetime = 5 * time.Minute

type attempt struct {
	redirect, verifier, nonce string
	expires                   time.Time
}

type SSO struct {
	issuer, clientID, secret string
	client                   *http.Client
	mu                       sync.Mutex
	attempts                 map[string]attempt
}

var _ authn.Provider = (*SSO)(nil)

// New does not contact the issuer, so configuration validation stays offline.
// An empty secret selects a public client with PKCE; confidential clients use
// client_secret_post in addition to PKCE. Neither mode persists provider tokens.
func New(issuer, clientID, secret string, transport http.RoundTripper) (*SSO, error) {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || strings.TrimSpace(clientID) != clientID || clientID == "" || len(clientID) > 256 || strings.ContainsAny(clientID, "\x00\r\n") {
		return nil, errors.New("OpenCSG requires an HTTPS root issuer and client_id")
	}
	return &SSO{issuer: issuer, clientID: clientID, secret: secret, attempts: map[string]attempt{}, client: &http.Client{
		Transport: transport, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (s *SSO) config(redirect string) oauth2.Config {
	return oauth2.Config{ClientID: s.clientID, ClientSecret: s.secret, RedirectURL: redirect,
		Endpoint: oauth2.Endpoint{AuthURL: s.issuer + "/login/oauth/authorize", TokenURL: s.issuer + "/api/login/oauth/access_token", AuthStyle: oauth2.AuthStyleInParams},
		Scopes:   []string{oidc.ScopeOpenID, "profile"}}
}

func (s *SSO) LoginURL(callback string) (string, error) {
	u, err := url.Parse(callback)
	if err != nil || u.Scheme != "https" && !authn.LoopbackHTTP(u) || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("OpenCSG callback requires HTTPS or literal HTTP loopback")
	}
	query, err := url.ParseQuery(u.RawQuery)
	state := query.Get("state")
	if err != nil || len(query) != 1 || len(query["state"]) != 1 || len(state) < 32 || len(state) > 256 {
		return "", errors.New("OpenCSG requires the host's per-attempt state")
	}
	// OAuth returns state separately. The registered redirect URI must be stable.
	u.RawQuery, u.ForceQuery = "", false
	a := attempt{redirect: u.String(), verifier: oauth2.GenerateVerifier(), nonce: oauth2.GenerateVerifier(), expires: time.Now().Add(attemptLifetime)}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, pending := range s.attempts {
		if !time.Now().Before(pending.expires) {
			delete(s.attempts, key)
		}
	}
	if _, exists := s.attempts[state]; exists || len(s.attempts) >= 128 {
		return "", errors.New("OpenCSG login attempt unavailable")
	}
	s.attempts[state] = a
	c := s.config(a.redirect)
	return c.AuthCodeURL(state, oauth2.S256ChallengeOption(a.verifier), oidc.Nonce(a.nonce)), nil
}

func (s *SSO) Complete(ctx context.Context, values url.Values) (authn.Identity, error) {
	identity, _, err := s.complete(ctx, values, false)
	return identity, err
}

// CompleteWithToken additionally checks userinfo subject equality. The caller
// can use this token during the same login to obtain the owner's credentials.
func (s *SSO) CompleteWithToken(ctx context.Context, values url.Values) (authn.Identity, string, error) {
	return s.complete(ctx, values, true)
}

func (s *SSO) complete(ctx context.Context, values url.Values, exportToken bool) (authn.Identity, string, error) {
	if len(values["state"]) != 1 {
		return authn.Identity{}, "", ErrAuthentication
	}
	s.mu.Lock()
	a, ok := s.attempts[values.Get("state")]
	delete(s.attempts, values.Get("state"))
	s.mu.Unlock()
	if !ok || !time.Now().Before(a.expires) || len(values["code"]) != 1 || values.Get("code") == "" || len(values.Get("code")) > 16<<10 || values.Has("error") || values.Has("jwt") || values.Has("jwt_token") {
		return authn.Identity{}, "", ErrAuthentication
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ctx = oidc.ClientContext(ctx, s.client)
	provider, err := oidc.NewProvider(ctx, s.issuer)
	if err != nil {
		return authn.Identity{}, "", fmt.Errorf("%w: OIDC discovery rejected", ErrAuthentication)
	}
	// Keep code and token traffic pinned to this configured OpenCSG origin.
	var metadata struct {
		JWKS     string `json:"jwks_uri"`
		UserInfo string `json:"userinfo_endpoint"`
	}
	if provider.Claims(&metadata) != nil || !strings.HasPrefix(metadata.JWKS, s.issuer+"/") || provider.Endpoint().AuthURL != s.issuer+"/login/oauth/authorize" || provider.Endpoint().TokenURL != s.issuer+"/api/login/oauth/access_token" {
		return authn.Identity{}, "", fmt.Errorf("%w: OIDC endpoints rejected", ErrAuthentication)
	}
	c := s.config(a.redirect)
	token, err := c.Exchange(ctx, values.Get("code"), oauth2.VerifierOption(a.verifier))
	if err != nil {
		// OAuth errors can include response bodies containing credentials.
		return authn.Identity{}, "", fmt.Errorf("%w: code exchange rejected", ErrAuthentication)
	}
	raw, _ := token.Extra("id_token").(string)
	if raw == "" || len(raw) > 64<<10 {
		return authn.Identity{}, "", fmt.Errorf("%w: ID token missing", ErrAuthentication)
	}
	id, err := provider.VerifierContext(ctx, &oidc.Config{ClientID: s.clientID}).Verify(ctx, raw)
	if err != nil || id.Nonce != a.nonce || id.Subject == "" || len(id.Subject) > 256 || strings.TrimSpace(id.Subject) != id.Subject || strings.ContainsAny(id.Subject, "\x00\r\n") {
		return authn.Identity{}, "", fmt.Errorf("%w: ID token verification rejected", ErrAuthentication)
	}
	if id.AccessTokenHash != "" && id.VerifyAccessToken(token.AccessToken) != nil {
		return authn.Identity{}, "", ErrAuthentication
	}
	var claims struct {
		Name            string `json:"name"`
		Username        string `json:"preferred_username"`
		AuthorizedParty string `json:"azp"`
	}
	if id.Claims(&claims) != nil || claims.AuthorizedParty != "" && claims.AuthorizedParty != s.clientID || len(id.Audience) > 1 && claims.AuthorizedParty != s.clientID {
		return authn.Identity{}, "", ErrAuthentication
	}
	name := claims.Name
	if name == "" {
		name = claims.Username
	}
	identity := authn.Identity{Provider: "opencsg", Authority: s.issuer, Subject: id.Subject, DisplayName: name, ValidUntil: id.Expiry}
	if !exportToken {
		return identity, "", nil
	}
	// Bind the access token to the verified ID token before a model credential
	// integration can use it. Neither token is persisted or put in a session.
	if metadata.UserInfo != s.issuer+"/api/userinfo" {
		return authn.Identity{}, "", fmt.Errorf("%w: userinfo endpoint rejected", ErrAuthentication)
	}
	user, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil || user.Subject != id.Subject {
		return authn.Identity{}, "", fmt.Errorf("%w: access token identity rejected", ErrAuthentication)
	}
	return identity, token.AccessToken, nil
}
