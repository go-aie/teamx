//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceResourcesBrowser(t *testing.T) {
	if os.Getenv("TEAMX_FILES_BROWSER") != "1" {
		t.Skip("optional Chromium workspace resource acceptance")
	}
	t.Setenv("USER", humanCaller.ID)
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprintf("direct-%v", direct), func(t *testing.T) {
			var m *roomManager
			var room roomSummary
			var external string
			if direct {
				external, _ = filepath.EvalSymlinks(t.TempDir())
				m = directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
				m.workspaceRoots = &[]string{external}
				m.factory = controlledActivationFactory(nil, nil)
				room = createDirectConversation(t, m, directSelection("resource-files"))
			} else {
				m, external = directoryTestManager(t)
				room = managerCreate(t, m, "resource-files")
			}
			saved, state := readManagedRoom(t, m, room)
			member := saved.Members[0]
			dir, _ := filepath.EvalSymlinks(filepath.Join(m.roomDir(room.RoomID), member.Workspace))
			doc := "# 相对资源预览\n\n[文档导航](guide.md#%E4%BD%BF%E7%94%A8) · [本页锚点](#本页末尾)\n\n![本地图片](../images/图%20片.png)\n\n![远程图片](https://invalid.example/never.png)\n\n[根外链接](../../secret)\n\n<script>window.workspaceScriptRan=true</script>\n\n"
			for i := 1; i <= 20; i++ {
				doc += fmt.Sprintf("![预算图片 %d](../images/%d.png)\n\n", i, i)
				writeFileFixture(t, filepath.Join(dir, fmt.Sprintf("images/%d.png", i)), attachmentTestPNG(t, false))
			}
			doc += "## 本页末尾\n\n最后一节。\n"
			writeFileFixture(t, filepath.Join(dir, "docs/说明.md"), []byte(doc))
			writeFileFixture(t, filepath.Join(dir, "docs/guide.md"), []byte("# 使用指南\n\n[返回文档](说明.md#本页末尾)\n\n## 使用\n\n文档锚点目标。\n\n![插图](../images/图%20片.png)"))
			writeFileFixture(t, filepath.Join(dir, "images/图 片.png"), attachmentTestPNG(t, false))
			writeFileFixture(t, filepath.Join(dir, "docs/仅下载.html"), []byte("<script>window.workspaceScriptRan=true</script>"))
			rootID := workspaceRootID(room.RoomIdentity, member.MembershipID, dir)
			state.Messages = []RoomMessage{{Seq: 1, ClientMessageID: "resources", Author: Author{"agent", member.AgentID}, AuthorMembershipID: member.MembershipID, WorkspaceRootID: rootID, Text: "[资源文档](docs/说明.md)", CreatedAt: time.Now().UTC()}}
			state.LatestSeq = 1
			state.Revision++
			store, err := OpenStore(m.roomDir(room.RoomID), room.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveRoom(state); err != nil {
				t.Fatal(err)
			}
			store.Close()
			openRoomOK(t, m, room, "resources-open")
			server := browserTestServer(t, m)
			fixture, _ := json.Marshal(map[string]any{"name": fmt.Sprintf("direct-%v", direct), "room": room.RoomIdentity, "member": member.MembershipID, "external": external})
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "node", "testdata/workspace_resources_browser.cjs", server.URL, string(fixture))
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("workspace resources browser: %v\n%s", err, output)
			}
			t.Log(string(output))
		})
	}
}
