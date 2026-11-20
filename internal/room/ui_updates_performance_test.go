//go:build e2e && (darwin || linux)

package room

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Opt-in paired browser measurement. Old static assets run against the same
// current backend and fresh, identical datasets; no checkout or provider writes.
func TestWebUpdatesPerformance(t *testing.T) {
	if os.Getenv("TEAMX_WEB_UPDATES_PERF") != "1" {
		t.Skip("optional local Chromium load comparison")
	}
	t.Setenv("USER", humanCaller.ID)
	ref := os.Getenv("TEAMX_WEB_UPDATES_POLLING_REF")
	if ref == "" {
		t.Fatal("set TEAMX_WEB_UPDATES_POLLING_REF to the polling baseline commit")
	}
	archive, err := exec.Command("git", "-C", "../..", "archive", ref, "internal/web/assets").Output()
	if err != nil {
		t.Fatal(err)
	}
	assets := map[string][]byte{}
	reader := tar.NewReader(bytes.NewReader(archive))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name := strings.TrimPrefix(header.Name, "internal/web/assets/")
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		assets[name] = data
	}
	if !bytes.Contains(assets["features/browser-lifecycle.mjs"], []byte("1500")) {
		t.Fatal("reference does not contain the polling baseline")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binaryData, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	binaryHash := fmt.Sprintf("%x", sha256.Sum256(binaryData))
	reports := map[string]updatePerformanceReport{}
	for _, dataset := range []string{"baseline", "loaded"} {
		for _, mode := range []string{"polling", "sse"} {
			t.Run(dataset+"/"+mode, func(t *testing.T) {
				m := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
				ensureAssistant(t, m)
				assistant := assistantSummary(t, m)
				if dataset == "loaded" {
					// Full retained count in the active Room, plus 100 inactive Rooms. Leave
					// capacity by clearing a prefix before continuous append measurement.
					_, state := readManagedRoom(t, m, assistant)
					state.Messages = historyRoom(maxMessages, strings.Repeat("x", 1024)).Messages
					for i := range state.Messages {
						state.Messages[i].Author = Author{Kind: "agent", ID: "reviewer"}
					}
					state.LatestSeq, state.Revision = maxMessages, maxMessages
					data, _ := json.Marshal(state)
					if err := os.WriteFile(filepath.Join(m.roomDir(assistant.RoomID), "room.json"), data, 0600); err != nil {
						t.Fatal(err)
					}
					for i := range 100 {
						id := RoomIdentity{RoomID: fmt.Sprintf("load-%03d", i), RoomInstance: fmt.Sprintf("instance-%03d", i)}
						dir := m.roomDir(id.RoomID)
						if err := os.MkdirAll(dir, 0700); err != nil {
							t.Fatal(err)
						}
						state := historyRoom(100, strings.Repeat("x", 1024))
						state.RoomIdentity, state.Members = id, []MemberState{}
						data, _ := json.Marshal(state)
						if err := os.WriteFile(filepath.Join(dir, "room.json"), data, 0600); err != nil {
							t.Fatal(err)
						}
						data, _ = json.Marshal(managedRoomConfig{Version: managedConfigVersion, RoomIdentity: id})
						if err := os.WriteFile(filepath.Join(dir, "room-config.json"), data, 0600); err != nil {
							t.Fatal(err)
						}
						m.index.Rooms = append(m.index.Rooms, roomIndexEntry{roomSummary: roomSummary{RoomIdentity: id, Name: id.RoomID, Status: "ready", CreatedAt: time.Now()}})
					}
				}
				m.factory = controlledActivationFactory(nil, nil)
				openRoomOK(t, m, assistant, "performance-open")
				svc, _ := m.runtimeServiceLocked(assistant.RoomIdentity)
				_, state := readManagedRoom(t, m, assistant)
				member := state.Members[0]
				id := MemberIdentity{RoomIdentity: assistant.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
				// Timestamp at successful atomic rename, before directory sync and actor
				// installation. This conservatively includes both in the DOM latency.
				var installed, memberInstalled atomic.Int64
				m.mu.Lock()
				managerRename := m.store.ops.rename
				m.store.ops.rename = func(from, to string) error {
					err := managerRename(from, to)
					if err == nil && filepath.Base(to) == "rooms.json" {
						memberInstalled.Store(time.Now().UnixMilli())
					}
					return err
				}
				m.mu.Unlock()
				svc.store.mu.Lock()
				originalRename := svc.store.ops.rename
				svc.store.ops.rename = func(from, to string) error {
					err := originalRename(from, to)
					if err == nil && filepath.Base(to) == "room.json" {
						installed.Store(time.Now().UnixMilli())
					}
					return err
				}
				svc.store.mu.Unlock()
				activity := func(marker string) error {
					svc.store.mu.Lock()
					defer svc.store.mu.Unlock()
					path, err := svc.store.dshPath(id, "dsh-turn.json", true)
					if err != nil {
						return err
					}
					result := dsh.TurnResult{StopReason: "end_turn", Text: strings.Repeat("x", 240<<10), Thought: strings.Repeat("x", 240<<10), Events: []dsh.ActivityEvent{}}
					// Ordered flow is capped at 256 KiB; total snapshot approaches 768 KiB.
					result.Events = append(result.Events, dsh.ActivityEvent{Seq: 1, Kind: "text", Text: marker + strings.Repeat("x", 240<<10)})
					if err := saveDSHDiagnostic(path, backendDiagnostic{Result: result}); err != nil {
						return err
					}
					return nil
				}
				if dataset == "loaded" {
					if err := activity("initial"); err != nil {
						t.Fatal(err)
					}
				}
				type snapshot struct {
					CPUms        float64 `json:"cpu_ms"`
					InputBlocks  int64   `json:"physical_input_blocks"`
					OutputBlocks int64   `json:"physical_output_blocks"`
					MutexWaitMS  float64 `json:"process_mutex_wait_ms"`
					Heap         uint64  `json:"heap_bytes"`
					Goroutines   int     `json:"goroutines"`
				}
				sample := func() snapshot {
					var usage syscall.Rusage
					_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)
					samples := []metrics.Sample{{Name: "/sync/mutex/wait/total:seconds"}}
					metrics.Read(samples)
					var mem runtime.MemStats
					runtime.ReadMemStats(&mem)
					return snapshot{CPUms: float64(usage.Utime.Sec+usage.Stime.Sec)*1000 + float64(usage.Utime.Usec+usage.Stime.Usec)/1000,
						InputBlocks: usage.Inblock, OutputBlocks: usage.Oublock, MutexWaitMS: samples[0].Value.Float64() * 1000, Heap: mem.HeapAlloc, Goroutines: runtime.NumGoroutine()}
				}
				var mu sync.Mutex
				counts := map[string]int{}
				var before snapshot
				var measuring bool
				var peakHeap uint64
				var peakGoroutines int
				go func() {
					ticker := time.NewTicker(100 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-t.Context().Done():
							return
						case <-ticker.C:
							mu.Lock()
							if measuring {
								var mem runtime.MemStats
								runtime.ReadMemStats(&mem)
								peakHeap = max(peakHeap, mem.HeapAlloc)
								peakGoroutines = max(peakGoroutines, runtime.NumGoroutine())
							}
							mu.Unlock()
						}
					}
				}()
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/__measure/start":
						mu.Lock()
						counts = map[string]int{}
						before = sample()
						peakHeap, peakGoroutines = before.Heap, before.Goroutines
						measuring = true
						mu.Unlock()
						_, _ = w.Write([]byte("{}"))
						return
					case "/__measure/end":
						mu.Lock()
						after := sample()
						measuring = false
						copied := counts
						heapPeak, goroutinePeak := max(peakHeap, after.Heap), max(peakGoroutines, after.Goroutines)
						mu.Unlock()
						_ = json.NewEncoder(w).Encode(map[string]any{"cpu_ms": after.CPUms - before.CPUms, "physical_input_blocks": after.InputBlocks - before.InputBlocks,
							"physical_output_blocks": after.OutputBlocks - before.OutputBlocks, "process_mutex_wait_ms": after.MutexWaitMS - before.MutexWaitMS,
							"heap_peak_sampled": heapPeak, "goroutines_peak_sampled": goroutinePeak, "heap_before": before.Heap, "heap_after": after.Heap, "goroutines_before": before.Goroutines, "goroutines_after": after.Goroutines, "http_counts_including_injector": copied})
						return
					case "/__measure/connections":
						m.updates.mu.Lock()
						n := len(m.updates.subs)
						m.updates.mu.Unlock()
						_ = json.NewEncoder(w).Encode(map[string]int{"connections": n})
						return
					case "/__measure/installed":
						_ = json.NewEncoder(w).Encode(map[string]any{"at": installed.Load(), "member_at": memberInstalled.Load()})
						return
					case "/__measure/activity":
						if err := activity(r.URL.Query().Get("marker")); err != nil {
							http.Error(w, err.Error(), 500)
							return
						}
						at := time.Now().UnixMilli()
						svc.store.changed("activity", id)
						_ = json.NewEncoder(w).Encode(map[string]any{"at": at})
						return
					}
					if mode == "polling" && (r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/ui/")) {
						name := strings.TrimPrefix(r.URL.Path, "/ui/")
						if r.URL.Path == "/" {
							name = "index.html"
						}
						if data, ok := assets[name]; ok {
							switch filepath.Ext(name) {
							case ".mjs", ".js":
								w.Header().Set("Content-Type", "text/javascript")
							case ".css":
								w.Header().Set("Content-Type", "text/css")
							case ".html":
								w.Header().Set("Content-Type", "text/html")
							}
							_, _ = w.Write(data)
							return
						}
						http.NotFound(w, r)
						return
					}
					if strings.HasPrefix(r.URL.Path, "/v1/") {
						mu.Lock()
						if measuring {
							counts[r.URL.Path]++
						}
						mu.Unlock()
					}
					m.ServeHTTP(w, r)
				})
				server := browserTestServer(t, m, handler)
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
				defer cancel()
				cmd := exec.CommandContext(ctx, "node", "testdata/web_updates_performance.cjs", server.URL, dataset, mode, ref)
				cmd.Env = append(os.Environ(), "TEAMX_WEB_UPDATES_BINARY_SHA256="+binaryHash, "TEAMX_WEB_UPDATES_GO_VERSION="+runtime.Version())
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("performance: %v\n%s", err, output)
				}
				var report updatePerformanceReport
				if err := json.Unmarshal(bytes.TrimSpace(output), &report); err != nil {
					t.Fatalf("decode measurement: %v\n%s", err, output)
				}
				reports[dataset+"/"+mode] = report
				t.Log(string(output))
			})
		}
	}
	// With a selected subtest only its measurement runs. A complete paired run
	// additionally enforces the load goal using actual HTTP/page counts.
	if len(reports) == 4 {
		for _, dataset := range []string{"baseline", "loaded"} {
			before, after := reports[dataset+"/polling"], reports[dataset+"/sse"]
			for i, old := range before.Scenarios {
				current := after.Scenarios[i]
				if current.Total >= old.Total || current.Bytes >= old.Bytes {
					t.Errorf("%s/%s: HTTP load did not fall: requests %d -> %d, bytes %d -> %d", dataset, old.Scenario, old.Total, current.Total, old.Bytes, current.Bytes)
				}
				if old.Message.Commits > 0 && current.Message.Commits > 0 {
					oldCost, newCost := old.Backend.CPU/float64(old.Message.Commits), current.Backend.CPU/float64(current.Message.Commits)
					t.Logf("%s/%s CPU per injected message: %.2f -> %.2f ms", dataset, old.Scenario, oldCost, newCost)
					// The delivery goal is lower overall observation cost, not a universal
					// per-message CPU guarantee. Preserve normalized costs alongside the
					// different input counts; enforce CPU reduction for the loaded windows.
					if dataset == "loaded" && current.Backend.CPU/current.Elapsed >= old.Backend.CPU/old.Elapsed {
						t.Errorf("%s/%s: CPU per measurement second did not fall", dataset, old.Scenario)
					}
				}
			}
		}
	}

}

// Only comparison fields are decoded here; complete raw measurements are saved
// by the browser runner, including failures, paging, latency and OS counters.
type updatePerformanceReport struct {
	Scenarios []struct {
		Scenario string  `json:"scenario"`
		Total    int     `json:"total"`
		Elapsed  float64 `json:"elapsed_ms"`
		Bytes    int64   `json:"bytes"`
		Backend  struct {
			CPU float64 `json:"cpu_ms"`
		} `json:"backend"`
		Message struct {
			Commits int `json:"commits"`
		} `json:"message_install_to_dom_ms"`
	} `json:"scenarios"`
}
