package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

type gitlabLoginHarness struct {
	h              *gitlabSetupHarness
	req            gitlabAuthRequest
	posts, revokes atomic.Int32
	user           atomic.Int64
	before         func(*http.Request)
	revokeStatus   atomic.Int32
}

func newGitlabLoginHarness(t *testing.T) *gitlabLoginHarness {
	t.Helper()
	h := newGitlabSetupHarness(t)
	init := gitlabSetupHTTP(t, h, "prepare", h.req)
	g := &gitlabLoginHarness{h: h, req: gitlabAuthRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: h.req.ID, Version: h.req.Version, ApplicationID: init.ApplicationID, Affected: []gitlabAuthConnection{{ID: h.req.ID, Version: h.req.Version}}}}
	g.user.Store(101)
	g.revokeStatus.Store(200)
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		status := 200
		body := ""
		switch r.URL.Path {
		case "/oauth/token":
			g.posts.Add(1)
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("client_secret") != "private-client-secret" || r.Form.Get("redirect_uri") != h.m.config.endpoint+gitlabCallbackPath || len(r.Form.Get("code_verifier")) != 43 {
				t.Error("invalid confidential PKCE exchange")
			}
			if g.before != nil {
				g.before(r)
			}
			body = `{"access_token":"candidate-access","refresh_token":"candidate-refresh","token_type":"bearer","expires_in":600}`
		case "/oauth/token/info":
			body = fmt.Sprintf(`{"resource_owner_id":%d,"scope":["api"],"application":{"uid":"private-client-id"}}`, g.user.Load())
		case "/api/v4/user":
			body = fmt.Sprintf(`{"id":%d,"username":"tester"}`, g.user.Load())
		case "/oauth/revoke":
			g.revokes.Add(1)
			status = int(g.revokeStatus.Load())
			body = `{}`
		default:
			return nil, errors.New("unexpected auth endpoint")
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	return g
}
func gitlabAuthHTTP(m *roomManager, action string, req gitlabAuthRequest, cookie *http.Cookie) *httptest.ResponseRecorder {
	data, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, m.config.endpoint+gitlabCookiePath+action, bytes.NewReader(data))
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w
}
func (g *gitlabLoginHarness) login(t *testing.T) (gitlabAuthView, *http.Cookie) {
	t.Helper()
	w := gitlabAuthHTTP(g.h.m, "login", g.req, nil)
	var v gitlabAuthView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("missing browser cookie")
	}
	for _, secret := range []string{"private-client-secret", "candidate-refresh"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("secret exposed")
		}
	}
	return v, cookies[0]
}
func (g *gitlabLoginHarness) callback(v gitlabAuthView, c *http.Cookie, change func(url.Values)) *httptest.ResponseRecorder {
	u, _ := url.Parse(v.URL)
	q := url.Values{"state": {u.Query().Get("state")}, "code": {"authorization-fixture"}}
	if change != nil {
		change(q)
	}
	r := httptest.NewRequest("GET", g.h.m.config.endpoint+gitlabCallbackPath+"?"+q.Encode(), nil)
	r.Header.Set("Origin", g.h.server.URL)
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	g.h.m.ServeHTTP(w, r)
	return w
}
func (g *gitlabLoginHarness) status(t *testing.T, v gitlabAuthView, c *http.Cookie) gitlabAuthView {
	t.Helper()
	w := gitlabAuthHTTP(g.h.m, "status", gitlabAuthRequest{ServiceEpoch: g.h.m.epoch, Caller: humanCaller, SessionID: v.SessionID}, c)
	var result gitlabAuthView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return result
}
