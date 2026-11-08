package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestLarkSelfSendIndependentProcessRecovery(t *testing.T) {
	for _, mode := range []string{"settled", "before_dispatch", "after_dispatch"} {
		t.Run(mode, func(t *testing.T) {
			h := selfSendHarness(t)
			id, _ := enableSelfSend(t, h, h.members[0], 0)
			server := httptest.NewServer(h.m)
			q := selfSendRequest(t, contractCLI(t, server.URL, h.svc, id), "stable-process-intent")
			server.Close()
			if mode != "settled" {
				path := filepath.Join(h.dir, "lark-cli")
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				gate := `
 def gate():
  import time
  open(base+'/blocked','w').write('ready')
  end=time.monotonic()+30
  while not os.path.exists(base+'/release') and time.monotonic()<end:time.sleep(.01)
 gate()
`
				if mode == "before_dispatch" {
					gate += " sys.exit(1)\n"
				}
				needle := " with open(base+'/posts','a')"
				if mode == "after_dispatch" {
					needle = " print(json.dumps({'ok':True,'identity':'user','data':{'message_id':'om_receipt'}}))"
				}
				if !strings.Contains(string(b), needle) {
					t.Fatal("fixture gate missing")
				}
				b = []byte(strings.Replace(string(b), needle, gate+needle, 1))
				if err = os.WriteFile(path, b, 0700); err != nil {
					t.Fatal(err)
				}
			}
			root := h.m.store.dir
			if err := h.m.close(); err != nil {
				t.Fatal(err)
			}
			p := startSelfSendProcess(t, root, h.room, true)
			cli := contractCLI(t, p.endpoint, &service{info: p.info}, id)
			wantPosts, wantStatus := 1, "succeeded"
			if mode == "settled" {
				if r, err := executeSelfSend(cli, q); err != nil || r.Status != wantStatus {
					t.Fatal(r, err)
				}
				p.stop(t, false)
			} else {
				done := make(chan error, 1)
				go func() { _, err := executeSelfSend(cli, q); done <- err }()
				waitSelfSendFile(t, filepath.Join(h.dir, "blocked"))
				if mode == "before_dispatch" {
					wantPosts = 0
				}
				// The native fixture has reached POST with the attempt already on
				// disk. Kill its parent service, then terminate the orphaned CLI
				// without allowing a pre-dispatch gate to issue a late write.
				p.stop(t, true)
				if err := os.WriteFile(filepath.Join(h.dir, "release"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("crashed HTTP request did not finish")
				}
				wantStatus = "unknown"
			}
			p2 := startSelfSendProcess(t, root, h.room)
			if p.info.ServiceEpoch == p2.info.ServiceEpoch {
				t.Fatal("service epoch did not rotate")
			}
			cli = contractCLI(t, p2.endpoint, &service{info: p2.info}, id)
			// Receipt recovery works across a new turn, without resolve or POST.
			q.TurnNumber = 99
			for range 2 {
				if r, err := executeSelfSend(cli, q); err != nil || r.Status != wantStatus || r.Attempts != 1 {
					t.Fatal(r, err)
				}
			}
			if wantStatus == "unknown" {
				q.TurnNumber, q.ID = 1, "replacement-intent"
				if _, err := executeSelfSend(cli, q); err == nil {
					t.Fatal("unknown intent resent under a different ID")
				}
			}
			if got := len(selfSendPosts(t, h.dir)); got != wantPosts {
				t.Fatal("native dispatch count", got, wantPosts)
			}
			p2.stop(t, false)
		})
	}
}

func TestLarkSelfSendLedgerFailures(t *testing.T) {
	for _, mode := range []string{"intent", "attempt", "result", "result_commit_unknown"} {
		t.Run(mode, func(t *testing.T) {
			h := selfSendHarness(t)
			id, _ := enableSelfSend(t, h, h.members[0], 0)
			server := httptest.NewServer(h.m)
			defer server.Close()
			cli := contractCLI(t, server.URL, h.svc, id)
			q := selfSendRequest(t, cli, "durable-intent")
			ops, saves := h.svc.store.ops, 0
			h.svc.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
				if filepath.Base(dir) == "records" {
					saves++
					if mode == "intent" && saves == 1 || mode == "attempt" && saves == 2 || mode == "result" && saves == 3 {
						return nil, errors.New("S3 injected persistence failure")
					}
				}
				return ops.createTemp(dir, pattern)
			}
			h.svc.store.ops.syncDir = func(dir string) error {
				if mode == "result_commit_unknown" && filepath.Base(dir) == "records" && saves == 3 {
					return errors.New("S3 result renamed but directory sync failed")
				}
				return ops.syncDir(dir)
			}
			r, err := executeSelfSend(cli, q)
			if mode != "attempt" && err == nil {
				t.Fatal("persistence failure hidden", r)
			}
			wantPosts := 0
			if strings.HasPrefix(mode, "result") {
				wantPosts = 1
			}
			if got := len(selfSendPosts(t, h.dir)); got != wantPosts {
				t.Fatal("dispatch without durable attempt", got)
			}
			h.svc.store.ops = ops
			root := h.m.store.dir
			if err := h.m.close(); err != nil {
				t.Fatal(err)
			}
			p := startSelfSendProcess(t, root, h.room)
			cli = contractCLI(t, p.endpoint, &service{info: p.info}, id)
			if mode != "intent" {
				want := map[string]string{"attempt": "not_applied", "result": "unknown", "result_commit_unknown": "succeeded"}[mode]
				if r, err := executeSelfSend(cli, q); err != nil || r.Status != want {
					t.Fatal("recovered outcome", r, err)
				}
				if got := len(selfSendPosts(t, h.dir)); got != wantPosts {
					t.Fatal("persistence failure replayed", got)
				}
			}
			p.stop(t, false)
		})
	}
}

