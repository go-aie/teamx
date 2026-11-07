package gitlab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestOAuthTypedAuthenticationHTTPBoundary(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/api/v4/user":
			if r.Header.Get("Authorization") != "Bearer access-fixture" || r.Header.Get("PRIVATE-TOKEN") != "" {
				t.Error("OAuth authentication header mismatch")
			}
			io.WriteString(w, `{"id":101,"username":"operator"}`)
		case "/api/v4/projects":
			if r.Header.Get("Authorization") != "Bearer access-fixture" || r.Header.Get("PRIVATE-TOKEN") != "" {
				t.Error("caller overrode credential")
			}
			io.WriteString(w, `{"message":"access-fixture"}`)
		case "/api/v4/redirect":
			w.Header().Set("Location", "https://external.invalid/")
			w.WriteHeader(302)
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	transport := server.Client().Transport
	credential := platform.AccessCredential{Kind: "oauth", Token: platform.NewSecret("access-fixture")}
	identity := VerifyIdentityWithCredential(context.Background(), server.URL, credential, transport)
	if identity.Status != "valid" || identity.UserID != 101 {
		t.Fatal(identity)
	}
	read := NewReadAPIWithCredential(server.URL, credential, transport)
	result, err := read(context.Background(), "/projects", nil, http.Header{"Authorization": {"Bearer caller-token"}, "Private-Token": {"caller-pat"}, "authorization": {"Bearer lowercase-token"}, "private-token": {"lowercase-pat"}}, 4096)
	if err != nil || strings.Contains(string(result.Data), "access-fixture") {
		t.Fatal("read/redaction failed", err)
	}

	if _, err = read(context.Background(), "/redirect", nil, nil, 4096); err == nil {
		t.Fatal("redirect accepted")
	}
	count := hits.Load()
	for _, kind := range []string{"", "bearer", "unknown"} {
		_, err = NewReadAPIWithCredential(server.URL, platform.AccessCredential{Kind: kind, Token: credential.Token}, transport)(context.Background(), "/projects", nil, nil, 4096)
		if err == nil {
			t.Fatal("inferred token type")
		}
	}
	if hits.Load() != count {
		t.Fatal("invalid authentication reached transport")
	}
	if v := VerifyIdentityWithCredential(context.Background(), "http://insecure.invalid", credential, transport); v.Status == "valid" {
		t.Fatal("insecure origin accepted")
	}
}

func TestOAuthRefreshProtocol(t *testing.T) {
	for _, mode := range []string{"success", "invalid_grant", "invalid_type", "malformed", "redirect", "scope_changed", "info_user_changed", "application_changed", "user_changed", "info_failed", "oversized", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/oauth/token":
					posts.Add(1)
					if r.Method != "POST" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.ParseForm() != nil || r.PostForm.Get("client_secret") != "client-secret" || r.PostForm.Get("refresh_token") != "refresh-fixture" || r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("redirect_uri") != "http://127.0.0.1:8088/callback" {
						t.Error("invalid refresh exchange")
					}
					switch mode {
					case "disconnect":
						h := w.(http.Hijacker)
						conn, _, _ := h.Hijack()
						conn.Close()
						return
					case "invalid_grant":
						w.WriteHeader(400)
						io.WriteString(w, `{"error":"invalid_grant","error_description":"client-secret refresh-fixture"}`)
						return
					case "invalid_type":
						io.WriteString(w, `{"access_token":"candidate-access","refresh_token":"candidate-refresh","token_type":"mac","expires_in":600}`)
						return
					case "malformed":
						io.WriteString(w, `{"access_token":"candidate-access"}`)
						return
					case "oversized":
						io.WriteString(w, strings.Repeat("s", (64<<10)+1))
						return
					case "redirect":
						http.Redirect(w, r, "https://elsewhere.invalid", 307)
						return
					}
					io.WriteString(w, `{"access_token":"candidate-access","refresh_token":"candidate-refresh","token_type":"Bearer","expires_in":600}`)
				case "/oauth/token/info":
					if r.Header.Get("Authorization") != "Bearer candidate-access" || r.Header.Get("PRIVATE-TOKEN") != "" {
						t.Error("token info header")
					}
					if mode == "info_failed" {
						w.WriteHeader(500)
						io.WriteString(w, "candidate-access client-secret")
						return
					}
					scope := "api"
					if mode == "scope_changed" {
						scope = "read_api"
					}
					id := 101
					if mode == "info_user_changed" {
						id = 102
					}
					clientID := "fixture-client"
					if mode == "application_changed" {
						clientID = "other-client"
					}
					fmt.Fprintf(w, `{"resource_owner_id":%d,"scope":[%q],"application":{"uid":%q}}`, id, scope, clientID)
				case "/api/v4/user":
					if r.Header.Get("Authorization") != "Bearer candidate-access" || r.Header.Get("PRIVATE-TOKEN") != "" {
						t.Error("user header")
					}
					id := 101
					if mode == "user_changed" {
						id = 102
					}
					fmt.Fprintf(w, `{"id":%d,"username":"operator"}`, id)
				default:
					t.Error("unexpected endpoint")
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			input := OAuthRefreshRequest{Origin: server.URL, ClientID: "fixture-client", ClientSecret: platform.NewSecret("client-secret"), RefreshToken: platform.NewSecret("refresh-fixture"), RedirectURI: "http://127.0.0.1:8088/callback", UserID: 101, Scopes: "api"}
			start := time.Now()
			tokens, err := RefreshOAuth(context.Background(), input, server.Client().Transport)
			if posts.Load() != 1 {
				t.Fatal("refresh POST count", posts.Load())
			}
			if mode == "success" {
				if err != nil || tokens.AccessToken.Reveal() != "candidate-access" || tokens.RefreshToken.Reveal() != "candidate-refresh" || tokens.ExpiresAt.Before(start.Add(599*time.Second)) || tokens.ExpiresAt.After(time.Now().Add(600*time.Second)) {
					t.Fatal("bad refresh result", err)
				}
			} else {
				if err == nil {
					t.Fatal("invalid refresh accepted")
				}
				want := "refresh_unknown"
				if mode == "invalid_grant" || mode == "scope_changed" || mode == "info_user_changed" || mode == "application_changed" || mode == "user_changed" {
					want = "reauthorization_required"
				}
				if OAuthRefreshState(err) != want {
					t.Fatal("wrong failure disposition", err)
				}
				for _, s := range []string{"client-secret", "refresh-fixture", "candidate-access", "candidate-refresh"} {
					if strings.Contains(err.Error(), s) {
						t.Fatal("diagnostic leak")
					}
				}
			}
		})
	}
}

type oauthFailTransport struct{ calls int }

func (tr *oauthFailTransport) RoundTrip(*http.Request) (*http.Response, error) {
	tr.calls++
	return nil, errors.New("client-secret refresh-fixture")
}
func TestOAuthRefreshTransportErrorsAreSafe(t *testing.T) {
	tr := &oauthFailTransport{}
	_, err := RefreshOAuth(context.Background(), OAuthRefreshRequest{Origin: "https://gitlab.invalid", ClientID: "client", ClientSecret: platform.NewSecret("client-secret"), RefreshToken: platform.NewSecret("refresh-fixture"), RedirectURI: "http://127.0.0.1/callback", UserID: 1, Scopes: "api"}, tr)
	if err == nil || strings.Contains(err.Error(), "client-secret") || tr.calls != 1 {
		t.Fatal("unsafe or replayed transport error", err)
	}
}
