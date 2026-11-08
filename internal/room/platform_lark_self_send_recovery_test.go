package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func enableSelfSend(t *testing.T, h *larkHarness, member MemberState, revision uint64) (MemberIdentity, toolBindingsFile) {
	t.Helper()
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	post, err := h.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "self-task-" + id.MembershipID, Text: "保存指定内容到本人飞书"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: post.Seq, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: post.Seq, OfferedUnreadSeq: post.Seq}}); err != nil {
		t.Fatal(err)
	}
	r := managerHTTP(h.m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: h.m.epoch, RoomIdentity: id.RoomIdentity, Caller: humanCaller, Revision: revision, Enabled: true, ConnectionVersion: h.c.Version, Binding: tool.Binding{AgentID: id.AgentID, MembershipID: id.MembershipID, Tool: "lark", ConnectionID: h.c.ID, ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: []tool.ActionGrant{{Operation: "message.send_self", TargetKind: "object", ObjectType: "user"}}}}})
	var bindings toolBindingsFile
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &bindings) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	return id, bindings
}

func selfSendRequest(t *testing.T, cli func([]string, any) error, key string) agentActionRequest {
	t.Helper()
	var v actionTargetView
	if err := cli([]string{"actions", "resolve", "--request", `{"provider":"lark","locator":{"self":true},"turn_number":1}`}, &v); err != nil || len(v.Actions) != 1 || !v.Actions[0].Executable {
		t.Fatal(v, err)
	}
	return agentActionRequest{followupCommand: followupCommand{TurnNumber: 1}, TargetRef: v.TargetRef, ID: key, Operation: platform.OperationMessageSendSelf, Input: ActionInput{Body: "S3 隔离测试保存内容"}}
}

func executeSelfSend(cli func([]string, any) error, q agentActionRequest) (referenceActionReceipt, error) {
	b, err := json.Marshal(q)
	if err != nil {
		return referenceActionReceipt{}, err
	}
	var r referenceActionReceipt
	err = cli([]string{"actions", "execute", "--request", string(b)}, &r)
	return r, err
}

func selfSendPosts(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "posts"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

func waitSelfSendFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", filepath.Base(path))
}

// Only native CLI responses are fixtures. This is a fresh OS process owning
// the production manager, HTTP actions, Store locks and persisted bindings.
func TestLarkSelfSendRecoveryProcess(t *testing.T) {
	root := os.Getenv("TEAMX_SELF_RECOVERY_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	var room roomSummary
	if err := json.Unmarshal([]byte(os.Getenv("TEAMX_SELF_RECOVERY_ROOM")), &room); err != nil {
		t.Fatal(err)
	}
	m := testManager(t, root, managerTestProfiles(t))
	server := httptest.NewServer(m)
	defer server.Close()
	var svc *service
	if os.Getenv("TEAMX_SELF_RECOVERY_MEMBERS") == "1" {
		var err error
		// Exercise ordinary service/member recovery with persistent scheduling.
		// No model profile is supplied: interrupted or unavailable sessions stay
		// paused, while durable action receipts remain independently queryable.
		svc, err = newService(m.roomDir(room.RoomID), room.RoomID, serviceConfig{endpoint: server.URL, cliPath: os.Args[0], persistentScheduling: true})
		if err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		m.runtimes[room.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
		m.mu.Unlock()
	} else {
		// Dispatch phase only: retain the fixed running turn prepared by the
		// parent without starting any model. The subsequent recovery is normal.
		svc = eventLiveService(t, &eventHarness{m: m, room: room})
	}
	m.bindPlatformReferences(svc)
	ready, _ := json.Marshal(struct {
		Endpoint string
		Info     ServiceInfo
	}{server.URL, svc.info})
	if err := os.WriteFile(os.Getenv("TEAMX_SELF_RECOVERY_READY"), ready, 0600); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

type selfSendProcess struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	endpoint string
	info     ServiceInfo
	log      string
	stopped  bool
}

func startSelfSendProcess(t *testing.T, root string, room roomSummary, dispatch ...bool) *selfSendProcess {
	t.Helper()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready.json")
	p := &selfSendProcess{cmd: exec.Command(os.Args[0], "-test.run=^TestLarkSelfSendRecoveryProcess$", "-test.timeout=2m"), log: filepath.Join(dir, "process.log")}
	b, _ := json.Marshal(room)
	recoverMembers := "1"
	if len(dispatch) > 0 && dispatch[0] {
		recoverMembers = "0"
	}
	p.cmd.Env = append(os.Environ(), "TEAMX_SELF_RECOVERY_ROOT="+root, "TEAMX_SELF_RECOVERY_ROOM="+string(b), "TEAMX_SELF_RECOVERY_READY="+ready, "TEAMX_SELF_RECOVERY_MEMBERS="+recoverMembers)
	log, err := os.Create(p.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	p.cmd.Stdout, p.cmd.Stderr = log, log
	p.stdin, err = p.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !p.stopped {
			_ = p.cmd.Process.Kill()
			_ = p.cmd.Wait()
		}
	})
	waitSelfSendFile(t, ready)
	var state struct {
		Endpoint string
		Info     ServiceInfo
	}
	b, err = os.ReadFile(ready)
	if err != nil || json.Unmarshal(b, &state) != nil {
		t.Fatal("invalid ready state", err)
	}
	p.endpoint, p.info = state.Endpoint, state.Info
	return p
}

func (p *selfSendProcess) stop(t *testing.T, crash bool) {
	t.Helper()
	if crash {
		_ = p.cmd.Process.Kill()
	} else {
		_ = p.stdin.Close()
	}
	err := p.cmd.Wait()
	p.stopped = true
	if err != nil && !crash {
		b, _ := os.ReadFile(p.log)
		t.Fatal("service shutdown failed", err, string(b))
	}
}
