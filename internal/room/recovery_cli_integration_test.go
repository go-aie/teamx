//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRealCLIRestartRestoresMembersSessionsAndToolIdentity(t *testing.T) {
	parallelFixtureTest(t)
	binary := filepath.Join(t.TempDir(), "teamx")
	buildFixtureCLI(t, binary)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	first := startRecoveryCLI(t, ctx, binary, dir)
	var a, removed MemberInfo
	if err := json.Unmarshal(first.run(t, "member", "add", "--agent", "A", "--profile", "fixture-"+"read,reply,ignore", "--json"), &a); err != nil {
		t.Fatal(err)
	}
	first.run(t, "member", "add", "--agent", "B", "--profile", "fixture-ignore")
	if err := json.Unmarshal(first.run(t, "member", "add", "--agent", "removed", "--profile", "fixture-"+"ignore", "--json"), &removed); err != nil {
		t.Fatal(err)
	}
	first.run(t, "send", "--text", "before restart", "--id", "one")
	first.run(t, "member", "run", "--agent", "A")
	before := first.wait(t, 1)
	first.run(t, "member", "pause", "--agent", "A")
	first.run(t, "member", "run", "--agent", "removed")
	// Observe a settled turn before removal, leaving real orphan snapshots.
	for {
		list := first.list(t)
		execution := list.Members[2].Execution
		if execution != nil && !execution.Active && execution.Agent.TurnNumber == 1 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
	}
	first.run(t, "member", "remove", "--agent", "removed")
	b := before.Members[1]
	client := cliClient{endpoint: first.Endpoint, http: &http.Client{Timeout: 3 * time.Second}}
	request := apiRequest{ServiceInfo: first.ServiceInfo, Caller: agentCaller(b.AgentID, b.MembershipID), Mode: ReadUnread}
	var oldPage ReadPage
	if err := client.request(ctx, "/v1/read", &request, &oldPage); err != nil || oldPage.ReadReceipt == "" {
		t.Fatal(oldPage, err)
	}
	first.stop(t)
	path := filepath.Join(dir, "agents", a.MembershipID, "session.json")
	var privateBefore virtualSnapshot
	if err := readSnapshot(path, &privateBefore, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	if privateBefore.Turns != 1 || privateBefore.ScriptPosition != 1 || privateBefore.Events[len(privateBefore.Events)-1] != "session closed" {
		t.Fatal(privateBefore)
	}
	second := startRecoveryCLI(t, ctx, binary, dir)
	after := second.list(t)
	if second.RoomIdentity != first.RoomIdentity || second.ServiceEpoch == first.ServiceEpoch || len(after.Members) != 2 ||
		after.Revision != before.Revision+1 || after.LatestSeq != 1 || !after.Members[0].Attached || !after.Members[1].Attached ||
		after.Members[0].MembershipID != a.MembershipID || after.Members[0].ReadThrough != 1 ||
		after.Members[0].Execution.Agent.LastOfferedUnreadSeq != 1 || after.Members[0].Execution.Agent.TurnNumber != 1 ||
		after.Members[1].ReadThrough != 0 || after.Members[1].UnreadCount != 1 {
		t.Fatal("recovery changed committed state", before, after)
	}
	// A still-unopened restored Session must not overwrite private history.
	var privateAfter virtualSnapshot
	if err := readSnapshot(path, &privateAfter, maxPrivateFileBytes); err != nil || !reflect.DeepEqual(privateBefore, privateAfter) {
		t.Fatal("lazy recovery changed Session", privateBefore, privateAfter, err)
	}
	// Old CLI tools must fail even when redirected to the new listening port.
	stale := exec.CommandContext(ctx, binary, "read", "--unread", "--json")
	stale.Env = append(cleanRoomEnv(), "TEAMX_ENDPOINT="+second.Endpoint, "TEAMX_ROOM_ID="+first.RoomID,
		"TEAMX_ROOM_INSTANCE="+first.RoomInstance, "TEAMX_SERVICE_EPOCH="+first.ServiceEpoch,
		"TEAMX_AGENT_ID="+a.AgentID, "TEAMX_MEMBERSHIP_ID="+a.MembershipID)
	if output, err := stale.CombinedOutput(); err == nil || !strings.Contains(string(output), "identity") {
		t.Fatal("old tool accepted", string(output), err)
	}
	client.endpoint = second.Endpoint
	request.ServiceInfo = second.ServiceInfo
	request.ReadReceipt = oldPage.ReadReceipt
	var confirmed ReadConfirmation
	if err := client.request(ctx, "/v1/read/confirm", &request, &confirmed); err == nil || !strings.Contains(err.Error(), "read_expired") {
		t.Fatal("old receipt accepted", err)
	}
	var freshPage ReadPage
	if err := client.request(ctx, "/v1/read", &request, &freshPage); err != nil || freshPage.ReadReceipt == oldPage.ReadReceipt {
		t.Fatal("old pending page blocked fresh read", freshPage, err)
	}
	second.run(t, "send", "--text", "after restart", "--id", "two")
	settled := second.wait(t, 2)
	if settled.LatestSeq != 3 || settled.Members[0].ReadThrough != 2 || settled.Members[0].UnreadCount != 0 {
		t.Fatal(settled)
	}
	if err := readSnapshot(path, &privateAfter, maxPrivateFileBytes); err != nil || privateAfter.SessionID != privateBefore.SessionID ||
		privateAfter.Turns != 2 || privateAfter.ScriptPosition != 2 || len(privateAfter.LastRead) != 1 || privateAfter.LastRead[0].Text != "after restart" {
		t.Fatal("private state or CLI environment was not restored", privateAfter, err)
	}
	if output := second.run(t, "read", "--all", "--json"); bytes.Contains(output, []byte("private turn")) || !bytes.Contains(output, []byte("A read 1 message(s); last: after restart")) {
		t.Fatal(string(output))
	}
	second.run(t, "member", "remove", "--agent", "A")
	second.stop(t)
	if _, err := os.Stat(filepath.Join(dir, "agents", removed.MembershipID, "session.json")); err != nil {
		t.Fatal("removal did not retain records", err)
	}
	third := startRecoveryCLI(t, ctx, binary, dir)
	if list := third.list(t); len(list.Members) != 1 || list.Members[0].AgentID != "B" || list.LatestSeq != 3 {
		t.Fatal("orphan files revived removed members", list)
	}
	third.stop(t)
}

func TestRealCLIRecoveryFailureNeverPublishesReadiness(t *testing.T) {
	parallelFixtureTest(t)
	binary := filepath.Join(t.TempDir(), "teamx")
	buildFixtureCLI(t, binary)
	dir, id := recoveryFixture(t)
	path := filepath.Join(dir, "agents", id.MembershipID, "session.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "serve", "--config", writeStartupConfig(t, t.TempDir(), ""), "--listen", "127.0.0.1:0", "--state-dir", dir)
	cmd.Env = cleanRoomEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err == nil || stdout.Len() != 0 || !strings.Contains(stderr.String(), "restore A session") {
		t.Fatal("failed recovery appeared ready", stdout.String(), stderr.String(), err)
	}
}
