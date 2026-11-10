package room

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testGitLabWorkspace() workspaceConfig {
	return workspaceConfig{Root: "workspaces", Repository: &workspaceRepository{URL: "https://gitlab.invalid/group/project.git", Branch: "main"},
		Binding: &workspaceBinding{Provider: "gitlab", URL: "https://gitlab.invalid", Project: "group/project", PlatformID: strings.Repeat("a", 32), PlatformGeneration: strings.Repeat("b", 32), ProjectID: 7}}
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
		{"missing-auth", func(w *workspaceConfig) { w.Binding.PlatformID = "" }},
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
			w.Binding.PlatformID = ""
			w.Binding.PlatformGeneration = ""
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
	w.Binding.PlatformID = ""
	w.Binding.PlatformGeneration = ""
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
