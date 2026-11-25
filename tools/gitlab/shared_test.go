package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Recreate the distribution entry point, including its actual shell launcher.
// A direct native-binary override would miss launcher/environment incompatibilities.
func packagedGlab(t *testing.T, native string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "distribution with spaces")
	launcher := filepath.Join(root, "bin", "glab")
	nativePath := filepath.Join(root, "runtime", "glab", "bin", "glab")
	for _, dir := range []string{filepath.Dir(launcher), filepath.Dir(nativePath)} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "packaging", "glab-launcher"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, script, 0700); err != nil {
		t.Fatal(err)
	}
	native, err = filepath.Abs(native)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(native, nativePath); err != nil {
		t.Fatal(err)
	}
	return launcher
}

func TestPackagedGlabPreservesArgumentsAndSharedIsolation(t *testing.T) {
	for _, shared := range []bool{false, true} {
		name := "personal"
		if shared {
			name = "shared"
		}
		t.Run(name, func(t *testing.T) {
			body := `test "$GLAB_CHECK_UPDATE" = false || exit 91
test "$GITLAB_TOKEN" = secret-test-token || exit 92
test -z "${GITLAB_PAT-}" || exit 93
`
			if shared {
				body += `if command -v dirname >/dev/null 2>&1; then exit 94; fi
if git --version >/dev/null 2>&1; then exit 95; fi
`
			}
			body += `printf '%s\n' "$@"
IFS= read -r body || :
printf 'stdin:%s\n' "$body"
`
			bin := packagedGlab(t, fakeGlab(t, body))
			t.Setenv("GITLAB_PAT", "operator-secret")
			t.Setenv("GLAB_CHECK_UPDATE", "true")
			a, out, stderr := testApp(t, nil)
			a.shared = shared
			getenv := a.getenv
			a.getenv = func(k string) string {
				if k == "TEAMX_GLAB_BIN" {
					return bin
				}
				if k == "GITLAB_HOST" {
					return "https://gitlab.test"
				}
				return getenv(k)
			}
			args := []string{"api", "projects/group%2Fproject/issues", "-f", "search=two words"}
			if code := a.run(t.Context(), args); code != 0 {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, out, stderr)
			}
			if shared {
				args = append(args, "--hostname", "gitlab.test")
			}
			want := strings.Join(args, "\n") + "\nstdin:stdin-body\n"
			if out.String() != want || stderr.Len() != 0 {
				t.Fatalf("streams changed: stdout=%q stderr=%q", out, stderr)
			}
		})
	}
}

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
