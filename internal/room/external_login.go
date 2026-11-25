package room

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/authn"
)

const externalLoginPath = "/v1/access/sso/login"
const externalCallbackPath = "/v1/access/sso/callback"
const externalAttemptCookie = "__Host-teamx-login-attempt"
const externalLocalAttemptCookie = "teamx-local-login-attempt"
const externalAttemptLifetime = 5 * time.Minute

// The host owns browser binding and atomic consumption. A provider cannot
// choose a local user or issue a session. Pending attempts deliberately expire
// on restart; no provider token is persisted here.
type externalLogin struct {
	mu       sync.Mutex
	provider authn.Provider
	origin   string
	callback string
	now      func() time.Time
	attempts map[[32]byte]externalAttempt
	window   time.Time
	started  int
	// admit is the local user/session owner's commit boundary. Success must
	// only be returned after durable admission and session publication.
	admit func(context.Context, http.ResponseWriter, authn.Identity) error
}

type externalAttempt struct {
	browser [32]byte
	expires time.Time
}

func newExternalLogin(provider authn.Provider, origin string, admit func(context.Context, http.ResponseWriter, authn.Identity) error) (*externalLogin, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" && !authn.LoopbackHTTP(u) || u.Host == "" || u.Path != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || provider == nil || admit == nil {
		return nil, errors.New("external login requires a provider, durable admission and HTTPS or literal loopback origin")
	}
	return &externalLogin{provider: provider, origin: origin, callback: origin + externalCallbackPath, now: time.Now, attempts: map[[32]byte]externalAttempt{}, admit: admit}, nil
}

func (l *externalLogin) cookie(value string, age int) *http.Cookie {
	u, _ := url.Parse(l.origin)
	secure := !authn.LoopbackHTTP(u)
	name := externalAttemptCookie
	if !secure {
		name = externalLocalAttemptCookie
	}
	return &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: age, Secure: secure, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

// ServeHTTP must run behind the deployment's existing trusted proxy/origin
// boundary. It handles only these two paths, before browser authentication.
func (l *externalLogin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	switch r.URL.Path {
	case externalLoginPath:
		if r.Method != http.MethodPost || r.Header.Get("Origin") != l.origin {
			accessError(w, 403, "invalid_origin")
			return
		}
		state, err := randomAccessToken()
		if err != nil {
			accessError(w, 503, "login_unavailable")
			return
		}
		browser, err := randomAccessToken()
		if err != nil {
			accessError(w, 503, "login_unavailable")
			return
		}
		callback := l.callback + "?" + url.Values{"state": {state}}.Encode()
		login, err := l.provider.LoginURL(callback)
		if err != nil {
			accessError(w, 503, "login_unavailable")
			return
		}
		l.mu.Lock()
		now := l.now()
		if l.window.IsZero() || !now.Before(l.window.Add(time.Minute)) {
			l.window = now
			l.started = 0
		}
		for key, attempt := range l.attempts {
			if !now.Before(attempt.expires) {
				delete(l.attempts, key)
			}
		}
		if len(l.attempts) >= 128 || l.started >= 60 {
			l.mu.Unlock()
			accessError(w, 429, "login_rate_limited")
			return
		}
		l.started++
		l.attempts[sha256.Sum256([]byte(state))] = externalAttempt{sha256.Sum256([]byte(browser)), now.Add(externalAttemptLifetime)}
		l.mu.Unlock()
		http.SetCookie(w, l.cookie(browser, int(externalAttemptLifetime/time.Second)))
		// Navigate separately from the same-origin POST: browsers apply a
		// document's form-action CSP to the entire external redirect chain.
		if r.Header.Get("Accept") == "application/json" {
			_ = writeJSON(w, map[string]string{"login_url": login})
			return
		}
		http.Redirect(w, r, login, http.StatusSeeOther)
	case externalCallbackPath:
		// Every callback clears sensitive URL parameters, including failure. The
		// host's access page can render this fixed, non-sensitive error marker.
		finish := func(ok bool) {
			http.SetCookie(w, l.cookie("", -1))
			target := "/access?sso=failed"
			if ok {
				target = "/"
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
		}
		if r.Method != http.MethodGet || len(r.URL.RawQuery) > 24<<10 {
			finish(false)
			return
		}
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(values["state"]) != 1 || !validAccessToken(values.Get("state")) {
			finish(false)
			return
		}
		cookie, err := r.Cookie(l.cookie("", 0).Name)
		if err != nil || !validAccessToken(cookie.Value) {
			finish(false)
			return
		}
		key := sha256.Sum256([]byte(values.Get("state")))
		l.mu.Lock()
		attempt, exists := l.attempts[key]
		valid := exists && l.now().Before(attempt.expires) && attempt.browser == sha256.Sum256([]byte(cookie.Value))
		if valid {
			delete(l.attempts, key)
		}
		l.mu.Unlock()
		if !valid {
			finish(false)
			return
		}
		identity, err := l.provider.Complete(r.Context(), values)
		if err != nil || identity.Provider == "" || identity.Authority == "" || identity.Subject == "" {
			finish(false)
			return
		}
		if err := l.admit(r.Context(), w, identity); err != nil {
			finish(false)
			return
		}
		finish(true)
	default:
		http.NotFound(w, r)
	}
}
