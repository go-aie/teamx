package inputstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

func consumeRecord(t *testing.T, r *Repository, provider, key, stream string, seq uint64) string {
	t.Helper()
	payload, _ := json.Marshal(approvalPayload{Instance: "https://approval.example", Account: "reader", Workflow: "workflow", WorkflowName: "审批", Case: stream, Node: "review", Text: key})
	receipt, err := r.AppendOnce(provider, AppendRequest{DeliveryID: key, Channel: "changes", Seq: seq, CreatedAt: time.Now().Add(-7 * 24 * time.Hour), ReceivedAt: time.Now(), Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	rec := r.parts[provider].records[receipt.Seq]
	return rec.Group.StreamRef(r.member, provider, rec.Channel)
}
func consumePage(t *testing.T, r *Repository, e Execution, q ReadRequest) ReadAttempt {
	t.Helper()
	a, err := r.Reserve(e, q)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Prepare(e, a.ID, json.RawMessage(`{"body":"saved evidence"}`)); err != nil {
		t.Fatal(err)
	}
	if err := r.Handoff(e, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.Confirm(e, a.ID); err != nil {
		t.Fatal(err)
	}
	return a
}
func TestConsumptionStreamsBoundsQuotaAndReopen(t *testing.T) {
	dir := t.TempDir()
	r := openTestRepo(t, dir, approvalCodec{provider: "a"}, approvalCodec{provider: "b"})
	a := consumeRecord(t, r, "a", "a1", "A", 1)
	b := consumeRecord(t, r, "a", "b", "B", 2)
	consumeRecord(t, r, "a", "a3", "A", 3)
	c := consumeRecord(t, r, "b", "c1", "C", 1)
	e := Execution{"generation", 1}
	if err := r.Begin(e); err != nil {
		t.Fatal(err)
	}
	consumeRecord(t, r, "a", "later", "A", 9)
	page := consumePage(t, r, e, ReadRequest{Turn: 1, Stream: a, ID: "A"})
	if !reflect.DeepEqual(page.Entries, []Location{{page.Snapshot.Partition, 1}, {page.Snapshot.Partition, 3}}) {
		t.Fatal(page)
	}
	obs, err := r.ConsumptionObservation(e)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := r.Catalog(1, obs)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := cat.ReadableList(ListRequest{})
	if len(list.Streams) != 2 {
		t.Fatal(list)
	}
	consumePage(t, r, e, ReadRequest{Turn: 1, Stream: c, ID: "C"})
	r = openTestRepo(t, dir, approvalCodec{provider: "a"}, approvalCodec{provider: "b"})
	if err := r.Begin(e); err != nil {
		t.Fatal(err)
	}
	state, _ := r.State(e)
	if state.Used != 3 {
		t.Fatal(state.Used)
	}
	if _, err := r.Reserve(e, ReadRequest{Turn: 1, Stream: a, ID: "A", Limit: 1}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	again, err := r.Reserve(e, ReadRequest{Turn: 1, Stream: a, ID: "A"})
	if err != nil || again.ID != page.ID || again.Phase != "settled" {
		t.Fatal(again, err)
	}
	cp, _ := r.loadConsumption("a")
	if cp.Through[a] != 3 || cp.Through[b] != 0 {
		t.Fatal(cp)
	}
	consumePage(t, r, e, ReadRequest{Turn: 1, Stream: b, ID: "B"})
	e2 := Execution{"rotated", 2}
	if err := r.Begin(e2); err != nil {
		t.Fatal(err)
	}
	pending, _ := r.PendingPage(e2)
	if pending == nil || pending.Stream != b || !pending.Confirmed {
		t.Fatal(pending)
	}
	if _, err := r.Reserve(e, ReadRequest{Turn: 1, Stream: a, ID: "A"}); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		consumeRecord(t, r, "a", fmt.Sprint("new", i), "A", uint64(10+i))
	}
	if err := r.Begin(Execution{"rotated", 3}); err != nil {
		t.Fatal(err)
	}
	e3 := Execution{"rotated", 3}
	p := consumePage(t, r, e3, ReadRequest{Turn: 3, Stream: a, ID: "limit"})
	if len(p.Entries) != 25 || !p.HasMore || p.Next != "" {
		t.Fatal(p)
	}
	st, _ := r.State(e3)
	if st.Used != 25 {
		t.Fatal(st.Used)
	}
}
func TestConsumptionFailureBoundaries(t *testing.T) {
	for _, point := range []string{"page_reserved", "page_result_saved", "page_prepared", "page_handoff", "page_confirmed", "page_settled", "page_aborted"} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			codec := approvalCodec{provider: "a"}
			r := openTestRepo(t, dir, codec)
			stream := consumeRecord(t, r, "a", "one", "A", 1)
			e := Execution{"g", 1}
			if err := r.Begin(e); err != nil {
				t.Fatal(err)
			}
			r.fault = func(p string) error {
				if p == point {
					return errors.New("crash")
				}
				return nil
			}
			q := ReadRequest{Turn: 1, Stream: stream, ID: "read"}
			a, err := r.Reserve(e, q)
			if err == nil && point == "page_aborted" {
				err = r.Abort(e, a.ID, "source unavailable")
			} else if err == nil {
				err = r.Prepare(e, a.ID, json.RawMessage(`{"body":"fixed"}`))
				if err == nil {
					err = r.Handoff(e, a.ID)
				}
				if err == nil {
					err = r.Confirm(e, a.ID)
				}
			}
			if !errors.Is(err, ErrCommitUnknown) {
				t.Fatal(err)
			}
			if _, err := r.State(e); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal(err)
			}
			r = openTestRepo(t, dir, codec)
			a, err = r.Reserve(e, q)
			if err != nil {
				t.Fatal(err)
			}
			st, _ := r.State(e)
			if point == "page_aborted" {
				if st.Used != 0 || a.Phase != "failed_before_output" || r.Barrier(e) != nil {
					t.Fatal(st)
				}
				if err := r.Abort(e, a.ID, "same"); err != nil {
					t.Fatal(err)
				}
				return
			}
			if st.Used != 1 {
				t.Fatal(st.Used)
			}
			if a.Phase == "reserved" {
				if err := r.Prepare(e, a.ID, json.RawMessage(`{"body":"fixed"}`)); err != nil {
					t.Fatal(err)
				}
			}
			if err := r.Handoff(e, a.ID); err != nil {
				t.Fatal(err)
			}
			if point != "page_settled" {
				if err := r.Abort(e, a.ID, "cannot release"); !errors.Is(err, ErrBusy) {
					t.Fatal(err)
				}
			}
			if err := r.Confirm(e, a.ID); err != nil {
				t.Fatal(err)
			}
			if err := r.Confirm(e, a.ID); err != nil {
				t.Fatal(err)
			}
			cp, _ := r.loadConsumption("a")
			if cp.Revision != 2 || cp.Through[stream] != 1 {
				t.Fatal(cp)
			}
		})
	}
}
func TestConsumptionAbortHandoffRaceAndBusy(t *testing.T) {
	for i := 0; i < 12; i++ {
		r := openTestRepo(t, t.TempDir(), approvalCodec{provider: "a"})
		a := consumeRecord(t, r, "a", "a", "A", 1)
		b := consumeRecord(t, r, "a", "b", "B", 2)
		e := Execution{"g", 1}
		_ = r.Begin(e)
		page, err := r.Reserve(e, ReadRequest{Turn: 1, Stream: a, ID: "a"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Reserve(e, ReadRequest{Turn: 1, Stream: b, ID: "b"}); !errors.Is(err, ErrBusy) {
			t.Fatal(err)
		}
		_ = r.Prepare(e, page.ID, json.RawMessage(`{"body":"fixed"}`))
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = r.Abort(e, page.ID, "local") }()
		go func() { defer wg.Done(); _ = r.Handoff(e, page.ID) }()
		wg.Wait()
		state, _ := r.State(e)
		p := state.Attempts[0]
		if p.Phase == "failed_before_output" {
			if state.Used != 0 {
				t.Fatal(state)
			}
			consumePage(t, r, e, ReadRequest{Turn: 1, Stream: b, ID: "b"})
		} else {
			if p.Phase != "awaiting_confirmation" || state.Used != 1 {
				t.Fatal(state)
			}
			if err := r.Barrier(e); !errors.Is(err, ErrBusy) {
				t.Fatal(err)
			}
		}
	}
}
func TestConsumptionSparseDeepPagingAndNewPartition(t *testing.T) {
	r := openTestRepo(t, t.TempDir(), approvalCodec{provider: "a"}, approvalCodec{provider: "b"})
	consumeRecord(t, r, "a", "noise", "noise", 1)
	a := consumeRecord(t, r, "a", "deep", "A", 900)
	consumeRecord(t, r, "a", "next", "A", 950)
	e := Execution{"g", 1}
	_ = r.Begin(e)
	b := consumeRecord(t, r, "b", "new", "B", 1)
	if _, err := r.Reserve(e, ReadRequest{Turn: 1, Stream: b, ID: "later"}); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	page := consumePage(t, r, e, ReadRequest{Turn: 1, Stream: a, ID: "deep", Limit: 1})
	if page.Through != 900 || page.Next == "" {
		t.Fatal(page)
	}
	next := consumePage(t, r, e, ReadRequest{Turn: 1, Cursor: page.Next})
	if next.From != 900 || next.Through != 950 || len(next.Entries) != 1 {
		t.Fatal(next)
	}
	cp, _ := r.loadConsumption("a")
	if len(cp.Through) != 1 {
		t.Fatal(cp)
	}
}
func TestConsumptionCorruptionAndResultLoss(t *testing.T) {
	for _, file := range []string{"input-state.json", "consumption.json", "result"} {
		t.Run(file, func(t *testing.T) {
			dir := t.TempDir()
			codec := approvalCodec{provider: "a"}
			r := openTestRepo(t, dir, codec)
			a := consumeRecord(t, r, "a", "a", "A", 1)
			e := Execution{"g", 1}
			_ = r.Begin(e)
			page := consumePage(t, r, e, ReadRequest{Turn: 1, Stream: a, ID: "a"})
			path := r.coordinatorPath()
			if file == "consumption.json" {
				path = r.consumptionPath("a")
			}
			if file == "result" {
				path = r.resultPath(page.ID)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if file == "consumption.json" {
				if _, err := OpenRepository(dir, r.member, []Codec{codec}, &diskPersistence{}); !errors.Is(err, ErrCorrupt) {
					t.Fatal(err)
				}
			} else if file == "result" {
				if _, err := r.Result(e, page.ID); !errors.Is(err, ErrExpired) {
					t.Fatal(err)
				}
			}
			if file == "input-state.json" { // Persistent authority must not silently renew a round's budget.
				_, err := OpenRepository(dir, r.member, []Codec{codec}, &diskPersistence{})
				if !errors.Is(err, ErrCorrupt) {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestReadArgumentsRejectNativeNullAndChangedContinuation(t *testing.T) {
	for _, b := range []string{`{"turn_number":1,"stream_ref":"s","read_id":"r","project":1}`, `{"turn_number":1,"cursor":"c","limit":1}`, `{"turn_number":1,"stream_ref":"s","read_id":"r","limit":0}`, `{"turn_number":1,"stream_ref":"s","read_id":"r","cursor":""}`, `{"turn_number":1,"stream_ref":"s","read_id":"r","limit":null}`, `{"turn_number":1,"turn_number":2,"stream_ref":"s","read_id":"r"}`} {
		if _, err := DecodeRead([]byte(b)); err == nil {
			t.Fatal(b)
		}
	}
}

func TestConsumptionProgressRetainsFiniteStallAcrossArrivalsAndRotation(t *testing.T) {
	dir := t.TempDir()
	codec := approvalCodec{provider: "a"}
	r := openTestRepo(t, dir, codec)
	a := consumeRecord(t, r, "a", "a", "A", 1)
	e := Execution{"g", 1}
	if err := r.Begin(e); err != nil {
		t.Fatal(err)
	}
	if err := r.Seal(e); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	backoff := []time.Duration{15 * time.Second, time.Minute, 5 * time.Minute}
	stalled, err := r.ReconcileStall(e, now, backoff)
	if err != nil || stalled == nil {
		t.Fatal(stalled, err)
	}
	if err := r.ClaimStall(e, now, backoff); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		due := stalled.Next
		if err := r.ClaimStall(e, due, backoff); err != nil {
			t.Fatal(err)
		}
		r = openTestRepo(t, dir, codec)
		consumeRecord(t, r, "a", fmt.Sprint("arrival", i), "new", uint64(2+i))
		e = Execution{fmt.Sprint("rotation", i), uint64(2 + i)}
		if err := r.Begin(e); err != nil {
			t.Fatal(err)
		}
		if err := r.Seal(e); err != nil {
			t.Fatal(err)
		}
		stalled, err = r.ReconcileStall(e, due, backoff)
		if err != nil || stalled.Attempts != i+1 || stalled.Bounds["a"].Through != 1 {
			t.Fatal(stalled, err)
		}
	}
	if err := r.ClaimStall(e, now.Add(time.Hour), backoff); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	e = Execution{"progress", 5}
	if err := r.Begin(e); err != nil {
		t.Fatal(err)
	}
	consumePage(t, r, e, ReadRequest{Turn: 5, Stream: a, ID: "finally"})
	stalled, err = r.ReconcileStall(e, now.Add(time.Hour), backoff)
	if err != nil || stalled != nil {
		t.Fatal(stalled, err)
	}
}
func TestConsumptionAuthorityDrivesReclaim(t *testing.T) {
	r := openTestRepo(t, t.TempDir(), approvalCodec{provider: "a"}, approvalCodec{provider: "b"})
	a := consumeRecord(t, r, "a", "a1", "A", 1)
	consumeRecord(t, r, "a", "b", "B", 2)
	consumeRecord(t, r, "a", "a3", "A", 3)
	c := consumeRecord(t, r, "b", "c", "C", 1)
	e := Execution{"g", 1}
	_ = r.Begin(e)
	consumePage(t, r, e, ReadRequest{Turn: 1, Stream: a, ID: "a"})
	consumePage(t, r, e, ReadRequest{Turn: 1, Stream: c, ID: "c"})
	facts, err := r.ConsumptionRelease("a", allFacts(r.parts["a"]))
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Records[1].Consumed || facts.Records[2].Consumed || !facts.Records[3].Consumed {
		t.Fatal(facts)
	}
	plan, err := r.PrepareReclaim("a", facts, time.Now())
	if err != nil || plan.Usage.Records != 0 {
		t.Fatal(plan, err)
	}
	if err := r.Seal(e); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"a", "b"} {
		facts, err := r.ConsumptionRelease(provider, allFacts(r.parts[provider]))
		if err != nil {
			t.Fatal(err)
		}
		plan, err := r.PrepareReclaim(provider, facts, time.Now())
		if err != nil || plan.Through != 1 {
			t.Fatal(plan, err)
		}
		if _, err := r.CommitReclaim(provider, plan, facts); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.parts["a"].records) != 2 || len(r.parts["b"].records) != 0 {
		t.Fatal("incorrect prefix")
	}
}

func TestConsumptionCatalogHidesLaterInputs(t *testing.T) {
	r := openTestRepo(t, t.TempDir(), approvalCodec{provider: "a"}, approvalCodec{provider: "b"})
	consumeRecord(t, r, "a", "old", "A", 1)
	e := Execution{"g", 1}
	if err := r.Begin(e); err != nil {
		t.Fatal(err)
	}
	consumeRecord(t, r, "b", "new-provider", "new", 1)
	payload, _ := json.Marshal(approvalPayload{Instance: "https://approval.example", Account: "reader", Workflow: "late-workflow", WorkflowName: "later-scope", Case: "later-stream", Node: "review", Text: "later entry"})
	if _, err := r.AppendOnce("a", AppendRequest{DeliveryID: "later", Channel: "changes", CreatedAt: time.Now(), ReceivedAt: time.Now(), Payload: payload}); err != nil {
		t.Fatal(err)
	}
	obs, err := r.ConsumptionObservation(e)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := r.Catalog(1, obs)
	if err != nil {
		t.Fatal(err)
	}
	list, err := cat.ReadableList(ListRequest{})
	if err != nil || len(list.Streams) != 1 {
		t.Fatal(list, err)
	}
}
func TestConsumptionMaterialReservationDoesNotRenewAfterCrash(t *testing.T) {
	dir := t.TempDir()
	codec := approvalCodec{provider: "a"}
	r := openTestRepo(t, dir, codec)
	a := consumeRecord(t, r, "a", "a", "A", 1)
	e := Execution{"g", 1}
	_ = r.Begin(e)
	page, err := r.Reserve(e, ReadRequest{Turn: 1, Stream: a, ID: "a"})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := r.ClaimMaterials(e, page.ID, 1, 4, 4<<20, int64(time.Second))
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	r = openTestRepo(t, dir, codec)
	ok, err = r.ClaimMaterials(e, page.ID, 1, 4, 4<<20, int64(time.Second))
	if err != nil || ok {
		t.Fatal(ok, err)
	}
	state, _ := r.State(e)
	if state.MaterialImages != 4 || state.MaterialBytes != 4<<20 || state.MaterialScans != 1 {
		t.Fatal(state)
	}
}

func TestConsumptionDirectReadOfNineHundredthRecord(t *testing.T) {
	dir := benchmarkFixture(t, 1000, DefaultSegmentBytes)
	r := openBenchmarkRepo(t, dir)
	e := Execution{"deep", 1}
	if err := r.Begin(e); err != nil {
		t.Fatal(err)
	}
	rec := r.parts["approval"].records[900]
	stream := rec.Group.StreamRef(r.member, "approval", rec.Channel)
	page := consumePage(t, r, e, ReadRequest{Turn: 1, Stream: stream, ID: "deep-target"})
	if len(page.Entries) != 1 || page.Entries[0].Seq != 900 {
		t.Fatal(page)
	}
	c, err := r.loadConsumption("approval")
	if err != nil || len(c.Through) != 1 || c.Through[stream] != 900 {
		t.Fatal(c, err)
	}
	pending, err := r.PendingLocations(nil)
	if err != nil || len(pending) != 999 {
		t.Fatal(len(pending), err)
	}
}

func TestReclaimedPositionsReleaseCheckpointSlotsButKeepDeliveryReceipts(t *testing.T) {
	dir := t.TempDir()
	r := openTestRepo(t, dir)
	stream := consumeRecord(t, r, "approval", "read", "A", 11)
	consumeRecord(t, r, "approval", "skip", "B", 41)
	e := Execution{"generation", 1}
	if err := r.Begin(e); err != nil {
		t.Fatal(err)
	}
	consumePage(t, r, e, ReadRequest{Turn: 1, Stream: stream, ID: "read"})
	part := r.parts["approval"]
	if err := r.Skip(e, part.records[41].Location, "locally excluded"); err != nil {
		t.Fatal(err)
	}
	if err := r.Seal(e); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(7 * 24 * time.Hour)
	facts, err := r.ConsumptionRelease("approval", allFacts(part))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := r.PrepareReclaim("approval", facts, now)
	if err != nil || plan.Through != 41 {
		t.Fatal(plan, err)
	}
	if _, err := r.CommitReclaim("approval", plan, facts); err != nil {
		t.Fatal(err)
	}

	r = openTestRepo(t, dir)
	c, err := r.loadConsumption("approval")
	if err != nil || len(c.Through) != 0 || len(c.Skipped) != 0 || c.Proof == "" {
		t.Fatal(c, err)
	}
	if len(r.parts["approval"].state.Receipts) != 2 {
		t.Fatal("body reclamation removed delivery evidence")
	}
	consumeRecord(t, r, "approval", "read-again", "A", 83)
	if err := r.Begin(Execution{"generation", 2}); err != nil {
		t.Fatal(err)
	}
	locs, err := r.PendingStream(Execution{"generation", 2}, stream)
	if err != nil || len(locs) != 1 || locs[0].Seq != 83 {
		t.Fatal(locs, err)
	}
}
