package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/workspace"
)

// This backend deliberately has no Git metadata and cannot execute commands.
// It exercises the real common preparation flow with opaque repository state.
type workspaceTestRepository struct {
	t          *testing.T
	checks     int
	clones     int
	configured int
	failClone  bool
	wrongClone bool
}

func (r *workspaceTestRepository) Check(_ context.Context, target string, initial bool) (bool, error) {
	r.checks++
	data, err := os.ReadFile(filepath.Join(target, "repository-id"))
	if errors.Is(err, os.ErrNotExist) && initial {
		return false, nil
	}
	if err != nil || string(data) != "expected repository" {
		return false, errors.New("repository missing or mismatched")
	}
	return true, nil
}

func (r *workspaceTestRepository) Clone(_ context.Context, _, target string, _ map[string]string) error {
	r.clones++
	if _, err := os.Stat(target + ".clone-pending"); err != nil {
		r.t.Fatal("clone started without durable marker", err)
	}
	if r.failClone {
		// Native CLIs may remove their own incomplete target after failure.
		return errors.New("clone failed before target creation")
	}
	if err := os.MkdirAll(target, 0700); err != nil {
		return err
	}
	identity := "expected repository"
	if r.wrongClone {
		identity = "another repository"
	}
	return os.WriteFile(filepath.Join(target, "repository-id"), []byte(identity), 0600)
}

func (r *workspaceTestRepository) Configure(_ context.Context, target string, _ bool, _ workspace.Access) error {
	r.configured++
	if _, err := os.Stat(target + ".clone-pending"); !errors.Is(err, os.ErrNotExist) {
		r.t.Fatal("unverified clone reached configuration", err)
	}
	return nil
}

func backendWorkspaceFixture(t *testing.T) (*workspaceFixture, *workspaceTestRepository, *int) {
	t.Helper()
	f := newWorkspaceFixture(t)
	r := &workspaceTestRepository{t: t}
	f.p.run = func(context.Context, string, map[string]string, string, ...string) ([]byte, error) {
		t.Fatal("common preparation executed a command directly")
		return nil, ErrInvalid
	}
	f.p.repository = func(platform.Execute, workspaceConfig) (workspace.Repository, error) { return r, nil }
	accessCalls := new(int)
	f.p.access = func(context.Context, string, managedRoomConfig, bool) (workspace.Access, error) {
		*accessCalls += 1
		if r.checks < len(f.config.Members) {
			t.Fatal("platform accessed before all local targets were checked")
		}
		return workspace.Access{Env: map[string]string{"PUBLIC_REFERENCE": "reference"}, Instructions: "backend instructions"}, nil
	}
	return f, r, accessCalls
}
