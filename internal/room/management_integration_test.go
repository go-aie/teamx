//go:build integration || e2e

package room

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// A production binary uses only the configured team directory across working directories.
func TestProductionRoomCatalogAcrossWorkingDirectories(t *testing.T) {
	parallelFixtureTest(t)
	binary := productionFixtureCLI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	type readyInfo struct {
		Endpoint     string `json:"endpoint"`
		DataDir      string `json:"data_dir"`
		TeamsDir     string `json:"teams_dir"`
		ServiceEpoch string `json:"service_epoch"`
	}
	start := func(cwd, dataDir, teamsDir string) (readyInfo, func()) {
		settings := map[string]string{}
		if dataDir != "" {
			settings["data_dir"] = dataDir
		}
		if teamsDir != "" {
			settings["teams_dir"] = teamsDir
		}
		path, err := isolatedStartupConfig(cwd, settings)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, binary, "serve", "--config", path, "--listen", "127.0.0.1:0")
		cmd.Dir, cmd.Env = cwd, cleanRoomEnv()
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		var once sync.Once
		stop := func() {
			once.Do(func() {
				_ = cmd.Process.Signal(os.Interrupt)
				if err := cmd.Wait(); err != nil {
					t.Error(err, stderr.String())
				}
			})
		}
		t.Cleanup(stop)
		line, err := bufio.NewReader(stdout).ReadBytes('\n')
		var info readyInfo
		if err != nil || json.Unmarshal(line, &info) != nil || info.Endpoint == "" {
			t.Fatal(string(line), err)
		}
		return info, stop
	}
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	teamsDir := filepath.Join(cwd, "teams")
	writeTestTeam(t, teamsDir, testTeamDefinition(t))
	info, stop := start(cwd, "", "")
	if info.DataDir != filepath.Join(cwd, "data") || info.TeamsDir != teamsDir {
		t.Fatal(info)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(method, endpoint string, data []byte) []byte {
		t.Helper()
		for {
			req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			r, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			result, readErr := io.ReadAll(r.Body)
			r.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if r.StatusCode == http.StatusOK {
				return result
			}
			var failure apiError
			if r.StatusCode != http.StatusConflict || json.Unmarshal(result, &failure) != nil || failure.Code != "busy" {
				t.Fatal(r.Status, string(result))
			}
			// Startup recovery can own the catalog briefly. Busy is returned
			// before mutation; repeat the same creation ID and exact request.
			select {
			case <-ctx.Done():
				t.Fatal("catalog did not settle", ctx.Err())
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	get := func(endpoint string) roomsResponse {
		t.Helper()
		var data roomsResponse
		if err := json.Unmarshal(request(http.MethodGet, endpoint+"/v1/rooms", nil), &data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	list := get(info.Endpoint)
	if len(list.Teams) != 1 || len(list.Rooms) != 0 {
		t.Fatal(list)
	}
	stop()
	info, stop = start(t.TempDir(), info.DataDir, teamsDir)
	req := roomCreateRequest{ServiceEpoch: info.ServiceEpoch, Caller: humanCaller, roomSelection: roomSelection{CreateID: "external-cwd", TeamID: "sample"}}
	var data []byte
	data, _ = json.Marshal(req)
	request(http.MethodPost, info.Endpoint+"/v1/rooms/create", data)
	created := get(info.Endpoint).Rooms[0]
	stop()
	info, stop = start(t.TempDir(), info.DataDir, "")
	list = get(info.Endpoint)
	if len(list.Rooms) != 1 || list.Rooms[0].RoomIdentity != created.RoomIdentity || len(list.Teams) != 0 || info.ServiceEpoch == req.ServiceEpoch {
		t.Fatal(list)
	}
	stop()
}

func TestRoomManagerCreationIsolationPersistenceAndDeduplication(t *testing.T) {
	parallelFixtureTest(t)
	dir := t.TempDir()
	profiles := managerTestProfiles(t)
	m := testManager(t, dir, profiles)
	first := managerCreate(t, m, "first")
	second := managerCreate(t, m, "second")
	if first.RoomID == second.RoomID || first.RoomInstance == second.RoomInstance || first.Name == "" || first.Status != "ready" {
		t.Fatal(first, second)
	}
	config, state := readManagedRoom(t, m, first)
	_, other := readManagedRoom(t, m, second)
	if config.SessionMaxTurns != 7 || config.OpeningMessage == "" || len(config.Members) != 3 || len(state.Messages) != 0 || state.LatestSeq != 0 || state.Revision != 0 {
		t.Fatal(config, state)
	}
	for i, member := range state.Members {
		if member.AgentID != other.Members[i].AgentID || member.MembershipID == other.Members[i].MembershipID || member.ReadThrough != 0 {
			t.Fatal(member, other.Members[i])
		}
		workspace := filepath.Join(m.roomDir(first.RoomID), config.Members[i].Workspace)
		if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
			t.Fatal(info, err)
		}
		if _, err := os.Stat(filepath.Join(m.roomDir(first.RoomID), "agents")); !os.IsNotExist(err) {
			t.Fatal("creation must not start Session or Agent execution", err)
		}
	}
	if again := managerCreate(t, m, "first"); !reflect.DeepEqual(again, first) {
		t.Fatal("lost-response retry changed receipt", first, again)
	}
	conflict := roomCreateRequest{ServiceEpoch: m.epoch, Caller: humanCaller, roomSelection: roomSelection{CreateID: "first", TeamID: "bookstore-night"}}
	if r := managerHTTP(m, "/v1/rooms/create", conflict); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	// A display name is never used as a filesystem path.
	conflict.CreateID, conflict.Name = "named", "../../same name"
	if r := managerHTTP(m, "/v1/rooms/create", conflict); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if len(m.index.Rooms) != 3 {
		t.Fatal(m.index)
	}
	// Persist a real history change. Reopening/dedup must not reinitialize it.
	store, err := OpenStore(m.roomDir(first.RoomID), first.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	state.LatestSeq, state.Revision = 1, 1
	state.Messages = []RoomMessage{{Seq: 1, ClientMessageID: "kept", Author: humanCaller.Author, Text: "keep history", CreatedAt: time.Now().UTC()}}
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	oldEpoch := m.epoch
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	// No catalog/runtime is needed merely to list existing rooms or retry a receipt.
	restored, err := newRoomManager(dir, 99)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	if restored.epoch == oldEpoch {
		t.Fatal("service epoch persisted")
	}
	if again := managerCreate(t, restored, "first"); !reflect.DeepEqual(again, first) {
		t.Fatal(again, first)
	}
	afterConfig, after := readManagedRoom(t, restored, first)
	if !reflect.DeepEqual(config, afterConfig) || !reflect.DeepEqual(state, after) {
		t.Fatal("reopen reinitialized Room", afterConfig, after)
	}
	response := managerHTTP(restored, "/v1/rooms", nil)
	var list roomsResponse
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &list) != nil || len(list.Rooms) != 3 {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestRoomManagerIdentityAndSelectionPreflight(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	base := roomCreateRequest{ServiceEpoch: m.epoch, Caller: humanCaller, roomSelection: roomSelection{CreateID: "test", TeamID: "takeout-dinner"}}
	for _, scenario := range []string{"stale epoch", "agent", "missing caller", "human membership", "unknown team", "missing create id", "long name"} {
		t.Run(scenario, func(t *testing.T) {
			req := base
			want := http.StatusBadRequest
			switch scenario {
			case "stale epoch":
				req.ServiceEpoch = "old"
				want = 403
			case "agent":
				req.Caller = agentCaller("xiaolin", "old")
				want = 403
			case "missing caller":
				req.Caller = Caller{}
				want = 403
			case "human membership":
				req.Caller.MembershipID = "old"
				want = 403
			case "unknown team":
				req.TeamID = "missing"
			case "missing create id":
				req.CreateID = ""
			case "long name":
				req.Name = strings.Repeat("x", 257)
			}
			if r := managerHTTP(m, "/v1/rooms/create", req); r.Code != want {
				t.Fatal(r.Code, r.Body.String())
			}
			if len(m.index.Rooms) != 0 {
				t.Fatal("rejection reserved Room")
			}
		})
	}
	for _, path := range []string{"/v1/send", "/v1/member/run"} {
		if r := managerHTTP(m, path, base); r.Code != 403 {
			t.Fatal(path, r.Code, r.Body.String())
		}
	}
	for _, body := range []string{`{"room_id":"old"}`, `{} {}`, `{"unknown":true}`} {
		r := httptest.NewRequest("POST", "/v1/rooms/create", strings.NewReader(body))
		w := httptest.NewRecorder()
		m.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "/v1/rooms/create", nil)
	r.Header.Set("Origin", "https://other.example")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	data := managerHTTP(m, "/v1/rooms", nil).Body.String()
	if strings.Contains(data, "private-base-role") || strings.Contains(data, "https://runtime.invalid/v1") || strings.Contains(data, "instructions") {
		t.Fatal("catalog exposed runtime or roles", data)
	}
}

func TestRoomManagerFrozenResourcesAndExplicitInterruptedCreationRetry(t *testing.T) {
	parallelFixtureTest(t)
	dir := t.TempDir()
	m := testManager(t, dir, managerTestProfiles(t))
	selection := roomSelection{CreateID: "interrupted", TeamID: "software-development"}
	e, err := m.prepare(selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(e.Initialization.Resources) != 0 {
		t.Fatal("software team still bundles provider tools")
	}
	// Generic resource freezing is independent of shipped platform resources.
	e.Initialization.Resources = []roomResource{
		{Path: "tools/helper.sh", Data: []byte("#!/bin/sh\nprintf 'frozen'\n"), Executable: true},
		{Path: "USAGE.md", Data: []byte("frozen resource instructions")},
	}
	next := m.index
	next.Rooms = append(next.Rooms, e)
	if err := m.saveIndex(next); err != nil {
		t.Fatal(err)
	}
	// This reservation simulates process exit before any Room files are written.
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored, err := newRoomManager(dir, 99)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	if _, err := os.Stat(restored.roomDir(e.RoomID)); !os.IsNotExist(err) {
		t.Fatal("startup automatically continued creation", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "rooms")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// Fail initial preparation, retain a visible diagnostic, and explicitly retry.
	if err := os.WriteFile(filepath.Join(dir, "rooms"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.create(selection); err == nil || restored.index.Rooms[0].Error == "" {
		t.Fatal("creation failure not retained", err)
	}
	if err := os.Remove(filepath.Join(dir, "rooms")); err != nil {
		t.Fatal(err)
	}
	summary, err := restored.create(selection)
	if err != nil || summary.RoomIdentity != e.RoomIdentity || summary.Status != "ready" || len(restored.index.Rooms) != 1 {
		t.Fatal(summary, err)
	}
	config, state := readManagedRoom(t, restored, summary)
	if config.SessionMaxTurns != 7 || len(state.Members) != 2 || state.Members[0].MembershipID != e.Initialization.Config.Members[0].MembershipID {
		t.Fatal(config, state)
	}
	for _, resource := range e.Initialization.Resources {
		path := filepath.Join(restored.roomDir(summary.RoomID), "resources", resource.Path)
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, resource.Data) {
			t.Fatal(path, err)
		}
		info, _ := os.Stat(path)
		if resource.Executable && info.Mode()&0100 == 0 {
			t.Fatal("tool lost executable permission", path)
		}
	}
}

func TestRoomManagerIndexCommitUnknownAndReopen(t *testing.T) {
	parallelFixtureTest(t)
	for _, afterRename := range []bool{false, true} {
		t.Run(map[bool]string{false: "before replace", true: "after replace"}[afterRename], func(t *testing.T) {
			dir := t.TempDir()
			m := testManager(t, dir, managerTestProfiles(t))
			rename := m.store.ops.rename
			calls := 0
			m.store.ops.rename = func(from, to string) error {
				calls++
				if calls == 2 {
					if afterRename {
						if err := rename(from, to); err != nil {
							return err
						}
					}
					return errors.New("simulated lost commit result")
				}
				return rename(from, to)
			}
			selection := roomSelection{CreateID: "uncertain", TeamID: "takeout-dinner"}
			if _, err := m.create(selection); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal(err)
			}
			id := m.index.Rooms[0].RoomIdentity
			if _, err := m.create(selection); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal("uncertain manager kept writing", err)
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			restored := testManager(t, dir, nil)
			got, err := restored.create(selection)
			if err != nil || got.RoomIdentity != id || len(restored.index.Rooms) != 1 {
				t.Fatal(got, err)
			}
		})
	}
}

func TestRoomManagerLocksNoOrphanScanAndCorruptIndex(t *testing.T) {
	parallelFixtureTest(t)
	dir := t.TempDir()
	m := testManager(t, dir, nil)
	if _, err := newRoomManager(dir, 1); !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
	if _, err := OpenStore(dir, "single-room"); !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	// An unindexed room and legacy state must not be imported implicitly.
	if err := os.MkdirAll(filepath.Join(dir, "rooms", "orphan", "agents"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "room.json"), []byte("legacy data"), 0600); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, dir, nil)
	if len(restored.index.Rooms) != 0 {
		t.Fatal("scanned orphan directory")
	}
	if err := restored.close(); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{`{"version":99,"rooms":[],"active_room_id":""}`, `{"version":1,"rooms":null,"active_room_id":""}`, `{"version":1,"rooms":[],"unknown":true}`} {
		if err := os.WriteFile(filepath.Join(dir, "rooms.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if manager, err := newRoomManager(dir, 1); err == nil {
			manager.close()
			t.Fatal("invalid index silently reset", data)
		}
	}
}

func TestRoomManagerConcurrentCreateRetries(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	req := roomCreateRequest{ServiceEpoch: m.epoch, Caller: humanCaller, roomSelection: roomSelection{CreateID: "double-click", TeamID: "takeout-dinner"}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := managerHTTP(m, "/v1/rooms/create", req)
			if r.Code != 200 && r.Code != 409 {
				t.Error(r.Code, r.Body.String())
			}
		}()
	}
	wg.Wait()
	result := managerCreate(t, m, "double-click")
	if len(m.index.Rooms) != 1 || result.RoomIdentity != m.index.Rooms[0].RoomIdentity {
		t.Fatal(m.index)
	}
}

func TestRoomManagerPartialCreationRejectsForeignRoomBeforeWriting(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	selection := roomSelection{CreateID: "foreign-room", TeamID: "takeout-dinner"}
	e, err := m.prepare(selection)
	if err != nil {
		t.Fatal(err)
	}
	next := m.index
	next.Rooms = append(next.Rooms, e)
	if err := m.saveIndex(next); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(m.roomDir(e.RoomID), e.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	foreign := RoomSnapshot{Version: formatVersion, RoomIdentity: RoomIdentity{RoomID: e.RoomID, RoomInstance: "foreign-instance"}, Messages: []RoomMessage{}, Members: []MemberState{}}
	if err := store.SaveRoom(foreign); err != nil {
		t.Fatal(err)
	}
	// The manager must honor each Room Store's lock as well as the root lock.
	if _, err := m.create(selection); !errors.Is(err, ErrLocked) {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.create(selection); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.roomDir(e.RoomID), "room-config.json")); !os.IsNotExist(err) {
		t.Fatal("wrote config before checking Room identity", err)
	}
	var after RoomSnapshot
	if err := readSnapshot(filepath.Join(m.roomDir(e.RoomID), "room.json"), &after, maxRoomFileBytes); err != nil || !reflect.DeepEqual(foreign, after) {
		t.Fatal(after, err)
	}
}
