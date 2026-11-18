package inputstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A deliberately different native domain: approval instances and nodes.
// No project/IID/chat fields or family switches are added to the common layer.
type approvalPayload struct{ Instance, Account, Workflow, WorkflowName, Case, Node, Status, Text string }
type approvalCodec struct {
	provider string
	policy   Policy
	channels []string
}

func (c approvalCodec) Descriptor() Descriptor {
	p := c.policy
	if p.Version == "" {
		p = PlatformPolicy()
	}
	channels := c.channels
	if len(channels) == 0 {
		channels = []string{"changes", "decisions"}
	}
	return Descriptor{c.provider, c.provider + ".approval", 1, 1, channels, p}
}
func (c approvalCodec) Decode(version int, b json.RawMessage) (Projection, error) {
	var p approvalPayload
	if version != 1 {
		return Projection{}, ErrVersion
	}
	if err := StrictDecode(b, &p); err != nil {
		return Projection{}, err
	}
	g := Group{Version: 1, Instance: p.Instance, Account: p.Account, Context: []string{p.Case}, Precision: "context"}
	if err := g.Validate(); err != nil {
		return Projection{}, err
	}
	return Projection{Group: g, Label: p.Case, SourceLabel: p.Account, Incomplete: p.Node == ""}, nil
}

type diskPersistence struct {
	before func(string, any) error
	after  func(string, any) error
}

func (d *diskPersistence) SaveInputJSON(path string, v any) error {
	if d.before != nil {
		if err := d.before(path, v); err != nil {
			return err
		}
	}
	if err := durableDir(filepath.Dir(path)); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".snapshot-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return errors.Join(ErrCommitUnknown, err)
	}
	if err = syncDir(filepath.Dir(path)); err != nil {
		return errors.Join(ErrCommitUnknown, err)
	}
	if d.after != nil {
		return d.after(path, v)
	}
	return nil
}

var storedMember = Member{"room", "instance", "member"}
var storedTime = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

