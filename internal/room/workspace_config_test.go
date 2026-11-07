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

func testGitLabWorkspace() workspaceConfig {
	return workspaceConfig{Root: "workspaces", Repository: &workspaceRepository{URL: "https://gitlab.invalid/group/project.git", Branch: "main"},
		Binding: &workspaceBinding{Provider: "gitlab", URL: "https://gitlab.invalid", Project: "group/project", ConnectionID: strings.Repeat("a", 32), ConnectionGeneration: strings.Repeat("b", 32), ProjectID: 7}}
}

func TestWorkspaceConfigurationValidation(t *testing.T) {
	w, err := normalizeWorkspace(workspaceConfig{}, false)
	if err != nil || w.Root != "workspaces" {
		t.Fatal(w, err)
	}
	w, err = normalizeWorkspace(workspaceConfig{Root: "./local/../output/./"}, false)
	if err != nil || w.Root != "output" {
		t.Fatal(w, err)
	}
	for _, root := range []string{".", "..", "../out", "local/../../out", "/tmp/out", `C:\out`, `local\out`, "${HOME}/out", "~/out", "agents/sub", "Agents/sub", "RESOURCES/sub", "resources", "notifications/child", "notification-queries/child", "x/../agents", "room.json/sub", ".lock", "bad\x00path", "bad\npath", " "} {
		t.Run(root, func(t *testing.T) {
			if _, err := normalizeWorkspace(workspaceConfig{Root: root}, false); err == nil {
				t.Fatal("accepted invalid root")
			}
		})
	}
	for _, test := range []struct {
		name   string
		change func(*workspaceConfig)
	}{
		{"unknown-provider", func(w *workspaceConfig) { w.Binding.Provider = "github" }},
		{"binding-only", func(w *workspaceConfig) { w.Repository = nil }},
		{"mismatched-project", func(w *workspaceConfig) { w.Binding.Project = "other/project" }},
		{"mismatched-host", func(w *workspaceConfig) { w.Binding.URL = "https://other.invalid" }},
		{"invalid-project", func(w *workspaceConfig) { w.Binding.Project = "group/../project" }},
		{"project-in-origin", func(w *workspaceConfig) { w.Binding.URL += "/group" }},
		{"http", func(w *workspaceConfig) { w.Repository.URL = "http://gitlab.invalid/group/project.git" }},
		{"userinfo", func(w *workspaceConfig) {
			w.Repository.URL = "https://user:never-echo-this-secret@gitlab.invalid/group/project.git"
		}},
		{"query", func(w *workspaceConfig) { w.Repository.URL += "?token=never-echo-this-secret" }},
		{"fragment", func(w *workspaceConfig) { w.Repository.URL += "#never-echo-this-secret" }},
		{"encoded-path", func(w *workspaceConfig) { w.Repository.URL = "https://gitlab.invalid/group%2fproject.git" }},
		{"missing-auth", func(w *workspaceConfig) { w.Binding.ConnectionID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := testGitLabWorkspace()
			test.change(&w)
			_, err := normalizeWorkspace(w, false)
			if err == nil || strings.Contains(err.Error(), "never-echo-this-secret") {
				t.Fatal("invalid config accepted or reflected secret")
			}
		})
	}
	for _, branch := range []string{"-main", "feature..bad", "a@{b", "a b", "a\nb", "a.lock", "a/.hidden", "a//b", "a/", "main.", "@"} {
		w := testGitLabWorkspace()
		w.Repository.Branch = branch
		if _, err := normalizeWorkspace(w, false); err == nil {
			t.Fatalf("accepted invalid branch %q", branch)
		}
	}
	for _, kind := range []string{"git-only", "connection", "suggestion"} {
		w := testGitLabWorkspace()
		if kind == "git-only" {
			w.Binding = nil
		}
		if kind == "suggestion" {
			w.Binding.ConnectionID = ""
			w.Binding.ConnectionGeneration = ""
			w.Binding.ProjectID = 0
		}
		if _, err := normalizeWorkspace(w, kind == "suggestion"); err != nil {
			t.Fatal(kind, err)
		}
	}

}

