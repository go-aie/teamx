//go:build integration || e2e

package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoomRenamePreservesRuntimeCreationReceiptAndReload(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	selection := roomSelection{CreateID: "named", TeamID: "takeout-dinner", Name: "Original"}
	room, err := m.create(selection)
	if err != nil {
		t.Fatal(err)
	}
	active := openRoomOK(t, m, room, "activate")
	info := active.info
	files := map[string][]byte{}
	for _, name := range []string{"room-config.json", "room.json"} {
		files[name], err = os.ReadFile(filepath.Join(m.roomDir(room.RoomID), name))
		if err != nil {
			t.Fatal(err)
		}
	}
	r := managerHTTP(m, "/v1/rooms/rename", roomRenameRequest{m.epoch, humanCaller, room.RoomIdentity, "  新名称  ", room.Name})
	var renamed roomSummary
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &renamed) != nil || renamed.Name != "新名称" || renamed.RoomIdentity != room.RoomIdentity {
		t.Fatal(r.Code, r.Body.String())
	}
	if m.runtimes[room.RoomIdentity].service != active || active.info != info || m.busy {
		t.Fatal("rename changed execution identity")
	}
	for name, before := range files {
		after, err := os.ReadFile(filepath.Join(m.roomDir(room.RoomID), name))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("rename changed frozen config or public state", name, err)
		}
	}
	if err := m.validateIndex(); err != nil {
		t.Fatal(err)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, m.store.dir, managerTestProfiles(t))
	again, err := restored.create(selection)
	if err != nil || again.Name != renamed.Name || again.RoomIdentity != room.RoomIdentity || len(restored.index.Rooms) != 1 {
		t.Fatal("original create receipt failed after rename/reload", again, err)
	}
}

func TestRoomRenameValidationAndConcurrentEdits(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "one")
	base := roomRenameRequest{m.epoch, humanCaller, room.RoomIdentity, "New name", room.Name}
	for _, tc := range []struct {
		name string
		edit func(*roomRenameRequest)
		want int
	}{
		{"empty", func(r *roomRenameRequest) { r.Name = "  " }, 400},
		{"too long", func(r *roomRenameRequest) { r.Name = strings.Repeat("中", 86) }, 400},
		{"no expectation", func(r *roomRenameRequest) { r.ExpectedName = "" }, 400},
		{"concurrent edit", func(r *roomRenameRequest) { r.ExpectedName = "stale" }, 409},
		{"epoch", func(r *roomRenameRequest) { r.ServiceEpoch = "stale" }, 403},
		{"instance", func(r *roomRenameRequest) { r.RoomInstance = "stale" }, 403},
		{"agent", func(r *roomRenameRequest) { r.Caller.Kind = "agent" }, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.edit(&req)
			if r := managerHTTP(m, "/v1/rooms/rename", req); r.Code != tc.want {
				t.Fatal(r.Code, r.Body.String())
			}
			if m.index.Rooms[0].Name != room.Name {
				t.Fatal("rejected request changed name")
			}
		})
	}
	m.busy = true
	if r := managerHTTP(m, "/v1/rooms/rename", base); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	m.busy = false
	if r := managerHTTP(m, "/v1/rooms/rename", nil); r.Code != 405 {
		t.Fatal(r.Code)
	}
	if r := managerHTTP(m, "/v1/rooms/rename", base); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	base.Name = "Another tab"
	if r := managerHTTP(m, "/v1/rooms/rename", base); r.Code != 409 {
		t.Fatal("stale edit overwrote new name", r.Code)
	}
}

func TestRoomRenameUncertainCommitReopensConsistently(t *testing.T) {
	parallelFixtureTest(t)
	for _, replaced := range []bool{false, true} {
		t.Run(map[bool]string{false: "before replace", true: "after replace"}[replaced], func(t *testing.T) {
			m := testManager(t, t.TempDir(), managerTestProfiles(t))
			room := managerCreate(t, m, "one")
			rename := m.store.ops.rename
			m.store.ops.rename = func(from, to string) error {
				if replaced {
					if err := rename(from, to); err != nil {
						return err
					}
				}
				return errors.New("lost rename result")
			}
			req := roomRenameRequest{m.epoch, humanCaller, room.RoomIdentity, "New name", room.Name}
			if r := managerHTTP(m, "/v1/rooms/rename", req); r.Code == 200 || !m.store.uncertain {
				t.Fatal(r.Code, r.Body.String())
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			restored := testManager(t, m.store.dir, managerTestProfiles(t))
			want := room.Name
			if replaced {
				want = req.Name
			}
			if restored.index.Rooms[0].Name != want {
				t.Fatal(restored.index.Rooms[0])
			}
		})
	}
}
