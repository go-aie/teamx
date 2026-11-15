package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// This tool exists only in isolated test servers. It exercises real source
// association and the existing Inbox actor's Read/Confirm without registration
// or mode changes in any production member.
func addMCPOutputProbe(h *mcpHTTP, inbox *inboxHarness, beforeConfirm func(), afterConfirm func(error)) {
	h.addOutputTool(mcp.NewTool("output_probe", mcp.WithDescription("Isolated output confirmation acceptance"),
		mcp.WithNumber("turn_number", mcp.Required())),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, func(context.Context) error, func(), error) {
			var args struct {
				Turn uint64 `json:"turn_number"`
			}
			if err := req.BindArguments(&args); err != nil {
				return nil, nil, nil, err
			}
			if id, ok := ctx.Value(mcpIdentityKey{}).(mcpIdentity); ok && (id.RoomIdentity != inbox.id.RoomIdentity || id.Caller.ID != inbox.id.AgentID || id.Caller.MembershipID != inbox.id.MembershipID) {
				return nil, nil, nil, ErrIdentity
			}
			release, err := inbox.store.beginMCPRead(inbox.id, args.Turn)
			if err != nil {
				return nil, nil, nil, err
			}
			page, err := referenceTestService(inbox).readReferenceInbox(ctx, inbox.id, args.Turn, 1, referenceTestResolver)
			if err != nil {
				return nil, nil, release, err
			}
			data, err := json.Marshal(page)
			if err != nil {
				return nil, nil, release, err
			}
			confirm := func(ctx context.Context) error {
				if beforeConfirm != nil {
					beforeConfirm()
				}
				_, err := inbox.addr.Call[InboxStatus](ctx, ConfirmInboxBatch{MemberIdentity: inbox.id, TurnNumber: args.Turn, Receipt: page.Batch.Receipt})
				if afterConfirm != nil {
					afterConfirm(err)
				}
				return err
			}
			return mcp.NewToolResultText(string(data)), confirm, release, nil
		})
}

func mcpProbeInbox(t *testing.T) *inboxHarness {
	t.Helper()
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	receiveReference(t, h, "probe", referenceTestSource("probe"))
	inboxRunning(t, h, 1, 1)
	return h
}

