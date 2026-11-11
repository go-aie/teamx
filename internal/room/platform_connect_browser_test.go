//go:build integration || e2e

package room

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real Chromium, actual TeamX HTTP/CLI, synthetic official CLI. This does not
// stand in for authorization against Feishu or a real model/Agent turn.
func TestPlatformConnectConversationBrowser(t *testing.T) {
	if os.Getenv("TEAMX_CONNECT_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	t.Setenv("USER", humanCaller.ID)
	m, svc, member, req := connectFixture(t)
	other := managerCreate(t, m, "connect-other")
	m.mu.Lock()
	for i := range m.index.Rooms {
		if m.index.Rooms[i].RoomInstance == req.RoomInstance {
			m.index.Rooms[i].Name = "Connect origin"
		}
		if m.index.Rooms[i].RoomInstance == other.RoomInstance {
			m.index.Rooms[i].Name = "Connect other"
		}
	}
	m.mu.Unlock()
	eventLiveService(t, &eventHarness{m: m, room: other})
	bin, dir := roomSetupFixture(t, "block")
	t.Setenv("PATH", filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
	server := httptest.NewServer(m)
	defer server.Close()
	m.config.endpoint = server.URL
	binary := productionFixtureCLI(t)
	env := cliToolEnv(server.URL, svc, member)
	cliEnv := os.Environ()
	for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
		cliEnv = append(cliEnv, key+"="+env(key))
	}
	start := exec.Command(binary, "platforms", "connect", "start", "--provider", "lark", "--name", req.Input.Name, "--operation-id", req.OperationID, "--source-seq", "1")
	start.Env = cliEnv
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	// A genuine CLI waiter is running throughout browser configuration/authorization.
	waitCtx, stopWait := context.WithCancel(context.Background())
	defer stopWait()
	wait := exec.CommandContext(waitCtx, binary, "platforms", "connect", "wait", "--operation-id", req.OperationID, "--timeout", "2m")
	wait.Env = cliEnv
	if err := wait.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stopWait(); _ = wait.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	browser := exec.CommandContext(ctx, "node", "testdata/platform_connect_browser.cjs", server.URL, dir, req.OperationID, req.RoomInstance, other.RoomInstance)
	browser.Env = os.Environ()
	out, err := browser.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
	done := connectWait(t, m, req, func(op platformConnectOperation) bool { return connectTerminal(op.Status) })
	if done.Status != "completed" || done.Connection.Status != "connected" {
		t.Fatal(done)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if strings.Count(string(calls), `"init", "--new"`) != 1 {
		t.Fatal("repeated application setup")
	}
}
