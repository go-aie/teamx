package inputstream

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestConsumptionReceiptAfterReclaimedCheckpoint(t *testing.T) {
	for _, mode := range []string{"same_process", "reopen", "legacy_pending_base", "confirmation_commit_interrupted", "retained_checkpoint_conflict"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			r := openTestRepo(t, dir)
			stream := consumeRecord(t, r, "approval", "old", "A", 102)
			first := Execution{"generation", 1}
			if err := r.Begin(first); err != nil {
				t.Fatal(err)
			}
			consumePage(t, r, first, ReadRequest{Turn: 1, Stream: stream, ID: "old-page"})
			if err := r.Seal(first); err != nil {
				t.Fatal(err)
			}
			facts := ReleaseSet{Revision: "verified", Records: map[uint64]ReleaseFacts{102: {Revision: "verified", SourceConfirmed: true, BusinessReleased: true}}}
			facts, err := r.ConsumptionRelease("approval", facts)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := r.PrepareReclaim("approval", facts, time.Now().Add(30*24*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.CommitReclaim("approval", plan, facts); err != nil {
				t.Fatal(err)
			}
			if r.parts["approval"].state.Reclaimed != 102 {
				t.Fatal("unexpected reclaim boundary")
			}
			if got := consumeRecord(t, r, "approval", "new", "A", 162); got != stream {
				t.Fatal("stream changed")
			}
			second := Execution{"generation", 2}
			if err = r.Begin(second); err != nil {
				t.Fatal(err)
			}
			q, err := r.BindCall(second, "new-call", stream, 0)
			if err != nil {
				t.Fatal(err)
			}
			a, err := r.Reserve(second, q)
			if err != nil || a.From != 102 || len(a.Entries) != 1 || a.Entries[0].Seq != 162 {
				t.Fatal("incorrect retained page", a, err)
			}
			body := json.RawMessage(`{"entries":["new body"],"remaining_entries":0}`)
			if err = r.Prepare(second, a.ID, body); err != nil {
				t.Fatal(err)
			}
			if err = r.Handoff(second, a.ID); err != nil {
				t.Fatal(err)
			}
			checkpoint, err := r.loadConsumption("approval")
			if err != nil || len(checkpoint.Through) != 0 {
				t.Fatal("active page changed checkpoint compaction", checkpoint, err)
			}
			switch mode {
			case "reopen":
				r = openTestRepo(t, dir)
			case "legacy_pending_base":
				state, err := r.State(second)
				if err != nil {
					t.Fatal(err)
				}
				// Reproduce a pending page written before the effective base was
				// used. Reopening must retain its identity and cached body.
				state.Attempts[0].From = 0
				if err = r.saveCoordinator(state); err != nil {
					t.Fatal(err)
				}
				r = openTestRepo(t, dir)
			case "confirmation_commit_interrupted":
				interrupted := errors.New("interrupted after native confirmation")
				r.fault = func(point string) error {
					if point == "page_confirmed" {
						return interrupted
					}
					return nil
				}
				if err = r.Confirm(second, a.ID); !errors.Is(err, interrupted) {
					t.Fatal("native confirmation not reached", err)
				}
				r = openTestRepo(t, dir)
			case "retained_checkpoint_conflict":
				checkpoint.Through[stream] = 103
				checkpoint.Checksum = ""
				checkpoint.Checksum = checksumState(checkpoint)
				if err = r.save(r.consumptionPath("approval"), checkpoint); err != nil {
					t.Fatal(err)
				}
				if err = r.Confirm(second, a.ID); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "input consumption position changed") {
					t.Fatal("real checkpoint conflict not reported", err)
				}
				if err = r.Barrier(second); !errors.Is(err, ErrBusy) {
					t.Fatal("conflicting page was acknowledged", err)
				}
				return
			}
			before, err := r.State(second)
			if err != nil {
				t.Fatal(err)
			}
			if err = r.Confirm(second, a.ID); err != nil {
				t.Fatal("receipt rejected", err)
			}
			if err = r.Receive(second, "new-call"); err != nil {
				t.Fatal(err)
			}
			if err = r.Barrier(second); err != nil {
				t.Fatal(err)
			}
			after, err := r.State(second)
			if err != nil {
				t.Fatal(err)
			}
			want := before.Attempts[0]
			want.Phase, want.Received = "settled", true
			if after.Used != 1 || !reflect.DeepEqual(after.Attempts[0], want) {
				t.Fatal("confirmation changed the original page", after)
			}
			if got, err := r.Result(second, a.ID); err != nil || string(got) != string(body) {
				t.Fatal("confirmation changed the cached result", string(got), err)
			}
		})
	}
}
