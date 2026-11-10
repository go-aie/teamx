package room

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/go-aie/teamx/internal/workspace"
	workspacegit "github.com/go-aie/teamx/internal/workspace/git"
)

// Preserve every existing GitLab requirement, including numeric ProjectID.
func normalizeGitWorkspace(w workspaceConfig, suggestion bool) (workspaceConfig, error) {
	if w.Repository != nil {
		source, err := workspacegit.Normalize(workspace.Source{URL: w.Repository.URL, Branch: w.Repository.Branch})
		if err != nil {
			return workspaceConfig{}, fmt.Errorf("%w: invalid workspace repository", ErrRequest)
		}
		repo := *w.Repository
		repo.URL, repo.Branch = source.URL, source.Branch
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
		if b.PlatformID != "" {
			if !validRestartID(b.PlatformID) ||
				(b.PlatformGeneration != "" && !validRestartID(b.PlatformGeneration)) || b.ProjectID < 0 ||
				(!suggestion && (b.PlatformGeneration == "" || b.ProjectID <= 0)) {
				return workspaceConfig{}, fmt.Errorf("%w: invalid workspace connection reference", ErrRequest)
			}
		} else if !suggestion || b.PlatformGeneration != "" || b.ProjectID != 0 {
			return workspaceConfig{}, fmt.Errorf("%w: GitLab requires a platform connection", ErrRequest)
		}
		w.Binding = &b
	}
	return w, nil
}
