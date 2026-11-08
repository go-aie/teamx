//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagedDeletionWaitsForTurnAndPrivateSessionCleanup(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	entered, cleanup, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var enteredOnce, cleanupOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	m.factory = controlledActivationFactory(func(_ MemberIdentity, ctx context.Context) error {
		enteredOnce.Do(func() { close(entered) })
		<-ctx.Done()
		return ctx.Err()
	}, func(_ MemberIdentity) {
		cleanupOnce.Do(func() { close(cleanup) })
		<-release
	})
	a, b := managerCreate(t, m, "delete-a"), managerCreate(t, m, "keep-b")
	s := openRoomOK(t, m, a, "enter-a")
	managedPost(t, m, s, "private-turn")
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no running turn")
	}
	beginDeletion(t, m, a)
	select {
	case <-cleanup:
	case <-time.After(5 * time.Second):
		t.Fatal("private Session was not closed")
	}
	if _, err := os.Stat(filepath.Join(m.roomDir(a.RoomID), "agents")); err != nil {
		t.Fatal("deleted data before Session cleanup", err)
	}
	if r := managerHTTP(m, "/v1/rooms/delete", deletionRequest{m.epoch, humanCaller, a.RoomIdentity}); r.Code != 409 {
		t.Fatal("same-room concurrent delete admitted", r.Code, r.Body.String())
	}
	if r := managerHTTP(m, "/v1/send", apiRequest{ServiceInfo: s.info, Caller: humanCaller, ClientMessageID: "late", Text: "late"}); r.Code != 409 {
		t.Fatal("human mutation admitted during deletion", r.Code, r.Body.String())
	}
	releaseOnce.Do(func() { close(release) })
	if remaining := awaitDeletion(t, m, a); remaining != nil {
		t.Fatal(remaining)
	}
	if _, err := os.Stat(m.roomDir(a.RoomID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Room or private Session data retained", err)
	}
	if m.runtimes[a.RoomIdentity] != nil {
		t.Fatal("deleted Room retained active ownership or activation receipts", m.index)
	}
	readManagedRoom(t, m, b)
	openRoomOK(t, m, b, "enter-survivor")
	if r := managerHTTP(m, "/v1/send", apiRequest{ServiceInfo: s.info, Caller: humanCaller, ClientMessageID: "stale", Text: "stale"}); r.Code != 403 {
		t.Fatal("stale deleted-Room identity accepted", r.Code, r.Body.String())
	}
}

