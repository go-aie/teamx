package room

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testTeamDefinition(t *testing.T) teamDefinition {
	t.Helper()
	p := managerTestProfiles(t)["runtime"]
	r := teamRuntime{Model: p.Model}
	r.DSH.BinaryPath = os.Args[0]
	return teamDefinition{ID: "sample", Name: "Sample", Description: "A team", OpeningMessage: "Opening", Runtime: &r,
		Members: []teamMember{{ID: "reviewer", Name: "Reviewer", Instructions: "review carefully", ToolEnv: map[string]string{"TEAMX_ROLE": "reviewer"}}}}
}
func writeTestTeam(t *testing.T, root string, team teamDefinition) string {
	t.Helper()
	dir := filepath.Join(root, team.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(team)
	if err := os.WriteFile(filepath.Join(dir, "team.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
func createTeamRoom(t *testing.T, m *roomManager, id string) roomSummary {
	t.Helper()
	w := managerHTTP(m, "/v1/rooms/create", roomCreateRequest{ServiceEpoch: m.epoch, Caller: humanCaller, roomSelection: roomSelection{TeamID: "sample", CreateID: id}})
	var summary roomSummary
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &summary) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return summary
}
func TestTeamConfigSnapshotsAndResourceIsolation(t *testing.T) {
	root, teams := t.TempDir(), t.TempDir()
	team := testTeamDefinition(t)
	team.Runtime.Model.Protocol = "openai-completions"
	team.Runtime.Model.ContextWindow, team.Runtime.Model.MaxTokens = 1048576, 32768
	team.Runtime.Model.ReasoningEffort = "low"
	team.Runtime.Model.InputModalities = []string{"text", "image"}
	team.Members[0].Instructions = ""
	team.Members[0].InstructionsFile = "prompts/role.md"
	team.Members[0].ToolEnv["ROLE_TOOL"] = "./tools/helper.sh"
	dir := writeTestTeam(t, teams, team)
	for _, sub := range []string{"prompts", "tools"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "prompts/role.md"), []byte("original instructions"), 0600)
	os.WriteFile(filepath.Join(dir, "tools/helper.sh"), []byte("#!/bin/sh\n"), 0700)
	m, err := newTeamRoomManager(root, teams, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}
	a, b := createTeamRoom(t, m, "a"), createTeamRoom(t, m, "b")
	if again := createTeamRoom(t, m, "a"); again.RoomIdentity != a.RoomIdentity {
		t.Fatal("duplicate create", again)
	}
	ca, sa := readManagedRoom(t, m, a)
	cb, sb := readManagedRoom(t, m, b)
	if ca.Runtime == nil || !reflect.DeepEqual(ca.Runtime.Model, team.Runtime.Model) || ca.Members[0].Instructions != "original instructions" || ca.Members[0].MembershipID == cb.Members[0].MembershipID || sa.Members[0].MembershipID == sb.Members[0].MembershipID {
		t.Fatal(ca, cb)
	}
	t.Setenv(team.Runtime.Model.APIKeyEnv, "a-secret-never-saved")
	for _, room := range []roomSummary{a, b} {
		cfg, err := m.activationConfig(room.RoomIdentity)
		if err != nil {
			t.Fatal(err)
		}
		p := cfg.dshProfiles[ca.Members[0].Runner.Profile]
		if !strings.HasPrefix(p.Workspace, m.roomDir(room.RoomID)) || !strings.HasPrefix(p.ToolEnv["ROLE_TOOL"], m.roomDir(room.RoomID)) || cfg.maxTurns != 4 || !reflect.DeepEqual(p.Model, team.Runtime.Model) {
			t.Fatal(p, cfg)
		}
	}
	data, _ := os.ReadFile(filepath.Join(m.roomDir(a.RoomID), "room-config.json"))
	if bytes.Contains(data, []byte(`"artifact"`)) {
		t.Fatal("snapshot added an unconfigured artifact", string(data))
	}
	if bytes.Contains(data, []byte("a-secret-never-saved")) {
		t.Fatal("saved credential")
	}
	catalog := managerHTTP(m, "/v1/rooms", nil).Body.String()
	if strings.Contains(catalog, "binary_path") || strings.Contains(catalog, "api_key_env") || strings.Contains(catalog, "original instructions") {
		t.Fatal(catalog)
	}
	// Retired catalog fields are rejected rather than silently ignored.
	for _, field := range []string{"example_id", "runtime_profile"} {
		body := map[string]any{"service_epoch": m.epoch, "caller": humanCaller, "team_id": "sample", "create_id": "bad", field: "old"}
		if w := managerHTTP(m, "/v1/rooms/create", body); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	m, err = newTeamRoomManager(root, teams, 99, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}
	cfg, err := m.activationConfig(a.RoomIdentity)
	if err != nil || cfg.maxTurns != 4 || len(m.teams) != 0 {
		t.Fatal(cfg, err)
	}
	if !reflect.DeepEqual(cfg.dshProfiles[ca.Members[0].Runner.Profile].Model, team.Runtime.Model) {
		t.Fatal("restored room lost frozen model configuration")
	}

}

func TestTeamCatalogValidation(t *testing.T) {
	for _, kind := range []string{"duplicate-member", "duplicate-team", "both-instructions", "neither-instructions", "traversal", "missing-prompt", "symlink", "identity", "credential", "protocol", "reasoning", "modalities", "image-protocol", "relative-binary", "relative-script", "missing-tool", "unknown-field", "incomplete-artifact"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			team := testTeamDefinition(t)
			switch kind {
			case "duplicate-member":
				team.Members = append(team.Members, team.Members[0])
			case "both-instructions":
				team.Members[0].InstructionsFile = "role.md"
			case "neither-instructions":
				team.Members[0].Instructions = ""
			case "traversal":
				team.Members[0].Instructions = ""
				team.Members[0].InstructionsFile = "../role.md"
			case "missing-prompt":
				team.Members[0].Instructions = ""
				team.Members[0].InstructionsFile = "missing.md"
			case "identity":
				team.Members[0].ToolEnv["TEAMX_ROOM_ID"] = "forged"
			case "credential":
				team.Members[0].ToolEnv[team.Runtime.Model.APIKeyEnv] = "secret"
			case "protocol":
				team.Runtime.Model.Protocol = "invalid"
			case "reasoning":
				team.Runtime.Model.ReasoningEffort = "invalid"
			case "modalities":
				team.Runtime.Model.InputModalities = []string{"image"}
			case "image-protocol":
				team.Runtime.Model.Protocol = "deepseek-messages"
				team.Runtime.Model.InputModalities = []string{"text", "image"}
			case "relative-binary":
				team.Runtime.DSH.BinaryPath = "node"
			case "relative-script":
				team.Runtime.DSH.BinaryArgs = []string{"relative/dsh.js"}
			case "incomplete-artifact":
				team.Runtime.DSH.Artifact.Source = "build"
			case "missing-tool":
				team.Members[0].ToolEnv["HELPER"] = "./missing.py"
			}
			dir := writeTestTeam(t, root, team)
			if kind == "duplicate-team" {
				os.Mkdir(filepath.Join(root, "other"), 0700)
				data, _ := os.ReadFile(filepath.Join(dir, "team.json"))
				os.WriteFile(filepath.Join(root, "other/team.json"), data, 0600)
			}
			if kind == "symlink" {
				if err := os.Symlink(os.Args[0], filepath.Join(dir, "linked")); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "unknown-field" {
				p := filepath.Join(dir, "team.json")
				data, _ := os.ReadFile(p)
				data = append([]byte(`{"runtime_profile":"no",`), data[1:]...)
				os.WriteFile(p, data, 0600)
			}
			if _, err := loadTeams(root, nil); err == nil {
				t.Fatal("accepted", kind)
			}
		})
	}
	teams, err := loadTeams("../../.teamx/teams", testTeamDefinition(t).Runtime)
	if err != nil || len(teams) != 5 {
		t.Fatal(len(teams), err)
	}
	software := teams["software-development"]
	for _, resource := range software.resources {
		if strings.HasSuffix(resource.Path, ".py") || strings.HasSuffix(resource.Path, ".sh") {
			t.Fatal("team still owns platform tools", resource.Path)
		}
	}
}

func TestTeamScionParallelRoomSnapshots(t *testing.T) {
	m, err := newTeamRoomManager(t.TempDir(), "../../.teamx/teams", 4, testTeamDefinition(t).Runtime)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}
	team := m.teams["scion-parallel-development"]
	wantIDs := []string{"architect", "developer-a", "developer-b", "developer-c"}
	if team.Scheduling != schedulingParallel || len(team.Members) != len(wantIDs) {
		t.Fatal("invalid parallel team", team.ID, team.Scheduling, len(team.Members))
	}
	serial, err := m.teams["software-development"].Scheduling.effective()
	if err != nil || serial != schedulingSerial {
		t.Fatal("original software team changed", serial, err)
	}
	var rooms []roomSummary
	for _, createID := range []string{"parallel-one", "parallel-two"} {
		w := managerHTTP(m, "/v1/rooms/create", roomCreateRequest{ServiceEpoch: m.epoch, Caller: humanCaller, roomSelection: roomSelection{TeamID: team.ID, CreateID: createID}})
		var room roomSummary
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &room) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		rooms = append(rooms, room)
	}
	seenMemberships, seenPaths := map[string]bool{}, map[string]bool{}
	for _, room := range rooms {
		saved, state := readManagedRoom(t, m, room)
		if saved.Scheduling != schedulingParallel || len(saved.Members) != len(wantIDs) || len(state.Members) != len(wantIDs) {
			t.Fatal("invalid creation snapshot", saved.Scheduling, len(saved.Members))
		}
		cfg, err := m.activationConfig(room.RoomIdentity)
		if err != nil || cfg.scheduling != schedulingParallel {
			t.Fatal("parallel activation", cfg.scheduling, err)
		}
		for i, member := range saved.Members {
			if member.AgentID != wantIDs[i] || member.ToolEnv["TEAMX_ROLE"] != member.AgentID || member.Instructions != team.Members[i].Instructions {
				t.Fatal("role identity or instructions not frozen", member.AgentID)
			}
			profile := cfg.dshProfiles[member.Runner.Profile]
			if seenMemberships[member.MembershipID] || seenPaths[profile.Workspace] || !strings.HasPrefix(profile.Workspace, m.roomDir(room.RoomID)+string(filepath.Separator)) {
				t.Fatal("members share identity or workspace", member.AgentID, profile.Workspace)
			}
			seenMemberships[member.MembershipID], seenPaths[profile.Workspace] = true, true
			bindings, err := m.readToolBindingsLocked(room.RoomIdentity)
			if err != nil || len(bindings.Bindings) != 0 {
				t.Fatal("template unexpectedly grants a platform binding", err)
			}
		}
	}
	// Opening a saved Room must not depend on mutable source instructions.
	m.teams = nil
	if cfg, err := m.activationConfig(rooms[0].RoomIdentity); err != nil || cfg.scheduling != schedulingParallel {
		t.Fatal("snapshot depends on source team", err)
	}
}

