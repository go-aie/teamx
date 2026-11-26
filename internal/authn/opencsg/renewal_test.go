package opencsg

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/authn"
)

type renewalIssuer struct {
	*issuerFixture
	refreshMode    string
	missingRefresh bool
	refreshHits    int
	accessSeconds  int
}

func newRenewalIssuer(t *testing.T) *renewalIssuer {
	f := newIssuerFixture(t, "client-secret")
	r := &renewalIssuer{issuerFixture: f, accessSeconds: 900}
	base := f.server.Config.Handler
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/api/login/oauth/access_token" {
			base.ServeHTTP(w, req)
			return
		}
		_ = req.ParseForm()
		if req.Form.Get("grant_type") == "authorization_code" {
			rr := httptest.NewRecorder()
			base.ServeHTTP(rr, req)
			if rr.Code != 200 {
				for key, values := range rr.Header() {
					w.Header()[key] = values
				}
				w.WriteHeader(rr.Code)
				_, _ = w.Write(rr.Body.Bytes())
				return
			}
			var body map[string]any
			_ = json.Unmarshal(rr.Body.Bytes(), &body)
			body["expires_in"] = r.accessSeconds
			if !r.missingRefresh {
				body["refresh_token"] = "original-refresh"
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(body)
			return
		}
		r.refreshHits++
		if req.Form.Get("grant_type") != "refresh_token" || req.Form.Get("refresh_token") != "original-refresh" || req.Form.Get("client_id") != "teamx-client" || req.Form.Get("client_secret") != "client-secret" || req.Form.Has("scope") {
			t.Error("refresh grant changed client or scope")
			w.WriteHeader(400)
			return
		}
		switch r.refreshMode {
		case "invalid_grant", "invalid_client", "unsupported_grant_type":
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": r.refreshMode, "error_description": "private-secret"})
			return
		case "outage":
			w.WriteHeader(503)
			_, _ = w.Write([]byte("private-secret"))
			return
		case "limited":
			w.Header().Set("Retry-After", "9999")
			w.WriteHeader(429)
			return
		case "redirect":
			http.Redirect(w, req, "https://foreign.invalid", 307)
			return
		}
		body := map[string]any{"access_token": "private-access-token", "refresh_token": "rotated-refresh", "token_type": "Bearer", "expires_in": 900}
		if r.refreshMode == "preserve" {
			delete(body, "refresh_token")
		}
		if r.refreshMode == "scope" {
			body["scope"] = "openid profile offline_access admin"
		}
		if r.refreshMode != "no-id" && r.refreshMode != "preserve" && r.refreshMode != "scope" {
			claims := map[string]any{"iss": f.server.URL, "sub": "stable-user-id", "aud": "teamx-client", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
			switch r.refreshMode {
			case "nonce":
				claims["nonce"] = f.query.Get("nonce")
			case "wrong-nonce":
				claims["nonce"] = "wrong"
			case "issuer":
				claims["iss"] = "https://foreign.invalid"
			case "subject":
				claims["sub"] = "foreign-user"
			case "audience":
				claims["aud"] = "foreign-client"
			case "azp":
				claims["azp"] = "foreign-client"
			case "expired":
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
			}
			data, _ := json.Marshal(claims)
			signature, _ := f.signer.Sign(data)
			raw, _ := signature.CompactSerialize()
			if r.refreshMode == "signature" {
				parts := strings.Split(raw, ".")
				parts[2] = strings.Repeat("A", 342)
				raw = strings.Join(parts, ".")
			}
			body["id_token"] = raw
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	return r
}
func (f *renewalIssuer) login(t *testing.T) (*SSO, authn.Login, error) {
	t.Helper()
	p, err := NewRenewable(f.server.URL, "teamx-client", "client-secret", f.server.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	state := strings.Repeat("a", 64)
	jump, err := p.LoginURL("https://teamx.example/v1/access/sso/callback?state=" + state)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(jump)
	f.query = u.Query()
	if f.query.Get("scope") != "openid profile offline_access" {
		t.Fatal("offline grant not requested")
	}
	login, err := p.CompleteGrant(context.Background(), url.Values{"code": {"one-use-code"}, "state": {state}})
	return p, login, err
}
func TestOIDCRenewalPreservesIdentityAndRotatesOnlyOnExplicitRefresh(t *testing.T) {
	f := newRenewalIssuer(t)
	p, login, err := f.login(t)
	if err != nil {
		t.Fatal(err)
	}
	if login.Grant.RefreshToken != "original-refresh" || f.tokenHits.Load() != 1 || login.VerifiedUntil.After(login.Grant.AccessExpires) {
		t.Fatal("bad initial grant")
	}
	for _, mode := range []string{"no-id", "", "nonce", "preserve"} {
		f.refreshMode = mode
		next, err := p.Refresh(context.Background(), login.Grant)
		if err != nil {
			t.Fatal(mode, err)
		}
		want := "rotated-refresh"
		if mode == "preserve" {
			want = "original-refresh"
		}
		if next.RefreshToken != want || next.Subject != login.Identity.Subject {
			t.Fatal("rotation or identity lost")
		}
		if _, err := p.Verify(context.Background(), next); err != nil {
			t.Fatal("userinfo binding failed", err)
		}
	}
	if f.tokenHits.Load() != 1 {
		t.Fatal("authorization code consumed twice")
	}
}
func TestOIDCRenewalRejectsInvalidClaimsAndClassifiesFailures(t *testing.T) {
	f := newRenewalIssuer(t)
	p, login, err := f.login(t)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"wrong-nonce", "issuer", "subject", "audience", "azp", "expired", "signature", "scope", "invalid_grant", "invalid_client", "unsupported_grant_type", "outage", "limited", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			f.refreshMode = mode
			_, err := p.Refresh(context.Background(), login.Grant)
			var failed *authn.Failure
			if !errors.As(err, &failed) || strings.Contains(err.Error(), "private") {
				t.Fatal("failure leaked provider content or lost category", err)
			}
			want := authn.InvalidGrant
			switch mode {
			case "invalid_client", "unsupported_grant_type", "redirect":
				want = authn.ClientConfig
			case "outage", "limited":
				want = authn.Temporary
			}
			if failed.Kind != want {
				t.Fatal("wrong category", failed.Kind, want)
			}
			if mode == "limited" && failed.RetryAfter != time.Minute {
				t.Fatal("retry-after not bounded")
			}
		})
	}
	f.mode = "foreign-user"
	if _, err := p.Verify(context.Background(), login.Grant); err == nil {
		t.Fatal("foreign userinfo accepted")
	}
}
func TestOIDCRenewalRequiresRefreshTokenWithoutChangingIdentityOnlyLogin(t *testing.T) {
	f := newRenewalIssuer(t)
	f.missingRefresh = true
	if _, _, err := f.login(t); err == nil {
		t.Fatal("long login silently accepted without refresh")
	}
	p, values := f.begin(t)
	if _, err := p.Complete(context.Background(), values); err != nil {
		t.Fatal("identity-only login regressed", err)
	}
}