func TestManagedDeletionInactiveRoomAndHistoricalPrivateData(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	m.factory = controlledActivationFactory(nil, nil)
	a, b := managerCreate(t, m, "keep-active"), managerCreate(t, m, "delete-inactive")
	s := openRoomOK(t, m, a, "enter-active")
	// A retired membership/generation still requires settlement before removal.
	old := filepath.Join(m.roomDir(b.RoomID), "agents", "retired", "session.json.dsh-old")
	if err := os.MkdirAll(old, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(m.roomDir(b.RoomID), "external-project")); err != nil {
		t.Fatal(err)
	}
	beginDeletion(t, m, b)
	failed := awaitDeletion(t, m, b)
	if failed == nil || failed.DeletionPhase != "closing" || !strings.Contains(failed.Error, "settlement") {
		t.Fatal("unsettled historical data was discarded", failed)
	}
	if m.runtimes[a.RoomIdentity].service != s || s.closing.Load() {
		t.Fatal("deleting an inactive Room stopped another Room")
	}
	if err := os.WriteFile(filepath.Join(old, "settled.json"), []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	beginDeletion(t, m, b)
	if failed := awaitDeletion(t, m, b); failed != nil {
		t.Fatal(failed)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "keep" {
		t.Fatal("deletion followed a workspace symlink", err)
	}
	if m.runtimes[a.RoomIdentity].service != s {
		t.Fatal("active Room changed")
	}
	managedPost(t, m, s, "still-usable")
}

func TestManagedDeletionInterruptedRemovalRequiresExplicitRetry(t *testing.T) {
	parallelFixtureTest(t)
	for _, phase := range []string{"closing", "removing"} {
		t.Run(phase, func(t *testing.T) {
			m := activationTestManager(t)
			a := managerCreate(t, m, "interrupted-delete")
			next := m.copyIndex()
			next.Rooms[0].Status, next.Rooms[0].DeletionPhase = "deleting", phase
			if err := m.saveIndex(next); err != nil {
				t.Fatal(err)
			}
			if phase == "removing" {
				if err := os.Remove(filepath.Join(m.roomDir(a.RoomID), "room-config.json")); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			restored := testManager(t, m.store.dir, managerTestProfiles(t))
			if err := restored.restoreRuntimes(); err != nil {
				t.Fatal(err)
			}
			awaitRecovery(t, restored)
			if restored.busy || restored.runtimes[a.RoomIdentity].service != nil || restored.operationDone != nil {
				t.Fatal("restart replayed deletion or activation")
			}
			if _, err := os.Stat(restored.roomDir(a.RoomID)); err != nil {
				t.Fatal("restart deleted Room data", err)
			}
			beginDeletion(t, restored, a)
			if failed := awaitDeletion(t, restored, a); failed != nil {
				t.Fatal(failed)
			}
		})
	}
}

func TestManagedDeletionIndexFailureAfterDataRemovalCanReopen(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	a := managerCreate(t, m, "commit-failure")
	rename := m.store.ops.rename
	calls := 0
	m.store.ops.rename = func(from, to string) error {
		calls++
		if calls == 3 { // Admission and verified settlement have committed.
			return errors.New("lost final index commit")
		}
		return rename(from, to)
	}
	beginDeletion(t, m, a)
	failed := awaitDeletion(t, m, a)
	if failed == nil || failed.DeletionPhase != "removing" || !m.store.uncertain {
		t.Fatal(failed)
	}
	if _, err := os.Stat(m.roomDir(a.RoomID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expected data removal before final commit failure", err)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, m.store.dir, managerTestProfiles(t))
	beginDeletion(t, restored, a)
	if failed := awaitDeletion(t, restored, a); failed != nil {
		t.Fatal(failed)
	}
	if err := restored.validateIndex(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedDeletionRejectsForeignCallersAndIdentities(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	a := managerCreate(t, m, "auth")
	valid := deletionRequest{m.epoch, humanCaller, a.RoomIdentity}
	for _, mutate := range []func(*deletionRequest){
		func(r *deletionRequest) { r.ServiceEpoch = "old" },
		func(r *deletionRequest) { r.Caller.Kind = "agent" },
		func(r *deletionRequest) { r.Caller.MembershipID = "member" },
		func(r *deletionRequest) { r.RoomInstance = "old-instance" },
		func(r *deletionRequest) { r.RoomID = "../other" },
	} {
		req := valid
		mutate(&req)
		if r := managerHTTP(m, "/v1/rooms/delete", req); r.Code != 403 {
			t.Fatal(r.Code, r.Body.String())
		}
	}
	if r := managerHTTP(m, "/v1/rooms/delete", nil); r.Code != 405 {
		t.Fatal(r.Code)
	}
	r := httptest.NewRequest("POST", "/v1/rooms/delete", strings.NewReader(`{}`))
	r.Header.Set("Origin", "https://foreign.example")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(m.index.Rooms) != 1 || m.index.Rooms[0].Status != "ready" {
		t.Fatal("rejected request changed catalog")
	}
	readManagedRoom(t, m, a)
}

func TestManagedDeletionUninitializedReservation(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	entry, err := m.prepare(roomSelection{CreateID: "reserved", TeamID: "takeout-dinner"})
	if err != nil {
		t.Fatal(err)
	}
	next := m.copyIndex()
	next.Rooms = append(next.Rooms, entry)
	if err := m.saveIndex(next); err != nil {
		t.Fatal(err)
	}
	beginDeletion(t, m, entry.roomSummary)
	if failed := awaitDeletion(t, m, entry.roomSummary); failed != nil {
		t.Fatal(failed)
	}
	if err := m.validateIndex(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedDeletionPreservesLockedOrForeignRoomData(t *testing.T) {
	parallelFixtureTest(t)
	for _, kind := range []string{"locked", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			m := activationTestManager(t)
			a := managerCreate(t, m, "protected")
			store, err := OpenStore(m.roomDir(a.RoomID), a.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if kind == "foreign" {
				state, err := store.LoadRoom()
				if err != nil {
					t.Fatal(err)
				}
				state.RoomInstance = "foreign-instance"
				if err := store.save(filepath.Join(store.dir, "room.json"), state, maxRoomFileBytes); err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			beginDeletion(t, m, a)
			if failed := awaitDeletion(t, m, a); failed == nil || failed.DeletionPhase != "closing" || failed.Error == "" {
				t.Fatal("removed protected data", failed)
			}
			if _, err := os.Stat(filepath.Join(m.roomDir(a.RoomID), "room.json")); err != nil {
				t.Fatal("data disappeared", err)
			}
		})
	}
}

func TestManagedDeletionRejectsInvalidSnapshots(t *testing.T) {
	parallelFixtureTest(t)
	for _, mode := range []string{"foreign", "missing-members", "unsafe-member", "duplicate-member", "version", "unknown-field", "malformed", "locked"} {
		t.Run(mode, func(t *testing.T) {
			m := activationTestManager(t)
			room := managerCreate(t, m, "protected-old-history")
			data, err := os.ReadFile(filepath.Join(m.roomDir(room.RoomID), "room.json"))
			if err != nil {
				t.Fatal(err)
			}
			var state map[string]any
			if err := json.Unmarshal(data, &state); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "foreign":
				state["room_instance"] = "foreign-instance"
			case "missing-members":
				delete(state, "members")
			case "unsafe-member":
				state["members"].([]any)[0].(map[string]any)["membership_id"] = "../outside"
			case "duplicate-member":
				state["members"] = append(state["members"].([]any), state["members"].([]any)[0])
			case "version":
				state["version"] = 999
			case "unknown-field":
				state["unsupported_business_field"] = []any{}
			}
			data, err = json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "malformed" {
				data = append(data, []byte(`{}`)...)
			}
			path := filepath.Join(m.roomDir(room.RoomID), "room.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "locked" {
				lock, err := lockDirectory(m.roomDir(room.RoomID))
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}
			beginDeletion(t, m, room)
			if failed := awaitDeletion(t, m, room); failed == nil {
				t.Fatal("deleted unverified ownership")
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != string(data) {
				t.Fatal("protected data changed", err)
			}
		})
	}
}