func TestLarkSelfSendBindingViewExpires(t *testing.T) {
	h := selfSendHarness(t)
	id, saved := enableSelfSend(t, h, h.members[0], 0)
	server := httptest.NewServer(h.m)
	defer server.Close()
	cli := contractCLI(t, server.URL, h.svc, id)
	request := selfSendRequest(t, cli, "after-generation-change")
	read := func() toolBindingsFile {
		t.Helper()
		path := fmt.Sprintf("/v1/tools/bindings?service_epoch=%s&caller_id=operator&room_id=%s&room_instance=%s", h.m.epoch, id.RoomID, id.RoomInstance)
		r := managerHTTP(h.m, path, nil)
		var v toolBindingsFile
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &v) != nil {
			t.Fatal(r.Code, r.Body.String())
		}
		return v
	}
	if read().Bindings[0].Evidence("self_delivery") == nil {
		t.Fatal("current self capability not displayed")
	}
	// S2 snapshots remain readable, but a proof without a frozen credential
	// version cannot acquire new writes after upgrading the service.
	legacy := saved
	legacy.Bindings = append([]tool.Binding(nil), saved.Bindings...)
	proof := *legacy.Bindings[0].Evidence("self_delivery")
	proof.CredentialVersion = 0
	legacy.Bindings[0].CapabilityEvidence = []platform.CapabilityEvidence{proof}
	h.m.mu.Lock()
	err := h.m.store.save(h.m.toolBindingsPath(id.RoomIdentity), legacy, 1<<20)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if read().Bindings[0].Evidence("self_delivery") != nil {
		t.Fatal("legacy proof displayed as a current write grant")
	}
	if _, err := executeSelfSend(cli, request); err == nil {
		t.Fatal("legacy proof admitted a new write")
	}
	h.m.mu.Lock()
	err = h.m.store.save(h.m.toolBindingsPath(id.RoomIdentity), saved, 1<<20)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	refs, err := h.m.platformConnectionReferencesLocked(h.c.ID)
	h.m.mu.Unlock()
	var rotated platformConnection
	if err == nil {
		owners := make([]RoomIdentity, len(refs))
		for i, ref := range refs {
			owners[i] = ref.RoomIdentity
		}
		rotated, err = h.m.updatePlatformConnection(platformConnectionRequest{ID: h.c.ID, Version: h.c.Version, Name: h.c.Name, Provider: "lark", URL: h.c.URL, CLIProfile: h.c.CLIProfile, ReferencedRooms: owners}, false)
	}
	if err != nil {
		t.Fatal(err)
	}
	if read().Bindings[0].Evidence("self_delivery") != nil {
		t.Fatal("stale self capability displayed as enabled")
	}
	verified, err := h.m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: rotated.ID, Version: rotated.Version})
	if err != nil {
		t.Fatal(err)
	}
	if read().Bindings[0].Evidence("self_delivery") != nil {
		t.Fatal("reverification silently restored a capability from a replaced credential")
	}
	if _, err := executeSelfSend(cli, request); err == nil || len(selfSendPosts(t, h.dir)) != 0 {
		t.Fatal("a fresh verified credential reused the old self grant", err)
	}
	var disk toolBindingsFile
	if err := readSnapshot(h.m.toolBindingsPath(id.RoomIdentity), &disk, 1<<20); err != nil || disk.Bindings[0].Evidence("self_delivery") == nil || disk.Revision != saved.Revision {
		t.Fatal("view destroyed historical evidence", err)
	}
	h.c = verified
	_, renewed := enableSelfSend(t, h, h.members[0], saved.Revision)
	if renewed.Bindings[0].Evidence("self_delivery").CredentialVersion != verified.CredentialVersion || read().Bindings[0].Evidence("self_delivery") == nil {
		t.Fatal("explicit re-enable did not freeze the current credential version")
	}
}

