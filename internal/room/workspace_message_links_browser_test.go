//go:build e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func TestWorkspaceMessageLinksBrowser(t *testing.T) {
	if os.Getenv("TEAMX_FILES_BROWSER") != "1" {
		t.Skip("optional Chromium message file links acceptance")
	}
	t.Setenv("USER", humanCaller.ID)
	for _, direct := range []bool{false, true} {
		name := "room"
		if direct {
			name = "direct"
		}
		t.Run(name, func(t *testing.T) {
			var m *roomManager
			var room roomSummary
			if direct {
				m = directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
				m.factory = controlledActivationFactory(nil, nil)
				room = createDirectConversation(t, m, directSelection("message-files-direct"))
			} else {
				m, _ = directoryTestManager(t)
				room = managerCreate(t, m, "message-files-room")
			}
			saved, _ := readManagedRoom(t, m, room)
			member := saved.Members[0]
			directory := filepath.Join(m.roomDir(room.RoomID), member.Workspace)
			writeFileFixture(t, filepath.Join(directory, "docs/中文 空格.md"), []byte("# 消息直达预览\n\n这是当前文件内容。"))
			svc := openRoomOK(t, m, room, "message-files-open")
			server := browserTestServer(t, m)
			id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
			opts := adapterOptions(svc.store, id)
			for i, value := range opts.CLIEnv {
				if strings.HasPrefix(value, "TEAMX_SERVICE_EPOCH=") {
					opts.CLIEnv[i] = "TEAMX_SERVICE_EPOCH=" + svc.info.ServiceEpoch
				}
			}
			var token string
			backend := dshBackend{store: svc.store, access: m.config.access, profile: dshProfile{Workspace: directory}, open: func(_ context.Context, o dsh.Options) (dshComponent, error) {
				token = o.ExecutionToken
				return &adapterComponent{}, nil
			}}
			session, err := backend.OpenSession(t.Context(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close(t.Context())
			send := func(key, text string) uint64 {
				t.Helper()
				body, _ := json.Marshal(apiRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id), ClientMessageID: key, Text: text})
				req, _ := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/v1/send", bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				response, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				var receipt PostReceipt
				if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil || response.StatusCode != 200 {
					t.Fatal(response.StatusCode, err)
				}
				return receipt.Seq
			}
			seq := send("browser-delivery", "已生成：[预览文件](docs/%E4%B8%AD%E6%96%87%20%E7%A9%BA%E6%A0%BC.md)。\n\n[引用式][result]\n\n[result]: docs/中文%20空格.md\n\n> [引用文本](docs/中文%20空格.md)\n\n`docs/中文 空格.md`")
			plain, revoke, err := m.config.access.issueExecution(executionBinding{ServiceInfo: svc.info, Caller: agentCallerIdentity(id)})
			if err != nil {
				t.Fatal(err)
			}
			defer revoke()
			token = plain
			legacy := send("browser-legacy", "[旧文件](docs/中文%20空格.md)")
			fixture, _ := json.Marshal(map[string]any{"name": name, "room": room.RoomIdentity, "seq": seq, "legacy": legacy})
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "node", "testdata/workspace_message_links_browser.cjs", server.URL, string(fixture))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("message links browser: %v\n%s", err, out)
			}
			t.Log(string(out))
		})
	}
}
