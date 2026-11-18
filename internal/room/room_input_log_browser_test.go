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

func TestRoomTimelineBrowserSendReplyClearAndRestart(t *testing.T) {
	if os.Getenv("TEAMX_ROOM_TIMELINE_BROWSER") != "1" {
		t.Skip("explicit Chromium acceptance opt-in")
	}
	t.Setenv("USER", humanCaller.ID)
	out := os.Getenv("TEAMX_ROOM_TIMELINE_BROWSER_OUTPUT")
	if !filepath.IsAbs(out) {
		t.Fatal("fresh absolute output required")
	}
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(out, "rooms")
	defaults := testTeamDefinition(t).Runtime
	manager := directTestManager(t, root, defaults)
	manager.config.unifiedInputs = true
	ensureAssistant(t, manager)
	manager.factory = controlledActivationFactory(nil, nil)
	summary := assistantSummary(t, manager)
	svc := openRoomOK(t, manager, summary, "native-browser-before")
	initial, err := svc.store.LoadRoom()
	if err != nil || initial.InputLog == nil {
		t.Fatal("normal service must use Room log", err)
	}
	run := func(m *roomManager, mode string) {
		server := browserTestServer(t, m)
		defer server.Close()
		id, _ := json.Marshal(summary.RoomIdentity)
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "node", "testdata/room_timeline_browser.cjs", server.URL, string(id), mode, out)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(string(b), err)
		}
		t.Log(string(b))
	}
	run(manager, "initial")
	before, err := svc.store.LoadRoom()
	if err != nil || before.ClearedThrough != 1 || len(before.Messages) != 1 || before.Messages[0].ReplyTo != 1 {
		t.Fatal(before, err)
	}
	if err := manager.close(); err != nil {
		t.Fatal(err)
	}
	manager = directTestManager(t, root, defaults)
	manager.config.unifiedInputs = true
	manager.factory = controlledActivationFactory(nil, nil)
	svc = openRoomOK(t, manager, summary, "native-browser-after")
	run(manager, "restored")
	after, err := svc.store.LoadRoom()
	if err != nil || after.ClearedThrough != 1 || after.LatestSeq != 3 || len(after.Messages) != 2 || after.Messages[0].ClientMessageID != before.Messages[0].ClientMessageID {
		t.Fatal(after, err)
	}
	t.Logf("Room %s/%s; retained data %s", summary.RoomID, summary.RoomInstance, root)
}
