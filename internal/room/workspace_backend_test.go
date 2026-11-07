package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestWorkspaceOpaqueRepositoryAndLocalRevisit(t *testing.T) {
	f, r, _ := backendWorkspaceFixture(t)
	before, _ := json.Marshal(f.config)
	prepared, err := f.ensure(true)
	if err != nil || prepared.ToolEnv["PUBLIC_REFERENCE"] != "reference" || prepared.Instructions != "backend instructions" {
		t.Fatal(prepared, err)
	}
	if r.clones != len(f.config.Members) || r.configured != len(f.config.Members) {
		t.Fatal("members were not independently prepared", r)
	}
	file := filepath.Join(f.member(0), "task-state")
	if err := os.WriteFile(file, []byte("member work"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ensure(false); err != nil {
		t.Fatal(err)
	}
	if r.clones != len(f.config.Members) {
		t.Fatal("revisit cloned again")
	}
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "member work" {
		t.Fatal("revisit altered member files", err)
	}
	if _, err := os.Stat(filepath.Join(f.member(1), "task-state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("members share working state", err)
	}
	after, _ := json.Marshal(f.config)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("preparation rewrote frozen configuration")
	}
}

func TestWorkspaceBackendPreflightBeforePlatformOrMutation(t *testing.T) {
	f, r, accessCalls := backendWorkspaceFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.member(1)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), f.member(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ensure(true); err == nil {
		t.Fatal("accepted unsafe second member")
	}
	if r.clones != 0 || r.configured != 0 || *accessCalls != 0 {
		t.Fatal("mutated before checking all member paths")
	}
	if _, err := os.Stat(f.member(0)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created first member before checking second", err)
	}
}

func TestWorkspaceBackendFailureMarkerSurvivesMissingOrWrongTarget(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "wrong-identity"}[wrong], func(t *testing.T) {
			f, r, _ := backendWorkspaceFixture(t)
			r.failClone, r.wrongClone = !wrong, wrong
			if _, err := f.ensure(true); err == nil {
				t.Fatal("accepted failed or mismatched clone")
			}
			if _, err := os.Stat(f.member(0) + ".clone-pending"); err != nil {
				t.Fatal("lost clone marker", err)
			}
			r.failClone, r.wrongClone = false, false
			if _, err := f.ensure(true); err == nil || r.clones != 1 || r.configured != 0 {
				t.Fatal("replayed or configured an unverified clone", err)
			}
		})
	}
}