func TestLarkSelfSendConcurrentRoomsAndMembers(t *testing.T) {
	h := selfSendHarness(t)
	path := filepath.Join(h.dir, "lark-cli")
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	script = []byte(strings.ReplaceAll(string(script), "'om_receipt'", "'om_'+payload['uuid']"))
	script = []byte(strings.Replace(string(script), "json.dump([m],open(base+'/messages.json','w'))", "previous=json.load(open(base+'/messages.json'));json.dump(previous+[m],open(base+'/messages.json','w'))", 1))
	if err := os.WriteFile(path, script, 0700); err != nil {
		t.Fatal(err)
	}
	id1, bindings := enableSelfSend(t, h, h.members[0], 0)
	id2, _ := enableSelfSend(t, h, h.members[1], bindings.Revision)
	r2 := managerCreate(t, h.m, "self-send-concurrent-room")
	_, state := readManagedRoom(t, h.m, r2)
	svc2 := eventLiveService(t, &eventHarness{m: h.m, room: r2})
	h.m.bindPlatformReferences(svc2)
	other := &larkHarness{t, h.m, r2, svc2, state.Members, h.c, h.dir, h.clock}
	id3, _ := enableSelfSend(t, other, other.members[0], 0)
	server := httptest.NewServer(h.m)
	defer server.Close()
	ids := []MemberIdentity{id1, id2, id3}
	services := []*service{h.svc, h.svc, svc2}
	type outcome struct {
		receipt referenceActionReceipt
		err     error
	}
	done := make(chan outcome, 9)
	for i, id := range ids {
		cli := contractCLI(t, server.URL, services[i], id)
		q := selfSendRequest(t, cli, "stable-intent")
		q.Input.Body = fmt.Sprintf("member %s", id.MembershipID)
		for range 3 {
			go func() {
				r, err := executeSelfSend(cli, q)
				done <- outcome{r, err}
			}()
		}
	}
	succeeded := 0
	for range 9 {
		select {
		case r := <-done:
			if r.err != nil || r.receipt.Status != "succeeded" && r.receipt.Status != "processing" {
				t.Fatal(r.receipt, r.err)
			}
			if r.receipt.Status == "succeeded" {
				succeeded++
			}
		case <-time.After(40 * time.Second):
			t.Fatal("shared account sends did not settle")
		}
	}
	if succeeded < 3 || len(selfSendPosts(t, h.dir)) != 3 {
		t.Fatal("same intent dispatched more than once", succeeded, selfSendPosts(t, h.dir))
	}
	for i, id := range ids {
		actions, err := services[i].store.queryActions(id, "stable-intent", "", "")
		if err != nil || len(actions) != 1 || actions[0].Status != "succeeded" || actions[0].Attempts != 1 {
			t.Fatal("member ledger lost isolated intent", actions, err)
		}
	}
}

