package gitlab

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func TestWorkspaceSharedProjectHTTPValidation(t *testing.T) {
	for _, kind := range []string{"valid", "forbidden", "redirect", "wrong-id", "wrong-path", "wrong-origin", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			count := 0
			transport := platformRoundTrip(func(r *http.Request) (*http.Response, error) {
				count++
				if r.URL.String() != "https://gitlab.invalid/api/v4/projects/group%2Fproject" || r.Header.Get("Authorization") != "Bearer private-test-token" {
					t.Fatal("wrong bound origin/auth")
				}
				body := `{"id":7,"path_with_namespace":"group/project","http_url_to_repo":"https://gitlab.invalid/group/project.git"}`
				status := 200
				switch kind {
				case "forbidden":
					status, body = 403, "private-test-token"
				case "redirect":
					status = 302
				case "wrong-id":
					body = strings.Replace(body, `"id":7`, `"id":0`, 1)
				case "wrong-path":
					body = strings.Replace(body, `"path_with_namespace":"group/project"`, `"path_with_namespace":"group/foreign"`, 1)
				case "wrong-origin":
					body = strings.Replace(body, "https://gitlab.invalid/group", "https://foreign.invalid/group", 1)
				case "malformed":
					body += "{}"
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Location": {"https://foreign.invalid"}}}, nil
			})
			id, err := VerifyProjectWithTransport(context.Background(), "https://gitlab.invalid", "group/project", platform.NewSecret("private-test-token"), transport)
			if kind == "valid" {
				if err != nil || id != 7 {
					t.Fatal(id, err)
				}
			} else if err == nil || strings.Contains(err.Error(), "private-test-token") {
				t.Fatal("accepted/reflected invalid remote result")
			}
			if count != 1 {
				t.Fatal("followed redirect or retried request")
			}
		})
	}
}
