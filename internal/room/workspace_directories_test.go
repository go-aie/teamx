package room

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func directoryTestManager(t *testing.T) (*roomManager, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := activationTestManager(t)
	m.workspaceRoots = &[]string{root}
	m.factory = controlledActivationFactory(nil, nil)
	return m, root
}

func TestWorkspaceMemberDirectoryNormalizationAndOwnership(t *testing.T) {
	if !equalWorkspaceRoomConfig(managedRoomConfig{Workspace: &workspaceConfig{Root: "workspaces"}}, managedRoomConfig{Workspace: &workspaceConfig{Root: "workspaces", MemberDirectories: map[string]string{}}}) {
		t.Fatal("empty directory map differs from absent map in snapshot comparison")
	}
	input := workspaceConfig{MemberDirectories: map[string]string{"member-a": "/tmp/中文 项目/../project", "member-b": ""}}
	w, err := normalizeWorkspace(input, true)
	if err != nil || w.MemberDirectories["member-a"] != "/tmp/project" || len(w.MemberDirectories) != 1 {
		t.Fatal(w, err)
	}
	w.MemberDirectories["member-a"] = "/other"
	if input.MemberDirectories["member-a"] == "/other" {
		t.Fatal("map alias")
	}
	copy := cloneWorkspaceOperation(workspaceOperation{Target: w, RequestTarget: &w})
	copy.Target.MemberDirectories["member-a"] = "/changed"
	if equalWorkspace(copy.Target, w) || w.MemberDirectories["member-a"] != "/other" {
		t.Fatal("operation map alias")
	}
	copy.RequestTarget.MemberDirectories["member-a"] = "/request"
	if w.MemberDirectories["member-a"] != "/other" {
		t.Fatal("request map alias")
	}
	for _, dir := range []string{"relative", "~/project", "$HOME/project", "/tmp/a\nproject", "/tmp/\x00", "/" + strings.Repeat("a", 4097)} {
		if _, err := normalizeWorkspace(workspaceConfig{MemberDirectories: map[string]string{"member-a": dir}}, true); err == nil {
			t.Fatal("accepted invalid path", dir)
		}
	}
	if err := validateMemberDirectories(w, []managedMember{{MemberState: MemberState{MembershipID: "other"}}}); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	member := managedMember{MemberState: MemberState{MembershipID: "member-a"}, Workspace: "workspaces/member-a"}
	r := resolveMemberWorkspace("/room", w, member)
	if r.Directory != "/other" || r.DefaultDirectory != "/room/workspaces/member-a" || r.Source != "external" {
		t.Fatal(r)
	}
	delete(w.MemberDirectories, "member-a")
	if resolveMemberWorkspace("/room", w, member).Source != "managed" {
		t.Fatal("default not restored")
	}
}

func TestWorkspaceRootsConfigurationDistinguishesDefaultAndDisabled(t *testing.T) {
	for _, value := range []string{"", "workspace_roots = []", "workspace_roots = ['/srv/projects', '/missing']"} {
		config, err := loadStartupConfig(writeStartupConfig(t, t.TempDir(), "[server]\n"+value+"\n"), true)
		if err != nil {
			t.Fatal(err)
		}
		if (config.Server.WorkspaceRoots == nil) != (value == "") {
			t.Fatal("lost omitted/empty distinction")
		}
		if value == "workspace_roots = []" && len(*config.Server.WorkspaceRoots) != 0 {
			t.Fatal("disabled roots")
		}
	}
	for _, roots := range [][]string{{"relative"}, {"~/projects"}, {"/tmp/\n"}, make([]string, 17)} {
		if validateWorkspaceRoots(&roots) == nil {
			t.Fatal("accepted malformed roots")
		}
	}
}

