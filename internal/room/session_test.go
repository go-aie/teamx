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
)

func TestCLIFixtureSessions(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "room")
	buildFixtureCLI(t, binary)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	dir := t.TempDir()
	process := exec.CommandContext(ctx, binary, "serve", "--config", writeStartupConfig(t, t.TempDir(), ""), "--listen", "127.0.0.1:0", "--state-dir", dir)
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
	t.Cleanup(func() {
		if !waited {
			_ = process.Process.Kill()
			_ = process.Wait()
		}
	})
	var ready struct {
		ServiceInfo
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(pipe).Decode(&ready); err != nil {
		t.Fatal("serve readiness", err)
	}
	client := cliClient{endpoint: ready.Endpoint, http: &http.Client{Timeout: 3 * time.Second}}
	listMembers := func() serviceMembers {
		t.Helper()
		var list serviceMembers
		req := apiRequest{ServiceInfo: ready.ServiceInfo, Caller: humanCaller}
		if err := client.request(ctx, "/v1/member/list", &req, &list); err != nil {
			t.Fatal(err)
		}
		return list
	}
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Env = append(cleanRoomEnv(), "TEAMX_ENDPOINT="+client.endpoint)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("%v: %v: %s", args, err, stderr.String())
		}
		return stdout.Bytes()
	}
	var ids [2]MemberIdentity
	for i, config := range []struct{ agent, script string }{{"A", "reply,read,ignore"}, {"B", "ignore,read"}} {
		var added MemberInfo
		if err := json.Unmarshal(run("member", "add", "--agent", config.agent, "--profile", "fixture-"+config.script, "--json"), &added); err != nil {
			t.Fatal(err)
		}
		ids[i] = MemberIdentity{RoomIdentity: ready.RoomIdentity, AgentID: added.AgentID, MembershipID: added.MembershipID}
		if _, err := os.Stat(filepath.Join(dir, "agents", added.MembershipID, "session.json")); !os.IsNotExist(err) {
			t.Fatal("Session created before first turn", err)
		}
	}
	run("send", "--text", "first secret body", "--id", "one")
	run("send", "--text", "second secret body", "--id", "two")
	if output := run("member", "list", "--json"); bytes.Contains(output, []byte("secret body")) {
		t.Fatal("notification leaked body", string(output))
	}
	wait := func(i int, turns uint64) AgentSnapshot {
		t.Helper()
		for {
			list := listMembers()
			status := list.Members[i].Execution
			if status != nil && !status.Active && status.Agent.TurnNumber == turns {
				if status.Agent.Turn.Status != "completed" || status.Error != "" {
					t.Fatal(status)
				}
				return *status.Agent
			}
			select {
			case <-ctx.Done():
				t.Fatal("turn did not settle", list, ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
	}
	load := func(i int) virtualSnapshot {
		t.Helper()
		var s virtualSnapshot
		if err := readSnapshot(filepath.Join(dir, "agents", ids[i].MembershipID, "session.json"), &s, maxPrivateFileBytes); err != nil {
			t.Fatal(err)
		}
		return s
	}
	run("member", "run", "--agent", "A")
	wait(0, 1)
	run("member", "pause", "--agent", "A")
	a1 := load(0)
	if a1.Turns != 1 || len(a1.LastRead) != 2 || a1.ProcessedThrough != 2 || !strings.Contains(a1.PrivateOutput, "private turn") {
		t.Fatal(a1)
	}
	run("member", "run", "--agent", "B")
	wait(1, 1)
	run("member", "pause", "--agent", "B")
	b1 := load(1)
	if b1.SessionID == a1.SessionID || len(b1.LastRead) != 0 || b1.ProcessedThrough != 0 {
		t.Fatal(a1, b1)
	}
	list := listMembers()
	if list.LatestSeq != 3 || list.Members[0].UnreadCount != 0 || list.Members[1].UnreadCount != 3 {
		t.Fatal(list)
	}
	// Only the explicit scripted send is public; private output stays on disk.
	var history RoomSnapshot
	err = readSnapshot(filepath.Join(dir, "room.json"), &history, maxRoomFileBytes)
	if err != nil || history.Messages[2].Author != (Author{Kind: "agent", ID: "A"}) || !strings.Contains(history.Messages[2].Text, "second secret body") {
		t.Fatal(history, err)
	}
	if bytes.Contains(run("read", "--all", "--json"), []byte("private turn")) {
		t.Fatal("ordinary output was broadcast")
	}
	run("send", "--text", "third body", "--id", "three")
	for i, agent := range []string{"A", "B"} {
		run("member", "run", "--agent", agent)
		wait(i, 2)
		run("member", "pause", "--agent", agent)
	}
	a2, b2 := load(0), load(1)
	if a2.SessionID != a1.SessionID || b2.SessionID != b1.SessionID || a2.Turns != 2 || b2.Turns != 2 || len(a2.LastRead) != 1 || len(b2.LastRead) != 4 {
		t.Fatal(a2, b2)
	}
	if a2.LastRead[0].Text != "third body" || b2.LastRead[0].Text != "first secret body" {
		t.Fatal("private contexts mixed", a2, b2)
	}
	run("send", "--text", "unread silence", "--id", "four")
	run("member", "run", "--agent", "A")
	wait(0, 3)
	list = listMembers()
	if list.LatestSeq != 5 || list.Members[0].UnreadCount != 1 {
		t.Fatal(list)
	}
	a3 := load(0)
	if a3.ScriptPosition != 3 || a3.Turns != 3 || len(a3.LastRead) != 0 || a3.ProcessedThrough != 4 {
		t.Fatal(a3)
	}
	// Both owned resources and persisted records survive a normal close.
	if err := process.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	err = process.Wait()
	waited = true
	if err != nil {
		t.Fatalf("serve exit: %v: %s", err, logs.String())
	}
	for _, id := range ids {
		var state virtualSnapshot
		if err := readSnapshot(filepath.Join(dir, "agents", id.MembershipID, "session.json"), &state, maxPrivateFileBytes); err != nil {
			t.Fatal(err)
		}
		if state.Events[len(state.Events)-1] != "session closed" || strings.Count(strings.Join(state.Events, ";"), "session opened") != 1 {
			t.Fatal(state.Events)
		}
	}
}

func TestVirtualSessionLoadsPrivateStateWithoutReplayingScript(t *testing.T) {
	h := newRoomHarness(t, historyRoom(8, "body"), nil)
	id := h.memberID("A", "generation-A")
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(h.store.dir, "agents", id.MembershipID, "session.json")}
	backend := virtualBackend{store: h.store, script: "fail,ignore"}
	s, err := backend.OpenSession(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	n := UnreadChanged{MemberIdentity: id, Revision: 8, LatestSeq: 8, NewestUnreadSeq: 8, UnreadCount: 8}
	if err := s.Run(h.ctx, n); err == nil {
		t.Fatal("expected script failure")
	}
	first, err := h.store.loadVirtualSession(id)
	if err != nil || first.ScriptPosition != 1 || first.LastError == "" {
		t.Fatal(first, err)
	}
	if err := s.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := backend.OpenSession(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Run(h.ctx, n); err != nil {
		t.Fatal("replayed failed action", err)
	}
	second, err := h.store.loadVirtualSession(id)
	if err != nil || second.SessionID != first.SessionID || second.Turns != 2 || second.ScriptPosition != 2 || second.LastError != "" {
		t.Fatal(second, err)
	}
	if err := reopened.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
}
