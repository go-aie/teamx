//go:build integration || e2e

package room

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceDirectoryRejectsSymlinkComponents(t *testing.T) {
	parallelFixtureTest(t)
	for _, component := range []string{"workspaces", "workspaces/nested", "workspaces/nested/member"} {
		t.Run(component, func(t *testing.T) {
			room, outside := t.TempDir(), t.TempDir()
			link := filepath.Join(room, component)
			if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, link); err != nil {
				t.Fatal(err)
			}
			if err := checkWorkspaceDirectory(room, "workspaces/nested/member"); !errors.Is(err, ErrInvalid) {
				t.Fatal("accepted symlink", err)
			}
		})
	}
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	e, err := m.prepare(roomSelection{CreateID: "symlink", TeamID: "takeout-dinner"})
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := os.MkdirAll(m.roomDir(e.RoomID), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(m.roomDir(e.RoomID), "workspaces")); err != nil {
		t.Fatal(err)
	}
	if err := m.initialize(e); !errors.Is(err, ErrInvalid) {
		t.Fatal("creation followed workspace symlink", err)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 0 {
		t.Fatal("created files outside room")
	}
}

func TestWorkspaceSnapshotRequiresCurrentConfiguration(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "current-room")
	saved, _ := readManagedRoom(t, m, room)
	for _, mutate := range []func(*managedRoomConfig){
		func(c *managedRoomConfig) { c.Workspace, c.WorkspaceVersion = nil, 0 },
		func(c *managedRoomConfig) { c.Workspace = nil },
		func(c *managedRoomConfig) { c.WorkspaceVersion = 0 },
		func(c *managedRoomConfig) { c.Workspace = &workspaceConfig{Root: "other"} },
		func(c *managedRoomConfig) { c.Members[0].Workspace = "../escape" },
	} {
		next := saved
		next.Members = append([]managedMember{}, saved.Members...)
		mutate(&next)
		if err := validateWorkspaceSnapshot(next); err == nil {
			t.Fatal("accepted missing or inconsistent workspace")
		}
	}
	path := filepath.Join(m.roomDir(room.RoomID), "room-config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	json.Unmarshal(data, &raw)
	raw["workspace"] = map[string]any{"root": "workspaces", "pat": "not-a-config-field"}
	data, _ = json.Marshal(raw)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var invalid managedRoomConfig
	if err := readSnapshot(path, &invalid, maxPrivateFileBytes); err == nil {
		t.Fatal("accepted credentials in workspace config")
	}
}

func TestWorkspaceBindingCannotBypassManagement(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "manual-binding")
	w := testGitLabWorkspace()
	setTestEffectiveWorkspace(t, m, room, w, 1)
	if _, err := m.activationConfig(room.RoomIdentity); !errors.Is(err, ErrRequest) || !strings.Contains(err.Error(), "no completed management operation") {
		t.Fatal("manually edited binding bypassed preparation boundary", err)
	}
	for _, endpoint := range []string{"/v1/workspace", "/v1/workspace/apply", "/v1/workspace/retry"} {
		if response := managerHTTP(m, endpoint, nil); response.Code == 200 {
			t.Fatal("unauthenticated request accepted", endpoint)
		}
	}
}
