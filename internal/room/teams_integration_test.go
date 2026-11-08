//go:build integration || e2e

package room

import (
	"reflect"
	"testing"
)

func TestManagedTeamRuntimeFrozenAtCreation(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	room := managerCreate(t, m, "frozen-runtime")
	saved, _ := readManagedRoom(t, m, room)
	if saved.Runtime == nil {
		t.Fatal("missing creation-time runtime snapshot")
	}
	original := saved.Runtime.profile()
	// Neither changing nor removing the source team can replace a saved runtime.
	changed := *m.teams["takeout-dinner"].Runtime
	changed.Model.ModelID = "changed-after-creation"
	changed.DSH.BinaryPath = "/missing/new-binary"
	team := m.teams["takeout-dinner"]
	team.Runtime = &changed
	m.teams[team.ID] = team
	config, err := m.activationConfig(room.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range saved.Members {
		got := config.dshProfiles[member.Runner.Profile]
		if !reflect.DeepEqual(got.Model, original.Model) || got.BinaryPath != original.BinaryPath || got.ToolEnv["BASE_SETTING"] != "preserved" {
			t.Fatal("source team replaced frozen runtime", got.Model)
		}
	}
	m.teams = nil
	if _, err := m.activationConfig(room.RoomIdentity); err != nil {
		t.Fatal("restore still depends on source teams", err)
	}
	missing := saved
	missing.Runtime = nil
	var entry roomIndexEntry
	for _, e := range m.index.Rooms {
		if e.RoomIdentity == room.RoomIdentity {
			entry = e
		}
	}
	if err := validateInitialization(entry, roomInitialization{Config: missing}); err == nil {
		t.Fatal("accepted missing runtime snapshot")
	}
}