func TestModelModalitiesCopies(t *testing.T) {
	defaults := testTeamDefinition(t).Runtime
	defaults.Model.InputModalities = []string{"text", "image"}
	flag := false
	defaults.Model.Compat.SupportsDeveloperRole = &flag
	teams := t.TempDir()
	team := testTeamDefinition(t)
	team.Runtime = nil
	writeTestTeam(t, teams, team)
	m, err := newTeamRoomManager(t.TempDir(), teams, 4, defaults)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	defaults.Model.InputModalities[1] = "text"
	flag = true
	loaded := m.teams[team.ID]
	if loaded.Runtime.Model.InputModalities[1] != "image" || *loaded.Runtime.Model.Compat.SupportsDeveloperRole {
		t.Fatal("team aliases global model slice")
	}
	profile := loaded.Runtime.profile()
	profile.Model.InputModalities[1] = "text"
	*profile.Model.Compat.SupportsDeveloperRole = true
	if loaded.Runtime.Model.InputModalities[1] != "image" || *loaded.Runtime.Model.Compat.SupportsDeveloperRole {
		t.Fatal("profile aliases team model slice")
	}
	entry, err := m.prepare(roomSelection{TeamID: team.ID, CreateID: "copy"})
	if err != nil {
		t.Fatal(err)
	}
	loaded.Runtime.Model.InputModalities[1] = "text"
	*loaded.Runtime.Model.Compat.SupportsDeveloperRole = true
	if entry.Initialization.Config.Runtime.Model.InputModalities[1] != "image" || *entry.Initialization.Config.Runtime.Model.Compat.SupportsDeveloperRole {
		t.Fatal("Room initialization aliases team model slice")
	}
}
