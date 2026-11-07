package scion

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
)

// VerifyRepository verifies native canonical identity and a nonempty existing branch.
// Run must pin and verify the selected account and isolate its environment.
// A missing branch (including a bounded/truncated response) is never cloned.
func VerifyRepository(ctx context.Context, run platform.Execute, name, branch string) (string, error) {
	fail := errors.New("Scion repository or branch verification failed")
	out, err := run(ctx, "", nil, "scion", "--json", "repo", "info", name)
	var repo struct {
		Schema, Name  string
		DefaultBranch string `json:"default_branch"`
		DeletedAt     *int64 `json:"deleted_at_ms"`
		Archived      bool
	}
	if err != nil {
		return "", err
	}
	if json.Unmarshal(out, &repo) != nil || repo.Schema != "scion.cli.repo/v1" || repo.Name != name || repo.DeletedAt != nil || repo.Archived {
		return "", fail
	}
	if branch == "" {
		branch = repo.DefaultBranch
	}
	if branch == "" {
		return "", fail
	}
	out, err = run(ctx, "", nil, "scion", "--json", "--repo", name, "branch", "ls")
	if err != nil {
		return "", err
	}
	var list struct {
		Schema, Repo string
		Branches     []struct{ Name, Changeset string }
	}
	if json.Unmarshal(out, &list) != nil || list.Schema != "scion.cli.branches/v1" || list.Repo != name {
		return "", fail
	}
	for _, ref := range list.Branches {
		if ref.Name == branch && validObjectID(ref.Changeset) {
			return branch, nil
		}
	}
	return "", fail
}

func validObjectID(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "c1") {
		return false
	}
	for _, c := range value[2:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
