package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

type platformRoundTrip func(*http.Request) (*http.Response, error)

func (f platformRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestPlatformGitLabVerificationTransport(t *testing.T) {
	for _, tc := range []struct {
		status     int
		body, code string
	}{
		{200, `{"id":1,"username":"operator","name":"extra"}`, ""},
		{200, `{"id":0,"username":"operator"}`, "invalid_response"},
		{200, `{"id":-1,"username":"operator"}`, "invalid_response"},
		{200, `{"id":"1","username":"operator"}`, "invalid_response"},
		{200, `{"username":"operator"}`, "invalid_response"},
		{401, `test-platform-secret`, "credential_rejected"},
		{403, `test-platform-secret`, "credential_rejected"},
		{500, `test-platform-secret`, "query_failed"},
		{200, `{"id":1,"username":"test-platform-secret"}`, "invalid_response"},
		{200, `{"id":1,"username":"user"} extra`, "invalid_response"},
		{200, `broken test-platform-secret`, "invalid_response"},
		{302, ``, "query_failed"},
		{200, `{"id":1,"username":"operator"}` + strings.Repeat(" ", 32<<10), "invalid_response"},
	} {
		t.Run(fmt.Sprint(tc.status, tc.code, tc.body), func(t *testing.T) {
			calls := 0
			transport := platformRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://gitlab.invalid/api/v4/user" || r.Header.Get("PRIVATE-TOKEN") != "test-platform-secret" {
					t.Error("incorrect target/auth")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Location": []string{"https://other.invalid/"}}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})
			result := VerifyIdentityWithTransport(context.Background(), "https://gitlab.invalid", platform.NewSecret("test-platform-secret"), transport)
			if calls != 1 || result.ErrorCode != tc.code || (result.Status == "valid") != (tc.code == "") {
				t.Fatal(result, calls)
			}
			if tc.code == "" && result.UserID != 1 {
				t.Fatal("stable user ID lost", result)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "test-platform-secret") {
				t.Fatal("remote body exposed")
			}
		})
	}
}
