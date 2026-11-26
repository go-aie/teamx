package opencsg

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-aie/teamx/internal/authn"
	"github.com/go-jose/go-jose/v4"
)

const maxTokenBytes = 64 << 10
const maxProviderBytes = 1 << 20

var _ authn.RenewableProvider = (*SSO)(nil)

type boundedTransport struct{ base http.RoundTripper }
type boundedBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, errors.New("authentication response too large")
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	resp, err := base.RoundTrip(r)
	if err == nil {
		resp.Body = &boundedBody{resp.Body, maxProviderBytes + 1}
	}
	return resp, err
}

func failure(kind authn.FailureKind) error { return &authn.Failure{Kind: kind} }

func (s *SSO) CompleteGrant(ctx context.Context, values url.Values) (authn.Login, error) {
	if !s.renewable {
		return authn.Login{}, failure(authn.ClientConfig)
	}
	id, token, a, err := s.complete(ctx, values, true)
	if err != nil {
		return authn.Login{}, err
	}
	g := authn.Grant{Provider: id.Provider, Authority: id.Authority, Subject: id.Subject,
		ClientID: s.clientID, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken,
		AccessExpires: token.Expiry, Nonce: a.nonce, Scope: "openid profile offline_access"}
	if scope, ok := token.Extra("scope").(string); ok {
		if !scopeSubset(scope, g.Scope) {
			return authn.Login{}, failure(authn.InvalidGrant)
		}
		g.Scope = scope
	}
	g.AuthTime = a.authTime
	if value := token.Extra("refresh_expires_in"); value != nil {
		encoded, err := json.Marshal(value)
		var seconds json.Number
		if err != nil || json.Unmarshal(encoded, &seconds) != nil {
			return authn.Login{}, failure(authn.InvalidGrant)
		}
		n, err := seconds.Int64()
		if err != nil || n <= 0 || n > int64((365*24*time.Hour)/time.Second) {
			return authn.Login{}, failure(authn.InvalidGrant)
		}
		g.RefreshExpires = time.Now().Add(time.Duration(n) * time.Second)
	}
	if !s.validGrant(g) || !time.Now().Before(g.AccessExpires) {
		return authn.Login{}, failure(authn.InvalidGrant)
	}
	now := time.Now()
	return authn.Login{Identity: id, Grant: g, VerifiedAt: now, VerifiedUntil: minTime(now.Add(authn.IdentityVerificationWindow), g.AccessExpires)}, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func scopeSubset(next, original string) bool {
	allowed := map[string]bool{}
	for _, value := range strings.Fields(original) {
		allowed[value] = true
	}
	for _, value := range strings.Fields(next) {
		if !allowed[value] {
			return false
		}
	}
	return true
}

func validToken(value string) bool {
	return value != "" && len(value) <= maxTokenBytes && !strings.ContainsAny(value, "\x00\r\n")
}

func (s *SSO) validGrant(g authn.Grant) bool {
	return g.Provider == "opencsg" && g.Authority == s.issuer && g.ClientID == s.clientID &&
		g.Subject != "" && len(g.Subject) <= 256 && validToken(g.AccessToken) && validToken(g.RefreshToken) &&
		!g.AccessExpires.IsZero() && len(g.Scope) <= 4096 && len(g.Nonce) <= 256
}

func (s *SSO) renewalProvider(ctx context.Context) (*oidc.Provider, error) {
	ctx = oidc.ClientContext(ctx, s.client)
	p, err := oidc.NewProvider(ctx, s.issuer)
	if err != nil {
		return nil, failure(authn.Temporary)
	}
	var meta struct {
		JWKS     string `json:"jwks_uri"`
		UserInfo string `json:"userinfo_endpoint"`
	}
	if p.Claims(&meta) != nil || !strings.HasPrefix(meta.JWKS, s.issuer+"/") ||
		p.Endpoint().AuthURL != s.issuer+"/login/oauth/authorize" ||
		p.Endpoint().TokenURL != s.issuer+"/api/login/oauth/access_token" || meta.UserInfo != s.issuer+"/api/userinfo" {
		return nil, failure(authn.ClientConfig)
	}
	return p, nil
}

// Refresh maintains authentication when access tokens expire before the local
// session's absolute deadline; it never extends that deadline.
// As of 2026-11-26, observed OpenCSG access tokens and the default local session
// both last 7 days. End-to-end refresh remains unverified: the IAM application's
// refresh_token grant is disabled.
// The caller must durably save the returned grant before Verify, even if userinfo
// is temporarily unavailable.
func (s *SSO) Refresh(ctx context.Context, old authn.Grant) (authn.Grant, error) {
	if !s.validGrant(old) || !old.RefreshExpires.IsZero() && !time.Now().Before(old.RefreshExpires) {
		return authn.Grant{}, failure(authn.InvalidGrant)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	p, err := s.renewalProvider(ctx)
	if err != nil {
		return authn.Grant{}, err
	}
	// Fetch verification keys before consuming a rotating grant. A JWKS outage
	// must remain retryable and must not be mistaken for a revoked identity.
	verifier, err := s.refreshVerifier(ctx, p)
	if err != nil {
		return authn.Grant{}, err
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {old.RefreshToken}, "client_id": {s.clientID}}
	if s.secret != "" {
		form.Set("client_secret", s.secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.issuer+"/api/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return authn.Grant{}, failure(authn.ClientConfig)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var body struct {
		Access         string      `json:"access_token"`
		Refresh        string      `json:"refresh_token"`
		Type           string      `json:"token_type"`
		Expires        json.Number `json:"expires_in"`
		RefreshExpires json.Number `json:"refresh_expires_in"`
		ID             string      `json:"id_token"`
		Scope          *string     `json:"scope"`
	}
	if err := s.jsonRequest(req, &body); err != nil {
		return authn.Grant{}, err
	}
	seconds, err := body.Expires.Int64()
	if err != nil || seconds <= 0 || seconds > int64((365*24*time.Hour)/time.Second) || !validToken(body.Access) || !strings.EqualFold(body.Type, "Bearer") || len(body.ID) > maxTokenBytes {
		return authn.Grant{}, failure(authn.InvalidGrant)
	}
	next := old
	next.AccessToken, next.AccessExpires = body.Access, time.Now().Add(time.Duration(seconds)*time.Second)
	if body.Refresh != "" {
		next.RefreshToken = body.Refresh
	}
	if body.RefreshExpires != "" {
		seconds, err := body.RefreshExpires.Int64()
		if err != nil || seconds <= 0 || seconds > int64((365*24*time.Hour)/time.Second) {
			return authn.Grant{}, failure(authn.InvalidGrant)
		}
		next.RefreshExpires = time.Now().Add(time.Duration(seconds) * time.Second)
	} else if body.Refresh != "" && body.Refresh != old.RefreshToken {
		next.RefreshExpires = time.Time{}
	}
	if body.Scope != nil {
		if !scopeSubset(*body.Scope, old.Scope) {
			return authn.Grant{}, failure(authn.InvalidGrant)
		}
		next.Scope = *body.Scope
	}
	if !s.validGrant(next) {
		return authn.Grant{}, failure(authn.InvalidGrant)
	}
	if body.ID != "" {
		id, err := verifier.Verify(ctx, body.ID)
		if err != nil || id.Subject != old.Subject || id.Nonce != "" && id.Nonce != old.Nonce {
			return authn.Grant{}, failure(authn.InvalidGrant)
		}
		var claims struct {
			AZP      string `json:"azp"`
			AuthTime int64  `json:"auth_time"`
		}
		if id.Claims(&claims) != nil || claims.AZP != "" && claims.AZP != s.clientID || len(id.Audience) > 1 && claims.AZP != s.clientID || old.AuthTime != 0 && claims.AuthTime != old.AuthTime || id.AccessTokenHash != "" && id.VerifyAccessToken(next.AccessToken) != nil {
			return authn.Grant{}, failure(authn.InvalidGrant)
		}
	}
	return next, nil
}

func (s *SSO) Verify(ctx context.Context, g authn.Grant) (authn.Identity, error) {
	if !s.validGrant(g) || !time.Now().Before(g.AccessExpires) {
		return authn.Identity{}, failure(authn.InvalidGrant)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := s.renewalProvider(ctx); err != nil {
		return authn.Identity{}, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, s.issuer+"/api/userinfo", nil)
	req.Header.Set("Authorization", "Bearer "+g.AccessToken)
	var body struct {
		Sub      string `json:"sub"`
		Name     string `json:"name"`
		Username string `json:"preferred_username"`
	}
	if err := s.jsonRequest(req, &body); err != nil {
		return authn.Identity{}, err
	}
	if body.Sub != g.Subject {
		return authn.Identity{}, failure(authn.InvalidGrant)
	}
	name := body.Name
	if name == "" {
		name = body.Username
	}
	return authn.Identity{Provider: g.Provider, Authority: g.Authority, Subject: g.Subject, DisplayName: name, ValidUntil: g.AccessExpires}, nil
}

func (s *SSO) jsonRequest(req *http.Request, target any) error {
	resp, err := s.client.Do(req)
	if err != nil {
		return failure(authn.Temporary)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProviderBytes+1))
	if err != nil || len(data) > maxProviderBytes {
		return failure(authn.Temporary)
	}
	if resp.StatusCode != http.StatusOK {
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &body)
		kind := authn.Temporary
		switch {
		case resp.StatusCode == 429 || resp.StatusCode >= 500:
		case body.Error == "invalid_client" || body.Error == "unauthorized_client" || resp.StatusCode >= 300 && resp.StatusCode < 400:
			kind = authn.ClientConfig
		case body.Error == "invalid_grant" || body.Error == "invalid_token" || req.Method == http.MethodGet && (resp.StatusCode == 401 || resp.StatusCode == 403):
			kind = authn.InvalidGrant
		default:
			kind = authn.ClientConfig
		}
		var delay time.Duration
		if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds > 0 {
			delay = min(time.Duration(min(seconds, 60))*time.Second, time.Minute)
		} else if at, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil {
			delay = max(0, min(time.Until(at), time.Minute))
		}
		return &authn.Failure{Kind: kind, RetryAfter: delay}
	}
	if json.Unmarshal(data, target) != nil {
		return failure(authn.Temporary)
	}
	return nil
}

func (s *SSO) refreshVerifier(ctx context.Context, p *oidc.Provider) (*oidc.IDTokenVerifier, error) {
	var meta struct {
		JWKS string   `json:"jwks_uri"`
		Algs []string `json:"id_token_signing_alg_values_supported"`
	}
	if p.Claims(&meta) != nil || !strings.HasPrefix(meta.JWKS, s.issuer+"/") {
		return nil, failure(authn.ClientConfig)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, meta.JWKS, nil)
	var keys jose.JSONWebKeySet
	if err := s.jsonRequest(request, &keys); err != nil {
		return nil, err
	}
	if len(keys.Keys) == 0 || len(keys.Keys) > 128 {
		return nil, failure(authn.ClientConfig)
	}
	static := &oidc.StaticKeySet{}
	for _, key := range keys.Keys {
		if key.Use != "" && key.Use != "sig" {
			continue
		}
		public := key.Public().Key
		switch public.(type) {
		case *rsa.PublicKey, *ecdsa.PublicKey, ed25519.PublicKey:
			static.PublicKeys = append(static.PublicKeys, public)
		}
	}
	if len(static.PublicKeys) == 0 {
		return nil, failure(authn.ClientConfig)
	}
	return oidc.NewVerifier(s.issuer, static, &oidc.Config{ClientID: s.clientID, SupportedSigningAlgs: meta.Algs}), nil
}
