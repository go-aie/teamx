package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/go-aie/teamx/internal/inputstream"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnifiedMCPAuthenticationOutputAndTerminalBarrier(t *testing.T) {
	h, s := unifiedReaderFixture(t, 1)
	a, _, _ := accessFixture(t)
	token, revoke, err := a.issueInputBinding(s)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	args := map[string]any{"stream_ref": roomStreamRef(s.storage.id)}
	fault := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder(), write: func(b []byte) (int, error) { return len(b) - 1, io.ErrShortWrite }}
	simpleMCPRequest(t, a, token, "original", "inbox_read", args, fault)
	if h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal("short output consumed")
	}
	agent, _ := h.store.LoadAgent(s.storage.id)
	agent.Turn.Status = "completed"
	if err := h.store.SaveAgent(agent); !errors.Is(err, inputstream.ErrBusy) {
		t.Fatal("terminal barrier", err)
	}
	out := httptest.NewRecorder()
	result := simpleMCPRequest(t, a, token, "original", "inbox_read", args, out)
	if result == nil || result.IsError || h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal(out.Body.String())
	}
	// Full HTTP output alone is not a framework receipt.
	if err := s.receiveCall(h.ctx, "original"); err != nil {
		t.Fatal(err)
	}
	if h.list(t).Members[0].ReadThrough != 1 {
		t.Fatal("receipt did not settle")
	}
	if err := h.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	out = httptest.NewRecorder()
	simpleMCPRequest(t, a, token, "late", "inbox_read", args, out)
	if out.Code == 200 && !strings.Contains(out.Body.String(), "error") {
		t.Fatal("late old execution accepted")
	}
	if a.inputMCP[false] == nil || len(a.inputMCP[false].server.ListTools()) != 4 {
		t.Fatal("tools not composed")
	}
}
func TestUnifiedCLIProcessAdapterConfirmsOnlyFrameworkReceipt(t *testing.T) {
	h, s := unifiedReaderFixture(t, 1)
	svc := s.service
	svc.inboxMode = "cli"
	a, _, _ := accessFixture(t)
	svc.access = a
	token, revokeCLI, err := a.issueExecution(executionBinding{ServiceInfo: svc.info, Caller: agentCallerIdentity(s.storage.id)})
	if err != nil {
		t.Fatal(err)
	}
	defer revokeCLI()
	binding, revoke, err := a.issueInputBinding(s)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	server := httptest.NewServer(a.wrap(http.HandlerFunc(svc.serveInputCLI)))
	defer server.Close()
	a.mu.Lock()
	a.internal = server.URL
	a.mu.Unlock()
	hidden, _ := json.Marshal(map[string]string{"token": binding, "call": "framework-shell"})
	values := map[string]string{"TEAMX_EXECUTION_TOKEN": token, "TEAMX_ENDPOINT": server.URL, "TEAMX_ROOM_ID": h.id.RoomID, "TEAMX_ROOM_INSTANCE": h.id.RoomInstance, "TEAMX_SERVICE_EPOCH": svc.info.ServiceEpoch, "TEAMX_AGENT_ID": s.storage.id.AgentID, "TEAMX_MEMBERSHIP_ID": s.storage.id.MembershipID, "TEAMX_INPUT_CONTRACT": inputstream.Contract, "DSH_TEAMX_INPUT_BINDING": string(hidden)}
	env := func(k string) string { return values[k] }
	args := []string{"inbox", "read", "--stream", roomStreamRef(s.storage.id)}
	short := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder(), write: func(b []byte) (int, error) { return 0, io.ErrShortWrite }}
	if err := runCLIWithInput(h.ctx, args, strings.NewReader(""), short, io.Discard, env); err == nil {
		t.Fatal("stdout failure ignored")
	}
	if h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal("stdout failure confirmed")
	}
	var out bytes.Buffer
	if err := runCLIWithInput(context.Background(), args, strings.NewReader(""), &out, io.Discard, env); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("只读检查")) || h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal(out.String())
	}
	digest, err := inputBodyDigest(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.receiveGroupBodies(h.ctx, "framework-shell", []string{digest}); err != nil {
		t.Fatal(err)
	}
	if h.list(t).Members[0].ReadThrough != 1 {
		t.Fatal("framework receipt missing")
	}
	values["TEAMX_MEMBERSHIP_ID"] = "another-member"
	if err := runCLIWithInput(h.ctx, args, strings.NewReader(""), io.Discard, io.Discard, env); err == nil {
		t.Fatal("cross-member credential")
	}
	values["TEAMX_MEMBERSHIP_ID"] = s.storage.id.MembershipID
	revokeCLI()
	if err := runCLIWithInput(h.ctx, args, strings.NewReader(""), io.Discard, io.Discard, env); err == nil {
		t.Fatal("revoked credential")
	}
}