func TestWorkspaceDirectoryPolicyCanonicalizationAndRevalidation(t *testing.T) {
	m, root := directoryTestManager(t)
	dir := filepath.Join(root, "中文 project")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if got, err := m.canonicalWorkspaceDirectory(alias, false); err != nil || got != dir {
		t.Fatal(got, err)
	}
	if _, err := m.canonicalWorkspaceDirectory(alias, true); err == nil {
		t.Fatal("frozen path followed a new link")
	}
	out := t.TempDir()
	if err := os.Symlink(out, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.canonicalWorkspaceDirectory(filepath.Join(root, "escape"), false); err == nil {
		t.Fatal("escaped root")
	}
	if err := os.Symlink("loop", filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.canonicalWorkspaceDirectory(filepath.Join(root, "loop"), false); err == nil {
		t.Fatal("accepted loop")
	}
	file := filepath.Join(root, "file")
	os.WriteFile(file, []byte("keep"), 0600)
	if _, err := m.canonicalWorkspaceDirectory(file, false); err == nil || err.Error() != "directory_not_directory" {
		t.Fatal("file was not diagnosed as a non-directory", err)
	}
	m.workspaceProtected = []string{dir}
	if _, err := m.canonicalWorkspaceDirectory(dir, false); err == nil {
		t.Fatal("accepted management data")
	}
	m.workspaceProtected = nil
	m.workspaceRoots = &[]string{filepath.Join(root, "unmounted"), root, root}
	if _, err := m.canonicalWorkspaceDirectory(dir, false); err != nil {
		t.Fatal("unavailable root blocked available root", err)
	}
	m.workspaceRoots = &[]string{}
	if _, err := m.canonicalWorkspaceDirectory(dir, false); err == nil {
		t.Fatal("disabled roots accepted path")
	}
	m.workspaceRoots = nil
	t.Setenv("HOME", root)
	if _, err := m.canonicalWorkspaceDirectory(dir, false); err != nil {
		t.Fatal("home default", err)
	}
	t.Setenv("HOME", "/")
	if _, err := m.canonicalWorkspaceDirectory(dir, false); err == nil {
		t.Fatal("home fallback broadened to filesystem root")
	}
}

func TestWorkspaceDirectorySnapshotDoesNotRequireLivePath(t *testing.T) {
	m := activationTestManager(t)
	room := managerCreate(t, m, "snapshot")
	c, _ := readManagedRoom(t, m, room)
	w, _ := normalizeWorkspace(*c.Workspace, false)
	w.MemberDirectories = map[string]string{c.Members[0].MembershipID: "/unavailable/mount/project"}
	c.Workspace = &w
	if err := validateWorkspaceSnapshot(c); err != nil {
		t.Fatal("unavailable path blocked repair", err)
	}
	encoded, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var decoded managedRoomConfig
	if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(c, decoded) {
		t.Fatal(err)
	}
	team := m.teams[c.TeamID]
	team.Workspace = &w
	if err := validateTeam(&team, nil); err == nil {
		t.Fatal("template propagated machine path")
	}
}

func TestWorkspaceDirectoryRequiresReadAndSearchPermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	m, root := directoryTestManager(t)
	dir := filepath.Join(root, "denied")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	for _, mode := range []os.FileMode{0, 0400, 0100} {
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := m.canonicalWorkspaceDirectory(dir, false); err == nil {
			t.Fatalf("accepted directory mode %o", mode)
		}
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := m.canonicalWorkspaceDirectory(dir, false); err != nil {
		t.Fatal("permission recovery was not rechecked", err)
	}
}

func TestExternalWorkspaceOmitsManagedRepositoryGuidanceAndCredentials(t *testing.T) {
	w := workspaceConfig{Binding: &workspaceBinding{Provider: "gitlab"}, MemberDirectories: map[string]string{"member": "/project"}}
	member := managedMember{MemberState: MemberState{MembershipID: "member"}, Instructions: "member role\n\nWorkspace 提供本地 Git 与限定仓库路径的 askpass；GitLab API 工具需要成员显式启用。\n\nOAuth 仓库网络操作使用 workspace-git。", ToolEnv: map[string]string{"GIT_ASKPASS": "/managed", "TEAMX_GITLAB_TOOLS": "/private", "GIT_TERMINAL_PROMPT": "0", "ROLE": "developer"}}
	if got := memberWorkspaceInstructions(w, member); got != "member role" {
		t.Fatal(got)
	}
	if got := memberWorkspaceToolEnv(w, member); !reflect.DeepEqual(got, map[string]string{"ROLE": "developer"}) {
		t.Fatal(got)
	}
	if len(member.ToolEnv) != 4 {
		t.Fatal("changed frozen environment")
	}
	w.MemberDirectories = nil
	if got := memberWorkspaceInstructions(w, member); got != member.Instructions {
		t.Fatal("default guidance changed")
	}
}
