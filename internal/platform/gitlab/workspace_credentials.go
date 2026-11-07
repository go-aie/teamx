package gitlab

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
)

func readPrivateJSON(path string, out any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 32<<10 {
		return errors.New("invalid private GitLab configuration")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("private GitLab configuration unavailable")
	}
	return decodeJSON(data, out)
}
func ReadWorkspaceBinding(path string) (Binding, error) {
	var b Binding
	if !filepath.IsAbs(path) || readPrivateJSON(path, &b) != nil || !validEventBinding(b) {
		return b, errors.New("invalid GitLab workspace binding")
	}
	return b, nil
}

// WorkspaceCredential is called only by a service-owned Git subprocess.
// Member environments never receive TEAMX_PLATFORM_TOKEN.
func WorkspaceCredential(ctx context.Context, b Binding, env func(string) string, _ http.RoundTripper) (platform.Secret, error) {
	token := env("TEAMX_PLATFORM_TOKEN")
	if ctx.Err() != nil || !validEventBinding(b) || !validToken(token) {
		return platform.Secret{}, platform.ErrCredential
	}
	return platform.NewSecret(token), nil
}

var gitPrompt = regexp.MustCompile(`^(Username|Password) for '([^']+)': ?$`)

// Askpass validates the exact repository before resolving any credential.
func Askpass(ctx context.Context, b Binding, prompt string, env func(string) string, transport http.RoundTripper) (string, error) {
	m := gitPrompt.FindStringSubmatch(prompt)
	if m == nil {
		return "", errors.New("unrecognized Git credential target")
	}
	target, err := url.Parse(m[2])
	origin, _ := url.Parse(b.URL)
	if err != nil || !validEventBinding(b) || target.Scheme != "https" || !strings.EqualFold(target.Host, origin.Host) || target.RawQuery != "" || target.ForceQuery || target.Fragment != "" || (target.Path != "/"+b.Project && target.Path != "/"+b.Project+".git") {
		return "", errors.New("Git credential target differs from Room project")
	}
	if target.User != nil {
		_, hasPassword := target.User.Password()
		if target.User.Username() != "oauth2" || hasPassword {
			return "", errors.New("invalid Git credential username")
		}
	}
	if m[1] == "Username" {
		return "oauth2", nil
	}
	secret, err := WorkspaceCredential(ctx, b, env, transport)
	if err != nil {
		return "", err
	}
	return secret.Reveal(), nil
}
