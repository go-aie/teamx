package room

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/go-aie/teamx/internal/platform"
)

// Workspace is effective room configuration, never a set of runtime overrides.
type workspaceConfig struct {
	Root       string               `json:"root"`
	Repository *workspaceRepository `json:"repository,omitempty"`
	Binding    *workspaceBinding    `json:"binding,omitempty"`
}

type workspaceRepository struct {
	URL    string `json:"url"`
	Branch string `json:"branch,omitempty"`
}

type workspaceBinding = platform.ProjectBinding

var workspaceEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Team suggestions may omit authentication; an effective binding must have
// exactly one source. Normalization always copies nested values.
func normalizeWorkspace(input workspaceConfig, suggestion bool) (workspaceConfig, error) {
	w := input
	if w.Root == "" {
		w.Root = "workspaces"
	}
	if !validText(w.Root) || strings.ContainsAny(w.Root, "\\\x00") || strings.Contains(w.Root, "${") ||
		strings.HasPrefix(w.Root, "~") || strings.ContainsRune(w.Root, ':') ||
		len(w.Root) > 1024 || strings.IndexFunc(w.Root, unicode.IsControl) >= 0 || path.IsAbs(w.Root) {
		return workspaceConfig{}, fmt.Errorf("%w: workspace root must be a room-relative directory", ErrRequest)
	}
	w.Root = path.Clean(w.Root)
	if w.Root == "." || w.Root == ".." || strings.HasPrefix(w.Root, "../") {
		return workspaceConfig{}, fmt.Errorf("%w: workspace root escapes its directory", ErrRequest)
	}
	// Root-level management files and directories must not become workspaces.
	switch strings.ToLower(strings.Split(w.Root, "/")[0]) {
	case "agents", "resources", "notifications", "notification-queries", "room.json", "room-config.json", "collaboration.json", "discussion-restart.json", ".lock":
		return workspaceConfig{}, fmt.Errorf("%w: workspace root overlaps room management data", ErrRequest)
	}
	if w.Repository != nil {
		repo := *w.Repository
		u, err := workspaceHTTPSURL(repo.URL)
		if err != nil || !workspaceProjectPath(strings.TrimPrefix(u.Path, "/")) || !validWorkspaceBranch(repo.Branch) {
			return workspaceConfig{}, fmt.Errorf("%w: invalid workspace repository", ErrRequest)
		}
		repo.URL = u.String()
		w.Repository = &repo
	}
	if w.Binding != nil {
		b := *w.Binding
		if b.Provider != "gitlab" || w.Repository == nil || !workspaceProjectPath(b.Project) || !strings.Contains(b.Project, "/") {
			return workspaceConfig{}, fmt.Errorf("%w: GitLab binding requires a repository and project path", ErrRequest)
		}
		u, err := workspaceHTTPSURL(b.URL)
		if err != nil || (u.Path != "" && u.Path != "/") {
			return workspaceConfig{}, fmt.Errorf("%w: GitLab URL must be an HTTPS origin", ErrRequest)
		}
		u.Path = ""
		b.URL = u.String()
		repo, _ := url.Parse(w.Repository.URL)
		if repo.Host != u.Host || strings.TrimSuffix(strings.TrimPrefix(repo.Path, "/"), ".git") != b.Project {
			return workspaceConfig{}, fmt.Errorf("%w: repository and GitLab project do not match", ErrRequest)
		}
		if b.ConnectionID != "" {
			if !validRestartID(b.ConnectionID) || b.TokenEnv != "" || b.CredentialID != "" ||
				(b.ConnectionGeneration != "" && !validRestartID(b.ConnectionGeneration)) || b.ProjectID < 0 ||
				(!suggestion && (b.ConnectionGeneration == "" || b.ProjectID <= 0)) {
				return workspaceConfig{}, fmt.Errorf("%w: invalid workspace connection reference", ErrRequest)
			}
		} else if b.ConnectionGeneration != "" || b.ProjectID != 0 {
			return workspaceConfig{}, ErrRequest
		} else if (b.TokenEnv != "" && (!workspaceEnvName.MatchString(b.TokenEnv) || len(b.TokenEnv) > 128)) ||
			(b.CredentialID != "" && !validRestartID(b.CredentialID)) ||
			(b.TokenEnv != "" && b.CredentialID != "") ||
			(suggestion && b.CredentialID != "") || (!suggestion && b.TokenEnv == "" && b.CredentialID == "") {
			return workspaceConfig{}, fmt.Errorf("%w: invalid workspace authentication source", ErrRequest)
		}
		w.Binding = &b
	}
	return w, nil
}

// Return static diagnostics: URLs can contain credentials and must not be echoed.
func workspaceHTTPSURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" ||
		len(value) > 4096 || strings.IndexFunc(value, unicode.IsSpace) >= 0 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return &url.URL{}, ErrRequest
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u, nil
}

func workspaceProjectPath(value string) bool {
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

func validWorkspaceBranch(branch string) bool {
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

func workspaceMemberPath(w workspaceConfig, membership string) string {
	return path.Join(w.Root, membership)
}

func workspacePathsOverlap(a, b string) bool {
	// Conservatively protect case-insensitive hosts as well as Linux.
	a, b = strings.ToLower(a), strings.ToLower(b)
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// Check existing components, including the member directory, before reading or
// creating anything. Missing directories are allowed; symlinks are not.
func checkWorkspaceDirectory(roomDir, relative string) error {
	if !safeResourcePath(relative) {
		return ErrInvalid
	}
	current := roomDir
	parts := append([]string{""}, strings.Split(relative, "/")...)
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: workspace path contains a symlink or non-directory", ErrInvalid)
		}
	}
	return nil
}

func validateWorkspaceSnapshot(c managedRoomConfig) error {
	if c.PlatformToolsVersion < 0 || c.PlatformToolsVersion > 1 {
		return ErrInvalid
	}
	if c.Workspace == nil || c.WorkspaceVersion == 0 {
		return fmt.Errorf("%w: workspace and workspace_version must be present together", ErrInvalid)
	}
	w, err := normalizeWorkspace(*c.Workspace, false)
	if err != nil || !equalWorkspace(w, *c.Workspace) {
		return fmt.Errorf("%w: invalid or noncanonical workspace snapshot", ErrInvalid)
	}
	for _, member := range c.Members {
		if member.Workspace != workspaceMemberPath(w, member.MembershipID) {
			return fmt.Errorf("%w: member workspace differs from room configuration", ErrInvalid)
		}
	}
	return nil
}

func equalWorkspace(a, b workspaceConfig) bool {
	if a.Root != b.Root || (a.Repository == nil) != (b.Repository == nil) || (a.Binding == nil) != (b.Binding == nil) {
		return false
	}
	return (a.Repository == nil || *a.Repository == *b.Repository) && (a.Binding == nil || *a.Binding == *b.Binding)
}
