package gitlab

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func TestOAuthLoginConfidentialPKCEHTTPBoundary(t *testing.T) {
	for _, mode := range []string{"success", "unknown_owner", "account", "scope", "client", "invalid_grant", "redirect", "malformed", "partial_pair", "revoke_failed"} {
		t.Run(mode, func(t *testing.T) {
			var posts, revokes atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/oauth/token":
					posts.Add(1)
					_ = r.ParseForm()
					if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("client_id") != "login-client" || r.Form.Get("client_secret") != "private-secret" || r.Form.Get("code") != "private-code" || r.Form.Get("code_verifier") != strings.Repeat("v", 43) {
						t.Error("confidential PKCE form")
					}
					switch mode {
					case "invalid_grant":
						w.WriteHeader(400)
						io.WriteString(w, `{"error":"invalid_grant","error_description":"private-secret"}`)
						return
					case "redirect":
						http.Redirect(w, r, "https://elsewhere.invalid", 307)
						return
					case "malformed":
						io.WriteString(w, `private-secret`)
						return
					case "partial_pair":
						io.WriteString(w, `{"access_token":"login-access","token_type":"bearer","expires_in":600}`)
						return
					}
					io.WriteString(w, `{"access_token":"login-access","refresh_token":"login-refresh","token_type":"bearer","expires_in":600}`)
				case "/oauth/token/info":
					if r.Header.Get("Authorization") != "Bearer login-access" || r.Header.Get("PRIVATE-TOKEN") != "" {
						t.Error("wrong auth")
					}
					id := 101
					scope := "api"
					client := "login-client"
					if mode == "account" {
						id = 202
					}
					if mode == "scope" {
						scope = "read_user"
					}
					if mode == "client" {
						client = "foreign"
					}
					fmt.Fprintf(w, `{"resource_owner_id":%d,"scope":[%q],"application":{"uid":%q}}`, id, scope, client)
				case "/api/v4/user":
					io.WriteString(w, `{"id":101,"username":"tester"}`)
				case "/oauth/revoke":
					revokes.Add(1)
					_ = r.ParseForm()
					if r.Form.Get("token") != "login-access" || r.Form.Get("client_secret") != "private-secret" {
						t.Error("revoke form")
					}
					if mode == "revoke_failed" {
						w.WriteHeader(500)
					}
					io.WriteString(w, `{}`)
				default:
					t.Error("unexpected request", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			expected := int64(101)
			if mode == "unknown_owner" {
				expected = 0
			}
			tokens, identity, err := ExchangeOAuthCode(context.Background(), OAuthLoginRequest{Origin: server.URL, ClientID: "login-client", ClientSecret: platform.NewSecret("private-secret"), Code: platform.NewSecret("private-code"), Verifier: platform.NewSecret(strings.Repeat("v", 43)), RedirectURI: "http://127.0.0.1:8088/callback", UserID: expected, Scopes: "api"}, server.Client().Transport)
			success := mode == "success" || mode == "unknown_owner" || mode == "revoke_failed"
			if (err == nil) != success {
				t.Fatal("exchange disposition", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-secret") {
				t.Fatal("unsafe diagnostic")
			}
			if success && (identity.UserID != 101 || tokens.AccessToken.Reveal() != "login-access") {
				t.Fatal("unverified identity")
			}
			if mode == "partial_pair" && tokens.AccessToken.Reveal() != "login-access" {
				t.Fatal("lost cleanup material")
			}
			if posts.Load() != 1 {
				t.Fatal("code POST replay")
			}
			if success {
				err = RevokeOAuth(context.Background(), server.URL, "login-client", platform.NewSecret("private-secret"), tokens.AccessToken, server.Client().Transport)
				if (err != nil) != (mode == "revoke_failed") || revokes.Load() != 1 {
					t.Fatal("revocation disposition", err)
				}
			}
		})
	}
}
