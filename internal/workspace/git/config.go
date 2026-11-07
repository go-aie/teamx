package git

import (
	"errors"
	"net/url"
	"strings"
	"unicode"

	"github.com/go-aie/teamx/internal/workspace"
)

// Normalize validates only Git repository syntax. Platform identity and
// permission checks remain with the caller's platform access bridge.
func Normalize(source workspace.Source) (workspace.Source, error) {
	u, err := repositoryURL(source.URL)
	if err != nil || !projectPath(strings.TrimPrefix(u.Path, "/")) || !validBranch(source.Branch) {
		return workspace.Source{}, errors.New("invalid Git workspace repository")
	}
	source.URL = u.String()
	return source, nil
}

func repositoryURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" ||
		len(value) > 4096 || strings.IndexFunc(value, unicode.IsSpace) >= 0 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return &url.URL{}, errors.New("invalid Git repository URL")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u, nil
}

func projectPath(value string) bool {
	if value == "" || len(value) > 2048 {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				return false
			}
		}
	}
	return true
}

func validBranch(branch string) bool {
	if branch == "" {
		return true
	}
	if len(branch) > 255 || branch == "@" || strings.HasPrefix(branch, "-") ||
		strings.Contains(branch, "..") || strings.Contains(branch, "@{") ||
		strings.ContainsAny(branch, " ~^:?*[\\") || strings.IndexFunc(branch, unicode.IsControl) >= 0 || strings.HasSuffix(branch, ".") {
		return false
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}