func TestLarkSelfSendRevocationRaces(t *testing.T) {
	for _, mode := range []string{"disable", "unbind", "rotate", "account", "remove"} {
		t.Run(mode, func(t *testing.T) {
			h := selfSendHarness(t)
			id, bindings := enableSelfSend(t, h, h.members[0], 0)
			if mode == "disable" {
				h.start()
			}
			server := httptest.NewServer(h.m)
			defer server.Close()
			cli := contractCLI(t, server.URL, h.svc, id)
			q := selfSendRequest(t, cli, "inflight")
			path := filepath.Join(h.dir, "lark-cli")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			gate := `
 import time
 open(base+'/blocked','w').write('ready')
 end=time.monotonic()+20
 while not os.path.exists(base+'/release') and time.monotonic()<end:time.sleep(.01)
`
			b = []byte(strings.Replace(string(b), " print(json.dumps({'ok':True,'identity':'user','data':{'message_id':'om_receipt'}}))", gate+" print(json.dumps({'ok':True,'identity':'user','data':{'message_id':'om_receipt'}}))", 1))
			if err := os.WriteFile(path, b, 0700); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{}, 2)
			go func() { _, _ = executeSelfSend(cli, q); done <- struct{}{} }()
			waitSelfSendFile(t, filepath.Join(h.dir, "blocked"))
			if mode == "disable" {
				// The remote message is visible before POST has returned a
				// receipt. Exclusion relies on the native self relation alone.
				messages, err := os.ReadFile(filepath.Join(h.dir, "messages.json"))
				if err != nil || !strings.Contains(string(messages), "om_receipt") {
					t.Fatal("message not visible before receipt", err)
				}
				if records, err := h.svc.store.queryActions(id, q.ID, "", ""); err != nil || len(records) != 1 || records[0].Result != nil {
					t.Fatal("expected unsettled send intent", err)
				}
				h.clock.add(3 * time.Second)
				s := h.state()
				h.m.mu.Lock()
				runner := h.m.notifications.NewRunnerLocked(s.Metadata().Key)
				h.m.mu.Unlock()
				runner.Step(context.Background())
				if err := h.deliver(); err != nil {
					t.Fatal(err)
				}
				if inbox := inboxRoutingPreview(t, h.svc, id); inbox.SnapshotSeq != 0 {
					t.Fatal("message preceding receipt entered Inbox", inbox)
				}
			}
			second := q
			second.ID, second.Input.Body = "waiting", "another selected body"
			go func() { _, _ = executeSelfSend(cli, second); done <- struct{}{} }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				h.m.mu.Lock()
				n := len(h.m.platformCalls)
				h.m.mu.Unlock()
				if n >= 2 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("second call did not enter shared-account processing")
				}
				time.Sleep(10 * time.Millisecond)
			}
			switch mode {
			case "disable", "unbind":
				req := toolBindingChange{ServiceEpoch: h.m.epoch, RoomIdentity: id.RoomIdentity, Caller: humanCaller, Revision: bindings.Revision, Enabled: mode == "disable", Binding: tool.Binding{AgentID: id.AgentID, MembershipID: id.MembershipID, Tool: "lark"}}
				if req.Enabled {
					req.Binding.ConnectionID, req.ConnectionVersion = h.c.ID, h.c.Version
				}
				r := managerHTTP(h.m, "/v1/tools/bind", req)
				if r.Code != 200 {
					t.Fatal(r.Code, r.Body.String())
				}
			case "rotate", "account":
				if mode == "account" {
					b = []byte(strings.ReplaceAll(string(b), "ou_reader", "ou_replacement"))
					if err = os.WriteFile(path, b, 0700); err != nil {
						t.Fatal(err)
					}
				}
				// Verification invalidates owned calls before accepting either a
				// fresh connection version or another actual profile user.
				if _, err = h.m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: h.c.ID, Version: h.c.Version}); err != nil {
					t.Fatal(err)
				}
			case "remove":
				if _, err = h.svc.remove(apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("revocation left an active native call")
				}
			}
			if got := len(selfSendPosts(t, h.dir)); got != 1 {
				t.Fatal("revocation dispatched pending content", got)
			}
			second.ID = "after-revocation"
			if _, err := executeSelfSend(cli, second); err == nil {
				t.Fatal("revoked binding admitted new send")
			}
			if mode != "remove" {
				if r, err := executeSelfSend(cli, q); err != nil || r.Status != "unknown" {
					t.Fatal("lost ambiguous receipt after revocation", r, err)
				}
			}
		})
	}
}
