package inputstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSharedInboxFullCapacityMeasurements(t *testing.T) {
	if os.Getenv("TEAMX_SHARED_CAPACITY") != "1" {
		t.Skip("opt-in full 10000-record persistence measurement")
	}
	r, inbox, a, b, disk := sharedFixture(t)
	dir := r.dir
	ga, _ := r.JoinShared(a)
	gb, _ := r.JoinShared(b)
	runtime.GC()
	var initial runtime.MemStats
	runtime.ReadMemStats(&initial)
	start := time.Now()
	seedSharedFullFixture(t, r, []string{ga.ID, gb.ID})
	appendDuration := time.Since(start)
	u, err := r.InspectUsage()
	if err != nil || u.Records != MaxRecords {
		t.Fatal(u, err)
	}
	payload, _ := json.Marshal(approvalPayload{Instance: "https://approval.example", Account: "reader", Case: "overflow", Node: "n"})
	q := AppendRequest{DeliveryID: "overflow", Channel: "changes", CreatedAt: time.Now().Add(-10 * 24 * time.Hour), ReceivedAt: time.Now(), Payload: payload}
	if _, err := r.AppendOnce("approval", q); !errors.Is(err, ErrCapacity) {
		t.Fatal("capacity gate", err)
	}
	if again := sharedAppend(t, r, "full-0", "stream-0"); again.Seq != 1 {
		t.Fatal(again)
	}
	var rootBytes, bodyBytes int64
	if err := filepath.WalkDir(filepath.Join(dir, "shared-inboxes"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if filepath.Ext(path) == ".jsonl" {
			bodyBytes += info.Size()
		} else {
			rootBytes += info.Size()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	for _, g := range []SharedGrant{ga, gb} {
		consumer, err := OpenRepository(dir, g.Member, []Codec{approvalCodec{provider: "approval"}}, disk)
		if err != nil {
			t.Fatal(err)
		}
		e := Execution{"full-session", 1}
		if err := consumer.Begin(e); err != nil {
			t.Fatal(err)
		}
		loc := Location{r.parts["approval"].state.Spec.Ref, 1}
		request := sharedRequest(t, r, inbox, g, "", loc, 0)
		request.Execution = e
		q := ReadRequest{Turn: 1, ID: "first", Stream: SharedStreamRef(inbox, g.Member, g.ID, request.Stream), Limit: 25}
		request, err = consumer.ReserveSharedIntent(e, q, request)
		if err != nil {
			t.Fatal(err)
		}
		page, err := r.FixSharedPage(request)
		if err != nil {
			t.Fatal(err)
		}
		attempt, err := consumer.ReserveShared(e, q, page)
		if err != nil {
			t.Fatal(err)
		}
		result, _ := json.Marshal(map[string]any{"entries": page.Entries})
		if err := consumer.Prepare(e, attempt.ID, result); err != nil {
			t.Fatal(err)
		}
		if err := consumer.Handoff(e, attempt.ID); err != nil {
			t.Fatal(err)
		}
		c := NewSharedConsumption(inbox, g)
		if err := c.Confirm(page, attempt.Proof()); err != nil {
			t.Fatal(err)
		}
		if err := SaveSharedConsumption(dir, c, disk); err != nil {
			t.Fatal(err)
		}
		if err := consumer.SettleOwned(e, attempt.ID, attempt.Proof()); err != nil {
			t.Fatal(err)
		}
		if err := r.ReleaseSharedPage(page, attempt.Proof(), false); err != nil {
			t.Fatal(err)
		}
	}
	firstPageDuration := time.Since(start)
	var memberBytes int64
	_ = filepath.WalkDir(filepath.Join(dir, "agents"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			memberBytes += info.Size()
		}
		return nil
	})
	r = nil
	runtime.GC()
	start = time.Now()
	r, err = OpenSharedRepository(dir, inbox, "events", approvalCodec{provider: "approval"}, disk)
	if err != nil {
		t.Fatal(err)
	}
	reopenDuration := time.Since(start)
	runtime.GC()
	var loaded runtime.MemStats
	runtime.ReadMemStats(&loaded)
	for _, g := range []SharedGrant{ga, gb} {
		if err := r.CloseSharedGrant(g.ID); err != nil {
			t.Fatal(err)
		}
	}
	_, candidates, err := r.ReclaimCandidates("approval", 128)
	if err != nil {
		t.Fatal(err)
	}
	facts := ReleaseSet{Revision: "verified-source-terminal", Records: map[uint64]ReleaseFacts{}}
	for _, l := range candidates {
		facts.Records[l.Seq] = ReleaseFacts{Revision: "source", SourceConfirmed: true, ReceiptReleased: true}
	}
	facts, err = r.OverlaySharedProtection(facts, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := r.PrepareReclaim("approval", facts, time.Now())
	if err != nil || plan.Through != 128 {
		t.Fatal(plan, err)
	}
	if _, err := r.CommitReclaim("approval", plan, facts); err != nil {
		t.Fatal(err)
	}
	if _, err := r.JoinShared(a); err != nil {
		t.Fatal(err)
	}
	appendStart := time.Now()
	receipt, err := r.AppendOnce("approval", q)
	fullAppend := time.Since(appendStart)
	if err != nil || receipt.Seq != MaxRecords+1 {
		t.Fatal("resume original ID after capacity release", receipt, err)
	}
	report := map[string]any{"records": MaxRecords, "streams": 100, "members": 2, "body_bytes": bodyBytes, "root_bytes": rootBytes, "member_state_and_results_bytes": memberBytes, "fixture_seed_seconds": appendDuration.Seconds(), "two_first_pages_seconds": firstPageDuration.Seconds(), "reopen_seconds": reopenDuration.Seconds(), "heap_delta_bytes": int64(loaded.HeapAlloc) - int64(initial.HeapAlloc), "capacity_pause": true, "prefix_reclaimed": 128, "resume_same_id": true, "near_capacity_append_seconds": fullAppend.Seconds(), "setup": "offline complete published fixture; overflow, replay, two first pages, reclaim and resumed append use normal engine", "fixture": "approval codec, short native payload; actual fsync/atomic persistence, no model or provider"}
	encoded, _ := json.Marshal(report)
	t.Log(string(encoded))
	if path := os.Getenv("TEAMX_SHARED_CAPACITY_REPORT"); path != "" {
		if err := os.WriteFile(path, append(encoded, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// Build a complete published fixture once. This measures full-size loading and
// reads without making every capacity check wait for 10000 serial fsyncs.
func seedSharedFullFixture(t *testing.T, r *Repository, recipients []string) {
	t.Helper()
	p := r.parts["approval"]
	root := cloneRoot(p.state)
	now := time.Now().UTC()
	var buffer bytes.Buffer
	var seg segment
	dir := filepath.Join(r.partDir("approval"), "records")
	if err := durableDir(dir); err != nil {
		t.Fatal(err)
	}
	flush := func() {
		if buffer.Len() == 0 {
			return
		}
		body := bytes.Clone(buffer.Bytes())
		seg.Bytes = int64(len(body))
		seg.Hash = Digest(body)
		path := filepath.Join(dir, seg.Name)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		root.Segments = append(root.Segments, seg)
		buffer.Reset()
	}
	for i := range MaxRecords {
		seq := uint64(i + 1)
		key := fmt.Sprintf("full-%d", i)
		payload, _ := json.Marshal(approvalPayload{Instance: "https://approval.example", Account: "reader", Case: fmt.Sprintf("stream-%d", i%100), Node: "node", Text: key})
		projection, err := r.codecs["approval"].Decode(1, payload)
		if err != nil {
			t.Fatal(err)
		}
		rec := StoredRecord{Recipients: recipients, Location: Location{root.Spec.Ref, seq}, DeliveryID: key, Channel: "changes", Codec: "approval.approval", Schema: 1, Group: projection.Group, CreatedAt: now.Add(-10 * 24 * time.Hour), ReceivedAt: now, Payload: payload, DeliveryDigest: Digest(payload)}
		rec.Digest = rec.checksum()
		line, err := encodeLine(rec)
		if err != nil {
			t.Fatal(err)
		}
		if buffer.Len() > 0 && int64(buffer.Len()+len(line)) > DefaultSegmentBytes {
			flush()
		}
		if buffer.Len() == 0 {
			seg = segment{Name: fmt.Sprintf("%020d.jsonl", seq), First: seq}
		}
		seg.Last = seq
		p.records[seq] = rec
		p.offsets[seq] = offset{seg.Name, int64(buffer.Len()), len(line)}
		p.index(r.member, rec, projection)
		buffer.Write(line)
		root.Receipts[deliveryKey(rec.Channel, key)] = Receipt{Channel: rec.Channel, Partition: root.Spec.Ref, Seq: seq, Key: key, Digest: rec.DeliveryDigest, Operation: Ref("append_", root.Spec.Ref, key, rec.DeliveryDigest, seq)}
	}
	flush()
	if err := syncDir(dir); err != nil {
		t.Fatal(err)
	}
	root.Latest = MaxRecords
	root.Revision++
	if err := r.saveRoot("approval", root); err != nil {
		t.Fatal(err)
	}
	p.state = root
}