func mcpProbeRequest(ctx context.Context) *http.Request {
	r := httptest.NewRequest("POST", "http://127.0.0.1:8088/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":"output-1","method":"tools/call","params":{"name":"output_probe","arguments":{"turn_number":1}}}`)).WithContext(ctx)
	r.Header.Set("Accept", "application/json, text/event-stream")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("MCP-Protocol-Version", "2025-11-25")
	r.RemoteAddr = "127.0.0.1:45000"
	return r
}

type mcpFaultWriter struct {
	*httptest.ResponseRecorder
	write func([]byte) (int, error)
	flush func() error
}

func (w *mcpFaultWriter) Write(data []byte) (int, error) {
	if w.write != nil {
		return w.write(data)
	}
	return w.ResponseRecorder.Write(data)
}
func (w *mcpFaultWriter) FlushError() error {
	if w.flush != nil {
		return w.flush()
	}
	w.ResponseRecorder.Flush()
	return nil
}

func TestMCPOutputConfirmationRequiresCompleteResponse(t *testing.T) {
	for _, mode := range []string{"success", "short write", "disconnect", "flush failure", "cancel before output", "cancel during write", "cancel during flush"} {
		t.Run(mode, func(t *testing.T) {
			inbox := mcpProbeInbox(t)
			ctx, cancel := context.WithCancel(inbox.ctx)
			defer cancel()
			h := newMCPHTTP()
			var confirms atomic.Int32
			addMCPOutputProbe(h, inbox, nil, func(err error) {
				if err != nil {
					t.Error(err)
				}
				confirms.Add(1)
			})
			w := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder()}
			switch mode {
			case "short write":
				w.write = func(p []byte) (int, error) { return len(p) - 1, nil }
			case "disconnect":
				w.write = func([]byte) (int, error) { return 0, io.ErrClosedPipe }
			case "flush failure":
				w.flush = func() error { return io.ErrClosedPipe }
			case "cancel before output":
				cancel()
			case "cancel during write":
				w.write = func(p []byte) (int, error) { cancel(); return len(p), nil }
			case "cancel during flush":
				w.flush = func() error { cancel(); return nil }
			}
			h.ServeHTTP(w, mcpProbeRequest(ctx))
			status := inboxCall[InboxStatus](t, inbox, InspectInbox{MemberIdentity: inbox.id})
			if mode == "success" {
				if confirms.Load() != 1 || status.ConsumedThrough != 1 || !w.Flushed || w.Result().ContentLength != int64(w.Body.Len()) {
					t.Fatalf("success did not confirm after flush: calls=%d status=%+v body=%s", confirms.Load(), status, w.Body.String())
				}
				cancel()
				if got := inboxCall[InboxStatus](t, inbox, InspectInbox{MemberIdentity: inbox.id}); got.ConsumedThrough != 1 {
					t.Fatal("cancel rolled back consumption")
				}
			} else if confirms.Load() != 0 || status.ConsumedThrough != 0 {
				t.Fatalf("failed output consumed: calls=%d status=%+v", confirms.Load(), status)
			}
		})
	}
}

func TestMCPTurnFinalizationWaitsForOutputConfirmation(t *testing.T) {
	inbox := mcpProbeInbox(t)
	h := newMCPHTTP()
	entered, unblock, confirmed, replied := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	addMCPOutputProbe(h, inbox, func() { close(entered); <-unblock }, func(err error) {
		if err != nil {
			t.Error(err)
		}
		close(confirmed)
	})
	go func() { defer close(replied); h.ServeHTTP(httptest.NewRecorder(), mcpProbeRequest(inbox.ctx)) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmation did not start")
	}
	finished := make(chan error, 1)
	go func() {
		inbox.store.finishMCPTurn(inbox.id, 1)
		state, err := inbox.store.LoadAgent(inbox.id)
		if err == nil {
			state.Turn.Status = "completed"
			err = inbox.store.SaveAgent(state)
		}
		finished <- err
	}()
	// Wait for admission to close; a newly arriving read cannot enter the turn.
	deadline := time.Now().Add(3 * time.Second)
	for {
		release, err := inbox.store.beginMCPRead(inbox.id, 1)
		if errors.Is(err, ErrClosing) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		release()
		if time.Now().After(deadline) {
			t.Fatal("turn did not close admission")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-finished:
		t.Fatalf("turn finalized before confirm: %v", err)
	default:
	}
	state, err := inbox.store.LoadAgent(inbox.id)
	if err != nil || state.Turn.Status != "running" {
		t.Fatal("confirm lost running turn", state, err)
	}
	close(unblock)
	select {
	case <-replied:
	case <-time.After(5 * time.Second):
		t.Fatal("reply did not settle")
	}
	<-confirmed
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if got := inboxCall[InboxStatus](t, inbox, InspectInbox{MemberIdentity: inbox.id}); got.ConsumedThrough != 1 {
		t.Fatal(got)
	}
	if _, err := inbox.store.beginMCPRead(inbox.id, 1); err == nil {
		t.Fatal("completed turn admitted")
	}
	inboxRunning(t, inbox, 2, 1)
	if _, err := inbox.store.beginMCPRead(inbox.id, 1); err == nil {
		t.Fatal("late request entered new turn")
	}
	release, err := inbox.store.beginMCPRead(inbox.id, 2)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func issueMCPProbeCredential(t *testing.T, a *serverAccess, inbox *inboxHarness) (string, func()) {
	t.Helper()
	path, err := inbox.store.dshPath(inbox.id, "session.json", true)
	if err != nil {
		t.Fatal(err)
	}
	ref := dshReference{Version: formatVersion, MemberIdentity: inbox.id, Directory: filepath.Base(dshStateDir(path))}
	if err := inbox.store.save(path, ref, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	binding := executionBinding{ServiceInfo: ServiceInfo{RoomIdentity: inbox.id.RoomIdentity, ServiceEpoch: "isolated-epoch"}, Caller: Caller{Author: Author{Kind: "agent", ID: inbox.id.AgentID}, MembershipID: inbox.id.MembershipID}}
	token, revoke, err := a.issueMCPExecution(binding, inbox.store, ref.Directory)
	if err != nil {
		t.Fatal(err)
	}
	return token, revoke
}

func TestMCPStandardHTTPClientAndCredentialIsolation(t *testing.T) {
	inbox := mcpProbeInbox(t)
	a, admin, _ := accessFixture(t)
	var confirms atomic.Int32
	addMCPOutputProbe(a.mcp, inbox, nil, func(err error) {
		if err != nil {
			t.Error(err)
		}
		confirms.Add(1)
	})
	token, revoke := issueMCPProbeCredential(t, a, inbox)
	defer revoke()
	server := httptest.NewServer(a.wrap(http.NotFoundHandler()))
	defer server.Close()
	a.internal, a.public = server.URL, server.URL
	c, err := client.NewStreamableHttpClient(server.URL+"/mcp", transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(inbox.ctx, 5*time.Second)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	init, err := c.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{ProtocolVersion: "2025-11-25", ClientInfo: mcp.Implementation{Name: "teamx-test", Version: "1"}}})
	if err != nil || init.ProtocolVersion != "2025-11-25" {
		t.Fatal(init, err)
	}
	list, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil || len(list.Tools) != 2 {
		t.Fatal(list, err)
	}
	result, err := c.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "output_probe", Arguments: map[string]any{"turn_number": 1}}})
	if err != nil || result.IsError || len(result.Content) != 1 {
		t.Fatal(result, err)
	}
	// Client receipt can precede the server's confirmation callback.
	deadline := time.Now().Add(3 * time.Second)
	for confirms.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if confirms.Load() != 1 {
		t.Fatal("standard client response not confirmed")
	}
	cliToken, release, err := a.issueExecution(executionBinding{})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	request := func(path, credential string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", server.URL+path, strings.NewReader(`{}`))
		r.RemoteAddr = "127.0.0.1:45000"
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		a.wrap(http.NotFoundHandler()).ServeHTTP(w, r)
		return w
	}
	for _, other := range []string{"", admin, cliToken} {
		if w := request("/mcp", other); w.Code != 401 {
			t.Fatal("non-MCP credential accepted", w.Code)
		}
	}
	if w := request("/v1/send", token); w.Code != 401 {
		t.Fatal("MCP credential authorized CLI", w.Code)
	}
	// Generation replacement and member removal are checked on every request.
	path, _ := inbox.store.dshPath(inbox.id, "session.json", true)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	r := mcpProbeRequest(inbox.ctx)
	r.Host = strings.TrimPrefix(server.URL, "http://")
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	a.wrap(http.NotFoundHandler()).ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("missing generation accepted", w.Code)
	}
	revoke()
	w = httptest.NewRecorder()
	a.wrap(http.NotFoundHandler()).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("revoked credential accepted", w.Code)
	}
}

func TestMCPProductionHasNoAcceptanceTools(t *testing.T) {
	h := newMCPHTTP()
	r := mcpProbeRequest(context.Background())
	r.Body = io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var response struct {
		Result struct {
			Tools []mcp.Tool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != 200 || len(response.Result.Tools) != 1 || response.Result.Tools[0].Name != "inbox_read" {
		t.Fatal(w.Code, w.Body.String(), err)
	}
}

func TestMCPOutputConfirmationSurvivesLaterCancellation(t *testing.T) {
	inbox := mcpProbeInbox(t)
	ctx, cancel := context.WithCancel(inbox.ctx)
	defer cancel()
	h := newMCPHTTP()
	var confirms int
	addMCPOutputProbe(h, inbox, cancel, func(err error) {
		if err != nil {
			t.Error(err)
		}
		confirms++
	})
	h.ServeHTTP(httptest.NewRecorder(), mcpProbeRequest(ctx))
	if got := inboxCall[InboxStatus](t, inbox, InspectInbox{MemberIdentity: inbox.id}); confirms != 1 || got.ConsumedThrough != 1 {
		t.Fatal("output-complete confirmation canceled", confirms, got)
	}
}

func TestMCPActualHTTPDisconnectDoesNotConfirm(t *testing.T) {
	h := newMCPHTTP()
	entered, unblock, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var confirms atomic.Int32
	h.addOutputTool(mcp.NewTool("output_probe"), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, func(context.Context) error, func(), error) {
		close(entered)
		// Synchronize on a disconnect observed by net/http. A successful local
		// write alone cannot prove that a peer received the response.
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Error("net/http did not observe the TCP disconnect")
			return nil, nil, nil, errors.New("disconnect not observed")
		}
		<-unblock
		return mcp.NewToolResultText("disconnected response"), func(context.Context) error { confirms.Add(1); return nil }, nil, nil
	})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(returned); h.ServeHTTP(w, r) }))
	defer s.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(s.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request := mcpProbeRequest(context.Background())
	request.Host = strings.TrimPrefix(s.URL, "http://")
	if err := request.Write(conn); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not enter")
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
	close(unblock)
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect did not settle")
	}
	if confirms.Load() != 0 {
		t.Fatal("disconnected HTTP response confirmed")
	}
}

func TestMCPMemberTerminalCheckpointDrainsOperations(t *testing.T) {
	for _, outcome := range []string{"completed", "interrupted"} {
		t.Run(outcome, func(t *testing.T) {
			backend := newControlledBackend()
			h, addr, id := memberHarness(t, backend, nil)
			input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
			controlMember(t, h, addr, id, "run", input, false, false)
			awaitTurn(t, h, backend)
			release, err := h.store.beginMCPRead(id, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if outcome == "completed" {
				backend.finish <- nil
			} else {
				controlMember(t, h, addr, id, "pause", UnreadChanged{}, false, true)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				h.store.mcpOperations.mu.Lock()
				closing := h.store.mcpOperations.turns[id].closing
				h.store.mcpOperations.mu.Unlock()
				if closing {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("member did not close operation admission")
				}
				time.Sleep(time.Millisecond)
			}
			state, err := h.store.LoadAgent(id)
			if err != nil || state.Turn.Status != "running" {
				t.Fatal("member checkpoint overtook operation", state, err)
			}
			release()
			status := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.Turn != nil && s.Agent.Turn.Status == outcome })
			if status.Agent.Turn.Status != outcome {
				t.Fatal(status)
			}
		})
	}
}

func TestMCPMultimodalResponseAndFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"images", "image short write", "tool error", "RPC error", "oversized", "serialization error"} {
		t.Run(mode, func(t *testing.T) {
			h := newMCPHTTP()
			var confirms int
			h.addOutputTool(mcp.NewTool("output_probe"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, func(context.Context) error, func(), error) {
				result := &mcp.CallToolResult{Content: []mcp.Content{
					mcp.NewTextContent("third-party material"), mcp.NewImageContent("aW1hZ2Ux", "image/png"), mcp.NewImageContent("aW1hZ2Uy", "image/jpeg"),
				}}
				var err error
				switch mode {
				case "tool error":
					result = mcp.NewToolResultError("isolated error")
				case "RPC error":
					err = errors.New("isolated error")
				case "oversized":
					result = mcp.NewToolResultText(strings.Repeat("x", maxMCPResponseBytes))
				case "serialization error":
					result.StructuredContent = make(chan int)
				}
				return result, func(context.Context) error { confirms++; return nil }, nil, err
			})
			w := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder()}
			if mode == "image short write" {
				w.write = func(p []byte) (int, error) { return len(p) - 1, nil }
			}
			h.ServeHTTP(w, mcpProbeRequest(context.Background()))
			if mode == "images" {
				var response struct {
					Result mcp.CallToolResult `json:"result"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || confirms != 1 || len(response.Result.Content) != 3 {
					t.Fatal("multimodal result incomplete", confirms, err)
				}
				if image, ok := response.Result.Content[1].(mcp.ImageContent); !ok || image.Data != "aW1hZ2Ux" || image.MIMEType != "image/png" {
					t.Fatal("image was not top-level MCP content")
				}
			} else if confirms != 0 {
				t.Fatal("invalid or incomplete response confirmed", confirms)
			}
		})
	}
}

