package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestSharedGitLabReusesCLIAndRejectsRepositoryAndFilePaths(t *testing.T) {
	bin := fakeGlab(t, `printf '%s\n' "$@"`)
	a, out, stderr := testApp(t, nil)
	a.shared = true
	a.getenv = func(k string) string {
		switch k {
		case "GITLAB_HOST":
			return "https://gitlab.test:8443"
		case "GITLAB_TOKEN":
			return "test-token"
		case "TEAMX_GLAB_BIN":
			return bin
		}
		return ""
	}
	if code := a.run(t.Context(), []string{"api", "projects/group%2Fproject/issues", "-X", "GET", "-f", "search=browser"}); code != 0 || !strings.Contains(out.String(), "--hostname\ngitlab.test") {
		t.Fatal(code, out.String(), stderr.String())
	}
	for _, args := range [][]string{
		{"repo", "clone", "group/project"}, {"mr", "checkout", "1"}, {"alias", "set", "x", "!sh"},
		{"api", "projects/:id"}, {"api", "https://other.invalid/api/v4/user"},
		{"api", "//other.invalid/user"}, {"api", "../user"},
		{"api", "user", "--hostname", "other.invalid"}, {"api", "user", "--header", "Authorization: bad"},
		{"api", "user", "--input", "/etc/passwd"}, {"api", "user", "-F", "body=@/etc/passwd"},
		{"api", "user", "-F", `input={"x":"prefix:repo"}`},
		{"api", "user", "--form", "file=@/etc/passwd"}, {"api", "user", "--", "--help"},
		{"work-items", "note", "--repo", "group/project", "--iid", "1", "--file", "/etc/passwd"},
	} {
		out.Reset()
		stderr.Reset()
		if a.run(context.Background(), args) == 0 || out.Len() != 0 {
			t.Fatal("unsafe command reached CLI", args, out.String())
		}
	}
	// Existing Go extensions retain the same API implementation and account.
	a.transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "gitlab.test:8443" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatal(r.URL, r.Header)
		}
		return response([]any{}), nil
	})
	out.Reset()
	if code := a.run(t.Context(), []string{"issue", "discussions", "list", "--repo", "group/project", "--iid", "7"}); code != 0 || strings.TrimSpace(out.String()) != "[]" {
		t.Fatal("existing API extension changed", code, out.String(), stderr.String())
	}
}

func TestSharedGitLabHelpNeverInvokesNativeDiscovery(t *testing.T) {
	a, out, _ := testApp(t, nil)
	a.shared = true
	a.getenv = func(string) string { t.Fatal("shared help resolved executable/account"); return "" }
	if a.run(t.Context(), []string{"--help"}) != 0 || !strings.Contains(out.String(), "glab api") {
		t.Fatal(out.String())
	}
}