func TestTeamWorkspaceDefaultsFreezeOnlyLocalConfiguration(t *testing.T) {
	root, teams := t.TempDir(), t.TempDir()
	team := testTeamDefinition(t)
	w := testGitLabWorkspace()
	w.Root = "./local-output/./"
	w.Binding.ConnectionID = ""
	w.Binding.ConnectionGeneration = ""
	w.Binding.ProjectID = 0
	team.Workspace = &w // Suggested GitLab binding has no required credentials.
	team.Members = append(team.Members, teamMember{ID: "writer", Name: "Writer", Instructions: "write carefully"})
	dir := writeTestTeam(t, teams, team)
	m, err := newTeamRoomManager(root, teams, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}
	a := createTeamRoom(t, m, "local-default")
	c, _ := readManagedRoom(t, m, a)
	if c.Workspace == nil || c.Workspace.Root != "local-output" || c.WorkspaceVersion != 1 || c.Workspace.Repository != nil || c.Workspace.Binding != nil {
		t.Fatal("suggested binding became effective", c.Workspace)
	}
	for _, member := range c.Members {
		if member.Workspace != "local-output/"+member.MembershipID {
			t.Fatal("member path differs from frozen root", member.Workspace)
		}
		if _, err := os.Stat(filepath.Join(m.roomDir(a.RoomID), member.Workspace, ".git")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("created a repository", err)
		}
	}
	marker := filepath.Join(m.roomDir(a.RoomID), c.Members[0].Workspace, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep files"), 0600); err != nil {
		t.Fatal(err)
	}
	configBefore, _ := os.ReadFile(filepath.Join(m.roomDir(a.RoomID), "room-config.json"))
	// Neither later template edits nor a removed template change a prior create.
	team.Workspace.Root = "other-output"
	writeTestTeam(t, teams, team)
	if again := createTeamRoom(t, m, "local-default"); again.RoomIdentity != a.RoomIdentity {
		t.Fatal("create retry changed room")
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	m, err = newTeamRoomManager(root, teams, 9, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}
	cfg, err := m.activationConfig(a.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range c.Members {
		if cfg.dshProfiles[member.Runner.Profile].Workspace != filepath.Join(m.roomDir(a.RoomID), member.Workspace) {
			t.Fatal("DSH path does not match frozen path")
		}
	}
	configAfter, _ := os.ReadFile(filepath.Join(m.roomDir(a.RoomID), "room-config.json"))
	data, _ := os.ReadFile(marker)
	if !bytes.Equal(configBefore, configAfter) || string(data) != "keep files" {
		t.Fatal("reopening changed config or files")
	}
}

func TestWorkspaceDirectoryRejectsSymlinkComponents(t *testing.T) {
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

func TestNewSoftwareWorkspaceDoesNotImplicitlyEnableGitLab(t *testing.T) {
	root, teams := t.TempDir(), t.TempDir()
	team := testTeamDefinition(t)
	team.ID = "software-development"
	dir := writeTestTeam(t, teams, team)
	// The legacy activation branch would fail if invoked. New snapshots must
	// remain plain local workspaces until explicit binding becomes available.
	if err := os.WriteFile(filepath.Join(dir, "prepare-managed.py"), []byte("raise RuntimeError('must not run')\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := newTeamRoomManager(root, teams, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}
	a, err := m.create(roomSelection{CreateID: "software-local", TeamID: team.ID})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := m.activationConfig(a.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cfg.dshProfiles {
		if p.ToolEnv["TEAMX_SOFTWARE_TOOLS"] != "" || p.ToolEnv["GIT_ASKPASS"] != "" {
			t.Fatal("new local workspace received implicit GitLab tools")
		}
	}
}

func TestWorkspaceBindingCannotBypassManagement(t *testing.T) {
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
