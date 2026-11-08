//go:build integration || e2e

package room

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	aEnv, bEnv := envFor(members[0]), envFor(members[1])
	pages := decodePages(t, run(aEnv, true, "read", "--latest", "1", "--json"))
	if len(pages) != 1 || len(pages[0].Messages) != 1 || pages[0].Messages[0].Seq != 3 {
		t.Fatal(pages)
	}
	run(aEnv, true, "send", "--text", "A reply", "--id", "a-reply")
	pages = decodePages(t, run(aEnv, true, "read", "--unread", "--limit", "200", "--json"))
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
	// Enough messages for multiple real CLI output pages.
	client := cliClient{endpoint: ready.Endpoint, http: &http.Client{Timeout: 3 * time.Second}}
	for i := range 105 {
		req := apiRequest{ServiceInfo: ready.ServiceInfo, Caller: humanCaller, ClientMessageID: fmt.Sprintf("bulk-%d", i), Text: strings.Repeat("x", 200)}
		var posted PostReceipt
		if err := client.request(ctx, "/v1/send", &req, &posted); err != nil {
			t.Fatal(err)
		}
	}
	pages = decodePages(t, run(humanEnv, true, "read", "--all", "--json"))
	if len(pages) != 2 || pages[0].SnapshotSeq != 109 || pages[1].SnapshotSeq != 109 || len(pages[0].Messages)+len(pages[1].Messages) != 109 {
		t.Fatal(pages)
	}
	// A compiled CLI must also return nonzero after successfully writing a
	// page whose confirmation fails.
	confirmProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/read/confirm" {
			writeAPIError(w, errors.New("injected confirmation failure"))
			return
		}
		response, err := http.DefaultClient.Post(ready.Endpoint+r.URL.Path, "application/json", r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "proxy failure", 500)
			return
		}
		defer response.Body.Close()
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	proxyEnv := append(append([]string{}, aEnv...), "TEAMX_ENDPOINT="+confirmProxy.URL)
	failure := run(proxyEnv, false, "read", "--unread", "--limit", "1", "--json")
	confirmProxy.Close()
	if !bytes.Contains(failure, []byte("confirmation failed or uncertain")) {
		t.Fatal(string(failure))
	}
	// A real stdout pipe with no reader must not acknowledge its unread page.
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = readEnd.Close()
	broken := exec.CommandContext(ctx, binary, "read", "--unread", "--limit", "200", "--json")
	broken.Env, broken.Stdout = bEnv, writeEnd
	var brokenLog bytes.Buffer
	broken.Stderr = &brokenLog
	err = broken.Run()
	_ = writeEnd.Close()
	if err == nil {
		t.Fatal("broken stdout returned success")
	}
	output = run(humanEnv, true, "member", "list", "--json")
	if err := json.Unmarshal(output, &list); err != nil {
		t.Fatal(err)
	}
	if list.Members[1].ReadThrough != 0 || list.Members[1].UnreadCount != 109 {
		t.Fatal("broken pipe consumed data", list)
	}
	run(aEnv, false, "member", "list", "--json")
	staleEnv := append(append([]string{}, aEnv...), "TEAMX_SERVICE_EPOCH=old-service")
	run(staleEnv, false, "read", "--all", "--json")
	run(humanEnv, true, "member", "remove", "--agent", "A", "--json")
	output = run(humanEnv, true, "member", "add", "--agent", "A", "--profile", "fixture-ignore", "--json")
	var rejoined MemberInfo
	if err := json.Unmarshal(output, &rejoined); err != nil {
		t.Fatal(err)
	}
	if rejoined.MembershipID == members[0].MembershipID || rejoined.ReadThrough != 109 {
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
