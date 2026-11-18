package inputstream

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Build a fixture with the exact published format without benchmarking 10,000
// fsyncs as setup. AppendOnce's durable boundaries are exercised separately.
func benchmarkFixture(b testing.TB, n int, segmentBytes int64) string {
	b.Helper()
	dir := b.TempDir()
	codec := approvalCodec{provider: "approval"}
	spec := codec.Descriptor().Spec(storedMember)
	r := openBenchmarkRepo(b, dir)
	root := rootState{Version: FormatVersion, Spec: spec, Revision: uint64(n), Latest: uint64(n), Receipts: map[string]Receipt{}}
	if err := durableDir(filepath.Join(r.partDir("approval"), "records")); err != nil {
		b.Fatal(err)
	}
	var body []byte
	var seg segment
	flush := func() {
		if len(body) == 0 {
			return
		}
		seg.Bytes = int64(len(body))
		seg.Hash = Digest(body)
		if err := os.WriteFile(filepath.Join(r.partDir("approval"), "records", seg.Name), body, 0600); err != nil {
			b.Fatal(err)
		}
		root.Segments = append(root.Segments, seg)
		body = nil
		seg = segment{}
	}
	for k := 1; k <= n; k++ {
		q := request(fmt.Sprintf("delivery-%d", k), fmt.Sprintf("case-%04d", k%1000), fmt.Sprintf("node-%d", k%5), strings.Repeat("本地中文输入 构建结果 ", 30))
		var native approvalPayload
		_ = json.Unmarshal(q.Payload, &native)
		if k%100 == 0 {
			native.Text += "目标失败 发布阻塞"
		}
		q.Payload, _ = json.Marshal(native)
		p, err := codec.Decode(1, q.Payload)
		if err != nil {
			b.Fatal(err)
		}
		rec := StoredRecord{Location: Location{spec.Ref, uint64(k)}, DeliveryID: q.DeliveryID, Channel: q.Channel, Codec: spec.Provider + ".approval", Schema: 1, Group: p.Group, CreatedAt: q.CreatedAt, ReceivedAt: q.ReceivedAt, Payload: q.Payload, DeliveryDigest: Digest(q.Payload)}
		rec.Digest = rec.checksum()
		line, _ := encodeLine(rec)
		if len(body) > 0 && int64(len(body)+len(line)) > segmentBytes {
			flush()
		}
		if len(body) == 0 {
			seg.Name = fmt.Sprintf("%020d.jsonl", k)
			seg.First = uint64(k)
		}
		body = append(body, line...)
		seg.Last = uint64(k)
		root.Receipts[deliveryKey(rec.Channel, rec.DeliveryID)] = Receipt{Partition: spec.Ref, Seq: uint64(k), Key: rec.DeliveryID, Digest: rec.DeliveryDigest, Operation: fmt.Sprintf("append-%d", k), Channel: rec.Channel}
	}
	flush()
	if err := r.saveCapacity(); err != nil {
		b.Fatal(err)
	}
	if err := r.saveRoot("approval", root); err != nil {
		b.Fatal(err)
	}
	return dir
}
func openBenchmarkRepo(b testing.TB, dir string) *Repository {
	b.Helper()
	r, err := OpenRepository(dir, storedMember, []Codec{approvalCodec{provider: "approval"}}, &diskPersistence{})
	if err != nil {
		b.Fatal(err)
	}
	return r
}
func BenchmarkLogReopen(b *testing.B) {
	for _, size := range []int64{64 << 10, 1 << 20, 4 << 20} {
		b.Run(fmt.Sprintf("segment-%dKiB", size>>10), func(b *testing.B) {
			dir := benchmarkFixture(b, 10000, size)
			b.ReportAllocs()
			b.ResetTimer()
			for k := 0; k < b.N; k++ {
				r := openBenchmarkRepo(b, dir)
				if len(r.parts["approval"].records) != 10000 {
					b.Fatal("lost rows")
				}
			}
		})
	}
}
func BenchmarkLogAppend(b *testing.B) {
	for _, size := range []int64{64 << 10, 1 << 20, 4 << 20} {
		b.Run(fmt.Sprintf("segment-%dKiB", size>>10), func(b *testing.B) {
			dir := benchmarkFixture(b, 1000, size)
			r := openBenchmarkRepo(b, dir)
			r.segmentBytes = size
			b.ReportAllocs()
			b.ResetTimer()
			for k := 0; k < b.N; k++ {
				if _, err := r.AppendOnce("approval", request("new-"+fmt.Sprint(k), "case-A", "manager", strings.Repeat("内容", 100))); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
func BenchmarkInputIndexRebuild(b *testing.B) {
	dir := benchmarkFixture(b, 10000, DefaultSegmentBytes)
	r := openBenchmarkRepo(b, dir)
	b.ReportAllocs()
	b.ResetTimer()
	for k := 0; k < b.N; k++ {
		c, err := r.Catalog(1, Observation{})
		if err != nil || len(c.entries) != 10000 {
			b.Fatal(err)
		}
	}
}
func BenchmarkInputProjectionDecode(b *testing.B) {
	codec := approvalCodec{provider: "approval"}
	q := request("one", "case-A", "manager", strings.Repeat("中文材料", 100))
	b.ReportAllocs()
	b.ResetTimer()
	for k := 0; k < b.N; k++ {
		if _, err := decodeProjection(codec, 1, q.Payload); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInputRetainedMemory(b *testing.B) {
	dir := benchmarkFixture(b, 10000, DefaultSegmentBytes)
	var storeBytes, catalogBytes uint64
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		runtime.GC()
		var before, stored, indexed runtime.MemStats
		runtime.ReadMemStats(&before)
		r := openBenchmarkRepo(b, dir)
		runtime.GC()
		runtime.ReadMemStats(&stored)
		c, err := r.Catalog(1, Observation{})
		if err != nil {
			b.Fatal(err)
		}
		runtime.GC()
		runtime.ReadMemStats(&indexed)
		if stored.HeapAlloc > before.HeapAlloc {
			storeBytes += stored.HeapAlloc - before.HeapAlloc
		}
		if indexed.HeapAlloc > stored.HeapAlloc {
			catalogBytes += indexed.HeapAlloc - stored.HeapAlloc
		}
		runtime.KeepAlive(c)
		runtime.KeepAlive(r)
	}
	b.StopTimer()
	b.ReportMetric(float64(storeBytes)/float64(b.N), "store-live-B")
	b.ReportMetric(float64(catalogBytes)/float64(b.N), "catalog-live-B")
}

func BenchmarkInputListFirstPage(b *testing.B) {
	dir := benchmarkFixture(b, 10000, DefaultSegmentBytes)
	r := openBenchmarkRepo(b, dir)
	c, err := r.Catalog(1, Observation{})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		c.discoveryBytes = 0
		c.pages = map[string]catalogPage{}
		if _, err := c.ReadableList(ListRequest{Limit: 10}); err != nil {
			b.Fatal(err)
		}
	}
}

// Five formal two-entry pages from one stream while 999 other streams share
// the partition. Includes durable reservation, result, handoff and checkpoint.
func BenchmarkInputStreamReadAtCapacity(b *testing.B) {
	dir := benchmarkFixture(b, 10000, DefaultSegmentBytes)
	r := openBenchmarkRepo(b, dir)
	p := r.parts["approval"]
	seq := uint64(998)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		if n > 0 && n%1000 == 0 {
			b.StopTimer()
			r = openBenchmarkRepo(b, benchmarkFixture(b, 10000, DefaultSegmentBytes))
			p = r.parts["approval"]
			b.StartTimer()
		}
		stream := p.streamRefs[seq]
		seq = seq%1000 + 1
		e := Execution{Generation: "benchmark", Turn: uint64(n + 1)}
		if err := r.Begin(e); err != nil {
			b.Fatal(err)
		}
		for page := 0; page < 5; page++ {
			q, err := r.BindCall(e, fmt.Sprintf("page-%d", page), stream, 2)
			if err != nil {
				b.Fatal(err)
			}
			a, err := r.Reserve(e, q)
			if err != nil || len(a.Entries) != 2 || a.Remaining != 8-2*page {
				b.Fatal(a, err)
			}
			for _, l := range a.Entries {
				if _, err := r.Read(a.Snapshot, l); err != nil {
					b.Fatal(err)
				}
			}
			if err := r.Prepare(e, a.ID, json.RawMessage(`{"entries":[1,2]}`)); err != nil {
				b.Fatal(err)
			}
			if err := r.Handoff(e, a.ID); err != nil {
				b.Fatal(err)
			}
			if err := r.Confirm(e, a.ID); err != nil {
				b.Fatal(err)
			}
		}
		if err := r.Seal(e); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLogAppendNearCapacity(b *testing.B) {
	dir := benchmarkFixture(b, MaxRecords-100, DefaultSegmentBytes)
	r := openBenchmarkRepo(b, dir)
	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		if n > 0 && n%100 == 0 {
			b.StopTimer()
			r = openBenchmarkRepo(b, benchmarkFixture(b, MaxRecords-100, DefaultSegmentBytes))
			b.StartTimer()
		}
		if _, err := r.AppendOnce("approval", request(fmt.Sprintf("new-%d", n), "case-0998", "node-1", "saved input")); err != nil {
			b.Fatal(err)
		}
	}
}
