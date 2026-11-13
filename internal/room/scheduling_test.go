package room

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTeamSchedulingValidationAndFrozenRecovery(t *testing.T) {
	for _, mode := range []schedulingMode{"", schedulingSerial, schedulingParallel} {
		t.Run(string(mode), func(t *testing.T) {
			root, teams := t.TempDir(), t.TempDir()
			team := testTeamDefinition(t)
			team.Scheduling = mode
			dir := writeTestTeam(t, teams, team)
			m, err := newTeamRoomManager(root, teams, 4, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = m.close() }()
			m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}
			t.Setenv(team.Runtime.Model.APIKeyEnv, "test")
			room := createTeamRoom(t, m, "frozen")
			saved, _ := readManagedRoom(t, m, room)
			want, _ := mode.effective()
			if saved.Scheduling != want {
				t.Fatal("new snapshot did not save effective mode", saved.Scheduling)
			}
			team.Scheduling = schedulingSerial
			if want == schedulingSerial {
				team.Scheduling = schedulingParallel
			}
			m.teams[team.ID] = team
			config, err := m.activationConfig(room.RoomIdentity)
			if err != nil || config.scheduling != want {
				t.Fatal("template changed existing Room", config.scheduling, err)
			}
			// Config cloning used by workspace/model operations preserves the mode.
			data, _ := json.Marshal(saved)
			var cloned managedRoomConfig
			if err := json.Unmarshal(data, &cloned); err != nil || cloned.Scheduling != want {
				t.Fatal(err, cloned.Scheduling)
			}
			if !equalWorkspaceRoomConfig(saved, cloned) {
				t.Fatal("cloned snapshot differs")
			}
			cloned.Scheduling = team.Scheduling
			if equalWorkspaceRoomConfig(saved, cloned) {
				t.Fatal("mode change bypassed workspace authorization")
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
			m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}
			config, err = m.activationConfig(room.RoomIdentity)
			if err != nil || config.scheduling != want {
				t.Fatal("restart lost frozen scheduling", config.scheduling, err)
			}
			// A corrupt snapshot must fail before runtime assembly.
			saved.Scheduling = "Parallel"
			if err := m.store.save(filepath.Join(m.roomDir(room.RoomID), "room-config.json"), saved, maxPrivateFileBytes); err != nil {
				t.Fatal(err)
			}
			if _, err := m.activationConfig(room.RoomIdentity); err == nil {
				t.Fatal("invalid snapshot activated")
			}
		})
	}
	for _, raw := range []string{`"Parallel"`, `" serial"`, `"other"`, `2`, `true`, `null`, `{}`, `[]`} {
		t.Run(raw, func(t *testing.T) {
			teams := t.TempDir()
			team := testTeamDefinition(t)
			dir := writeTestTeam(t, teams, team)
			path := filepath.Join(dir, "team.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = append([]byte(`{"scheduling":`+raw+`,`), data[1:]...)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadTeams(teams, nil); err == nil {
				t.Fatal("accepted invalid team scheduling", raw)
			}
			var c managedRoomConfig
			if err := json.Unmarshal([]byte(`{"scheduling":`+raw+`}`), &c); err == nil {
				t.Fatal("accepted invalid snapshot scheduling", raw)
			}
		})
	}
}

func TestTeamSchedulingLegacyReceiptsRemainUnchanged(t *testing.T) {
	root, teams := t.TempDir(), t.TempDir()
	writeTestTeam(t, teams, testTeamDefinition(t))
	m, err := newTeamRoomManager(root, teams, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	entry, err := m.prepare(roomSelection{TeamID: "sample", CreateID: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	entry.Initialization.Config.Scheduling = "" // old initialization and room-config have no field
	if err := m.initialize(entry); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(m.roomDir(entry.RoomID), "room-config.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(before, []byte(`"scheduling"`)) {
		t.Fatal("legacy fixture has scheduling")
	}
	if err := m.initialize(entry); err != nil {
		t.Fatal("legacy initialization receipt changed", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("reading legacy config rewrote it", err)
	}
	m.index.Rooms = append(m.index.Rooms, entry)
	legacy := entry.Initialization.Config
	explicit := legacy
	explicit.Scheduling = schedulingSerial
	if !equalWorkspaceRoomConfig(legacy, explicit) {
		t.Fatal("legacy and explicit serial authorization differ")
	}
	m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}
	t.Setenv(legacy.Runtime.Model.APIKeyEnv, "test")
	config, err := m.activationConfig(entry.RoomIdentity)
	if err != nil || config.scheduling != schedulingSerial {
		t.Fatal("legacy config did not restore serial", config.scheduling, err)
	}
	after, _ = os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("activation rewrote legacy file")
	}
}

func TestTeamSchedulingModelSelectionRewritePreservesMode(t *testing.T) {
	for _, mode := range []schedulingMode{"", schedulingParallel} {
		t.Run(string(mode), func(t *testing.T) {
			root, teams := t.TempDir(), t.TempDir()
			writeTestTeam(t, teams, testTeamDefinition(t))
			m, err := newTeamRoomManager(root, teams, 4, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer m.close()
			room := createTeamRoom(t, m, "model-rewrite")
			saved, _ := readManagedRoom(t, m, room)
			saved.Scheduling = mode
			saved.Members[0].ModelSelection = &modelSelection{Mode: "default"}
			saved.Members[0].ModelSelectionVersion = 1
			path := filepath.Join(m.roomDir(room.RoomID), "room-config.json")
			if err := m.store.save(path, saved, maxPrivateFileBytes); err != nil {
				t.Fatal(err)
			}
			member := saved.Members[0]
			change := modelChangeMember{MemberIdentity: MemberIdentity{room.RoomIdentity, member.AgentID, member.MembershipID}, Before: *member.ModelSelection, BeforeVersion: 1, Selection: modelSelection{Mode: "explicit", EntryID: "next"}, SelectionVersion: 2}
			control := modelController{manager: m}
			if err := control.saveSelectionLocked(change); err != nil {
				t.Fatal(err)
			}
			after, _ := readManagedRoom(t, m, room)
			if after.Scheduling != mode || after.Members[0].ModelSelectionVersion != 2 || after.Members[0].ModelSelection.EntryID != "next" {
				t.Fatal(after)
			}
			if !equalWorkspaceRoomConfig(saved, after) {
				t.Fatal("model rewrite changed frozen Room attributes")
			}
		})
	}
}
