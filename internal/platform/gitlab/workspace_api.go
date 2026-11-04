package gitlab

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
)

type workspaceProject struct {
	ID            int64  `json:"id"`
	Path          string `json:"path_with_namespace"`
	URL           string `json:"http_url_to_repo"`
	DefaultBranch string `json:"default_branch"`
}

func readProject(ctx context.Context, b Binding, read ReadAPI) (workspaceProject, error) {
	var p workspaceProject
	path := url.PathEscape(b.Project)
	if b.ProjectID > 0 {
		path = strconv.FormatInt(b.ProjectID, 10)
	}
	response, err := read(ctx, "/projects/"+path, nil, nil, 32<<10)
	if err != nil {
		return p, err
	}
	if decodeJSON(response.Data, &p) != nil || p.ID <= 0 || b.ProjectID > 0 && p.ID != b.ProjectID || p.Path != b.Project || p.URL != b.URL+"/"+b.Project+".git" {
		return p, errors.New("GitLab project does not match repository binding")
	}
	return p, nil
}

// User is queried only by explicit application when Git author data is absent.
func User(ctx context.Context, read ReadAPI) (string, string, error) {
	var user struct {
		Name        string `json:"name"`
		Email       string `json:"email"`
		CommitEmail string `json:"commit_email"`
	}
	response, err := read(ctx, "/user", nil, nil, 32<<10)
	if err != nil {
		return "", "", err
	}
	if decodeJSON(response.Data, &user) != nil {
		return "", "", errors.New("invalid GitLab author")
	}
	if user.CommitEmail != "" {
		user.Email = user.CommitEmail
	}
	if user.Name == "" || user.Email == "" || strings.ContainsAny(user.Name+user.Email, "\r\n\x00") {
		return "", "", errors.New("GitLab user lacks Git author information")
	}
	return user.Name, user.Email, nil
}
