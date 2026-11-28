package scion

import (
	"context"
	"encoding/json"
	"reflect"
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
	for _, tc := range [][2]string{{"\n", ""}, {"https://evil.invalid/repo", ""}, {"a/b/c", ""}, {"", "99"}, {"", "2001"}, {"", "0100"}, {"", "-1"}} {
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
