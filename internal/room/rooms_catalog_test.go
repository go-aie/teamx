package room

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestRoomsCatalogFreezesNestedMetadata(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	a := managerCreate(t, m, "frozen")
	m.mu.Lock()
	stamp := time.Now().UTC()
	m.index.Rooms[0].LastMessageAt = &stamp
	m.index.DefaultAssistant = &defaultAssistantRecord{Status: "pending", Error: "original"}
	m.catalogChangedLocked()
	old := m.catalog.Load()
	stamp = stamp.Add(time.Hour)
	m.index.DefaultAssistant.Error = "changed"
	m.index.Rooms[0].Name = "changed"
	team := m.teams[a.TeamID]
	team.Members[0].Name = "changed"
	m.teams[a.TeamID] = team
	m.catalogChangedLocked()
	m.mu.Unlock()
	if old.assistant.Error != "original" || old.rooms[0].Name != a.Name || old.rooms[0].LastMessageAt.Equal(stamp) {
		t.Fatal("published metadata aliases index")
	}
	for _, team := range old.teams {
		if team.ID == a.TeamID && team.Members[0].Name == "changed" {
			t.Fatal("published member slice aliases team")
		}
	}
	// Restore synthetic assistant metadata before normal shutdown saves the index.
	m.mu.Lock()
	m.index.DefaultAssistant = nil
	m.mu.Unlock()
}

func TestRoomsCatalogConcurrentPublicationAndReads(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	a := managerCreate(t, m, "concurrent")
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				w := managerHTTP(m, "/v1/rooms", nil)
				var got roomsResponse
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Rooms) != 1 || got.Rooms[0].RoomIdentity != a.RoomIdentity {
					t.Errorf("invalid catalog %d %s", w.Code, w.Body.String())
					return
				}
			}
		})
	}
	for i := range 30 {
		m.mu.Lock()
		next := m.copyIndex()
		next.Rooms[0].Name = fmt.Sprintf("name-%d", i)
		err := m.saveIndex(next)
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	got := managerHTTP(m, "/v1/rooms", nil)
	var catalog roomsResponse
	if json.Unmarshal(got.Body.Bytes(), &catalog) != nil || catalog.Rooms[0].Name != "name-29" {
		t.Fatal(got.Body.String())
	}
}

func TestRoomsCatalogHealthIndependentOfPersistenceLock(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	managerCreate(t, m, "health")
	m.store.mu.Lock()
	done := make(chan int, 1)
	go func() { done <- managerHTTP(m, "/v1/rooms", nil).Code }()
	select {
	case code := <-done:
		if code != 200 {
			t.Error(code)
		}
	case <-time.After(time.Second):
		t.Error("health read waited on persistence mutex")
	}
	m.store.mu.Unlock()
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 100 {
			m.store.check(false)
			m.store.check(true)
		}
	})
	m.store.markUncertain()
	wg.Wait()
	w := managerHTTP(m, "/v1/rooms", nil)
	var got roomsResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.WriteError != ErrCommitUnknown.Error() {
		t.Fatal(w.Code, w.Body.String())
	}
	if !errors.Is(m.store.check(true), ErrCommitUnknown) {
		t.Fatal("write fence lost")
	}
	if err := m.store.Close(); err != nil {
		t.Fatal(err)
	}
	if w := managerHTTP(m, "/v1/rooms", nil); w.Code == 200 {
		t.Fatal("closed store returned normal catalog")
	}
}

func TestRoomsCatalogSummaryFailureIsLocal(t *testing.T) {
	for _, fault := range []string{"corrupt-body", "invalid-mentions", "invalid-mentions-and-receipt", "replaced-instance", "replaced-config"} {
		t.Run(fault, func(t *testing.T) {
			m := testManager(t, t.TempDir(), managerTestProfiles(t))
			a, b := managerCreate(t, m, "bad-summary"), managerCreate(t, m, "good-summary")
			config, state := readManagedRoom(t, m, a)
			path := filepath.Join(m.roomDir(a.RoomID), "room.json")
			data := []byte("{")
			switch fault {
			case "invalid-mentions", "invalid-mentions-and-receipt":
				state.LatestSeq, state.Revision = 1, 1
				state.Messages = []RoomMessage{{Seq: 1, Text: "@a", Mentions: []MessageMention{{Start: 0, End: 99, Kind: "human", ID: "a"}}}}
				data, _ = json.Marshal(state)
				if fault == "invalid-mentions-and-receipt" {
					if err := os.WriteFile(filepath.Join(m.roomDir(a.RoomID), "human-read.json"), []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "replaced-instance":
				state.RoomInstance = "replacement"
				data, _ = json.Marshal(state)
			case "replaced-config":
				config.RoomInstance = "replacement"
				path = filepath.Join(m.roomDir(a.RoomID), "room-config.json")
				data, _ = json.Marshal(config)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			w := managerHTTP(m, "/v1/rooms", nil)
			var got roomsResponse
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || len(got.Rooms) != 2 {
				t.Fatal(w.Code, w.Body.String())
			}
			for _, r := range got.Rooms {
				switch r.RoomIdentity {
				case a.RoomIdentity:
					if r.SummaryStatus != "unavailable" || r.SummaryError == "" || r.RuntimeStatus != "closed" {
						t.Fatal(r)
					}
					if fault == "replaced-config" && len(r.Members) > 0 {
						t.Fatal("replacement roster leaked into old identity")
					}
					if fault != "replaced-config" && (r.HumanRead.Status != "unavailable" || r.LastMessagePreview != nil) {
						t.Fatal("invalid history exposed as a committed summary")
					}
				case b.RoomIdentity:
					if r.SummaryStatus != "ready" || r.HumanRead.Status != "ready" {
						t.Fatal(r)
					}
				}
			}
		})
	}
}

func TestRoomsCatalogSettingsReadWithoutManagerGate(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	// Settings have their own immutable snapshot; no second settings cache in
	// the catalog can return an old time zone after the settings commit notification.
	m.settings = settingsFromFile(t, writeStartupConfig(t, t.TempDir(), ""), true)
	before := m.settings.snapshot()
	if _, err := m.settings.save("UTC", before.SettingsVersion); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/rooms", nil))
	m.mu.Unlock()
	var got roomsResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.settingsView != m.settings.snapshot().settingsView {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRoomsCatalogDuringSettingsCommit(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	m.settings = settingsFromFile(t, writeStartupConfig(t, t.TempDir(), ""), true)
	before := m.settings.snapshot()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	original := m.settings.ops.syncDir
	m.settings.ops.syncDir = func(dir string) error { close(entered); <-release; return original(dir) }
	go func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, err := m.settings.save("UTC", before.SettingsVersion)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("settings commit did not reach hook")
	}
	readDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { readDone <- managerHTTP(m, "/v1/rooms", nil) }()
	select {
	case w := <-readDone:
		var got roomsResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.settingsView != before.settingsView {
			t.Error("settings commit blocked or leaked candidate", w.Code, w.Body.String())
		}
	case <-time.After(time.Second):
		t.Error("catalog waited on config document I/O")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	w := managerHTTP(m, "/v1/rooms", nil)
	var got roomsResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.TimeZone != "UTC" || got.SettingsVersion != before.SettingsVersion+1 {
		t.Fatal(w.Code, w.Body.String())
	}
}
