//go:build integration || e2e

package room

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the managed root with no listener: assets/bootstrap must remain
// available before the first room and while the old service is settling.
func TestManagedWebBootstrapAssetsAndOrigin(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	for _, busy := range []bool{false, true} {
		m.busy = busy
		for path, contentType := range map[string]string{
			"/": "text/html", "/ui/app.js": "text/javascript", "/ui/client.mjs": "text/javascript",
			"/ui/style.css": "text/css", "/v1/ui": "application/json",
		} {
			res := managerHTTP(m, path, nil)
			if res.Code != http.StatusOK || !strings.Contains(res.Header().Get("Content-Type"), contentType) {
				t.Fatalf("busy=%v %s: %d %s", busy, path, res.Code, res.Body.String())
			}
			if path == "/v1/ui" {
				var bootstrap struct {
					Managed         bool   `json:"managed"`
					ServiceEpoch    string `json:"service_epoch"`
					TimeZone        string `json:"time_zone"`
					SettingsVersion uint64 `json:"settings_version"`
				}
				if json.Unmarshal(res.Body.Bytes(), &bootstrap) != nil || !bootstrap.Managed || bootstrap.ServiceEpoch != m.epoch || bootstrap.TimeZone == "" || bootstrap.SettingsVersion == 0 || res.Header().Get("Cache-Control") != "no-store" {
					t.Fatal(res.Body.String(), res.Header())
				}
			} else if res.Header().Get("Content-Security-Policy") == "" {
				t.Fatal("missing content policy", path)
			}
			if path == "/" {
				body := res.Body.String()
				if !strings.Contains(body, `id="room-nav"`) || !strings.Contains(body, `id="create-dialog"`) ||
					strings.Contains(body, "加载并启用") || strings.Contains(body, `class="example-panel"`) {
					t.Fatal("managed page missing navigation or retained the old example entry")
				}
			}
		}
	}
	m.busy = false
	req := httptest.NewRequest(http.MethodGet, "/v1/ui", nil)
	req.Header.Set("Origin", "https://unrelated.example")
	res := httptest.NewRecorder()
	m.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatal("cross-origin bootstrap allowed", res.Code)
	}
	res = httptest.NewRecorder()
	m.ServeHTTP(res, httptest.NewRequest(http.MethodHead, "/", nil))
	if res.Code != http.StatusOK || res.Body.Len() != 0 {
		t.Fatal("HEAD", res.Code, res.Body.String())
	}
}

func TestManagedWebCatalogReflectsDurableRoomActivity(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	room := managerCreate(t, m, "activity")
	updated := room.UpdatedAt.Add(time.Minute).Truncate(time.Second)
	path := filepath.Join(m.roomDir(room.RoomID), "room.json")
	if err := os.Chtimes(path, updated, updated); err != nil {
		t.Fatal(err)
	}
	res := managerHTTP(m, "/v1/rooms", nil)
	var catalog roomsResponse
	if err := json.Unmarshal(res.Body.Bytes(), &catalog); err != nil || res.Code != http.StatusOK ||
		len(catalog.Rooms) != 1 || !catalog.Rooms[0].UpdatedAt.Equal(updated) || catalog.Rooms[0].LastMessageAt != nil {
		t.Fatal(catalog, res.Code, err)
	}
	if !m.index.Rooms[0].UpdatedAt.Equal(room.UpdatedAt) {
		t.Fatal("listing activity must not mutate the catalog")
	}
}

func TestManagedWebCatalogUsesLastPublicMessageTime(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	room := managerCreate(t, m, "message-time")
	_, state := readManagedRoom(t, m, room)
	store, err := OpenStore(m.roomDir(room.RoomID), room.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Existing histories can predate catalog creation, for example after migration.
	first := room.CreatedAt.Add(-time.Hour).Truncate(time.Second)
	last := first.Add(time.Minute)
	state.Messages = []RoomMessage{
		{Seq: 1, ClientMessageID: "human", Author: humanCaller.Author, Text: "request", CreatedAt: first},
		{Seq: 2, ClientMessageID: "agent", Author: Author{Kind: "agent", ID: "developer"}, Text: "done", CreatedAt: last},
	}
	state.LatestSeq, state.Revision = 2, 2
	checkTime := func(want *time.Time) {
		t.Helper()
		if err := store.SaveRoom(state); err != nil {
			t.Fatal(err)
		}
		// A later state write must not become the last public message time.
		updated := room.UpdatedAt.Add(time.Hour)
		path := filepath.Join(m.roomDir(room.RoomID), "room.json")
		if err := os.Chtimes(path, updated, updated); err != nil {
			t.Fatal(err)
		}
		res := managerHTTP(m, "/v1/rooms", nil)
		var catalog roomsResponse
		if err := json.Unmarshal(res.Body.Bytes(), &catalog); err != nil || res.Code != http.StatusOK || len(catalog.Rooms) != 1 {
			t.Fatal(catalog, res.Code, err)
		}
		got := catalog.Rooms[0].LastMessageAt
		if (got == nil) != (want == nil) || (got != nil && !got.Equal(*want)) {
			t.Fatalf("last message time = %v, want %v", got, want)
		}
		if m.index.Rooms[0].LastMessageAt != nil || !m.index.Rooms[0].UpdatedAt.Equal(room.UpdatedAt) {
			t.Fatal("listing must not mutate the catalog")
		}
	}
	checkTime(&last) // Inactive room, Agent's last public reply.
	state.Members[0].ReadThrough = state.LatestSeq
	state.Revision++
	checkTime(&last) // Read confirmation changes state, but adds no message.
	next := last.Add(time.Minute)
	state.Messages = append(state.Messages, RoomMessage{Seq: 3, ClientMessageID: "follow-up", Author: humanCaller.Author, Text: "thanks", CreatedAt: next})
	state.LatestSeq, state.Revision = 3, state.Revision+1
	checkTime(&next) // Human messages update the same timestamp.
	state.Messages = []RoomMessage{}
	state.ClearedThrough, state.Revision = state.LatestSeq, state.Revision+1
	for i := range state.Members {
		state.Members[i].ReadThrough = state.LatestSeq
	}
	checkTime(nil) // A cleared discussion no longer has a last visible message.
}