func TestMCPAuthenticationRejectsRetiredIdentity(t *testing.T) {
	for _, change := range []string{"generation", "membership", "restart"} {
		t.Run(change, func(t *testing.T) {
			inbox := mcpProbeInbox(t)
			a, _, _ := accessFixture(t)
			token, revoke := issueMCPProbeCredential(t, a, inbox)
			defer revoke()
			switch change {
			case "generation":
				path, err := inbox.store.dshPath(inbox.id, "session.json", true)
				if err != nil {
					t.Fatal(err)
				}
				ref := dshReference{Version: formatVersion, MemberIdentity: inbox.id, Directory: filepath.Base(dshStateDir(path)) + "-" + strings.Repeat("b", 32)}
				if err := inbox.store.save(path, ref, maxPrivateFileBytes); err != nil {
					t.Fatal(err)
				}
			case "membership":
				state, err := inbox.store.LoadRoom()
				if err != nil {
					t.Fatal(err)
				}
				var members []MemberState
				for _, member := range state.Members {
					if member.MembershipID != inbox.id.MembershipID {
						members = append(members, member)
					}
				}
				state.Members = members
				if err := inbox.store.SaveRoom(state); err != nil {
					t.Fatal(err)
				}
			case "restart":
				fresh, _, _ := accessFixture(t)
				a = fresh
			}
			w := accessRequest(a.wrap(http.NotFoundHandler()), "POST", "/mcp", token, "", `{}`, nil)
			if w.Code != 403 && w.Code != 401 {
				t.Fatal("retired identity accepted", w.Code)
			}
		})
	}
}

