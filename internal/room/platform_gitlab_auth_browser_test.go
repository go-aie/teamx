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
)

func TestGitLabAuthBrowser(t *testing.T) {
	if os.Getenv("TEAMX_G3_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	g := newGitlabLoginHarness(t)
	m := g.h.m
	server := httptest.NewServer(m)
	defer server.Close()
	m.config.endpoint = server.URL
	m.mu.Lock()
	file, _ := m.platforms.Load()
	file.OAuthApplications[0].RedirectURI = server.URL + gitlabCallbackPath

	if err := m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	original := m.platformHTTP
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasPrefix(r.URL.Path, "/.well-known/") || r.URL.Path == "/api/v4/user/applications" {
			return g.h.server.Client().Transport.RoundTrip(r)
		}
		return original.RoundTrip(r)
	})
	m.mu.Unlock()
	g.h.mu.Lock()
	if g.h.remote == nil {
		g.h.remote = map[string]any{}
	}
	g.h.remote["callback_url"] = server.URL + gitlabCallbackPath
	g.h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/gitlab_oauth_browser.cjs", server.URL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
