package scion

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRepositoryDirectoryFilteringBoundsAndEvidence(t *testing.T) {
	for _, tc := range []struct {
		query, token, prefix, limit, next string
		truncated                         bool
		names                             []string
	}{
		{"", "", "", "100", "200", true, []string{"Owner/repo", "Owner/other"}},
		{"ISSUE", "200", "", "200", "400", true, []string{"Owner/repo"}},
		{"Owner/re", "2000", "Owner/re", "2000", "", true, []string{"Owner/repo"}},
	} {
		t.Run(tc.query+tc.token, func(t *testing.T) {
			run := func(_ context.Context, _ QueryScope, args ...string) ([]byte, error) {
				if !reflect.DeepEqual(args, []string{"--json", "repo", "ls", "--limit", tc.limit, "--", tc.prefix}) {
					t.Fatal(args)
				}
				repos := []map[string]any{}
				for _, name := range []string{"Owner/repo", "Owner/other", "Owner/archived", "Owner/deleted"} {
					if tc.prefix != "" && name != "Owner/repo" {
						continue
					}
					description := ""
					if name == "Owner/repo" {
						description = "Issue tracker"
					}
					r := map[string]any{"schema": "scion.cli.repo/v1", "name": name, "created_at_ms": 100, "description": description, "archived": name == "Owner/archived"}
					if name == "Owner/deleted" {
						r["deleted_at_ms"] = 101
					}
					repos = append(repos, r)
				}
				return json.Marshal(map[string]any{"schema": "scion.cli.repos/v1", "repos": repos, "truncated": tc.truncated})
			}
			page, err := ListRepositories(t.Context(), run, QueryScope{}, tc.query, tc.token)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, r := range page.Repositories {
				names = append(names, r.Name)
				if !r.Valid() {
					t.Fatal(r)
				}
			}
			if !reflect.DeepEqual(names, tc.names) || page.NextPageToken != tc.next || page.Truncated != tc.truncated {
				t.Fatal(page, names)
			}
		})
	}
	for _, tc := range [][2]string{{"\n", ""}, {"https://evil.invalid/repo", ""}, {"a//b", ""}, {"a/../b", ""}, {"a/./b", ""}, {"/a/b", ""}, {"a/b//", ""}, {"", "99"}, {"", "2001"}, {"", "0100"}, {"", "-1"}} {
		calls := 0
		_, err := ListRepositories(t.Context(), func(context.Context, QueryScope, ...string) ([]byte, error) { calls++; return nil, nil }, QueryScope{}, tc[0], tc[1])
		if err == nil || calls != 0 {
			t.Fatal(tc, err, calls)
		}
	}
	for _, raw := range []string{
		`{"schema":"scion.cli.repos/v1","repos":[],"truncated":false}`,
		`{"schema":"bad","repos":[],"truncated":false}`,
		`{"schema":"scion.cli.repos/v1","repos":[]}`,
		`{"schema":"scion.cli.repos/v1","truncated":false}`,
		`{"schema":"scion.cli.repos/v1","repos":[{"schema":"scion.cli.repo/v1","name":"Owner/repo","created_at_ms":0}],"truncated":false}`,
		`{"schema":"scion.cli.repos/v1","repos":[{"schema":"scion.cli.repo/v1","name":"Owner/repo","created_at_ms":1},{"schema":"scion.cli.repo/v1","name":"Owner/repo","created_at_ms":1}],"truncated":false}`,
	} {
		_, err := ListRepositories(t.Context(), func(context.Context, QueryScope, ...string) ([]byte, error) { return []byte(raw), nil }, QueryScope{}, "", "")
		if (err == nil) != (raw == `{"schema":"scion.cli.repos/v1","repos":[],"truncated":false}`) {
			t.Fatal(raw, err)
		}
	}
}

func TestRepositoryDirectoryWithNestedNamespaces(t *testing.T) {
	// Native Scion directories include both organization subgroups and users.
	repositories := []Repository{
		{RepositoryEvidence: RepositoryEvidence{"OpenCSG/Orbit", 100}},
		{RepositoryEvidence: RepositoryEvidence{"OpenCSG/csghub/csghub-portal", 101}},
		{RepositoryEvidence: RepositoryEvidence{"user/csgship/teamx-demo", 102}},
	}
	for _, tc := range []struct {
		query  string
		prefix string
		want   []Repository
	}{
		{"", "", repositories},
		{"OpenCSG", "", repositories[:2]},
		{"OpenCSG/", "OpenCSG/", repositories[:2]},
		{"OpenCSG/csghub/", "OpenCSG/csghub/", repositories[1:2]},
		{"OpenCSG/csghub/csghub-p", "OpenCSG/csghub/csghub-p", repositories[1:2]},
		{"user/csgship/", "user/csgship/", repositories[2:]},
	} {
		t.Run(tc.query, func(t *testing.T) {
			run := func(_ context.Context, _ QueryScope, args ...string) ([]byte, error) {
				if !reflect.DeepEqual(args, []string{"--json", "repo", "ls", "--limit", "100", "--", tc.prefix}) {
					t.Fatal(args)
				}
				rows := []map[string]any{}
				for _, r := range repositories {
					if !strings.HasPrefix(r.Name, tc.prefix) {
						continue
					}
					rows = append(rows, map[string]any{"schema": "scion.cli.repo/v1", "name": r.Name, "created_at_ms": r.Created, "description": r.Description})
				}
				return json.Marshal(map[string]any{"schema": "scion.cli.repos/v1", "repos": rows, "truncated": false})
			}
			page, err := ListRepositories(t.Context(), run, QueryScope{}, tc.query, "")
			if err != nil || !reflect.DeepEqual(page.Repositories, tc.want) || page.Truncated {
				t.Fatal(page, err)
			}
		})
	}
}

func TestNormalizeRepositoryNestedNamespaces(t *testing.T) {
	const origin = "https://scion.invalid"
	for _, name := range []string{"OpenCSG/Orbit", "OpenCSG/csghub/csghub-portal", "user/csgship/teamx-demo"} {
		for _, input := range []string{name, origin + "/" + name} {
			got, err := NormalizeRepository(origin, input)
			if err != nil || got != name {
				t.Fatal(input, got, err)
			}
		}
	}
	for _, name := range []string{"repo", "a//b", "a/./b", "a/../b", "/a/b", "a/b/", "a/b\\c", "a/b?c", "a/b\nc", "a/" + strings.Repeat("x", 129), "https://other.invalid/a/b/c"} {
		if _, err := NormalizeRepository(origin, name); err == nil {
			t.Fatal("accepted invalid repository", name)
		}
	}
}
