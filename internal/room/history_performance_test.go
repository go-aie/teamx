//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Opt-in capacity evidence, never part of ordinary CI. Fixture construction is
// excluded from measured operations; all measurements use the actual service.
func TestHistoryCapacityPerformance(t *testing.T) {
	out := os.Getenv("TEAMX_HISTORY_PERFORMANCE_OUTPUT")
	if out == "" {
		t.Skip("explicit fresh performance output required")
	}
	if !filepath.IsAbs(out) {
		t.Fatal("absolute output required")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"status": "failed", "room_records": maxMessages, "private_records": inputstream.MaxRecords, "shared_records": inputstream.MaxRecords, "logical_streams": 2001, "fixture": "full record-count capacity, 512-byte saved text; private bulk import and normal shared appends; no model or provider"}
	defer func() {
		if t.Failed() {
			report["status"] = "failed"
		}
		b, err := json.MarshalIndent(report, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(out, "report.json"), b, 0600)
		}
		if err != nil {
			t.Error(err)
		}
	}()
	runtime.GC()
	var initial runtime.MemStats
	runtime.ReadMemStats(&initial)
	state := historyRoom(maxMessages, strings.Repeat("r", 512))
	state.InputLog = &roomLogRoot{Version: 1}
	fixture := os.Getenv("TEAMX_HISTORY_CAPACITY_FIXTURE")
	var configure func(*Room, *Store)
	if fixture != "" {
		if !filepath.IsAbs(fixture) {
			t.Fatal("absolute disposable fixture required")
		}
		configure = func(room *Room, store *Store) {
			for _, name := range []string{"room.json", "room-input", "inboxes", "shared-inboxes", "agents"} {
				target := filepath.Join(store.dir, name)
				if err := os.RemoveAll(target); err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(filepath.Join(fixture, name))
				if err != nil {
					t.Fatal(err)
				}
				if info.IsDir() {
					if err := os.CopyFS(target, os.DirFS(filepath.Join(fixture, name))); err != nil {
						t.Fatal(err)
					}
				} else {
					data, err := os.ReadFile(filepath.Join(fixture, name))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(target, data, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			restored, err := NewRoom(store)
			if err != nil {
				t.Fatal(err)
			}
			*room = *restored // Install the copied native publication before spawning the Actor.
			report["fixture"] = "verified preserved full-capacity fixture, copied into a fresh disposable Store; no model or provider"
		}
	}
	h := newRoomHarnessWithTimeout(t, state, configure, 20*time.Minute)
	opening := time.Now()
	id := h.memberID("A", "generation-A")
	svc := &service{unifiedInputs: true, inputAdapters: storageAdapters(), store: h.store, room: h.addr, info: ServiceInfo{RoomIdentity: h.id, ServiceEpoch: "capacity"}}
	p, err := svc.inputStorage(id)
	if err != nil {
		t.Fatal(err)
	}
	report["cold_storage_open_ms"] = float64(time.Since(opening).Microseconds()) / 1000
	if fixture == "" {
		cap := gitlab.InputCapability()
		entries := make([]inputstream.ImportEntry, inputstream.MaxRecords)
		for i := range entries {
			item := storedGitLab(id, fmt.Sprintf("private-%d", i), "events", "7", fmt.Sprint(i%1000+1), strings.Repeat("p", 512))
			q, err := cap.EncodeStoredInput(item)
			if err != nil {
				t.Fatal(err)
			}
			q.Seq = uint64(i + 1)
			q.ReceivedAt = time.Now()
			entries[i] = inputstream.ImportEntry{AppendRequest: q}
		}
		seed := time.Now()
		if err := p.repository.ImportPartition("gitlab", 0, inputstream.MaxRecords, entries); err != nil {
			t.Fatal(err)
		}
		entries = nil
		if _, err := svc.configureSharedMembers(h.ctx, 0, []MemberIdentity{id}); err != nil {
			t.Fatal(err)
		}
		shared := h.store.sharedHandle.Load()
		_, err = sharedOwnerValue(h.ctx, shared, func() (struct{}, error) {
			for i := range inputstream.MaxRecords {
				item := storedGitLab(id, fmt.Sprintf("shared-%d", i), "events", "7", fmt.Sprint(i%1000+1), strings.Repeat("s", 512))
				item.Route.Target = notification.Target{Mode: "shared_inbox", InboxID: sharedProjectInbox}
				q, e := cap.EncodeStoredInput(item)
				if e != nil {
					return struct{}{}, e
				}
				q.ReceivedAt = time.Now()
				if _, e = shared.repository.AppendOnce("gitlab", q); e != nil {
					return struct{}{}, e
				}
			}
			return struct{}{}, shared.publish()
		})
		if err != nil {
			t.Fatal(err)
		}
		report["fixture_seed_seconds"] = time.Since(seed).Seconds()
	} else if _, err := svc.store.sharedInputs(false); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, InputStateRef: "input-state.json", TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: maxMessages}}); err != nil {
		t.Fatal(err)
	}
	s, err := newInputSession(h.ctx, svc, p, "capacity", 1, false, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	other, otherReader := unifiedReaderFixtureWithTimeout(t, 10, 20*time.Minute)
	metrics := map[string][]float64{}
	measure := func(name string, fn func()) {
		start := time.Now()
		fn()
		metrics[name] = append(metrics[name], float64(time.Since(start).Microseconds())/1000)
	}
	fresh := func() { s = nextHistoryTurn(t, h, s) }
	// First call includes the cold service gate/native-log checks. Subsequent
	// observations use fresh turn budgets; no cache or quota is reset in-place.
	measure("cold_directory", func() { historyResult[historyDirectory](t, s, "cold", "history_list", map[string]any{"limit": 20}) })
	var ref, sharedRef string
	retained, err := p.repository.RetainedStreams()
	if err != nil {
		t.Fatal(err)
	}
	ref = retained[0].Ref
	for i := range 20 {
		fresh()
		measure("warm_directory", func() { historyResult[historyDirectory](t, s, "list", "history_list", map[string]any{"limit": 20}) })
		measure("known_stream_recent", func() {
			page, _ := historyRead(t, s, "known", ref, 20)
			if len(page.Entries) != 10 {
				t.Fatal(len(page.Entries))
			}
		})
		measure("other_room_read", func() {
			historyRead(t, otherReader, fmt.Sprintf("other-%d", i), roomStreamRef(otherReader.storage.id), 1)
		})
	}
	fresh()
	calls, streams := 0, 0
	measure("deep_directory_all_pages", func() {
		page, _ := historyResult[historyDirectory](t, s, "deep-0", "history_list", map[string]any{"limit": 20})
		for {
			calls++
			streams += len(page.Streams)
			for _, v := range page.Streams {
				if v.Kind == "shared" {
					sharedRef = v.Ref
				}
			}
			if page.Next == "" {
				break
			}
			page, _ = historyResult[historyDirectory](t, s, fmt.Sprintf("deep-%d", calls), "history_list", map[string]string{"cursor": page.Next})
		}
	})
	if streams != 2001 || calls != 101 {
		t.Fatal("directory capacity", streams, calls)
	}
	report["directory_locate_calls"] = map[string]int{"known_stream": 1, "first_screen": 2, "last_screen_with_limit_20": calls + 1, "all_directory_pages": calls}
	fresh()
	measure("room_large_limit", func() {
		page, raw := historyRead(t, s, "large", roomStreamRef(id), 10000)
		report["large_limit_supplied"] = map[string]any{"entries": len(page.Entries), "text_bytes": len(raw), "has_older": page.Next != ""}
		if page.Next == "" || len(raw) > historyReadBytes {
			t.Fatal("unbounded result")
		}
	})
	fresh()
	measure("shared_recent", func() {
		page, _ := historyRead(t, s, "shared", sharedRef, 20)
		if len(page.Entries) != 10 {
			t.Fatal("shared range", len(page.Entries))
		}
	})
	fresh()
	measure("continuous_body_pages", func() {
		page, _ := historyRead(t, s, "body-first", ref, 3)
		n := len(page.Entries)
		pages := 1
		for page.Next != "" {
			page, _ = historyNext(t, s, fmt.Sprintf("body-%d", pages), page.Next)
			pages++
			n += len(page.Entries)
		}
		if n != 10 || pages != 4 {
			t.Fatal("body coverage", n, pages)
		}
		report["body_pages"] = pages
		report["body_entries"] = n
	})
	// Retain a fully prepared Room response while other owners do ordinary work.
	// This models a slow HTTP output without holding a common Store/owner slot.
	bID := MemberIdentity{RoomIdentity: h.id, AgentID: "B", MembershipID: "capacity-B"}
	roomCall[MemberInfo](t, h, AddMember{MemberIdentity: bID, Target: h.target, Runner: state.Members[0].Runner})
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: bID, InputStateRef: "input-state.json", TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: maxMessages}}); err != nil {
		t.Fatal(err)
	}
	bStorage, err := svc.inputStorage(bID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newInputSession(h.ctx, svc, bStorage, "capacity-B", 1, false, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	fresh()
	_, release, err := b.prepareHistory(h.ctx, "held-output", "history_read", mustJSON(t, historyReadRequest{Stream: roomStreamRef(bID), Limit: 20}))
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	measure("store_lock_while_history_output_held", func() { h.store.mu.Lock(); h.store.mu.Unlock() })
	type concurrentResult struct {
		name string
		ms   float64
		err  error
	}
	work := make(chan concurrentResult, 2)
	go func() {
		start := time.Now()
		err := p.run(h.ctx, "capacity-reclaim", func(context.Context) error {
			snap, err := p.repository.Snapshot("gitlab")
			if err != nil {
				return err
			}
			locations := []inputstream.Location{}
			facts := inputstream.ReleaseSet{Revision: "capacity-verified", Records: map[uint64]inputstream.ReleaseFacts{}}
			for seq := uint64(1); seq <= 32; seq++ {
				locations = append(locations, inputstream.Location{Partition: snap.Partition, Seq: seq})
				facts.Records[seq] = inputstream.ReleaseFacts{Revision: "verified", SourceConfirmed: true, BusinessReleased: true, ReceiptReleased: true}
			}
			if _, err := p.repository.ExcludeCandidates("gitlab", locations, "verified exclusion"); err != nil {
				return err
			}
			facts, err = p.repository.ConsumptionRelease("gitlab", facts)
			if err != nil {
				return err
			}
			plan, err := p.repository.PrepareReclaim("gitlab", facts, time.Now())
			if err != nil {
				return err
			}
			if plan.Through != 32 {
				return fmt.Errorf("reclaim through %d", plan.Through)
			}
			_, err = p.repository.CommitReclaim("gitlab", plan, facts)
			return err
		})
		if err == nil {
			_, err = p.Receive(storedGitLab(id, "capacity-concurrent", "events", "7", "5000", "concurrent appended evidence"), time.Now())
		}
		work <- concurrentResult{"concurrent_reclaim_then_append", float64(time.Since(start).Microseconds()) / 1000, err}
	}()
	go func() {
		start := time.Now()
		_, err := other.addr.Call[ReadPage](other.ctx, ReadMessages{RoomIdentity: other.id, Caller: agentCallerIdentity(otherReader.storage.id), Mode: ReadLatest, Latest: 2})
		work <- concurrentResult{"concurrent_other_room_read", float64(time.Since(start).Microseconds()) / 1000, err}
	}()
	measure("concurrent_formal_consumption", func() { readInputStream(t, s, "concurrent-formal", roomStreamRef(id), 20, true) })
	measure("concurrent_directory", func() {
		historyResult[historyDirectory](t, s, "concurrent-list", "history_list", map[string]any{"limit": 20})
	})
	for range 2 {
		select {
		case result := <-work:
			if result.err != nil {
				t.Fatal(result.err)
			}
			metrics[result.name] = append(metrics[result.name], result.ms)
		case <-time.After(5 * time.Second):
			t.Fatal("held History output blocked a common owner or lock")
		}
	}
	release()
	if got := h.list(t).Members[0].ReadThrough; got != 20 {
		t.Fatal("concurrent formal Room page did not consume exactly 20 entries", got)
	}
	report["concurrent_authority"] = map[string]any{"reclaimed": 32, "appended": 1, "other_member_formally_consumed": h.list(t).Members[0].ReadThrough}
	measure("formal_consumption_without_history_output", func() { readInputStream(t, s, "control-formal", roomStreamRef(id), 20, true) })
	if got := h.list(t).Members[0].ReadThrough; got != 40 {
		t.Fatal("control formal page did not consume the next 20 entries", got)
	}
	runtime.GC()
	var loaded runtime.MemStats
	runtime.ReadMemStats(&loaded)
	report["heap_delta_bytes"] = int64(loaded.HeapAlloc) - int64(initial.HeapAlloc)
	report["heap_bytes"] = loaded.HeapAlloc
	report["system_bytes"] = loaded.Sys
	summaries := map[string]any{}
	for name, values := range metrics {
		slices.Sort(values)
		summaries[name] = map[string]any{"samples": len(values), "p95_ms": values[(len(values)*95+99)/100-1], "min_ms": values[0], "max_ms": values[len(values)-1]}
	}
	report["latency"] = summaries
	report["status"] = "passed"
	t.Log("capacity report", filepath.Join(out, "report.json"))
}
