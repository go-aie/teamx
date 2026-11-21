//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Real HTTP, persisted Room configuration and Chromium. Only Agent execution
// is substituted; this does not claim DSH or real-model acceptance.
func TestWorkspaceFilesBrowser(t *testing.T) {
	if os.Getenv("TEAMX_FILES_BROWSER") != "1" {
		t.Skip("optional Chromium workspace files acceptance")
	}
	t.Setenv("USER", humanCaller.ID)
	for _, tc := range []struct {
		name             string
		direct, external bool
	}{{"room-default", false, false}, {"room-external", false, true}, {"direct-default", true, false}, {"direct-external", true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			var m *roomManager
			var external string
			var room roomSummary
			if tc.direct {
				m = directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
				m.config.unifiedInputs = true
				m.factory = controlledActivationFactory(nil, nil)
				external = t.TempDir()
				real, err := filepath.EvalSymlinks(external)
				if err != nil {
					t.Fatal(err)
				}
				external = real
				m.workspaceRoots = &[]string{external}
				room = createDirectConversation(t, m, directSelection(tc.name))
			} else {
				m, external = directoryTestManager(t)
				room = managerCreate(t, m, tc.name)
			}
			saved, _ := readManagedRoom(t, m, room)
			member := saved.Members[0]
			dir := filepath.Join(m.roomDir(room.RoomID), member.Workspace)
			if tc.external {
				dir = external
				saved.Workspace.MemberDirectories = map[string]string{member.MembershipID: external}
				if err := m.store.save(filepath.Join(m.roomDir(room.RoomID), "room-config.json"), saved, maxPrivateFileBytes); err != nil {
					t.Fatal(err)
				}
			}
			writeFileFixture(t, filepath.Join(dir, "docs/示例.md"), []byte("# 工作区预览\n\n这是当前文件内容。\n\n| 功能 | 状态 |\n| --- | --- |\n| Markdown | 可用 |\n\n- [x] 只读预览\n\n```go\nfmt.Println(\"hello\")\n```\n\n<script>alert(1)</script>\n\n![远程图片](https://invalid.example/image.png)\n\n[相对文档](other.md)\n"))
			for i := 0; i < 30; i++ {
				writeFileFixture(t, filepath.Join(dir, "docs", string(rune('a'+i))+".txt"), []byte("text"))
			}
			writeFileFixture(t, filepath.Join(dir, "docs/architecture/deep/subfolder/guide.md"), []byte("# Deep guide"))
			writeFileFixture(t, filepath.Join(dir, "docs/这是一份名称很长的工作区文件用于检查窄屏省略显示.md"), []byte("# Long name"))
			writeFileFixture(t, filepath.Join(dir, "docs/file2.txt"), []byte("second"))
			newest := filepath.Join(dir, "docs/file10.txt")
			writeFileFixture(t, newest, []byte("tenth"))
			if err := os.Chtimes(newest, time.Now(), time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			writeFileFixture(t, filepath.Join(dir, ".hidden"), []byte("hidden"))
			writeFileFixture(t, filepath.Join(dir, ".teamx/auth/token"), []byte("SECRET"))
			writeFileFixture(t, filepath.Join(dir, "image.png"), []byte("image placeholder"))
			if len(saved.Members) > 1 {
				other := filepath.Join(m.roomDir(room.RoomID), saved.Members[1].Workspace)
				writeFileFixture(t, filepath.Join(other, "other.md"), []byte("# Other source"))
			}
			var otherRoom roomSummary
			if tc.direct {
				otherRoom = createDirectConversation(t, m, directSelection(tc.name+"-other"))
			} else {
				otherRoom = managerCreate(t, m, tc.name+"-other")
			}
			otherSaved, _ := readManagedRoom(t, m, otherRoom)
			writeFileFixture(t, filepath.Join(m.roomDir(otherRoom.RoomID), otherSaved.Members[0].Workspace, "docs/示例.md"), []byte("# Other Room"))
			openRoomOK(t, m, otherRoom, "other-open")
			openRoomOK(t, m, room, "files-open")
			server := browserTestServer(t, m)
			fixture, _ := json.Marshal(map[string]any{"name": tc.name, "room": room.RoomIdentity, "member": member.MembershipID, "direct": tc.direct, "other_room": otherRoom.RoomIdentity, "other_member": saved.Members[len(saved.Members)-1].MembershipID})
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "node", "testdata/workspace_files_browser.cjs", server.URL, string(fixture))
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("workspace files browser: %v\n%s", err, output)
			}
			t.Log(string(output))
		})
	}
}
