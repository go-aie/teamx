package gitlab

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// ProjectPage contains only metadata used by the human project picker.
type Project struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Path string `json:"path_with_namespace"`
}
type ProjectPage struct {
	Projects []Project `json:"projects"`
	NextPage int       `json:"next_page"`
}

func ValidProjectSearch(search string) bool {
	return len(search) <= 256 && !strings.ContainsFunc(search, unicode.IsControl)
}

// ListProjects searches the selected account's accessible projects remotely.
func ListProjects(ctx context.Context, read ReadAPI, search string, page int) (ProjectPage, error) {
	if !ValidProjectSearch(search) || page < 1 || page > 1000 {
		return ProjectPage{}, &queryError{Code: "invalid_request"}
	}
	const perPage = 50
	q := url.Values{"simple": {"true"}, "order_by": {"id"}, "sort": {"asc"}, "per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
	if search != "" {
		q.Set("search", search)
		q.Set("search_namespaces", "true")
	}
	response, err := read(ctx, "/projects", q, nil, 1<<20)
	if err != nil {
		return ProjectPage{}, err
	}
	var projects []Project
	if decodeJSON(response.Data, &projects) != nil || projects == nil || len(projects) > perPage {
		return ProjectPage{}, &queryError{Code: "invalid_response"}
	}
	for _, p := range projects {
		if p.ID <= 0 || p.Name == "" || len(p.Name) > 1024 || !validEventsProjectPath(p.Path) {
			return ProjectPage{}, &queryError{Code: "invalid_response"}
		}
	}
	next, err := nextPage(response.Header, page, perPage, len(projects))
	if err != nil || next > 1000 {
		return ProjectPage{}, &queryError{Code: "invalid_response"}
	}
	return ProjectPage{Projects: projects, NextPage: next}, nil
}

// GetProject resolves a native project ID through the selected account. Callers
// compare the returned path with the selected candidate before committing.
func GetProject(ctx context.Context, read ReadAPI, id int64) (Project, error) {
	if id <= 0 {
		return Project{}, &queryError{Code: "invalid_request"}
	}
	response, err := read(ctx, "/projects/"+strconv.FormatInt(id, 10), nil, nil, 32<<10)
	if err != nil {
		return Project{}, err
	}
	var p Project
	if decodeJSON(response.Data, &p) != nil || p.ID != id || p.Name == "" || len(p.Name) > 1024 || !validEventsProjectPath(p.Path) {
		return Project{}, &queryError{Code: "invalid_response"}
	}
	return p, nil
}

// ProjectReadErrorCode exposes only a bounded classification, never a remote
// body or transport error. Empty means the error belongs to another boundary.
func ProjectReadErrorCode(err error) string {
	var q *queryError
	if !errors.As(err, &q) {
		return ""
	}
	switch q.Status {
	case 401:
		return "credential"
	case 403:
		return "permission_denied"
	case 404:
		return "not_found"
	case 429:
		return "rate_limited"
	}
	return q.Code
}
