package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/registry"
)

// Use a real cookie jar through the production Space router: manually adding
// cookies to a callback would conceal a cookie Path that excludes that URL.
func TestGitLabCallbackRoutesPersonalSpaces(t *testing.T) {
	home := t.TempDir()
	s, endpoint, stop := startSpaceServerFixture(t, home)
	id := strings.Repeat("b", 32)
	if _, err := s.create(spaceChangeInput{RequestID: id, Name: "Work"}); err != nil {
		t.Fatal(err)
	}
	waitSpaceState(t, s, id, "setup_required")
	model := setupModelInput{RequestID: "work-model", ExpectedVersion: 1, ModelParameters: modelTestEntry("work").ModelParameters, APIKey: "fixture-key"}
	if status := spaceHTTPRequest(t, endpoint, spaceRoute(id)+"/v1/setup/model", model, nil); status != 202 {
		t.Fatal("prepare second Space", status)
	}
	runtimes := []*SpaceRuntime{s.initial, waitSpaceState(t, s, id, "ready")}
	type browserAuth struct {
		client *http.Client
		req    gitlabAuthRequest
		view   gitlabAuthView
		state  string
		posts  atomic.Int32
	}
	auths := []*browserAuth{{}, {}}
	request := func(client *http.Client, path string, input any) (*http.Response, []byte) {
		t.Helper()
		method := http.MethodGet
		var body io.Reader
		if input != nil {
			data, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			method, body = http.MethodPost, bytes.NewReader(data)
		}
		r, err := http.NewRequest(method, endpoint+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if input != nil {
			r.Header.Set("Origin", endpoint)
			r.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response, data
	}
	start := func(i int) {
		t.Helper()
		a, runtime := auths[i], runtimes[i]
		response, data := request(a.client, spaceRoute(runtime.SpaceID)+gitlabCookiePath+"login", a.req)
		if response.StatusCode != 200 || json.Unmarshal(data, &a.view) != nil {
			t.Fatal("start authorization", response.StatusCode, string(data))
		}
		cookies := response.Cookies()
		if len(cookies) != 1 || cookies[0].Path != "/" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
			t.Fatal("authorization cookie cannot reach the shared callback")
		}
		jump, err := url.Parse(a.view.URL)
		if err != nil || jump.Query().Get("redirect_uri") != endpoint+gitlabCallbackPath {
			t.Fatal("authorization lost the fixed callback")
		}
		a.state = jump.Query().Get("state")
		if bound, ok := s.gitlabCallbacks.lookup(a.state); !ok || bound != runtime.SpaceContext {
			t.Fatal("authorization lost its original runtime")
		}
	}
	callback := func(state string) string {
		return gitlabCallbackPath + "?" + url.Values{"state": {state}, "code": {"fixture-code"}}.Encode()
	}
	for i, runtime := range runtimes {
		a, m := auths[i], runtime.manager
		jar, _ := cookiejar.New(nil)
		a.client = &http.Client{Jar: jar, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		app := registry.OAuthApplication{ID: strings.Repeat("d", 32), Origin: "https://gitlab.example", OwnerID: 999, ClientID: "fixture-client", ClientSecret: "private-secret", RedirectURI: endpoint + gitlabCallbackPath, Scopes: []string{"api"}}
		m.mu.Lock()
		file, err := m.platforms.Load()
		if err == nil {
			file.OAuthApplications = []registry.OAuthApplication{app}
			err = m.store.save(m.platformsPathForTest(), file, 2<<20)
		}
		m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
			body := ""
			switch r.URL.Path {
			case "/oauth/token":
				a.posts.Add(1)
				_ = r.ParseForm()
				if r.Form.Get("redirect_uri") != endpoint+gitlabCallbackPath || r.Form.Get("client_secret") != app.ClientSecret || len(r.Form.Get("code_verifier")) != 43 {
					t.Error("invalid confidential PKCE exchange")
				}
				body = `{"access_token":"private-access","refresh_token":"private-refresh","token_type":"bearer","expires_in":600}`
			case "/oauth/token/info":
				body = fmt.Sprintf(`{"resource_owner_id":%d,"scope":["api"],"application":{"uid":"fixture-client"}}`, 101+i)
			case "/api/v4/user":
				body = fmt.Sprintf(`{"id":%d,"username":"user%d"}`, 101+i, i)
			default:
				return nil, fmt.Errorf("unexpected OAuth request: %s", r.URL.Path)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		a.req = gitlabAuthRequest{ServiceEpoch: m.epoch, Caller: Caller{Kind: "human", ID: runtime.OwnerUserID}, ApplicationID: app.ID, Origin: app.Origin, Name: "My GitLab", Scopes: []string{"api"}}
		start(i)
	}
	for _, check := range []struct {
		client *http.Client
		path   string
		status int
	}{
		{auths[0].client, callback("unknown-state"), 403},
		{auths[1].client, callback(auths[0].state), 403},
		{auths[0].client, callback(auths[1].state), 403},
		{auths[0].client, spaceRoute(id) + callback(auths[0].state), 404},
		{auths[1].client, "/v1/spaces/" + id + "/platforms/gitlab/callback?state=" + auths[1].state, 404},
	} {
		if response, _ := request(check.client, check.path, nil); response.StatusCode != check.status {
			t.Fatal("invalid callback admitted", response.StatusCode)
		}
	}
	if auths[0].posts.Load()+auths[1].posts.Load() != 0 {
		t.Fatal("invalid callback exchanged a code")
	}
	retired := runtimes[1].SpaceContext
	retired.RuntimeEpoch = "retired-runtime"
	if err := s.gitlabCallbacks.register("retired-state", retired, t.Context()); err != nil {
		t.Fatal(err)
	}
	if response, _ := request(auths[1].client, callback("retired-state"), nil); response.StatusCode != 403 {
		t.Fatal("callback rebound to a replacement runtime", response.StatusCode)
	}
	s.gitlabCallbacks.remove("retired-state")
	// Complete the non-default Space first, then the default Space. Root routing
	// must follow state even when the browser currently has another Space open.
	for _, i := range []int{1, 0} {
		a, runtime := auths[i], runtimes[i]
		response, _ := request(a.client, callback(a.state), nil)
		completion := spaceRoute(runtime.SpaceID) + gitlabCompletePath
		if response.StatusCode != 303 || response.Header.Get("Location") != completion {
			t.Fatal("callback returned to the wrong Space", response.StatusCode, response.Header.Get("Location"))
		}
		if _, ok := s.gitlabCallbacks.lookup(a.state); ok {
			t.Fatal("consumed callback retained a route")
		}
		response, data := request(a.client, completion, nil)
		if response.StatusCode != 200 || !bytes.Contains(data, []byte(spaceRoute(runtime.SpaceID)+"/#platform-settings")) || bytes.Contains(data, []byte("fixture-code")) {
			t.Fatal("completion lost its original Space or leaked a code")
		}
		statusReq := gitlabAuthRequest{ServiceEpoch: a.req.ServiceEpoch, Caller: a.req.Caller, SessionID: a.view.SessionID}
		response, data = request(a.client, spaceRoute(runtime.SpaceID)+gitlabCookiePath+"status", statusReq)
		var result gitlabAuthView
		if response.StatusCode != 200 || json.Unmarshal(data, &result) != nil || result.Status != "succeeded" {
			t.Fatal("scoped status lost the browser cookie", response.StatusCode, string(data))
		}
		file, err := runtime.manager.platforms.Load()
		if err != nil || len(file.OAuthCredentials) != 1 || file.OAuthCredentials[0].UserID != int64(101+i) {
			t.Fatal("grant committed to the wrong Space", err)
		}
		if response, _ := request(a.client, callback(a.state), nil); response.StatusCode != 403 || a.posts.Load() != 1 {
			t.Fatal("callback replay exchanged another code")
		}
		if i == 1 {
			other, err := runtimes[0].manager.platforms.Load()
			if err != nil || len(other.OAuthCredentials) != 0 {
				t.Fatal("non-default callback changed the default Space", err)
			}
		}
	}
	start(1)
	a := auths[1]
	response, _ := request(a.client, spaceRoute(id)+gitlabCookiePath+"cancel", gitlabAuthRequest{ServiceEpoch: a.req.ServiceEpoch, Caller: a.req.Caller, SessionID: a.view.SessionID})
	if response.StatusCode != 200 {
		t.Fatal("scoped cancel lost the browser cookie", response.StatusCode)
	}
	if response, _ := request(a.client, callback(a.state), nil); response.StatusCode != 403 || a.posts.Load() != 1 {
		t.Fatal("cancelled callback admitted work")
	}
	start(1)
	archiveSpaceForTest(t, s, id)
	if response, _ := request(a.client, callback(a.state), nil); response.StatusCode != 403 || a.posts.Load() != 1 {
		t.Fatal("archived callback admitted work")
	}
	start(0)
	oldState := auths[0].state
	stop()
	if _, ok := s.gitlabCallbacks.lookup(oldState); ok {
		t.Fatal("stopped runtime retained an authorization route")
	}
	restarted, _, _ := startSpaceServerFixture(t, home)
	if _, ok := restarted.gitlabCallbacks.lookup(oldState); ok {
		t.Fatal("restart restored a pending authorization route")
	}
}

func TestGitLabCallbackRoutesRejectDuplicateAndExpiredState(t *testing.T) {
	var routes gitlabCallbackRoutes
	space := SpaceContext{SpaceID: "original", OwnerUserID: "alice", RuntimeEpoch: "original-epoch"}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := routes.register("state", space, ctx); err != nil {
		t.Fatal(err)
	}
	if err := routes.register("state", SpaceContext{SpaceID: "foreign"}, t.Context()); err == nil {
		t.Fatal("duplicate state replaced its original runtime")
	}
	if bound, ok := routes.lookup("state"); !ok || bound != space {
		t.Fatal("duplicate registration changed the original binding")
	}
	cancel()
	if _, ok := routes.lookup("state"); ok {
		t.Fatal("expired route admitted work before manager settlement")
	}
}