func TestMCPRetainsHostOriginAndCredentialChecks(t *testing.T) {
	inbox := mcpProbeInbox(t)
	a, admin, handler := accessFixture(t)
	token, revoke := issueMCPProbeCredential(t, a, inbox)
	defer revoke()
	cookie := accessLogin(t, handler, admin)
	if w := accessRequest(handler, "POST", "/mcp", "", "http://127.0.0.1:8088", `{}`, cookie); w.Code != 401 {
		t.Fatal("browser session authorized MCP", w.Code)
	}
	if w := accessRequest(handler, "POST", "/mcp", token, "https://evil.invalid", `{}`, nil); w.Code != 403 {
		t.Fatal("cross-origin MCP accepted", w.Code)
	}
	r := mcpProbeRequest(inbox.ctx)
	r.Host = "evil.invalid"
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("untrusted Host accepted", w.Code)
	}
	a.disabled = true
	if w := accessRequest(handler, "POST", "/mcp", "", "", `{}`, nil); w.Code != 401 {
		t.Fatal("disabled browser login bypassed MCP credentials", w.Code)
	}
}

type mcpDeadlineWriter struct {
	*mcpFaultWriter
	deadline func(time.Time) error
}

func (w *mcpDeadlineWriter) SetWriteDeadline(at time.Time) error { return w.deadline(at) }

func TestMCPOutputDeadlineBoundsAndFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "bounded output", true: "deadline failure"}[failed], func(t *testing.T) {
			inbox := mcpProbeInbox(t)
			h := newMCPHTTP()
			var confirms int
			addMCPOutputProbe(h, inbox, nil, func(err error) {
				if err != nil {
					t.Error(err)
				}
				confirms++
			})
			var deadlines []time.Time
			w := &mcpDeadlineWriter{mcpFaultWriter: &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder()}, deadline: func(at time.Time) error {
				deadlines = append(deadlines, at)
				if failed {
					return io.ErrClosedPipe
				}
				return nil
			}}
			before := time.Now()
			h.ServeHTTP(w, mcpProbeRequest(inbox.ctx))
			if len(deadlines) == 0 || deadlines[0].Before(before.Add(14*time.Second)) || deadlines[0].After(time.Now().Add(16*time.Second)) {
				t.Fatal("missing bounded HTTP output deadline", deadlines)
			}
			if failed {
				if confirms != 0 {
					t.Fatal("deadline failure confirmed output")
				}
			} else if confirms != 1 || len(deadlines) != 2 || !deadlines[1].IsZero() {
				t.Fatal("successful output did not confirm/reset deadline", confirms, deadlines)
			}
		})
	}
}
