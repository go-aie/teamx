//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real Chromium + production HTTP and storage, with only the model substituted.
// Real DSH cwd/file-sharing acceptance belongs to the separate runtime gate.
func TestWorkspaceDirectoriesBrowser(t *testing.T) {
	if os.Getenv("TEAMX_WORKSPACE_BROWSER") != "1" {
		t.Skip("optional Chromium directory selection acceptance")
	}
	t.Setenv("USER", humanCaller.ID)
	m, root := directoryTestManager(t)
	child := filepath.Join(root, "中文 项目 "+strings.Repeat("long-", 12))
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docs", "src", ".hidden-folder"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	room := managerCreate(t, m, "directory-browser")
	openRoomOK(t, m, room, "browser-open")
	saved, _ := readManagedRoom(t, m, room)
	defaultDir, err := filepath.EvalSymlinks(filepath.Join(m.roomDir(room.RoomID), saved.Members[0].Workspace))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(defaultDir, "notes"), 0700); err != nil {
		t.Fatal(err)
	}
	server := browserTestServer(t, m)
	data, _ := json.Marshal(map[string]any{"root": root, "child": child, "default": defaultDir, "room": room.RoomIdentity, "member": saved.Members[0], "epoch": m.epoch, "caller": humanCaller.ID})
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/workspace_directories_browser.cjs", server.URL, string(data))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("directory browser: %v\n%s", err, output)
	}
	t.Log(string(output))
}
