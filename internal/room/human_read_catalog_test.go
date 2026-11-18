package room

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestHumanReadCatalogRetainedHistoryAndFileReadBounds(t *testing.T) {
	store, room, h := humanReadFixture(t, 100)
	room.Messages[0].Author = Author{Kind: "human", ID: "owner"}
	room.Messages[1].Text = "普通回复"
	room.Messages[2].Text = "@店长 @店长"
	if err := store.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	humanReadConfirm(t, h, store, 100, 100)
	before, _ := os.ReadFile(h.path())
	reads := []string{}
	read := func(path string, v any, limit int64) error {
		reads = append(reads, filepath.Base(path))
		return readSnapshot(path, v, limit)
	}
	_, err, got := readHumanReadCatalogFiles(store.dir, room.RoomIdentity, "owner", "店长", read)
	if err != nil || got.Status != "ready" || got.Revision != 1 || got.UnreadCount != 99 || got.MentionCount != 97 || got.SnapshotSeq != 100 {
		t.Fatal(got, err)
	}
	if !reflect.DeepEqual(reads, []string{"human-read.json", "room.json"}) {
		t.Fatal(reads)
	}
	after, _ := os.ReadFile(h.path())
	if string(before) != string(after) {
		t.Fatal("catalog wrote receipts")
	}
	humanReadOpen(t, h, store, 100)
	room.ClearedThrough, room.Messages = 99, room.Messages[99:]
	room.Members[0].ReadThrough = 99
	if err := store.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	_, err, got = readHumanReadCatalog(store.dir, room.RoomIdentity, "owner", "店长")
	if err != nil || got.Status != "ready" || got.ClearedThrough != 99 || got.UnreadCount != 0 || got.MentionCount != 0 {
		t.Fatal(got, err)
	}
}

func TestHumanReadCatalogUnavailableAndBoundedRetry(t *testing.T) {
	store, room, h := humanReadFixture(t, 4)
	_, err, initial := readHumanReadCatalog(store.dir, room.RoomIdentity, "owner", "")
	if err != nil || initial.Status != "ready" || initial.Revision != 0 || initial.UnreadCount != 4 {
		t.Fatal(initial, err)
	}
	if _, err := os.Stat(h.path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("catalog created file", err)
	}
	humanReadConfirm(t, h, store, 4, 4)
	valid, _ := os.ReadFile(h.path())
	for _, kind := range []string{"corrupt", "operator", "identity", "version", "ahead", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			var file humanReadFile
			_ = json.Unmarshal(valid, &file)
			switch kind {
			case "operator":
				file.OperatorID = "other"
			case "identity":
				file.RoomInstance = "other"
			case "version":
				file.Version++
			case "ahead":
				file.ObservedLatestSeq++
			case "duplicate":
				file.MentionSeenSeqs = []uint64{4, 4}
			}
			data, _ := json.Marshal(file)
			if kind == "corrupt" {
				data = []byte("{")
			}
			if err := os.WriteFile(h.path(), data, 0600); err != nil {
				t.Fatal(err)
			}
			reads := 0
			read := func(path string, v any, limit int64) error { reads++; return readSnapshot(path, v, limit) }
			preview, err, got := readHumanReadCatalogFiles(store.dir, room.RoomIdentity, "owner", "", read)
			if err != nil || preview.LatestSeq != 4 || got.Status != "unavailable" || got.HumanReadCounts != nil {
				t.Fatal(got, err)
			}
			want := 2
			if kind == "ahead" {
				want = 4
			}
			if reads != want {
				t.Fatal("unbounded or repeated history read", reads)
			}
			encoded, _ := json.Marshal(got)
			if string(encoded) != `{"status":"unavailable"}` {
				t.Fatal(string(encoded))
			}
		})
	}
	// Simulate a replaced snapshot: the single retry must use a fresh pair.
	_ = os.WriteFile(h.path(), valid, 0600)
	reads := 0
	read := func(path string, v any, limit int64) error {
		reads++
		if err := readSnapshot(path, v, limit); err != nil {
			return err
		}
		if reads == 2 {
			v.(*RoomSnapshot).LatestSeq--
		}
		return nil
	}
	// Keep the simulated old snapshot structurally valid.
	readOld := func(path string, v any, limit int64) error {
		err := read(path, v, limit)
		if reads == 2 {
			p := v.(*RoomSnapshot)
			p.Messages = p.Messages[:3]
		}
		return err
	}
	_, err, got := readHumanReadCatalogFiles(store.dir, room.RoomIdentity, "owner", "", readOld)
	if err != nil || got.Status != "ready" || reads != 4 {
		t.Fatal(got, reads, err)
	}
}

func TestHumanReadCatalogClosedRoomsAndFaultIsolation(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	a, b := managerCreate(t, m, "reading-a"), managerCreate(t, m, "reading-b")
	_, snapshot := readManagedRoom(t, m, a)
	snapshot.Messages = []RoomMessage{{Seq: 1, Author: Author{Kind: "agent", ID: "xiaolin"}, Text: "@" + humanOperator() + " @" + humanOperator(), CreatedAt: a.CreatedAt}}
	snapshot.LatestSeq, snapshot.Revision = 1, 1
	data, _ := json.Marshal(snapshot)
	if err := os.WriteFile(filepath.Join(m.roomDir(a.RoomID), "room.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	indexBefore, _ := os.ReadFile(m.indexPath())
	if err := os.WriteFile(filepath.Join(m.roomDir(b.RoomID), "human-read.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	w := managerHTTP(m, "/v1/rooms", nil)
	var catalog roomsResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, room := range catalog.Rooms {
		if room.RuntimeStatus != "closed" || room.Service != nil {
			t.Fatal("catalog started runtime", room)
		}
		if room.RoomIdentity == a.RoomIdentity && (room.HumanRead.Status != "ready" || room.HumanRead.UnreadCount != 1 || room.HumanRead.MentionCount != 1 || room.LastMessagePreview == nil) {
			t.Fatal(room)
		}
		if room.RoomIdentity == b.RoomIdentity && (room.HumanRead.Status != "unavailable" || room.HumanRead.HumanReadCounts != nil) {
			t.Fatal(room)
		}
	}
	indexAfter, _ := os.ReadFile(m.indexPath())
	if string(indexBefore) != string(indexAfter) || len(m.runtimes) != 0 {
		t.Fatal("read changed catalog/runtime")
	}
	m.mu.Lock()
	w = managerHTTP(m, "/v1/rooms", nil)
	m.mu.Unlock()
	if w.Code != 200 {
		t.Fatal("management contention blocked the catalog", w.Code, w.Body.String())
	}
}
