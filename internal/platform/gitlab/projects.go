package gitlab

import (
	"context"
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