func TestOIDCRenewalRequiresRefreshAndRejectsUserinfoIdentity(t *testing.T) {
	f := newRenewalIssuer(t)
	f.missingRefresh = true
	if _, _, err := f.login(t); err == nil {
		t.Fatal("long login accepted without refresh token")
	}
	f.missingRefresh = false
	p, login, err := f.login(t)
	if err != nil {
		t.Fatal(err)
	}
	f.mode = "foreign-user"
	if _, err := p.Verify(context.Background(), login.Grant); err == nil {
		t.Fatal("foreign userinfo accepted")
	}
}

func TestOIDCRenewalUserinfoRefusalIsFinal(t *testing.T) {
	f := newRenewalIssuer(t)
	p, login, err := f.login(t)
	if err != nil {
		t.Fatal(err)
	}
	base := f.server.Config.Handler
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/userinfo" {
			w.WriteHeader(401)
			return
		}
		base.ServeHTTP(w, r)
	})
	_, err = p.Verify(context.Background(), login.Grant)
	var failure *authn.Failure
	if !errors.As(err, &failure) || failure.Kind != authn.InvalidGrant {
		t.Fatal("userinfo refusal did not invalidate grant", err)
	}
}

func TestOIDCRenewalInitialVerificationWindow(t *testing.T) {
	for _, ttl := range []time.Duration{15 * time.Minute, 2 * time.Hour} {
		t.Run(ttl.String(), func(t *testing.T) {
			f := newRenewalIssuer(t)
			f.accessSeconds = int(ttl / time.Second)
			_, login, err := f.login(t)
			if err != nil {
				t.Fatal(err)
			}
			window := login.VerifiedUntil.Sub(login.VerifiedAt)
			if ttl > time.Hour {
				if window != time.Hour {
					t.Fatalf("remote check window = %s, want 1h", window)
				}
			} else if !login.VerifiedUntil.Equal(login.Grant.AccessExpires) || window > ttl {
				t.Fatal("remote check deadline exceeded short-lived access token")
			}
		})
	}
}
