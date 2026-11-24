//go:build e2e

package room

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlaboauth"
)

func TestServiceGitLabBrowserApplicationSelectionAndAuthorization(t *testing.T) {
	if os.Getenv("TEAMX_GITLAB_SERVICE_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	f := managedGitLabFixture(t)
	g := f.users[0]
	m := g.h.m
	access, _, _ := accessFixture(t)
	access.disabled = true
	bindFixtureAccessSubject(access, humanOperator())
	m.config.access = access
	protected := access.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := spaceRoute(access.space.SpaceID)
		if strings.HasPrefix(r.URL.Path, prefix+"/") {
			r = r.Clone(r.Context())
			copyURL := *r.URL
			copyURL.Path = strings.TrimPrefix(copyURL.Path, prefix)
			r.URL = &copyURL
		}
		m.ServeHTTP(w, r)
	}))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/spaces" {
			_ = writeJSON(w, map[string]any{"can_create": true, "default_space_id": m.config.access.space.SpaceID, "spaces": []map[string]any{{"id": m.config.access.space.SpaceID, "name": "Work", "status": "active", "runtime_state": "ready", "capabilities": map[string]bool{"managed_repositories": false, "gitlab": true, "gitlab_application_management": false, "scion": false, "computer": false}}}})
			return
		}
		protected.ServeHTTP(w, r)
	}))
	endpoint := "http://" + server.Listener.Addr().String()
	access.internal, access.public, access.activeListen = endpoint, endpoint, server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	m.config.endpoint = server.URL
	snapshot := gitlaboauth.Snapshot{Version: 1, Applications: []gitlaboauth.Application{f.app}}
	snapshot.Applications[0].RedirectURI = server.URL + gitlabCallbackPath
	snapshot.Applications[0].Secret = "private-client-secret"
	catalog, err := gitlaboauth.Open(gitlaboauth.Store{Load: func() (gitlaboauth.Snapshot, error) { return snapshot, nil }, Save: func(s gitlaboauth.Snapshot) error { snapshot = s; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := catalog.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	m.config.gitlabApplications = catalog.ForSpace(humanOperator(), m.config.access.space.SpaceID)
	m.platforms.SetOAuthOwner(humanOperator(), m.config.access.space.SpaceID)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/gitlab_service_applications_browser.cjs", server.URL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		m.mu.Lock()
		states := map[string]string{}
		for id, session := range m.gitlabAuthSessions {
			states[id] = session.Status + ":" + session.ErrorCode
		}
		m.mu.Unlock()
		t.Fatalf("browser: %v\n%s\n%v", err, out, states)
	}
	t.Log(strings.TrimSpace(string(out)))
	file := mustSnapshot(t, m)
	if len(file.OAuthCredentials) != 1 || file.OAuthCredentials[0].UserID != 201 || file.OAuthApplications[0].ClientSecret != "" {
		t.Fatal("browser did not save private grant")
	}
}
