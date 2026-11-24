package opencsg

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/authn"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var testExpiry = time.Now().Add(time.Hour).Unix()

func testToken(user string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"current_user":"`+user+`","uuid":"attacker-selected","exp":`+strconv.FormatInt(testExpiry, 10)+`}`)) + ".signature"
}

func TestSSOOwnerCheckedIdentity(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/user/alice/tokens" || r.URL.RawQuery != "app=git" || r.Header.Get("Authorization") != "Bearer "+testToken("alice") {
			t.Errorf("unexpected exchange request")
			w.WriteHeader(403)
			return
		}
		io.WriteString(w, `{"data":[{"user_name":"alice","user_uuid":"database-uuid","token":"business-secret"}]}`)
	}))
	defer server.Close()
	s, err := New("https://login.example", server.URL, server.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	callback := "https://teamx.example/v1/access/sso/callback?state=random-attempt"
	login, err := s.LoginURL(callback)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(login)
	if u.Path != "/sso/login" || u.Query().Get("redirect_url") != callback {
		t.Fatal("lost host callback binding")
	}
	identity, err := s.Complete(context.Background(), url.Values{"jwt_token": {testToken("alice")}, "csghub_base_url": {"https://attacker.example"}})
	if err != nil {
		t.Fatal(err)
	}
	if identity != (authn.Identity{Provider: "opencsg", Authority: server.URL, Subject: "database-uuid", DisplayName: "alice", ValidUntil: time.Unix(testExpiry, 0)}) {
		t.Fatalf("unexpected identity: %+v", identity)
	}
}

func TestSSORejectsUnprovenIdentity(t *testing.T) {
	for name, body := range map[string]string{
		"empty":        `{"data":[]}`,
		"no UUID":      `{"data":[{"user_name":"alice","token":"secret"}]}`,
		"other user":   `{"data":[{"user_name":"bob","user_uuid":"b"}]}`,
		"mixed UUID":   `{"data":[{"user_name":"alice","user_uuid":"a"},{"user_name":"alice","user_uuid":"b"}]}`,
		"invalid JSON": `{"data":`,
		"oversize":     strings.Repeat(" ", (1<<20)+1),
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := New("https://login.example", "https://hub.example", transportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			if _, err := s.Complete(context.Background(), url.Values{"jwt": {testToken("alice")}}); !errors.Is(err, ErrAuthentication) {
				t.Fatal("accepted unproven subject")
			}
		})
	}
}

func TestSSORejectsRedirectAndSanitizesFailure(t *testing.T) {
	var leaked bool
	attacker := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer attacker.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, attacker.URL, 307) }))
	defer origin.Close()
	s, _ := New("https://login.example", origin.URL, origin.Client().Transport)
	if _, err := s.Complete(context.Background(), url.Values{"jwt": {testToken("alice")}}); !errors.Is(err, ErrAuthentication) || leaked {
		t.Fatal("redirect accepted")
	}
	s.client.Transport = transportFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("secret transport error") })
	if _, err := s.Complete(context.Background(), url.Values{"jwt": {testToken("alice")}}); err != ErrAuthentication {
		t.Fatal("transport details escaped")
	}
}

func TestSSORejectsAmbiguousCallbackBeforeNetwork(t *testing.T) {
	s, _ := New("https://login.example", "https://hub.example", transportFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected request"); return nil, nil }))
	for _, values := range []url.Values{
		{}, {"jwt": {"malformed"}}, {"jwt": {testToken("../bob")}},
		{"jwt": {testToken("alice"), testToken("bob")}},
		{"jwt": {testToken("alice")}, "jwt_token": {testToken("bob")}},
	} {
		if _, err := s.Complete(context.Background(), values); err != ErrAuthentication {
			t.Fatal("accepted ambiguous input")
		}
	}
}

func TestSSOFixedHTTPSOrigins(t *testing.T) {
	for _, origin := range []string{"http://hub.example", "https://hub.example/", "https://user@hub.example", "https://hub.example?", "https://hub.example?q=x", "https://hub.example/#secret"} {
		if _, err := New("https://login.example", origin, nil); err == nil {
			t.Fatalf("accepted %q", origin)
		}
	}
}
