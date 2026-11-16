//go:build e2e

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

// Real Chromium and HTTP/persistence, without a model or external platform.
func TestHumanReadBrowser(t *testing.T) {
	if os.Getenv("TEAMX_HUMAN_READ_BROWSER") != "1" {
		t.Skip("set TEAMX_HUMAN_READ_BROWSER=1 with Node, Playwright and Chromium installed")
	}
	for _, mode := range []string{"combined-1440", "combined-390", "combined-320", "reading", "locating", "history", "history-large"} {
		t.Run(mode, func(t *testing.T) {
			n := 120
			combined := strings.HasPrefix(mode, "combined-")
			if combined {
				n = 100
			}
			if strings.HasPrefix(mode, "history") {
				n = 10000
			}
			dir := t.TempDir()
			store := openTestStore(t, dir)
			text := "普通回复：请逐条查看消息。"
			if combined {
				text = strings.Repeat("普通回复：用于核对实际可见范围。\n\n", 8)
			}
			if mode == "history-large" {
				text = strings.Repeat("x", 6600)
			}
			room := historyRoom(n, text)
			room.Members = []MemberState{}
			for i := range room.Messages {
				room.Messages[i].Author = Author{Kind: "agent", ID: "assistant"}
			}
			mentions := []int{100, n - 1, n}
			if combined {
				mentions = []int{10, 70, 95}
				for _, seq := range []int{5, 55, 96} {
					room.Messages[seq-1].Author = Author{Kind: "human", ID: humanOperator()}
					room.Messages[seq-1].Text = "@" + humanOperator() + " 自己的消息不计数。"
				}
			}
			if strings.HasPrefix(mode, "history") || mode == "locating" {
				mentions = append(mentions, 1)
			}
			if mode == "locating" {
				mentions = append(mentions, 21)
			}
			for _, seq := range mentions {
				room.Messages[seq-1].Text = "@" + humanOperator() + " 请查看这条消息。"
				if combined {
					room.Messages[seq-1].Text += " @" + humanOperator() + " 重复提及只计一条。"
				}
			}
			if err := store.SaveRoom(room); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			svc, err := newService(dir, room.RoomID, serviceConfig{})
			if err != nil {
				t.Fatal(err)
			}
			defer closeService(t, svc)
			server := browserTestServer(t, svc)
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "node", "testdata/human_read_browser.cjs", server.URL, mode)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("Chromium: %v\n%s", err, output)
			} else {
				t.Log(string(output))
			}
		})
	}
}

func TestHumanReadCatalogBrowser(t *testing.T) {
	if os.Getenv("TEAMX_HUMAN_READ_BROWSER") != "1" {
		t.Skip("set TEAMX_HUMAN_READ_BROWSER=1 with Playwright and Chromium installed")
	}
	t.Setenv("USER", humanCaller.ID)
	m := directTestManager(t, t.TempDir(), testTeamDefinition(t).Runtime)
	ensureAssistant(t, m)
	direct := assistantSummary(t, m)
	m.teams = map[string]teamDefinition{"sample": testTeamDefinition(t)}
	group, broken := createTeamRoom(t, m, "reading-group"), createTeamRoom(t, m, "reading-broken")
	for i := range m.index.Rooms {
		if m.index.Rooms[i].RoomIdentity == group.RoomIdentity {
			m.index.Rooms[i].Name = "协作房间"
		}
		if m.index.Rooms[i].RoomIdentity == broken.RoomIdentity {
			m.index.Rooms[i].Name = "阅读故障房间"
		}
	}
	for _, target := range []roomSummary{direct, group, broken} {
		_, room := readManagedRoom(t, m, target)
		room.Messages = historyRoom(120, "普通回复：请逐条查看消息。").Messages
		room.LatestSeq, room.Revision = 120, 120
		for i := range room.Messages {
			room.Messages[i].Author = Author{Kind: "agent", ID: "reviewer"}
		}
		mentions := []int{1, 100}
		if !target.IsDirect {
			mentions = []int{1, 21, 100, 119, 120}
		}
		for _, seq := range mentions {
			room.Messages[seq-1].Text = "@" + humanOperator() + " 请查看这条消息。"
		}
		data, _ := json.Marshal(room)
		if err := os.WriteFile(filepath.Join(m.roomDir(target.RoomID), "room.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(m.roomDir(broken.RoomID), "human-read.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	m.factory = controlledActivationFactory(nil, nil)
	for _, target := range []roomSummary{direct, group} {
		svc := openRoomOK(t, m, target, "browser-open-"+target.RoomID)
		svc.grantsFrozen.Store(true)
	}
	server := browserTestServer(t, m)
	defer server.Close()
	m.config.endpoint = server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/human_read_catalog_browser.cjs", server.URL, direct.RoomID, group.RoomID, broken.RoomID)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Chromium: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
}
