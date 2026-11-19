//go:build integration || e2e

package room

import (
	"encoding/json"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run inside a container with a real bind mount. This checks production
// browsing/application and an actual child cwd, with a controlled model.
// It deliberately does not claim real DSH or container sandbox acceptance.
func TestWorkspaceDirectoryMountedPath(t *testing.T) {
	dir := os.Getenv("TEAMX_WORKSPACE_MOUNT_PATH")
	if dir == "" {
		t.Skip("set TEAMX_WORKSPACE_MOUNT_PATH to an existing dedicated mounted test directory")
	}
	if !filepath.IsAbs(dir) {
		t.Fatal("absolute container path required")
	}
	m, _ := directoryTestManager(t)
	m.workspaceRoots = &[]string{filepath.Dir(dir)}
	room := managerCreate(t, m, "mounted-workspace")
	openRoomOK(t, m, room, "initial")
	saved, _ := readManagedRoom(t, m, room)
	member := saved.Members[0]
	q := url.Values{"room_id": {room.RoomID}, "room_instance": {room.RoomInstance}, "service_epoch": {m.epoch}, "caller_id": {humanCaller.ID}, "agent_id": {member.AgentID}, "membership_id": {member.MembershipID}, "path": {dir}}
	response := managerHTTP(m, "/v1/workspace/directories?"+q.Encode(), nil)
	var listing workspaceDirectoryListing
	if err := json.Unmarshal(response.Body.Bytes(), &listing); err != nil || response.Code != 200 || listing.Path != dir || !listing.CanSelect {
		t.Fatal("mounted directory not selectable", response.Code, response.Body.String(), err)
	}
	applyMemberDirectories(t, m, room, map[string]string{member.MembershipID: dir})
	status := queryWorkspace(t, m, room, "")
	if status.Members[0].EffectiveDirectory != dir || status.Members[0].DirectorySource != "external" {
		t.Fatal("container path was not committed", status.Members)
	}
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "pwd -P > mounted-cwd.txt; cat mounted-input.txt")
	cmd.Dir = m.runtimes[room.RoomIdentity].service.dshProfiles[member.Runner.Profile].Workspace
	result, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(result)) != "HOST_MOUNT_INPUT" {
		t.Fatal("mounted file/cwd proof", err, string(result))
	}
	proof, err := os.ReadFile(filepath.Join(dir, "mounted-cwd.txt"))
	if err != nil || strings.TrimSpace(string(proof)) != dir {
		t.Fatal("wrong child cwd", err)
	}
	applyMemberDirectories(t, m, room, nil)
	if _, err := os.Stat(filepath.Join(dir, "mounted-input.txt")); err != nil {
		t.Fatal("restoring default removed mounted content", err)
	}
	report, _ := json.MarshalIndent(map[string]any{"status": "passed", "scope": "real container bind mount; production directory browsing/apply; real shell cwd; controlled model", "container_path": dir, "host_marker_read": true, "restored_default_kept_files": true, "dsh": "not invoked", "browser": "separate desktop acceptance"}, "", "  ")
	if file := os.Getenv("TEAMX_WORKSPACE_MOUNT_REPORT"); file != "" {
		if err := os.WriteFile(file, append(report, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(string(report))
}
