//go:build e2e

package room

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestGitLabProjectPickerBrowser(t *testing.T) {
	if os.Getenv("TEAMX_GITLAB_PROJECT_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	h, _ := newIndependentEventHarness(t)
	eventLiveService(t, h)
	h.m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/api/v4/projects" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		body := `[{"id":7,"name":"同名项目","path_with_namespace":"remote/project"}]`
		header := http.Header{}
		status := 200
		switch r.URL.Query().Get("search") {
		case "fail":
			status = 403
			body = `{}`
		case "missing":
			body = `[]`
		case "target":
			body = `[{"id":8,"name":"同名项目","path_with_namespace":"remote/other"}]`
		default:
			if r.URL.Query().Get("page") == "2" {
				body = `[{"id":8,"name":"同名项目","path_with_namespace":"remote/other"}]`
			} else {
				header.Set("X-Next-Page", "2")
			}
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	server := httptest.NewServer(h.m)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/gitlab_projects_browser.cjs", server.URL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}
