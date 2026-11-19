//go:build integration || e2e

package room

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCLIFixtureWorkflow(t *testing.T) {
	parallelFixtureTest(t)
	binary := filepath.Join(t.TempDir(), "room")
	buildFixtureCLI(t, binary)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	process := exec.CommandContext(ctx, binary, "serve", "--config", writeStartupConfig(t, t.TempDir(), ""), "--listen", "127.0.0.1:0", "--state-dir", t.TempDir())
	process.Env = cleanRoomEnv()
	pipe, err := process.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	process.Stderr = &logs
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = process.Process.Kill()
			_ = process.Wait()
		}
	}()
	scanner := bufio.NewScanner(pipe)
	if !scanner.Scan() {
		t.Fatal("missing serve readiness")
	}
	var ready struct {
		ServiceInfo
		Endpoint string `json:"endpoint"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &ready); err != nil {
		t.Fatal(err)
	}
	humanEnv := append(cleanRoomEnv(), "TEAMX_ENDPOINT="+ready.Endpoint)
	run := func(env []string, success bool, args ...string) []byte {
		t.Helper()
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = env
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if (err == nil) != success {
			t.Fatalf("%v: %v stdout=%s stderr=%s", args, err, stdout.String(), stderr.String())
		}
		if !success {
			return stderr.Bytes()
		}
		return stdout.Bytes()
	}
	var members [2]MemberInfo
	for i, id := range []string{"A", "B"} {
		output := run(humanEnv, true, "member", "add", "design-review", "--agent", id, "--profile", "fixture-ignore", "--json")
		if err := json.Unmarshal(output, &members[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		run(humanEnv, true, "send", "design-review", "--text", "secret-body", "--id", fmt.Sprint(i), "--json")
	}
	// A stable retry is not appended a second time.
	run(humanEnv, true, "send", "design-review", "--text", "secret-body", "--id", "0", "--json")
	var list serviceMembers
	for {
		output := run(humanEnv, true, "member", "list", "design-review", "--json")
		if bytes.Contains(output, []byte("secret-body")) {
			t.Fatal("notification leaked body")
		}
		if err := json.Unmarshal(output, &list); err != nil {
			t.Fatal(err)
		}
		if len(list.Members) == 2 && list.Members[0].Notification != nil && list.Members[1].Notification != nil && list.Members[0].Notification.UnreadCount == 3 && list.Members[1].Notification.UnreadCount == 3 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("notifications missing")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if list.LatestSeq != 3 || list.Members[0].UnreadCount != 3 || list.Members[1].UnreadCount != 3 {
		t.Fatal(list)
	}
	envFor := func(m MemberInfo) []string {
		return append(append([]string{}, humanEnv...), "TEAMX_ROOM_ID="+ready.RoomID, "TEAMX_ROOM_INSTANCE="+ready.RoomInstance,
			"TEAMX_SERVICE_EPOCH="+ready.ServiceEpoch, "TEAMX_AGENT_ID="+m.AgentID, "TEAMX_MEMBERSHIP_ID="+m.MembershipID)
	}
	aEnv := envFor(members[0])
	for _, args := range [][]string{{"read"}, {"read", "--all"}, {"read", "--latest", "1"}, {"read", "--unread"}, {"read", "--help"}} {
		if failure := run(aEnv, false, args...); !bytes.Contains(failure, []byte(`unknown command "read"`)) {
			t.Fatal(string(failure))
		}
	}
	run(aEnv, true, "send", "--text", "A reply", "--id", "a-reply")
	client := cliClient{endpoint: ready.Endpoint, http: &http.Client{Timeout: 3 * time.Second}}
	native := apiRequest{ServiceInfo: ready.ServiceInfo, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Mode: ReadUnread, Limit: 200}
	var body bytes.Buffer
	if err := readRoomPagesForTest(ctx, client, native, &body, true); err != nil {
		t.Fatal(err)
	}
	pages := decodePages(t, body.Bytes())
	if len(pages) != 1 || len(pages[0].Messages) != 3 || pages[0].ScannedThrough != 4 {
		t.Fatal(pages)
	}
	output := run(humanEnv, true, "member", "list", "--json")
	if err := json.Unmarshal(output, &list); err != nil {
		t.Fatal(err)
	}
	if list.Members[0].UnreadCount != 0 || list.Members[1].UnreadCount != 4 {
		t.Fatal(list)
	}

	run(aEnv, false, "member", "list", "--json")
	staleEnv := append(append([]string{}, aEnv...), "TEAMX_SERVICE_EPOCH=old-service")
	run(staleEnv, false, "send", "--text", "stale", "--id", "stale-epoch")
	run(humanEnv, true, "member", "remove", "--agent", "A", "--json")
	output = run(humanEnv, true, "member", "add", "--agent", "A", "--profile", "fixture-ignore", "--json")
	var rejoined MemberInfo
	if err := json.Unmarshal(output, &rejoined); err != nil {
		t.Fatal(err)
	}
	if rejoined.MembershipID == members[0].MembershipID || rejoined.ReadThrough != 4 {
		t.Fatal(rejoined)
	}
	run(aEnv, false, "send", "--text", "old generation", "--id", "stale")
	if err := process.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err = process.Wait()
	waited = true
	if err != nil {
		t.Fatalf("serve exit: %v logs=%s", err, logs.String())
	}
}
