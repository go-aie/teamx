//go:build integration || e2e

package room

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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
