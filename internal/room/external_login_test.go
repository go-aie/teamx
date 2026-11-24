package room

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/authn"
)

type loginProviderStub struct {
	calls atomic.Int32
	fail  bool
}

func (*loginProviderStub) LoginURL(callback string) (string, error) {
	return "https://login.example/sso/login?" + url.Values{"redirect_url": {callback}}.Encode(), nil
}
func (p *loginProviderStub) Complete(context.Context, url.Values) (authn.Identity, error) {
	p.calls.Add(1)
	if p.fail {
		return authn.Identity{}, errors.New("remote unavailable")
	}
	return authn.Identity{Provider: "test", Authority: "https://hub.example", Subject: "database-user"}, nil
}

func beginLogin(t *testing.T, login *externalLogin) (string, *http.Cookie) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "https://teamx.example"+externalLoginPath, nil)
	r.Header.Set("Origin", "https://teamx.example")
	w := httptest.NewRecorder()
	login.ServeHTTP(w, r)
	if w.Code != 303 {
		t.Fatalf("start = %d", w.Code)
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	callback := u.Query().Get("redirect_url")
	return callback, w.Result().Cookies()[0]
}

func callbackLogin(login *externalLogin, callback string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, callback+"&jwt_token=secret", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	login.ServeHTTP(w, r)
	return w
}

func TestExternalLoginOneUseConcurrentCallback(t *testing.T) {
	provider := &loginProviderStub{}
	var admissions atomic.Int32
	login, err := newExternalLogin(provider, "https://teamx.example", func(_ context.Context, _ http.ResponseWriter, id authn.Identity) error {
		if id.Subject != "database-user" {
			t.Error("wrong identity")
		}
		admissions.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	callback, cookie := beginLogin(t, login)
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Fatal("invalid browser binding")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { callbackLogin(login, callback, cookie) })
	}
	wg.Wait()
	if admissions.Load() != 1 || provider.calls.Load() != 1 {
		t.Fatal("replayed callback admitted")
	}
}

func TestExternalLoginBrowserExpiryAndFailure(t *testing.T) {
	for _, kind := range []string{"browser", "expired", "remote", "local"} {
		t.Run(kind, func(t *testing.T) {
			provider := &loginProviderStub{fail: kind == "remote"}
			var commits int
			login, _ := newExternalLogin(provider, "https://teamx.example", func(context.Context, http.ResponseWriter, authn.Identity) error {
				commits++
				return errors.New("durable write failed")
			})
			callback, cookie := beginLogin(t, login)
			if kind == "browser" {
				other, _ := randomAccessToken()
				cookie.Value = other
			}
			if kind == "expired" {
				login.now = func() time.Time { return time.Now().Add(externalAttemptLifetime) }
			}
			w := callbackLogin(login, callback, cookie)
			if w.Code != 303 || w.Header().Get("Location") != "/access?sso=failed" || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatal("unsafe failure response")
			}
			if kind != "local" && commits != 0 {
				t.Fatal("failure admitted user")
			}
			if kind == "local" && commits != 1 {
				t.Fatal("missing durable admission")
			}
			if kind == "local" || kind == "remote" {
				callbackLogin(login, callback, cookie)
				if provider.calls.Load() != 1 {
					t.Fatal("failed exchange replayed")
				}
			}
		})
	}
}

func TestExternalLoginOriginAndPendingLimit(t *testing.T) {
	login, _ := newExternalLogin(&loginProviderStub{}, "https://teamx.example", func(context.Context, http.ResponseWriter, authn.Identity) error { return nil })
	w := httptest.NewRecorder()
	login.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "https://teamx.example"+externalLoginPath, nil))
	if w.Code != 403 {
		t.Fatal("missing CSRF check")
	}
	for range 60 {
		beginLogin(t, login)
	}
	r := httptest.NewRequest(http.MethodPost, "https://teamx.example"+externalLoginPath, nil)
	r.Header.Set("Origin", "https://teamx.example")
	w = httptest.NewRecorder()
	login.ServeHTTP(w, r)
	if w.Code != 429 {
		t.Fatal("pending attempts unbounded")
	}
}

func TestExternalLoginAllowsOnlyLiteralLocalHTTP(t *testing.T) {
	admit := func(context.Context, http.ResponseWriter, authn.Identity) error { return nil }
	for _, origin := range []string{"http://127.0.0.1:18088", "http://[::1]:18088"} {
		login, err := newExternalLogin(&loginProviderStub{}, origin, admit)
		if err != nil {
			t.Fatal(err)
		}
		cookie := login.cookie("browser", 300)
		if cookie.Secure || cookie.Name != externalLocalAttemptCookie || !cookie.HttpOnly {
			t.Fatal("incorrect loopback cookie")
		}
	}
	for _, origin := range []string{"http://192.168.1.2:18088", "http://example.com", "http://localhost:18088", "http://127.0.0.1.attacker.example"} {
		if _, err := newExternalLogin(&loginProviderStub{}, origin, admit); err == nil {
			t.Fatalf("accepted non-literal HTTP loopback %s", origin)
		}
	}
}
