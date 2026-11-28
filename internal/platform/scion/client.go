package scion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-aie/teamx/internal/platform/notification"
)

const MaxDiscoveryLimit = 2000
const MaxNotificationOutput = 8 << 20

type RepositoryEvidence struct {
	Name    string `json:"name"`
	Created int64  `json:"created_at_ms"`
}

func (r RepositoryEvidence) Valid() bool { return validRepository(r.Name) && r.Created > 0 }
func validInstance(s string) bool {
	u, e := url.Parse(s)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.RawPath == "" && !strings.ContainsAny(s, "\x00\r\n")
}
func validRepository(s string) bool {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || len(p) > 128 || strings.Trim(p, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.") != "" {
			return false
		}
	}
	return true
}
func NormalizeRepository(origin, value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.Contains(value, "://") {
		u, e := url.Parse(value)
		if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Scheme+"://"+u.Host != origin {
			return "", notification.ErrRequest
		}
		value = strings.Trim(u.Path, "/")
	}
	if !validRepository(value) {
		return "", notification.ErrRequest
	}
	return value, nil
}

// NativeExecute is bounded, account-pinned transport, supplied by the application.
type NativeExecute func(context.Context, QueryScope, ...string) ([]byte, error)
type Issue struct {
	Schema  string `json:"schema"`
	Repo    string `json:"repo"`
	Number  int64  `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Author  string `json:"author"`
	State   string `json:"state"`
	Created int64  `json:"created_at_ms"`
	Updated int64  `json:"updated_at_ms"`
}

func (i Issue) Valid() bool {
	return i.Schema == "scion.cli.issue/v1" && validRepository(i.Repo) && i.Number > 0 && i.Title != "" && i.Author != "" && i.State == "open" && i.Created > 0 && i.Updated >= i.Created
}
func ReadRepository(ctx context.Context, run NativeExecute, q QueryScope, name string) (RepositoryEvidence, error) {
	b, e := run(ctx, q, "--json", "repo", "info", name)
	if e != nil {
		return RepositoryEvidence{}, e
	}
	var r struct {
		Schema   string
		Name     string
		Created  int64  `json:"created_at_ms"`
		Deleted  *int64 `json:"deleted_at_ms"`
		Archived bool
	}
	if json.Unmarshal(b, &r) != nil || r.Schema != "scion.cli.repo/v1" || r.Name != name || r.Created <= 0 || r.Deleted != nil || r.Archived {
		return RepositoryEvidence{}, errors.New("repository_unavailable")
	}
	return RepositoryEvidence{r.Name, r.Created}, nil
}
func readIssues(ctx context.Context, run NativeExecute, q QueryScope, repo string, limit int) ([]Issue, bool, int, error) {
	b, e := run(ctx, q, "--json", "--repo", repo, "issue", "ls", "--state", "open", "--limit", strconv.Itoa(limit))
	if e != nil {
		return nil, false, len(b), e
	}
	var list struct {
		Schema    string
		Repo      string
		Issues    *[]Issue
		Truncated *bool
	}
	if json.Unmarshal(b, &list) != nil || list.Schema != "scion.cli.issue-list/v1" || list.Repo != repo || list.Issues == nil || list.Truncated == nil || len(*list.Issues) > limit {
		return nil, false, len(b), errors.New("invalid_response")
	}
	seen := map[int64]bool{}
	for _, i := range *list.Issues {
		if !i.Valid() || i.Repo != repo || seen[i.Number] {
			return nil, false, len(b), errors.New("invalid_response")
		}
		seen[i.Number] = true
	}
	return *list.Issues, *list.Truncated, len(b), nil
}
func issueURL(instance, repo string, number int64) string {
	return fmt.Sprintf("%s/%s/issues/%d", instance, repo, number)
}
