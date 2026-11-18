package inputstream

import (
	"errors"
	"os"
	"testing"
)

func TestLocalExclusionBeforeModelTurnReopensWithoutBudgetAuthority(t *testing.T) {
	dir := t.TempDir()
	codec := approvalCodec{provider: "a"}
	r := openTestRepo(t, dir, codec)
	consumeRecord(t, r, "a", "excluded", "A", 1)
	loc := Location{Partition: r.parts["a"].state.Spec.Ref, Seq: 1}
	if err := r.ExcludeLocal(loc, "verified_source_exclusion"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.coordinatorPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("local exclusion created model turn", err)
	}
	r = openTestRepo(t, dir, codec)
	pending, err := r.PendingLocations(nil)
	if err != nil || len(pending) != 0 {
		t.Fatal(pending, err)
	}
	e := Execution{Generation: "new-session", Turn: 1}
	if err := r.Begin(e); err != nil {
		t.Fatal(err)
	}
	state, err := r.State(e)
	if err != nil || state.Used != 0 || len(state.Attempts) != 0 {
		t.Fatal(state, err)
	}
	if r.parts["a"].state.LocalOnly {
		t.Fatal("formal initialization marker not published")
	}
	// Once an execution can allocate delivery, losing its state cannot reset it.
	if err := os.Remove(r.coordinatorPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRepository(dir, storedMember, []Codec{codec}, &diskPersistence{}); !errors.Is(err, ErrCorrupt) {
		t.Fatal("missing model coordinator reset", err)
	}
}
