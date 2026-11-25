package opencsg

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/authn"
)

func gatewayTestIdentity(s *SSO) authn.Identity {
	return authn.Identity{Provider: "opencsg", Authority: s.hub, Subject: "verified-uuid", DisplayName: "alice"}
}

func TestGatewayKeyUsesAuthenticatedNamespaceAndLoginToken(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer "+testToken("alice") {
			t.Error("exchange used a credential other than the login JWT")
		}
		switch r.URL.Path {
		case "/api/v1/user/alice/tokens":
			io.WriteString(w, `{"data":[{"user_name":"alice","user_uuid":"verified-uuid","token":"git-token-is-not-a-model-key"}]}`)
		case "/api/v1/namespaces/verified-uuid/apikeys/builtin":
			if r.URL.RawQuery != "current_user=alice" {
				t.Error("wrong key owner")
			}
			io.WriteString(w, `{"data":{"token":"gk_alice-quota"}}`)
		default:
			t.Error("used an unverified namespace")
			w.WriteHeader(403)
		}
	}))
	defer server.Close()
	s, _ := New("https://login.example", server.URL, server.Client().Transport)
	values := url.Values{"jwt_token": {testToken("alice")}, "user_uuid": {"attacker-selected"}, "csghub_base_url": {"https://attacker.example"}}
	identity, err := s.Complete(context.Background(), values)
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.GatewayAPIKey(context.Background(), identity, values)
	if err != nil || key != "gk_alice-quota" || requests != 2 {
		t.Fatal("did not fetch the authenticated user's built-in key", err)
	}
}

func TestGatewayKeyFailuresNeverReturnResponseSecretsOrFollowRedirects(t *testing.T) {
	for name, body := range map[string]string{
		"missing": `{"data":{}}`, "git token": `{"data":{"token":"git-secret"}}`,
		"empty": `{"data":{"token":"gk_"}}`, "invalid": `{"data":`,
		"oversize": strings.Repeat("s", (64<<10)+1),
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := New("https://login.example", "https://hub.example", transportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			identity := gatewayTestIdentity(s)
			if key, err := s.GatewayAPIKey(t.Context(), identity, url.Values{"jwt": {"login-secret"}}); key != "" || !errors.Is(err, ErrGatewayCredentials) {
				t.Fatal("accepted invalid key or leaked response")
			}
		})
	}
	attacker := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("credential redirect followed") }))
	defer attacker.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, attacker.URL, 307) }))
	defer origin.Close()
	s, _ := New("https://login.example", origin.URL, origin.Client().Transport)
	if key, err := s.GatewayAPIKey(t.Context(), gatewayTestIdentity(s), url.Values{"jwt": {"login-secret"}}); key != "" || err != ErrGatewayCredentials {
		t.Fatal("redirect accepted")
	}
	s.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("login-secret") })
	if _, err := s.GatewayAPIKey(t.Context(), gatewayTestIdentity(s), url.Values{"jwt": {"login-secret"}}); err != ErrGatewayCredentials {
		t.Fatal("transport details escaped")
	}
}
