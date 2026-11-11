//go:build integration || e2e

package room

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Chromium and TeamX HTTP/CLI are real; GitLab OAuth is simulated. Real remote
// authorization and model continuation have separate operator-driven evidence.
func TestGitLabConnectConversationBrowser(t *testing.T) {
	gitlabConnectConversationBrowser(t, connectFixture, false)
}
func TestDefaultAssistantGitLabConnectConversationBrowser(t *testing.T) {
	gitlabConnectConversationBrowser(t, assistantConnectFixture, false)
}
func TestGitLabConnectFirstApplicationConversationBrowser(t *testing.T) {
	gitlabConnectConversationBrowser(t, assistantConnectFixture, true)
}
func gitlabConnectConversationBrowser(t *testing.T, fixture platformConnectionFixture, firstApplication bool) {
	if os.Getenv("TEAMX_CONNECT_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	t.Setenv("USER", humanCaller.ID)
	g, svc, member, req := gitlabConnectFixtureFor(t, fixture)
	m := g.h.m
	server := httptest.NewServer(m)
	defer server.Close()
	m.config.endpoint = server.URL
	m.mu.Lock()
	file, _ := m.platforms.Load()
	file.OAuthApplications[0].RedirectURI = server.URL + gitlabCallbackPath
	if firstApplication {
		file.OAuthApplications = nil
		req.Input.ApplicationID = ""
		original := m.platformHTTP
		m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/api/v4/user/applications" {
				return original.RoundTrip(r)
			}
			body := fmt.Sprintf(`[{"id":41,"application_id":"private-client-id","application_name":"TeamX fixture","callback_url":%q,"confidential":true}]`, m.config.endpoint+gitlabCallbackPath)
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
		})
	}
	err := m.store.save(m.platformsPathForTest(), file, 2<<20)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	binary := productionFixtureCLI(t)
	env := cliToolEnv(server.URL, svc, member)
	cliEnv := os.Environ()
	for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
		cliEnv = append(cliEnv, key+"="+env(key))
	}
	start := exec.Command(binary, "platforms", "connect", "start", "--provider", "gitlab", "--name", req.Input.Name, "--origin", req.Input.Origin, "--application-id", req.Input.ApplicationID, "--account-username", "tester", "--operation-id", req.OperationID, "--source-seq", "1", "--notify", "inbox")
	start.Env = cliEnv
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	browser := exec.CommandContext(ctx, "node", "testdata/platform_connect_gitlab_browser.cjs", server.URL, req.OperationID, req.RoomInstance, req.Input.Origin, fmt.Sprint(firstApplication))
	browser.Env = os.Environ()
	if out, err := browser.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	} else {
		t.Log(string(out))
	}
	done := connectHTTP(t, m, "status", connectObservation(req))
	if done.Status != "completed" || g.posts.Load() != 1 {
		t.Fatal(done)
	}
	if n := waitConnectDelivery(t, m, done.ID); n.Event.EventType != "operation_completed" {
		t.Fatal(n)
	}
}
