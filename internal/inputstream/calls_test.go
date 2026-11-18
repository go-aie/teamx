package inputstream

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestFrameworkCallReplayReopenAndReceiptLoss(t *testing.T) {
	dir := t.TempDir()
	r := openTestRepo(t, dir)
	stream := consumeRecord(t, r, "approval", "a1", "A", 1)
	consumeRecord(t, r, "approval", "a2", "A", 2)
	other := consumeRecord(t, r, "approval", "b", "B", 3)
	e := Execution{"generation", 1}
	if err := r.Begin(e); err != nil {
		t.Fatal(err)
	}
	q, err := r.BindCall(e, "logical-1", stream, 1)
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.Reserve(e, q)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.Prepare(e, a.ID, json.RawMessage(`{"entries":["body1"],"remaining_entries":1}`)); err != nil {
		t.Fatal(err)
	}
	if err = r.Handoff(e, a.ID); err != nil {
		t.Fatal(err)
	}
	r = openTestRepo(t, dir)
	replay, err := r.BindCall(e, "logical-2", stream, 2)
	if err != nil || replay != q {
		t.Fatal(replay, err)
	}
	if _, err = r.BindCall(e, "cannot-switch", other, 1); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	lost := errors.New("interrupted after native confirmation")
	r.fault = func(point string) error {
		if point == "page_confirmed" {
			return lost
		}
		return nil
	}
	if err = r.Confirm(e, a.ID); !errors.Is(err, lost) {
		t.Fatal("missing confirmation fault", err)
	}
	r = openTestRepo(t, dir)
	if err := r.Barrier(e); err != nil {
		t.Fatal("durable native confirmation should settle the framework receipt", err)
	}
	if err = r.Receive(e, "logical-2"); err != nil {
		t.Fatal(err)
	}
	if err = r.Receive(e, "logical-2"); err != nil {
		t.Fatal("receipt retry", err)
	}
	next, err := r.BindCall(e, "logical-3", stream, 1)
	if err != nil || reflect.DeepEqual(next, q) {
		t.Fatal(next, err)
	}
	b, err := r.Reserve(e, next)
	if err != nil || len(b.Entries) != 1 || b.Entries[0].Seq != 2 || b.Remaining != 0 {
		t.Fatal(b, err)
	}
	state, _ := r.State(e)
	if state.Used != 2 {
		t.Fatal(state.Used)
	}
	if _, err = r.BindCall(Execution{"generation", 2}, "late", stream, 1); !errors.Is(err, ErrIdentity) {
		t.Fatal("old execution impersonation", err)
	}
}
