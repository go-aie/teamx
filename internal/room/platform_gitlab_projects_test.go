//go:build integration || e2e

package room

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGitLabProjectPickerBeforeSubscription(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "project-picker")
	c := verifyTodoAccount(t, m, platformTestSave(t, m, platformTestRequest(m)), 101)
	installOAuthFixture(t, m, c, time.Now().Add(time.Second))
	var refreshes atomic.Int32
	refresh := oauthTransport(&refreshes, nil)
	reads := 0
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/oauth/") || r.URL.Path == "/api/v4/user" {
			return refresh.RoundTrip(r)
		}
		reads++
		if r.Method != "GET" || r.URL.Path != "/api/v4/projects" || r.URL.Query().Get("search") != "team/project" || r.URL.Query().Get("page") != "2" || r.Header.Get("Authorization") != "Bearer rotated-access" {
			t.Fatal("wrong project read", r.Method, r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Next-Page": {"3"}}, Body: io.NopCloser(strings.NewReader(`[{"id":7,"name":"Project","path_with_namespace":"team/project","description":"private-extra"}]`))}, nil
	})
	req := gitlabProjectsRequest{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, PlatformID: c.ID, PlatformVersion: c.Version, Search: " team/project ", Page: 2}
	response := managerHTTP(m, "/v1/platform/gitlab/projects", req)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"next_page":3`) || strings.Contains(response.Body.String(), "private-extra") || strings.Contains(response.Body.String(), "access") {
		t.Fatal(response.Code, response.Body.String())
	}
	if reads != 1 || refreshes.Load() != 1 {
		t.Fatal("unexpected request count", reads, refreshes.Load())
	}
	m.mu.Lock()
	states := m.notifications.StatesLocked(room.RoomIdentity)
	m.mu.Unlock()
	if len(states) != 0 {
		t.Fatal("browsing created a subscription")
	}
	assertActionCallsSettled(t, m)
	for _, mutate := range []func(*gitlabProjectsRequest){
		func(r *gitlabProjectsRequest) { r.Caller.Kind = "agent" },
		func(r *gitlabProjectsRequest) { r.ServiceEpoch = "stale" },
		func(r *gitlabProjectsRequest) { r.RoomInstance = "stale" },
		func(r *gitlabProjectsRequest) { r.PlatformVersion++ },
		func(r *gitlabProjectsRequest) { r.Search = "bad\nsearch" },
		func(r *gitlabProjectsRequest) { r.Page = -1 },
		func(r *gitlabProjectsRequest) { r.Page = 1001 },
	} {
		bad := req
		mutate(&bad)
		if got := managerHTTP(m, "/v1/platform/gitlab/projects", bad); got.Code == 200 {
			t.Fatal("invalid project read accepted")
		}
	}
	if reads != 1 {
		t.Fatal("invalid request reached GitLab")
	}
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest("GET", "/v1/platform/gitlab/projects", nil))
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
}

func TestGitLabProjectPickerDiscardsRevokedAccount(t *testing.T) {
	parallelFixtureTest(t)
	h, c := newIndependentEventHarness(t)
	a := installOAuthFixture(t, h.m, c, time.Now().Add(time.Hour))
	h.m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) {
		h.m.mu.Lock()
		_, _, err := h.m.platforms.LogoutOAuth(a.Authorization.CredentialID)
		h.m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`[{"id":7,"name":"Project","path_with_namespace":"team/project"}]`))}, nil
	})
	req := gitlabProjectsRequest{ServiceEpoch: h.m.epoch, RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, PlatformID: c.ID, PlatformVersion: c.Version}
	w := managerHTTP(h.m, "/v1/platform/gitlab/projects", req)
	if w.Code == 200 || strings.Contains(w.Body.String(), "team/project") {
		t.Fatal("revoked account result disclosed", w.Code, w.Body.String())
	}
	assertActionCallsSettled(t, h.m)
}
