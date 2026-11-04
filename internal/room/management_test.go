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

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func managerTestProfiles(t *testing.T) map[string]dshProfile {
	t.Helper()
	profile := dshProfile{BinaryPath: os.Args[0], Workspace: t.TempDir(), Instructions: "private-base-role-not-published",
		Model:   dsh.ModelConfig{Protocol: "openai-completions", BaseURL: "https://runtime.invalid/v1", ModelID: "fixture", APIKeyEnv: "TEAMX_MANAGER_TEST_KEY"},
		ToolEnv: map[string]string{"BASE_SETTING": "preserved"}}
	t.Setenv(profile.Model.APIKeyEnv, "offline-test-only")
	return map[string]dshProfile{"runtime": profile}
}

func testManager(t *testing.T, dir string, profiles map[string]dshProfile) *roomManager {
	t.Helper()
	m, err := newRoomManager(dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	m.teams = map[string]teamDefinition{}
	if profiles != nil {
		base := profiles["runtime"]
		runtime := teamRuntime{Model: base.Model}
		runtime.DSH.BinaryPath, runtime.DSH.BinaryArgs, runtime.DSH.Artifact = base.BinaryPath, base.BinaryArgs, base.Artifact
		for id, agents := range map[string][]string{
			"takeout-dinner":       {"xiaolin", "laochen", "xiaoman"},
			"bookstore-night":      {"xiaoye", "alan", "qixi", "laozhou"},
			"software-development": {"developer", "reviewer"},
		} {
			team := teamDefinition{ID: id, Name: id, Description: "Fixture team", OpeningMessage: "Opening", Runtime: &runtime}
			for _, agent := range agents {
				env := map[string]string{"TEAMX_ROLE": agent}
				for k, v := range base.ToolEnv {
					env[k] = v
				}
				team.Members = append(team.Members, teamMember{ID: agent, Name: agent, Instructions: "private-role-" + agent, ToolEnv: env})
			}
			m.teams[id] = team
		}
	}
	t.Cleanup(func() {
		if err := m.close(); err != nil {
			t.Error(err)
		}
	})
	return m
}

func managerHTTP(m *roomManager, path string, body any) *httptest.ResponseRecorder {
	var data []byte
	method := http.MethodGet
	if body != nil {
		data, _ = json.Marshal(body)
		method = http.MethodPost
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	return w
}

func managerCreate(t *testing.T, m *roomManager, id string) roomSummary {
	t.Helper()
	r := managerHTTP(m, "/v1/rooms/create", roomCreateRequest{ServiceEpoch: m.epoch, Caller: humanCaller,
		roomSelection: roomSelection{CreateID: id, TeamID: "takeout-dinner"}})
	var summary roomSummary
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &summary) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	return summary
}

func readManagedRoom(t *testing.T, m *roomManager, summary roomSummary) (managedRoomConfig, RoomSnapshot) {
	t.Helper()
	var config managedRoomConfig
	var state RoomSnapshot
	if err := readSnapshot(filepath.Join(m.roomDir(summary.RoomID), "room-config.json"), &config, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	if err := readSnapshot(filepath.Join(m.roomDir(summary.RoomID), "room.json"), &state, maxRoomFileBytes); err != nil {
		t.Fatal(err)
	}
	return config, state
}

func TestRoomManagerCreationIsolationPersistenceAndDeduplication(t *testing.T) {
	dir := t.TempDir()
	profiles := managerTestProfiles(t)
	m := testManager(t, dir, profiles)
	first := managerCreate(t, m, "first")
	second := managerCreate(t, m, "second")
	if first.RoomID == second.RoomID || first.RoomInstance == second.RoomInstance || first.Name == "" || first.Status != "ready" || m.index.ActiveRoomID != "" {
		t.Fatal(first, second)
	}
	config, state := readManagedRoom(t, m, first)
	_, other := readManagedRoom(t, m, second)
	if config.MaxTurns != 7 || config.OpeningMessage == "" || len(config.Members) != 3 || len(state.Messages) != 0 || state.LatestSeq != 0 || state.Revision != 0 {
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
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &list) != nil || len(list.Rooms) != 3 || list.ActiveRoomID != "" {
		t.Fatal(response.Code, response.Body.String())
	}
}

func TestRoomManagerIdentityAndSelectionPreflight(t *testing.T) {
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
	if config.MaxTurns != 7 || len(state.Members) != 2 || state.Members[0].MembershipID != e.Initialization.Config.Members[0].MembershipID {
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

func TestServeManagementFlagsRejectMixedModes(t *testing.T) {
	for _, args := range [][]string{{"--dsh-config", "old.json"}, {"--state-dir", ""}, {"--max-turns", "0"}, {"--merge-window", "-1s"}} {
		var stdout, stderr bytes.Buffer
		args = append([]string{"--config", writeStartupConfig(t, t.TempDir(), "")}, args...)
		if err := serve(args, &stdout, &stderr); !errors.Is(err, ErrRequest) || stdout.Len() != 0 {
			t.Fatal(args, err, stdout.String())
		}
	}
}

// A production binary uses only the configured team directory across working directories.
func TestProductionRoomCatalogAcrossWorkingDirectories(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "teamx")
	if data, err := exec.Command("go", "build", "-o", binary, "../../cmd/teamx").CombinedOutput(); err != nil {
		t.Fatal(err, string(data))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
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
	get := func(endpoint string) roomsResponse {
		r, err := client.Get(endpoint + "/v1/rooms")
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		var data roomsResponse
		if r.StatusCode != 200 || json.NewDecoder(r.Body).Decode(&data) != nil {
			t.Fatal(r.Status)
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
	r, err := client.Post(info.Endpoint+"/v1/rooms/create", "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal(r.Status, string(body))
	}
	created := get(info.Endpoint).Rooms[0]
	stop()
	info, stop = start(t.TempDir(), info.DataDir, "")
	list = get(info.Endpoint)
	if len(list.Rooms) != 1 || list.Rooms[0].RoomIdentity != created.RoomIdentity || len(list.Teams) != 0 || info.ServiceEpoch == req.ServiceEpoch {
		t.Fatal(list)
	}
	stop()
}

func TestRoomManagerPartialCreationRejectsForeignRoomBeforeWriting(t *testing.T) {
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

func TestRoomManagerTeamsAcrossWorkingDirectories(t *testing.T) {
	firstCWD, secondCWD, teamsDir := t.TempDir(), t.TempDir(), t.TempDir()
	team := testTeamDefinition(t)
	team.ID = "takeout-dinner"
	writeTestTeam(t, teamsDir, team)
	t.Chdir(firstCWD)
	m, err := newTeamRoomManager("./data", teamsDir, 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := m.store.dir
	created := managerCreate(t, m, "outside-repository")
	if root != filepath.Join(firstCWD, "data") || len(m.teams) != 1 {
		t.Fatal(root, m.teams)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	t.Chdir(secondCWD)
	restored, err := newTeamRoomManager(root, teamsDir, 99, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.close()
	if len(restored.teams) != 1 || managerCreate(t, restored, "outside-repository").RoomIdentity != created.RoomIdentity {
		t.Fatal("catalog or creation identity depended on current directory")
	}
}

func TestRetiredExampleCatalogIsUnavailable(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := serve([]string{"--examples-dir", "old"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") || stdout.Len() != 0 {
		t.Fatal("retired CLI flag accepted", err, stdout.String())
	}
	svc, err := newService(t.TempDir(), "no-catalog", serviceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.close(ctx); err != nil {
			t.Error(err)
		}
	})
	data, _ := json.Marshal(apiRequest{ServiceInfo: svc.info, Caller: humanCaller})
	w := httptest.NewRecorder()
	svc.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/examples/load", bytes.NewReader(data)))
	if w.Code != http.StatusNotFound {
		t.Fatal("retired route is available", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	svc.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/ui", nil))
	var bootstrap map[string]json.RawMessage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &bootstrap) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, exists := bootstrap["examples"]; exists {
		t.Fatal("retired catalog exposed", w.Body.String())
	}
}
