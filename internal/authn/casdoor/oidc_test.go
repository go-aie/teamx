package casdoor

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/authn"
	"github.com/go-jose/go-jose/v4"
)

type issuerFixture struct {
	server    *httptest.Server
	signer    jose.Signer
	key       *rsa.PrivateKey
	query     url.Values
	secret    string
	mode      string
	tokenHits atomic.Int32
}

func newIssuerFixture(t *testing.T, secret string) *issuerFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "fixture"))
	if err != nil {
		t.Fatal(err)
	}
	f := &issuerFixture{key: key, signer: signer, secret: secret}
	f.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			keyURL := f.server.URL + "/.well-known/jwks"
			if f.mode == "foreign-keys" {
				keyURL = "https://foreign.invalid/jwks"
			}
			issuer := f.server.URL
			if f.mode == "discovery-issuer" {
				issuer = "https://foreign.invalid"
			}
			userinfo := f.server.URL + "/api/userinfo"
			if f.mode == "foreign-userinfo" {
				userinfo = "https://foreign.invalid/userinfo"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": f.server.URL + "/login/oauth/authorize", "token_endpoint": f.server.URL + "/api/login/oauth/access_token", "jwks_uri": keyURL, "userinfo_endpoint": userinfo, "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/api/userinfo":
			if r.Header.Get("Authorization") != "Bearer private-access-token" {
				t.Error("userinfo did not use the exchanged token")
				w.WriteHeader(401)
				return
			}
			subject := "stable-user-id"
			if f.mode == "foreign-user" {
				subject = "another-user"
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"sub": subject})
		case "/.well-known/jwks":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "fixture", Algorithm: "RS256", Use: "sig"}}})
		case "/api/login/oauth/access_token":
			f.tokenHits.Add(1)
			if f.mode == "token-redirect" {
				http.Redirect(w, r, "https://foreign.invalid/token", http.StatusTemporaryRedirect)
				return
			}
			if f.mode == "token-error" {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"sensitive-upstream-body"}`))
				return
			}
			if r.ParseForm() != nil || r.Method != http.MethodPost || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "one-use-code" || r.Form.Get("client_id") != "teamx-client" || r.Form.Get("client_secret") != f.secret || r.Form.Get("redirect_uri") != f.query.Get("redirect_uri") {
				t.Error("code exchange did not bind the client and registered redirect")
				w.WriteHeader(400)
				return
			}
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("code_verifier") == "" || base64.RawURLEncoding.EncodeToString(hash[:]) != f.query.Get("code_challenge") {
				t.Error("PKCE proof did not match the authorization request")
				w.WriteHeader(400)
				return
			}
			claims := map[string]any{"iss": f.server.URL, "aud": "teamx-client", "sub": "stable-user-id", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": f.query.Get("nonce"), "name": "Alice"}
			switch f.mode {
			case "issuer":
				claims["iss"] = "https://foreign.invalid"
			case "audience":
				claims["aud"] = "another-client"
			case "expired":
				claims["exp"] = time.Now().Add(-time.Minute).Unix()
			case "nonce":
				claims["nonce"] = "another-attempt"
			case "no-nonce":
				delete(claims, "nonce")
			case "subject":
				claims["sub"] = ""
			case "authorized-party":
				claims["azp"] = "another-client"
			case "multiple-audiences":
				claims["aud"] = []string{"teamx-client", "another-client"}
			case "token-hash":
				claims["at_hash"] = "wrong-access-token"
			case "username":
				delete(claims, "name")
				claims["preferred_username"] = "alice-account"
			}
			body, _ := json.Marshal(claims)
			signed, err := f.signer.Sign(body)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			raw, err := signed.CompactSerialize()
			if err != nil {
				t.Error(err)
			}
			if f.mode == "signature" {
				parts := strings.Split(raw, ".")
				parts[2] = base64.RawURLEncoding.EncodeToString(make([]byte, 256))
				raw = strings.Join(parts, ".")
			}
			if f.mode == "missing-id-token" {
				raw = ""
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "private-access-token", "token_type": "Bearer", "id_token": raw})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *issuerFixture) begin(t *testing.T) (*SSO, url.Values) {
	t.Helper()
	p, err := New(f.server.URL, "teamx-client", f.secret, f.server.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	const callback = "https://teamx.example/v1/access/sso/callback"
	state := strings.Repeat("a", 64)
	jump, err := p.LoginURL(callback + "?state=" + state)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(jump)
	f.query = u.Query()
	if u.Scheme+"://"+u.Host+u.Path != f.server.URL+"/login/oauth/authorize" || f.query.Get("client_id") != "teamx-client" || f.query.Get("response_type") != "code" || f.query.Get("redirect_uri") != callback || f.query.Get("state") != state || f.query.Get("code_challenge_method") != "S256" || len(f.query.Get("nonce")) < 32 || f.query.Get("scope") != "openid profile" || f.query.Has("client_secret") || f.query.Has("code_verifier") {
		t.Fatal("authorization request did not preserve the fixed redirect and per-attempt protections")
	}
	return p, url.Values{"state": {state}, "code": {"one-use-code"}}
}

func TestOIDCCodeExchangeVerifiesIdentityAndConsumesAttempt(t *testing.T) {
	for _, secret := range []string{"", "private-client-secret"} {
		t.Run(map[bool]string{true: "public", false: "confidential"}[secret == ""], func(t *testing.T) {
			f := newIssuerFixture(t, secret)
			p, values := f.begin(t)
			id, err := p.Complete(t.Context(), values)
			if err != nil || id.Provider != "casdoor" || id.Authority != f.server.URL || id.Subject != "stable-user-id" || id.DisplayName != "Alice" || !id.ValidUntil.After(time.Now()) {
				t.Fatal("verified identity not returned", id, err)
			}
			if _, err := p.Complete(t.Context(), values); !errors.Is(err, ErrAuthentication) || f.tokenHits.Load() != 1 {
				t.Fatal("replay reached the token endpoint")
			}
		})
	}
}

func TestOIDCExportsOnlyTheVerifiedUsersAccessToken(t *testing.T) {
	f := newIssuerFixture(t, "private-client-secret")
	for _, mode := range []string{"", "foreign-user", "foreign-userinfo", "signature", "nonce"} {
		t.Run(mode, func(t *testing.T) {
			f.mode = mode
			p, values := f.begin(t)
			id, token, err := p.CompleteWithToken(t.Context(), values)
			if mode == "" {
				if err != nil || id.Subject != "stable-user-id" || token != "private-access-token" {
					t.Fatal("verified token was not available to the host integration", err)
				}
			} else if !errors.Is(err, ErrAuthentication) || token != "" || id != (authn.Identity{}) {
				t.Fatal("a mismatched or unverified user's token escaped", err)
			}
			if _, token, err := p.CompleteWithToken(t.Context(), values); err == nil || token != "" {
				t.Fatal("authorization code was exported twice")
			}
		})
	}
}

func TestOIDCRejectsInvalidIdentityAndUntrustedEndpoints(t *testing.T) {
	f := newIssuerFixture(t, "private-client-secret")
	for _, mode := range []string{"issuer", "audience", "expired", "nonce", "no-nonce", "subject", "signature", "authorized-party", "multiple-audiences", "token-hash", "missing-id-token", "foreign-keys", "discovery-issuer", "token-error", "token-redirect"} {
		t.Run(mode, func(t *testing.T) {
			f.mode = mode
			p, values := f.begin(t)
			id, err := p.Complete(t.Context(), values)
			if !errors.Is(err, ErrAuthentication) || id != (authn.Identity{}) || strings.Contains(err.Error(), "private-") || strings.Contains(err.Error(), "sensitive-upstream-body") {
				t.Fatal("unverified identity or upstream secrets escaped", err)
			}
			if _, err := p.Complete(t.Context(), values); !errors.Is(err, ErrAuthentication) {
				t.Fatal("failed attempt remained reusable")
			}
		})
	}
	f.mode = "username"
	p, values := f.begin(t)
	if id, err := p.Complete(t.Context(), values); err != nil || id.DisplayName != "alice-account" {
		t.Fatal("verified username fallback missing", err)
	}
}

func TestOIDCRejectsMissingExpiredAndAmbiguousAttemptsBeforeExchange(t *testing.T) {
	f := newIssuerFixture(t, "")
	for _, mode := range []string{"state", "code", "duplicate-state", "duplicate-code", "error", "jwt_token", "expired", "restart"} {
		t.Run(mode, func(t *testing.T) {
			p, values := f.begin(t)
			switch mode {
			case "state":
				values.Set("state", "foreign-state")
			case "code":
				values.Del("code")
			case "duplicate-state":
				values.Add("state", "other")
			case "duplicate-code":
				values.Add("code", "other")
			case "error", "jwt_token":
				values.Set(mode, "value")
			case "expired":
				a := p.attempts[values.Get("state")]
				a.expires = time.Now().Add(-time.Second)
				p.attempts[values.Get("state")] = a
			case "restart":
				p, _ = New(f.server.URL, "teamx-client", "", f.server.Client().Transport)
			}
			if _, err := p.Complete(t.Context(), values); !errors.Is(err, ErrAuthentication) {
				t.Fatal("unbound authorization code accepted")
			}
		})
	}
	if f.tokenHits.Load() != 0 {
		t.Fatal("invalid attempts sent codes to the issuer")
	}
}

func TestOIDCRequiresPinnedIssuerAndDynamicHostState(t *testing.T) {
	for _, issuer := range []string{"http://issuer.example", "https://user@issuer.example", "https://issuer.example/path", "https://issuer.example?query", "https://issuer.example#fragment"} {
		if _, err := New(issuer, "client", "", nil); err == nil {
			t.Fatal("invalid issuer accepted")
		}
	}
	p, _ := New("https://issuer.example", "client", "", nil)
	for _, callback := range []string{"https://teamx.example/callback", "https://teamx.example/callback?state=casdoor", "http://teamx.example/callback?state=" + strings.Repeat("a", 64), "https://teamx.example/callback?state=" + strings.Repeat("a", 64) + "&state=other"} {
		if _, err := p.LoginURL(callback); err == nil {
			t.Fatal("unbound callback accepted")
		}
	}
}

func TestOIDCPendingProofsAreBoundedAndExpire(t *testing.T) {
	p, _ := New("https://issuer.example", "client", "", nil)
	for i := range 128 {
		if _, err := p.LoginURL(fmt.Sprintf("https://teamx.example/callback?state=%064d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.LoginURL("https://teamx.example/callback?state=" + strings.Repeat("x", 64)); err == nil {
		t.Fatal("pending PKCE proofs grew without a bound")
	}
	for state, pending := range p.attempts {
		pending.expires = time.Now().Add(-time.Second)
		p.attempts[state] = pending
	}
	if _, err := p.LoginURL("https://teamx.example/callback?state=" + strings.Repeat("y", 64)); err != nil || len(p.attempts) != 1 {
		t.Fatal("expired proofs blocked a new login", err)
	}
}
