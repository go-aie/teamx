//go:build e2e

package room

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Same-machine, same-data comparison with the previous catalog's file projection.
// Opt-in measurements are not CI timing gates and require no model or platform.
func TestHumanReadCatalogPerformance(t *testing.T) {
	if os.Getenv("TEAMX_HUMAN_READ_PERF") != "1" {
		t.Skip("set TEAMX_HUMAN_READ_PERF=1")
	}
	for _, sample := range []struct {
		name                       string
		rooms, messages, textBytes int
	}{
		{"single-10000", 1, 10000, 32}, {"ten-1000", 10, 1000, 32}, {"near-64MiB", 1, 10000, 6600},
	} {
		t.Run(sample.name, func(t *testing.T) {
			m := testManager(t, t.TempDir(), nil)
			var bytes int64
			for i := 0; i < sample.rooms; i++ {
				identity := RoomIdentity{RoomID: fmt.Sprintf("room-%d", i), RoomInstance: fmt.Sprintf("instance-%d", i)}
				room := historyRoom(sample.messages, strings.Repeat("x", sample.textBytes-len(humanOperator())-2)+" @"+humanOperator())
				room.RoomIdentity, room.Members = identity, []MemberState{}
				for j := range room.Messages {
					room.Messages[j].Author = Author{Kind: "agent", ID: "reviewer"}
				}
				dir := m.roomDir(identity.RoomID)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(room)
				bytes += int64(len(data))
				if err := os.WriteFile(filepath.Join(dir, "room.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
				config, _ := json.Marshal(managedRoomConfig{RoomIdentity: identity})
				if err := os.WriteFile(filepath.Join(dir, "room-config.json"), config, 0600); err != nil {
					t.Fatal(err)
				}
				// Exercise a nearly full seen set too, with enough unread text to count.
				seen := make([]uint64, 0, sample.messages/2)
				for seq := uint64(2); seq <= room.LatestSeq; seq += 2 {
					seen = append(seen, seq)
				}
				receipt := humanReadFile{Version: 1, RoomIdentity: identity, OperatorID: humanOperator(), Revision: 1, ObservedLatestSeq: room.LatestSeq, SeenSeqs: seen}
				data, _ = json.Marshal(receipt)
				if err := os.WriteFile(filepath.Join(dir, "human-read.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
				m.index.Rooms = append(m.index.Rooms, roomIndexEntry{roomSummary: roomSummary{RoomIdentity: identity, Name: identity.RoomID, Status: "ready", CreatedAt: time.Now()}})
			}
			legacy := func() {
				m.mu.Lock()
				defer m.mu.Unlock()
				rows := make([]roomRuntimeSummary, 0, len(m.index.Rooms))
				for _, e := range m.index.Rooms {
					summary := roomRuntimeSummary{roomSummary: e.roomSummary, RuntimeStatus: "closed"}
					if info, err := os.Stat(filepath.Join(m.roomDir(e.RoomID), "room.json")); err == nil && info.ModTime().After(summary.UpdatedAt) {
						summary.UpdatedAt = info.ModTime().UTC()
					}
					var room RoomSnapshot
					if err := readSnapshot(filepath.Join(m.roomDir(e.RoomID), "room.json"), &room, maxRoomFileBytes); err != nil {
						t.Fatal(err)
					}
					if len(room.Messages) > 0 {
						last := room.Messages[len(room.Messages)-1]
						summary.LastMessageAt = &last.CreatedAt
						summary.LastMessagePreview = summarizeRoomMessage(last)
					}
					var config managedRoomConfig
					if err := readSnapshot(filepath.Join(m.roomDir(e.RoomID), "room-config.json"), &config, maxPrivateFileBytes); err != nil {
						t.Fatal(err)
					}
					rows = append(rows, summary)
				}
				w := httptest.NewRecorder()
				_ = json.NewEncoder(w).Encode(roomsResponse{Rooms: rows, Operator: humanOperator(), ServiceEpoch: m.epoch, Teams: m.teamSummaries(), AgentRoles: builtinAgentRoleSummaries()})
			}
			current := func() {
				w := managerHTTP(m, "/v1/rooms", nil)
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
			}
			// Warm both projections; alternate order to reduce cache/GC bias.
			legacy()
			current()
			before, after := []float64{}, []float64{}
			measure := func(fn func()) float64 {
				start := time.Now()
				fn()
				return float64(time.Since(start).Microseconds()) / 1000
			}
			for i := 0; i < 24; i++ {
				if i%2 == 0 {
					before = append(before, measure(legacy))
					after = append(after, measure(current))
				} else {
					after = append(after, measure(current))
					before = append(before, measure(legacy))
				}
			}
			stats := func(a []float64) map[string]float64 {
				slices.Sort(a)
				return map[string]float64{"p50": a[len(a)/2], "p95": a[int(float64(len(a))*.95)]}
			}
			// Actual HTTP busy rejection while a catalog holds the management mutex.
			busy := []float64{}
			for i := 0; i < 5; i++ {
				done := make(chan struct{})
				go func() { managerHTTP(m, "/v1/rooms", nil); close(done) }()
				time.Sleep(time.Millisecond)
				for {
					select {
					case <-done:
						goto next
					default:
					}
					start := time.Now()
					w := managerHTTP(m, "/v1/rooms", nil)
					if w.Code == 409 {
						busy = append(busy, float64(time.Since(start).Microseconds())/1000)
					}
					time.Sleep(100 * time.Microsecond)
				}
			next:
			}
			var snapshot RoomSnapshot
			_ = readSnapshot(filepath.Join(m.roomDir("room-0"), "room.json"), &snapshot, maxRoomFileBytes)
			h := newHumanReadStore(m.roomDir("room-0"), snapshot, humanOperator())
			store, openErr := OpenStore(m.roomDir("room-0"), "room-0")
			if openErr != nil {
				t.Fatal(openErr)
			}
			defer store.Close()
			state, err := h.update(store, nil, nil, humanOperator())
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(state)
			report := map[string]any{"sample": sample.name, "rooms": sample.rooms, "messagesPerRoom": sample.messages, "snapshotBytes": bytes, "samples": 24, "legacyCatalogMs": stats(before), "catalogMs": stats(after), "stateBytes": len(encoded), "normalHistoryReadsPerRoom": 1, "normalReceiptReadsPerRoom": 1, "busyResponses": len(busy)}
			if len(busy) > 0 {
				report["busyResponseMs"] = stats(busy)
			}
			encoded, _ = json.Marshal(report)
			t.Log(string(encoded))
			if after[len(after)-1] > 1500 {
				t.Fatal("catalog persistently risks missing the 1.5 second poll interval")
			}
		})
	}
}
