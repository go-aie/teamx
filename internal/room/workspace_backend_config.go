package room

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/go-aie/teamx/internal/workspace"
	workspacescion "github.com/go-aie/teamx/internal/workspace/scion"
)

// Kind-less snapshots retain their original Git semantics without rewriting.
// Version control kind and platform provider must be compatible independently.
func normalizeWorkspaceBackend(w workspaceConfig, suggestion bool) (workspaceConfig, error) {
	if w.Repository == nil || w.Repository.Kind == "" || w.Repository.Kind == "git" {
		return normalizeGitWorkspace(w, suggestion)
	}
	if w.Repository.Kind != "scion" {
		return workspaceConfig{}, ErrRequest
	}
	source, err := workspacescion.Normalize(workspace.Source{URL: w.Repository.URL, Branch: w.Repository.Branch})
	if err != nil {
		return workspaceConfig{}, fmt.Errorf("%w: invalid Scion workspace repository", ErrRequest)
	}
	repo := *w.Repository
	repo.URL, repo.Branch = source.URL, source.Branch
	w.Repository = &repo
	return normalizeScionWorkspace(w, suggestion)
}

func normalizeScionWorkspace(w workspaceConfig, suggestion bool) (workspaceConfig, error) {
	if w.Binding == nil {
		return workspaceConfig{}, fmt.Errorf("%w: Scion requires a platform connection", ErrRequest)
	}
	b := *w.Binding
	u, err := workspaceHTTPSURL(b.URL)
	repo, _ := url.Parse(w.Repository.URL)
	if err != nil || u.Path != "" || b.Provider != "scion" || !workspaceProjectPath(b.Project) || repo.Scheme+"://"+repo.Host != u.String() || strings.TrimPrefix(repo.Path, "/") != b.Project || b.ProjectID != 0 {
		return workspaceConfig{}, fmt.Errorf("%w: Scion repository and binding must match", ErrRequest)
	}
	b.URL = u.String()
	if !validRestartID(b.ConnectionID) || b.ConnectionGeneration != "" && !validRestartID(b.ConnectionGeneration) || !suggestion && (b.ConnectionGeneration == "" || w.Repository.Branch == "") {
		return workspaceConfig{}, ErrRequest
	}
	w.Binding = &b
	return w, nil
}
