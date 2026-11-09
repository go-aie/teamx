package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func managerTestProfiles(t *testing.T) map[string]dshProfile {
	t.Helper()
	keyID, err := newOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	key := "TEAMX_MANAGER_TEST_KEY_" + keyID
	if err := os.Setenv(key, "offline-test-only"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv(key) })
	profile := dshProfile{BinaryPath: os.Args[0], Workspace: t.TempDir(), Instructions: "private-base-role-not-published",
		Model:   dsh.ModelConfig{Protocol: "openai-completions", BaseURL: "https://runtime.invalid/v1", ModelID: "fixture", APIKeyEnv: key},
		ToolEnv: map[string]string{"BASE_SETTING": "preserved"}}
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

func TestServeManagementFlagsRejectMixedModes(t *testing.T) {
	for _, args := range [][]string{{"--dsh-config", "old.json"}, {"--state-dir", ""}, {"--session-max-turns", "0"}, {"--merge-window", "-1s"}} {
		var stdout, stderr bytes.Buffer
		args = append([]string{"--config", writeStartupConfig(t, t.TempDir(), "")}, args...)
		if err := serve(args, &stdout, &stderr); !errors.Is(err, ErrRequest) || stdout.Len() != 0 {
			t.Fatal(args, err, stdout.String())
		}
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
