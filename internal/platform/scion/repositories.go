package scion

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/go-aie/teamx/internal/platform/notification"
)

type Repository struct {
	RepositoryEvidence
	Description string `json:"description"`
}

type RepositoryPage struct {
	Repositories  []Repository `json:"repositories"`
	Truncated     bool         `json:"truncated"`
	NextPageToken string       `json:"next_page_token,omitempty"`
}

func ValidRepositorySearch(query string) bool {
	return len(query) <= 256 && !strings.ContainsAny(query, "\x00\r\n")
}

// The CLI exposes a bounded prefix, not a page cursor. Read a larger prefix on
// demand; callers merge by name. Keyword filtering never implies full coverage.
func ListRepositories(ctx context.Context, run NativeExecute, q QueryScope, query, token string) (RepositoryPage, error) {
	page := RepositoryPage{Repositories: []Repository{}}
	if !ValidRepositorySearch(query) {
		return page, notification.ErrRequest
	}
	limit := 100
	if token != "" {
		n, e := strconv.Atoi(token)
		if e != nil || n < 100 || n > MaxDiscoveryLimit || strconv.Itoa(n) != token {
			return page, notification.ErrRequest
		}
		limit = n
	}
	query = strings.TrimSpace(query)
	prefix := ""
	if strings.Contains(query, "/") {
		for _, part := range strings.Split(strings.TrimSuffix(query, "/"), "/") {
			if !validRepositoryPart(part) {
				return page, notification.ErrRequest
			}
		}
		prefix = query
	}
	b, err := run(ctx, q, "--json", "repo", "ls", "--limit", strconv.Itoa(limit), "--", prefix)
	if err != nil {
		return page, err
	}
	var list struct {
		Schema string
		Repos  *[]struct {
			Repository
			Schema   string
			Archived bool
			Deleted  *int64 `json:"deleted_at_ms"`
		}
		Truncated *bool
	}
	if json.Unmarshal(b, &list) != nil || list.Schema != "scion.cli.repos/v1" || list.Repos == nil || list.Truncated == nil || len(*list.Repos) > limit {
		return page, errors.New("invalid_response")
	}
	seen := map[string]bool{}
	for _, r := range *list.Repos {
		if r.Schema != "scion.cli.repo/v1" || !r.RepositoryEvidence.Valid() || !strings.HasPrefix(r.Name, prefix) || seen[r.Name] {
			return RepositoryPage{}, errors.New("invalid_response")
		}
		seen[r.Name] = true
		if r.Deleted == nil && !r.Archived && (prefix != "" || strings.Contains(strings.ToLower(r.Name+" "+r.Description), strings.ToLower(query))) {
			page.Repositories = append(page.Repositories, r.Repository)
		}
	}
	page.Truncated = *list.Truncated
	if page.Truncated && limit < MaxDiscoveryLimit {
		page.NextPageToken = strconv.Itoa(min(limit*2, MaxDiscoveryLimit))
	}
	return page, nil
}
