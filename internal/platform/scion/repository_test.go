package scion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRepositoryCanonicalBranchEvidence(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "empty", "wrong-repo", "wrong-schema", "digest-only", "git-sha", "wrong-kind", "transport", "invalid-json"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			run := func(_ context.Context, _ string, _ map[string]string, exe string, args ...string) ([]byte, error) {
				calls++
				if exe != "scion" {
					t.Fatal(exe)
				}
				if calls == 1 {
					return []byte(`{"schema":"scion.cli.repo/v1","name":"Org/repo","default_branch":"main"}`), nil
				}
				if mode == "transport" {
					return nil, errors.New("read failed")
				}
				if mode == "invalid-json" {
					return []byte("{"), nil
				}
				id := "c1" + strings.Repeat("a", 64)
				repo, schema := "Org/repo", "scion.cli.branches/v1"
				switch mode {
				case "wrong-repo":
					repo = "Org/other"
				case "wrong-schema":
					schema = "unknown"
				case "digest-only":
					id = strings.Repeat("a", 64)
				case "git-sha":
					id = strings.Repeat("a", 40)
				case "wrong-kind":
					id = "b1" + strings.Repeat("a", 64)
				case "empty":
					id = ""
				}
				refs := []map[string]string{{"name": "main", "changeset": id}}
				if mode == "missing" {
					refs = nil
				}
				return json.Marshal(map[string]any{"schema": schema, "repo": repo, "branches": refs})
			}
			branch, err := VerifyRepository(context.Background(), run, "Org/repo", "")
			if mode == "valid" {
				if err != nil || branch != "main" {
					t.Fatal(branch, err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid evidence", mode)
			}
		})
	}
}