func request(key, caseID, node, text string) AppendRequest {
	b, _ := json.Marshal(approvalPayload{"https://approval.test", "account", "workflow", "采购审批", caseID, node, "waiting", text})
	return AppendRequest{DeliveryID: key, Channel: "changes", CreatedAt: storedTime, ReceivedAt: storedTime.Add(time.Hour), Payload: b}
}
func openTestRepo(t *testing.T, dir string, codecs ...Codec) *Repository {
	t.Helper()
	if len(codecs) == 0 {
		codecs = []Codec{approvalCodec{provider: "approval"}}
	}
	r, err := OpenRepository(dir, storedMember, codecs, &diskPersistence{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func appendTest(t *testing.T, r *Repository, provider string, q AppendRequest) Receipt {
	t.Helper()
	v, err := r.AppendOnce(provider, q)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func allFacts(p *partition, blocked ...uint64) ReleaseSet {
	f := ReleaseSet{Revision: "domain-1", Records: map[uint64]ReleaseFacts{}}
	for n := range p.records {
		f.Records[n] = ReleaseFacts{Revision: "saved-1", Consumed: true, SourceConfirmed: true, BusinessReleased: true}
	}
	for _, n := range blocked {
		v := f.Records[n]
		v.Consumed = false
		f.Records[n] = v
	}
	return f
}
func TestLogPublishedRangesSparseIdentityAndReceipts(t *testing.T) {
	dir := t.TempDir()
	r := openTestRepo(t, dir)
	r.segmentBytes = 1800
	q := request("same", "case-A", "manager", "first\n第二行 \"quote\" \\ slash")
	q.Seq = 3
	a := appendTest(t, r, "approval", q)
	q2 := request("other", "case-B", "finance", "正文")
	q2.Seq = 9
	appendTest(t, r, "approval", q2)
	q3 := request("third", "case-A", "manager", "tail")
	q3.Seq = 17
	appendTest(t, r, "approval", q3)
	if len(r.parts["approval"].state.Segments) < 2 {
		t.Fatal("did not cross segments")
	}
	replay := appendTest(t, r, "approval", q)
	if replay != a {
		t.Fatal("receipt changed")
	}
	changed := q
	changed.Payload = request("same", "case-A", "manager", "different").Payload
	if _, err := r.AppendOnce("approval", changed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	// Same delivery ID in a new channel has its own dedup namespace.
	q.Channel = "decisions"
	q.Seq = 18
	other := appendTest(t, r, "approval", q)
	if other.Seq == a.Seq {
		t.Fatal("channels merged")
	}
	reopened := openTestRepo(t, dir)
	snap, _ := reopened.Snapshot("approval")
	locs, err := reopened.Scan(snap)
	if err != nil || len(locs) != 4 {
		t.Fatal(locs, err)
	}
	for _, l := range locs {
		record, err := reopened.Read(snap, l)
		if err != nil || record.Location != l {
			t.Fatal(err)
		}
	}
	if _, err := reopened.Read(snap, Location{"another-member", 3}); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	if _, err := reopened.Read(snap, Location{snap.Partition, 4}); !errors.Is(err, ErrCorrupt) {
		t.Fatal("gap is not a retained record", err)
	}
	if replay := appendTest(t, reopened, "approval", q2); replay.Seq != 9 {
		t.Fatal(replay)
	}
	if u, _ := reopened.InspectUsage(); u.Records != 4 {
		t.Fatal(u)
	}
}
func TestLogAppendCrashRecovery(t *testing.T) {
	for _, point := range []string{"reserved", "body_synced", "published", "settled"} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			r := openTestRepo(t, dir)
			first := appendTest(t, r, "approval", request("old", "A", "manager", "old"))
			q := request("new", "A", "manager", "new")
			r.fault = func(at string) error {
				if at == point {
					return fmt.Errorf("crash at %s", at)
				}
				return nil
			}
			if _, err := r.AppendOnce("approval", q); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal(err)
			}
			if _, err := r.InspectUsage(); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal("unfrozen", err)
			}
			reopened := openTestRepo(t, dir)
			u, _ := reopened.InspectUsage()
			want := uint64(1)
			if point == "published" || point == "settled" {
				want = 2
			}
			if u.Records != want {
				t.Fatal(u, want)
			}
			receipt := appendTest(t, reopened, "approval", q)
			if receipt.Seq != first.Seq+1 {
				t.Fatal(receipt)
			}
			again := appendTest(t, reopened, "approval", q)
			if receipt != again {
				t.Fatal("not idempotent")
			}
			if u, _ := reopened.InspectUsage(); u.Records != 2 {
				t.Fatal(u)
			}
			if len(reopened.capacity.Pending) != 0 {
				t.Fatal("reservation leaked")
			}
		})
	}
	// The first delivery can die before a root exists. Reopen keeps its reserved
	// identity, ignores all unpublished bytes, and retries that exact delivery.
	t.Run("first delivery", func(t *testing.T) {
		dir := t.TempDir()
		r := openTestRepo(t, dir)
		r.fault = func(s string) error {
			if s == "body_synced" {
				return errors.New("crash")
			}
			return nil
		}
		q := request("first", "A", "manager", "hello")
		_, _ = r.AppendOnce("approval", q)
		r = openTestRepo(t, dir)
		if u, _ := r.InspectUsage(); u.Records != 0 {
			t.Fatal(u)
		}
		appendTest(t, r, "approval", q)
	})
}
func TestLogTailsAndCommittedDamage(t *testing.T) {
	for _, tail := range []string{"{\"short\":", "{\"complete\":true}\n"} {
		t.Run(tail, func(t *testing.T) {
			dir := t.TempDir()
			r := openTestRepo(t, dir)
			appendTest(t, r, "approval", request("one", "A", "n", "saved"))
			s := r.parts["approval"].state.Segments[0]
			path := filepath.Join(r.partDir("approval"), "records", s.Name)
			f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			_, _ = f.WriteString(tail)
			_ = f.Close()
			r = openTestRepo(t, dir)
			appendTest(t, r, "approval", request("two", "B", "n", "after"))
			if u, _ := r.InspectUsage(); u.Records != 2 {
				t.Fatal(u)
			}
			if info, _ := os.Stat(path); info.Size() <= s.Bytes {
				t.Fatal("next append missing")
			}
		})
	}
	for _, damage := range []string{"missing", "short", "bad-checksum", "unknown-schema", "missing-root", "missing-ledger"} {
		t.Run(damage, func(t *testing.T) {
			dir := t.TempDir()
			r := openTestRepo(t, dir)
			appendTest(t, r, "approval", request("one", "A", "n", "saved"))
			p := r.parts["approval"]
			seg := p.state.Segments[0]
			path := filepath.Join(r.partDir("approval"), "records", seg.Name)
			switch damage {
			case "missing":
				_ = os.Remove(path)
			case "short":
				_ = os.Truncate(path, seg.Bytes-3)
			case "bad-checksum":
				b, _ := os.ReadFile(path)
				b[10] ^= 1
				_ = os.WriteFile(path, b, 0600)
			case "missing-root":
				_ = os.Remove(filepath.Join(r.partDir("approval"), "partition.json"))
			case "missing-ledger":
				_ = os.Remove(r.capacityPath())
			case "unknown-schema":
				rec := p.records[1]
				rec.Schema = 99
				rec.Digest = rec.checksum()
				b, _ := encodeLine(rec)
				_ = os.WriteFile(path, b, 0600)
				p.state.Segments[0].Bytes = int64(len(b))
				p.state.Segments[0].Hash = Digest(b)
				_ = r.saveRoot("approval", p.state)
			}
			_, err := OpenRepository(dir, storedMember, []Codec{approvalCodec{provider: "approval"}}, &diskPersistence{})
			if err == nil {
				t.Fatal("corruption reset to empty")
			}
			if damage == "unknown-schema" && !errors.Is(err, ErrVersion) {
				t.Fatal(err)
			}
		})
	}
}
func TestLogUnknownCommitAndPrePublishFailure(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			dir := t.TempDir()
			disk := &diskPersistence{}
			r, err := OpenRepository(dir, storedMember, []Codec{approvalCodec{provider: "approval"}}, disk)
			if err != nil {
				t.Fatal(err)
			}
			q := request("one", "A", "n", "saved")
			fail := func(path string, _ any) error {
				if strings.HasSuffix(path, "partition.json") {
					if after {
						return ErrCommitUnknown
					}
					return errors.New("no disk space")
				}
				return nil
			}
			if after {
				disk.after = fail
			} else {
				disk.before = fail
			}
			_, err = r.AppendOnce("approval", q)
			if err == nil {
				t.Fatal("missing failure")
			}
			if after {
				if _, e := r.AppendOnce("approval", q); !errors.Is(e, ErrCommitUnknown) {
					t.Fatal(e)
				}
			}
			r = openTestRepo(t, dir)
			got := appendTest(t, r, "approval", q)
			if got.Seq != 1 {
				t.Fatal(got)
			}
		})
	}
}
func TestSharedCapacityAndPartitionBackpressure(t *testing.T) {
	policy := PlatformPolicy()
	policy.MaxRecords = 2
	codecs := []Codec{approvalCodec{provider: "a", policy: policy}, approvalCodec{provider: "b", policy: PlatformPolicy()}}
	dir := t.TempDir()
	r, err := OpenRepository(dir, storedMember, codecs, &diskPersistence{}, Usage{3, MaxBytes})
	if err != nil {
		t.Fatal(err)
	}
	appendTest(t, r, "a", request("a1", "A", "n", "one"))
	appendTest(t, r, "a", request("a2", "A", "n", "two"))
	if _, err := r.AppendOnce("a", request("a3", "A", "n", "three")); err == nil {
		t.Fatal("partition limit")
	} else {
		var ce *CapacityError
		if !errors.As(err, &ce) || ce.Scope != "partition" {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, err := r.AppendOnce("b", request(fmt.Sprint(n), "B", "n", "body"))
			results <- err
		}(n)
	}
	wg.Wait()
	close(results)
	success, refused := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else {
			var ce *CapacityError
			if !errors.As(err, &ce) || ce.Scope != "member" {
				t.Fatal(err)
			}
			refused++
		}
	}
	if success != 1 || refused != 1 {
		t.Fatal(success, refused)
	}
	reopened, err := OpenRepository(dir, storedMember, codecs, &diskPersistence{})
	if err != nil {
		t.Fatal(err)
	}
	if u, _ := reopened.InspectUsage(); u.Records != 3 {
		t.Fatal(u)
	}
}
func TestReclaimIndependentPrefixesAndCrashRecovery(t *testing.T) {
	for _, point := range []string{"", "reclaim_prepared", "reclaim_rewritten", "reclaim_boundary", "reclaim_deleted", "reclaim_settled"} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			codecs := []Codec{approvalCodec{provider: "a"}, approvalCodec{provider: "b"}}
			r := openTestRepo(t, dir, codecs...)
			r.segmentBytes = 1 << 20
			first := appendTest(t, r, "a", request("a1", "A", "n", "one"))
			appendTest(t, r, "a", request("a2", "B", "n", "two"))
			appendTest(t, r, "a", request("a3", "A", "n", "three"))
			appendTest(t, r, "b", request("b1", "C", "n", "four"))
			facts := allFacts(r.parts["a"], 2)
			now := storedTime.Add(30 * 24 * time.Hour)
			if point != "" {
				r.fault = func(at string) error {
					if at == point {
						return errors.New("crash")
					}
					return nil
				}
			}
			plan, err := r.PrepareReclaim("a", facts, now)
			if point == "reclaim_prepared" {
				if err == nil {
					t.Fatal("missing crash")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if plan.Through != 1 {
					t.Fatal("unconsumed B did not block A3", plan)
				}
				_, err = r.CommitReclaim("a", plan, facts)
				if point != "" && err == nil {
					t.Fatal("missing crash")
				}
				if point == "" && err != nil {
					t.Fatal(err)
				}
			}
			r = openTestRepo(t, dir, codecs...)
			// If the boundary did not publish, resume the persisted intent with exactly
			// the same facts and clock. Otherwise reopening already completed cleanup.
			if r.parts["a"].state.Reclaimed == 0 {
				plan, err = r.PrepareReclaim("a", facts, now)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = r.CommitReclaim("a", plan, facts); err != nil {
					t.Fatal(err)
				}
			}
			snap, _ := r.Snapshot("a")
			if snap.Reclaimed != 1 || snap.Through != 3 {
				t.Fatal(snap)
			}
			if _, err := r.Read(snap, Location{snap.Partition, 1}); !errors.Is(err, ErrExpired) {
				t.Fatal(err)
			}
			if got := appendTest(t, r, "a", request("a1", "A", "n", "one")); got != first {
				t.Fatal("receipt lost after reclaim")
			}
			bFacts := allFacts(r.parts["b"])
			bPlan, err := r.PrepareReclaim("b", bFacts, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.CommitReclaim("b", bPlan, bFacts); err != nil {
				t.Fatal(err)
			}
			if u, _ := r.InspectUsage(); u.Records != 2 {
				t.Fatal(u)
			}
			if r.parts["a"].state.Latest != 3 || r.parts["b"].state.Latest != 1 {
				t.Fatal("high water moved")
			}
			r = openTestRepo(t, dir, codecs...)
			if u, _ := r.InspectUsage(); u.Records != 2 {
				t.Fatal(u)
			}
		})
	}
}
func TestReclaimRequiresCompleteFreshEvidence(t *testing.T) {
	for _, reason := range []string{"missing", "source", "business", "inflight", "unconsumed", "not-expired", "received-time"} {
		t.Run(reason, func(t *testing.T) {
			policy := PlatformPolicy()
			if reason == "received-time" {
				policy.TimeBasis = "received"
			}
			r := openTestRepo(t, t.TempDir(), approvalCodec{provider: "approval", policy: policy})
			q := request("one", "A", "n", "saved")
			q.ReceivedAt = storedTime.Add(60 * 24 * time.Hour)
			appendTest(t, r, "approval", q)
			f := allFacts(r.parts["approval"])
			v := f.Records[1]
			now := storedTime.Add(30 * 24 * time.Hour)
			switch reason {
			case "missing":
				delete(f.Records, 1)
			case "source":
				v.SourceConfirmed = false
				f.Records[1] = v
			case "business":
				v.BusinessReleased = false
				f.Records[1] = v
			case "inflight":
				v.InFlight = true
				f.Records[1] = v
			case "unconsumed":
				v.Consumed = false
				f.Records[1] = v
			case "not-expired":
				now = storedTime.Add(time.Hour)
			}
			p, err := r.PrepareReclaim("approval", f, now)
			if err != nil || p.Through != 0 {
				t.Fatal(p, err)
			}
		})
	}
	r := openTestRepo(t, t.TempDir())
	appendTest(t, r, "approval", request("one", "A", "n", "saved"))
	f := allFacts(r.parts["approval"])
	p, err := r.PrepareReclaim("approval", f, storedTime.Add(30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	f.Revision = "changed"
	if _, err := r.CommitReclaim("approval", p, f); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
}

func TestStorageSizeBoundsAndReceiptCapacity(t *testing.T) {
	policy := PlatformPolicy()
	policy.MaxRecordBytes = 2048
	policy.MaxBytes = 4096
	policy.MaxReceipts = 2
	r := openTestRepo(t, t.TempDir(), approvalCodec{provider: "approval", policy: policy})
	q := request("one", "A", "n", strings.Repeat("x", 3000))
	if _, err := r.AppendOnce("approval", q); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	// Find the exact encoded record boundary, including envelope and newline.
	q = request("one", "A", "n", "")
	base := StoredRecord{Location: Location{approvalCodec{provider: "approval", policy: policy}.Descriptor().Spec(storedMember).Ref, 1}, DeliveryID: q.DeliveryID, Channel: q.Channel, Codec: "approval.approval", Schema: 1, CreatedAt: q.CreatedAt, ReceivedAt: q.ReceivedAt, Payload: q.Payload, DeliveryDigest: Digest(q.Payload)}
	p, _ := r.codecs["approval"].Decode(1, q.Payload)
	base.Group = p.Group
	base.Digest = base.checksum()
	line, _ := encodeLine(base)
	padding := 2048 - len(line)
	q = request("one", "A", "n", strings.Repeat("x", padding))
	appendTest(t, r, "approval", q)
	if u, _ := r.InspectUsage(); u.Bytes != 2048 {
		t.Fatal("incorrect encoded byte metering", u)
	}
	tooLarge := request("two", "A", "n", strings.Repeat("x", padding+1))
	if _, err := r.AppendOnce("approval", tooLarge); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	appendTest(t, r, "approval", request("two", "B", "n", strings.Repeat("x", padding)))
	if _, err := r.AppendOnce("approval", request("tri", "C", "n", "")); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	facts := allFacts(r.parts["approval"])
	plan, _ := r.PrepareReclaim("approval", facts, storedTime.Add(30*24*time.Hour))
	id, err := r.CommitReclaim("approval", plan, facts)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := r.CommitReclaim("approval", plan, facts); err != nil || again != id {
		t.Fatal("reclaim proof not idempotent", again, err)
	}
	if _, err := r.AppendOnce("approval", request("tri", "C", "n", "")); err == nil {
		t.Fatal("receipt proof bound ignored")
	} else {
		var ce *CapacityError
		if !errors.As(err, &ce) || ce.Scope != "receipts" {
			t.Fatal(err)
		}
	}
}
func TestNewChannelReusesPublishedPartitionAndQuota(t *testing.T) {
	dir := t.TempDir()
	r := openTestRepo(t, dir, approvalCodec{provider: "approval", channels: []string{"changes"}})
	first := appendTest(t, r, "approval", request("one", "A", "n", "saved"))
	r = openTestRepo(t, dir, approvalCodec{provider: "approval"})
	q := request("one", "A", "n", "new-channel")
	q.Channel = "decisions"
	next := appendTest(t, r, "approval", q)
	if next.Partition != first.Partition || next.Seq != 2 {
		t.Fatal(next)
	}
	r = openTestRepo(t, dir)
	if u, _ := r.InspectUsage(); u.Records != 2 {
		t.Fatal(u)
	}
}
func TestMissingRootDuringExistingReservationIsCorruption(t *testing.T) {
	dir := t.TempDir()
	r := openTestRepo(t, dir)
	appendTest(t, r, "approval", request("old", "A", "n", "old"))
	r.fault = func(s string) error {
		if s == "reserved" {
			return errors.New("crash")
		}
		return nil
	}
	_, _ = r.AppendOnce("approval", request("new", "B", "n", "new"))
	_ = os.Remove(filepath.Join(r.partDir("approval"), "partition.json"))
	if _, err := OpenRepository(dir, storedMember, []Codec{approvalCodec{provider: "approval"}}, &diskPersistence{}); !errors.Is(err, ErrCorrupt) {
		t.Fatal("lost root mistaken for a fresh partition", err)
	}
}

func TestReclaimProtectsRetainedInFlightSegment(t *testing.T) {
	r := openTestRepo(t, t.TempDir())
	for n := 1; n <= 3; n++ {
		appendTest(t, r, "approval", request(fmt.Sprint(n), "A", "node", "saved"))
	}
	f := allFacts(r.parts["approval"], 2)
	last := f.Records[3]
	last.InFlight = true
	f.Records[3] = last
	plan, err := r.PrepareReclaim("approval", f, storedTime.Add(30*24*time.Hour))
	if err != nil || plan.Through != 0 {
		t.Fatal("retained suffix lost its physical protection", plan, err)
	}
	last.InFlight = false
	f.Records[3] = last
	plan, err = r.PrepareReclaim("approval", f, storedTime.Add(30*24*time.Hour))
	if err != nil || plan.Through != 1 {
		t.Fatal(plan, err)
	}
	if _, err := r.CommitReclaim("approval", plan, f); err != nil {
		t.Fatal(err)
	}
}

func TestReadableDirectoryDoesNotRequireNativeQueryCapabilities(t *testing.T) {
	r := openTestRepo(t, t.TempDir(), approvalCodec{provider: "approval"})
	appendTest(t, r, "approval", request("one", "A", "node", "saved text"))
	c, err := r.Catalog(1, Observation{})
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.ReadableList(ListRequest{})
	if err != nil || len(list.Streams) != 1 {
		t.Fatal(list, err)
	}
}

func TestLongSavedLabelTruncatesOnlyProjection(t *testing.T) {
	r := openTestRepo(t, t.TempDir())
	name := strings.Repeat("名", 800)
	q := request("one", name, "node", "saved text")
	appendTest(t, r, "approval", q)
	c, err := r.Catalog(1, Observation{})
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.ReadableList(ListRequest{})
	if err != nil || len(list.Streams[0].Label) > 200 || !strings.Contains(list.Streams[0].Hint, "标签已截断") {
		t.Fatal(list, err)
	}
	snap, _ := r.Snapshot("approval")
	record, err := r.Read(snap, Location{snap.Partition, 1})
	if err != nil || string(record.Payload) != string(q.Payload) {
		t.Fatal("immutable content was truncated", err)
	}
}

func TestReclaimedOnlyPartitionKeepsHighWatermarkWithoutDeliveryReceipt(t *testing.T) {
	dir := t.TempDir()
	r := openTestRepo(t, dir)
	const highWater = 73
	spec := r.codecs["approval"].Descriptor().Spec(storedMember)
	if err := r.saveCapacity(); err != nil {
		t.Fatal(err)
	}
	if err := r.saveRoot("approval", rootState{
		Version: FormatVersion, Spec: spec, Revision: 1, Latest: highWater, Reclaimed: highWater, Receipts: map[string]Receipt{},
	}); err != nil {
		t.Fatal(err)
	}
	r = openTestRepo(t, dir)
	snapshot, err := r.Snapshot("approval")
	if err != nil || snapshot.Through != highWater || snapshot.Reclaimed != highWater {
		t.Fatal(snapshot, err)
	}
	expired := Location{Partition: spec.Ref, Seq: highWater}
	if _, err := r.Read(snapshot, expired); !errors.Is(err, ErrExpired) {
		t.Fatal("reclaimed body should be absent", err)
	}
	if _, err := r.DeliverySourceRef(expired, "changes", "reclaimed-delivery"); !errors.Is(err, ErrExpired) {
		t.Fatal("receipt was manufactured or could not be classified", err)
	}
	next := appendTest(t, r, "approval", request("reclaimed-delivery", "A", "node", "new input"))
	if next.Seq != highWater+1 {
		t.Fatal("sequence restarted after upgrading empty retention", next)
	}
	if _, err := r.DeliverySourceRef(expired, "changes", next.Key); !errors.Is(err, ErrExpired) {
		t.Fatal("a later redelivery obscured the reclaimed position", err)
	}
}
