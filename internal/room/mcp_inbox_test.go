package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/attachment"
	"github.com/mark3labs/mcp-go/mcp"
)

func issueInboxCredential(t *testing.T, a *serverAccess, svc *service, id MemberIdentity, images bool) (string, func()) {
	t.Helper()
	path, err := svc.store.dshPath(id, "session.json", true)
	if err != nil {
		t.Fatal(err)
	}
	ref := sessionReference{Version: formatVersion, MemberIdentity: id, Directory: filepath.Base(dshStateDir(path))}
	if err := svc.store.save(path, ref, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	token, revoke, err := a.issueMCPInbox(executionBinding{ServiceInfo: svc.info, Caller: agentCallerIdentity(id)}, svc, ref.Directory, images)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revoke)
	return token, revoke
}

func mcpInboxFixture(t *testing.T) (*inboxHarness, *service, *serverAccess, string) {
	t.Helper()
	return mcpInboxFixtureWithTimeout(t, 10*time.Second)
}

func mcpInboxFixtureWithTimeout(t *testing.T, timeout time.Duration) (*inboxHarness, *service, *serverAccess, string) {
	t.Helper()
	h := newInboxHarnessWithTimeout(t, InboxLimits{}, timeout)
	initReferenceTest(t, h)
	svc := referenceTestService(h)
	svc.info.ServiceEpoch = "isolated-epoch"
	svc.referenceResolver = referenceTestResolver
	a, _, _ := accessFixture(t)
	token, _ := issueInboxCredential(t, a, svc, h.id, true)
	return h, svc, a, token
}

func callInbox(t *testing.T, a *serverAccess, token string, args any, w http.ResponseWriter) (*mcp.CallToolResult, string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "inbox-request", "method": "tools/call", "params": map[string]any{"name": "inbox_read", "arguments": args}})
	if err != nil {
		t.Fatal(err)
	}
	req := mcpProbeRequest(context.Background())
	req.Body = io.NopCloser(bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+token)
	if w == nil {
		w = httptest.NewRecorder()
	}
	a.wrap(http.NotFoundHandler()).ServeHTTP(w, req)
	var recorder *httptest.ResponseRecorder
	switch v := w.(type) {
	case *httptest.ResponseRecorder:
		recorder = v
	case *mcpFaultWriter:
		recorder = v.ResponseRecorder
	}
	if recorder == nil {
		return nil, ""
	}
	var response struct {
		Result *mcp.CallToolResult `json:"result"`
		Error  any                 `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		return nil, recorder.Body.String()
	}
	if response.Result == nil || response.Result.IsError {
		return nil, recorder.Body.String()
	}
	return response.Result, ""
}
func inboxToolView(t *testing.T, result *mcp.CallToolResult) mcpInboxView {
	t.Helper()
	if result == nil || len(result.Content) == 0 {
		t.Fatal("no Inbox result")
	}
	data, _ := json.Marshal(result.Content[0])
	var block struct {
		Text string `json:"text"`
	}
	var view mcpInboxView
	if json.Unmarshal(data, &block) != nil || json.Unmarshal([]byte(block.Text), &view) != nil {
		t.Fatal(string(data))
	}
	if bytes.Contains(data, []byte("receipt")) || bytes.Contains(data, []byte("access_token")) || result.StructuredContent != nil {
		t.Fatal("internal data exposed", string(data))
	}
	return view
}
func mustInboxCall(t *testing.T, a *serverAccess, token string, turn int, cursor string, limit int) (*mcp.CallToolResult, mcpInboxView) {
	t.Helper()
	result, err := callInbox(t, a, token, map[string]any{"turn_number": turn, "cursor": cursor, "limit": limit}, nil)
	if err != "" {
		t.Fatal(err)
	}
	return result, inboxToolView(t, result)
}

func TestMCPInboxFixedPaginationRetriesAndBusinessFailure(t *testing.T) {
	h, svc, a, token := mcpInboxFixture(t)
	for n := 1; n <= 3; n++ {
		receiveReference(t, h, fmt.Sprint(n), referenceTestSource(fmt.Sprint(n)))
	}
	inboxRunning(t, h, 1, 3)
	receiveReference(t, h, "later", referenceTestSource("later"))
	first, v := mustInboxCall(t, a, token, 1, "", 1)
	if len(v.Entries) != 1 || v.Entries[0].Seq != 1 || !v.HasMore || v.Next == "" || v.Snapshot != 3 {
		t.Fatal(v)
	}
	firstBytes, _ := json.Marshal(first)
	for _, limit := range []int{1, 25} {
		retry, _ := mustInboxCall(t, a, token, 1, "", limit)
		data, _ := json.Marshal(retry)
		if !bytes.Equal(data, firstBytes) {
			t.Fatal("retry changed page")
		}
	}
	_, second := mustInboxCall(t, a, token, 1, v.Next, 1)
	if second.Entries[0].Seq != 2 || second.PageAfter != 1 || second.Through != 2 {
		t.Fatal(second)
	}
	// Even after another receipt has been issued, replaying the first page must
	// not reconfirm its old receipt against the actor's second page.
	mustInboxCall(t, a, token, 1, "", 1)
	_, third := mustInboxCall(t, a, token, 1, second.Next, 1)
	if third.HasMore || third.Entries[0].Seq != 3 {
		t.Fatal(third)
	}
	if got := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); got.ConsumedThrough != 3 {
		t.Fatal(got)
	}
	h.store.finishMCPTurn(h.id, 1)
	state, _ := h.store.LoadAgent(h.id)
	state.Turn.Status = "failed"
	if err := h.store.SaveAgent(state); err != nil {
		t.Fatal(err)
	}
	if got := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); got.ConsumedThrough != 3 {
		t.Fatal("business failure rolled back input", got)
	}
	referenceRunning(t, h, 2, 3, 4)
	if _, err := callInbox(t, a, token, map[string]any{"turn_number": 2, "cursor": v.Next}, nil); err == "" {
		t.Fatal("cross-turn cursor accepted")
	}
	_, next := mustInboxCall(t, a, token, 2, "", 25)
	if len(next.Entries) != 1 || next.Entries[0].Seq != 4 {
		t.Fatal(next)
	}
	if _, err := svc.memberInbox(h.ctx, apiRequest{Caller: agentCallerIdentity(h.id), TurnNumber: 2}, "read"); err == nil || !strings.Contains(err.Error(), "inbox_read") {
		t.Fatal("mixed CLI read accepted", err)
	}
	if _, err := svc.memberInbox(h.ctx, apiRequest{Caller: agentCallerIdentity(h.id), TurnNumber: 2}, "confirm"); err == nil {
		t.Fatal("mixed CLI confirm accepted")
	}
}

func TestMCPInboxShortOutputConcurrencyAndReceiptReuse(t *testing.T) {
	h, svc, a, token := mcpInboxFixture(t)
	receiveReference(t, h, "one", referenceTestSource("one"))
	receiveReference(t, h, "two", referenceTestSource("two"))
	inboxRunning(t, h, 1, 2)
	short := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder(), write: func(p []byte) (int, error) { return len(p) - 1, nil }}
	callInbox(t, a, token, map[string]any{"turn_number": 1, "limit": 1}, short)
	before := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if before.ConsumedThrough != 0 || before.Batch == nil {
		t.Fatal(before)
	}
	session := svc.store.mcpOperations.inboxes[h.id]
	page := session.pages[""]
	if _, err := callInbox(t, a, token, map[string]any{"turn_number": 1, "cursor": page.next}, nil); err == "" {
		t.Fatal("advanced before complete output")
	}
	entered, unblock := make(chan struct{}), make(chan struct{})
	w := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder(), flush: func() error { close(entered); <-unblock; return nil }}
	done := make(chan struct{})
	go func() { defer close(done); callInbox(t, a, token, map[string]any{"turn_number": 1, "limit": 25}, w) }()
	<-entered
	if _, err := callInbox(t, a, token, map[string]any{"turn_number": 1}, nil); err == "" || !strings.Contains(err, "pending") {
		t.Error("concurrent call not busy", err)
	}
	close(unblock)
	<-done
	after := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if after.ConsumedThrough != 1 || after.Batch.Receipt != before.Batch.Receipt {
		t.Fatal("retry skipped or changed receipt", before, after)
	}
}

func TestMCPInboxConfirmFailureKeepsReceiptAndBlocksNewPages(t *testing.T) {
	h, svc, a, token := mcpInboxFixture(t)
	for _, key := range []string{"one", "two"} {
		receiveReference(t, h, key, referenceTestSource(key))
	}
	inboxRunning(t, h, 1, 2)
	original := h.store.ops.createTemp
	var fail atomic.Bool
	// Switch on only at output flush, after linkage and receipt persistence.
	h.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		if fail.Load() && strings.Contains(dir, string(filepath.Separator)+"inboxes"+string(filepath.Separator)) {
			return nil, os.ErrPermission
		}
		return original(dir, pattern)
	}
	t.Cleanup(func() { h.store.ops.createTemp = original })
	w := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder(), flush: func() error { fail.Store(true); return nil }}
	result, err := callInbox(t, a, token, map[string]any{"turn_number": 1, "limit": 1}, w)
	if err != "" {
		t.Fatal(err)
	}
	v := inboxToolView(t, result)
	status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	receipt := status.Batch.Receipt
	if status.ConsumedThrough != 0 || svc.store.mcpOperations.inboxes[h.id].confirmationError() == nil {
		t.Fatal("confirmation failure hidden", status)
	}
	if _, err := callInbox(t, a, token, map[string]any{"turn_number": 1, "cursor": v.Next}, nil); err == "" || !strings.Contains(err, "confirmation pending") {
		t.Fatal("new page issued after failed confirm", err)
	}
	if current := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); current.Batch.Receipt != receipt {
		t.Fatal("pending receipt replaced")
	}
	fail.Store(false)
	mustInboxCall(t, a, token, 1, "", 1)
	_, next := mustInboxCall(t, a, token, 1, v.Next, 1)
	if next.Entries[0].Seq != 2 {
		t.Fatal(next)
	}
}

func TestMCPInboxArgumentsEmptyAndIdentity(t *testing.T) {
	h, _, a, token := mcpInboxFixture(t)
	roomCall[PostReceipt](t, h.roomHarness, h.roomHarness.post(humanCaller, "room-only", "room input"))
	inboxRoomRunning(t, h, 1, 1)
	for _, args := range []any{map[string]any{"turn_number": 1, "agent_id": "B"}, map[string]any{"turn_number": 2}} {
		if _, err := callInbox(t, a, token, args, nil); err == "" {
			t.Fatal("invalid args accepted", args)
		}
	}
	_, view := mustInboxCall(t, a, token, 1, "", 25)
	if len(view.Entries) != 0 || view.EmptyReason == "" || view.HasMore {
		t.Fatal(view)
	}
	if status := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); status.Batch != nil {
		t.Fatal("empty page created receipt", status)
	}
}

func TestMCPInboxTurnImageBudgetAndDeduplication(t *testing.T) {
	for _, textOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(textOnly), func(t *testing.T) {
			h, svc, a, token := mcpInboxFixture(t)
			svc.store.mcpOperations.inboxes[h.id].images = !textOnly
			svc.imageMaterials = imageMaterialConfig{MaxImages: 2}
			var paths []string
			svc.materialAttachments = &materialAttachments{
				download: func(_ context.Context, req attachmentRequest, _ *attachmentReader, _ attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
					paths = append(paths, req.Path)
					if strings.HasSuffix(req.Path, "missing.png") {
						return nil, "", nil, attachment.ErrUnavailable
					}
					return []byte("image-bytes"), "image/png", &attachmentAccess{}, nil
				}, check: func(context.Context, ServiceInfo, attachmentReader, TurnRecord, []materialNotice, []*attachmentAccess) error {
					return nil
				},
			}
			base := "/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/"
			for n, body := range []string{"plain notification", "![one](" + base + "one.png) ![repeat](" + base + "one.png) ![two](" + base + "two.png)", "![shared](" + base + "one.png) ![third](" + base + "three.png)"} {
				q := h.receive(fmt.Sprint(n))
				q.Item.Source = referenceTestSource(fmt.Sprint(n))
				q.Item.Text = body
				inboxCall[InboxReceipt](t, h, q)
			}
			inboxRunning(t, h, 1, 3)
			_, plain := mustInboxCall(t, a, token, 1, "", 1)
			result, multi := mustInboxCall(t, a, token, 1, plain.Next, 1)
			if len(multi.Images) != 2 || len(multi.Images[0].Positions) != 2 {
				t.Fatal(multi.Images)
			}
			if !textOnly && (len(result.Content) != 5 || multi.Images[0].Status != "supplied") {
				t.Fatal(result, multi)
			}
			_, last := mustInboxCall(t, a, token, 1, multi.Next, 1)
			if !textOnly && (len(paths) != 2 || last.Images[0].Shared == "" || last.Images[1].Status != "omitted") {
				t.Fatal(paths, last)
			}
			if textOnly && (len(paths) != 0 || multi.Images[0].Reason != "current model is text-only") {
				t.Fatal(paths, multi)
			}
		})
	}
}

func TestMCPInboxCloseCancelsAndDrainsBeforeCLIReentry(t *testing.T) {
	h, svc, a, token := mcpInboxFixture(t)
	receiveReference(t, h, "one", referenceTestSource("one"))
	inboxRunning(t, h, 1, 1)
	entered, unblock := make(chan struct{}), make(chan struct{})
	w := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder(), flush: func() error { close(entered); <-unblock; return nil }}
	done := make(chan struct{})
	go func() { defer close(done); callInbox(t, a, token, map[string]any{"turn_number": 1}, w) }()
	<-entered
	a.mu.Lock()
	var execution *mcpExecution
	for _, e := range a.mcpExecutions {
		execution = e
	}
	a.mu.Unlock()
	closed := make(chan struct{})
	go func() { defer close(closed); execution.inbox.close() }()
	// close stops admission but waits for the response operation to release.
	select {
	case <-closed:
		t.Fatal("closed before drain")
	case <-time.After(20 * time.Millisecond):
	}
	close(unblock)
	<-done
	<-closed
	if _, err := svc.memberInbox(h.ctx, apiRequest{Caller: agentCallerIdentity(h.id), TurnNumber: 1}, "read"); err == nil {
		t.Fatal("same turn changed mode on close")
	}
	roomCall[PostReceipt](t, h.roomHarness, h.roomHarness.post(humanCaller, "room-only", "room input"))
	inboxRoomRunning(t, h, 2, 1)
	if _, err := svc.memberInbox(h.ctx, apiRequest{Caller: agentCallerIdentity(h.id), TurnNumber: 2}, "read"); err != nil {
		t.Fatal("explicit CLI return on new turn failed", err)
	}
}

func TestMCPInboxDSHInjectsAndRevokesOnNewResumeAndFailedOpen(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	svc := referenceTestService(h)
	svc.info.ServiceEpoch = "epoch-test"
	a, _, _ := accessFixture(t)
	opts := adapterOptions(h.store, h.id)
	opts.CLIEnv = append(opts.CLIEnv, "TEAMX_SERVICE_EPOCH=epoch-test")
	// adapterOptions already supplies a service epoch; replace rather than duplicate.
	for i, entry := range opts.CLIEnv {
		if strings.HasPrefix(entry, "TEAMX_SERVICE_EPOCH=") {
			opts.CLIEnv[i] = "TEAMX_SERVICE_EPOCH=epoch-test"
		}
	}
	var tokens []string
	extra := dsh.MCPHTTPServer{Type: "http", Name: "sandbox", URL: "http://127.0.0.1:18080/mcp", Headers: []dsh.MCPHeader{{Name: "Authorization", Value: "Bearer independent-sandbox-key"}}}
	b := dshBackend{store: h.store, access: a, inboxMCP: svc, agentMCP: agentMCPBindings{h.id: {extra}}, open: func(_ context.Context, options dsh.Options) (dshComponent, error) {
		if len(options.MCPServers) != 2 || options.MCPServers[0].Name != "teamx" || options.MCPServers[1].Name != "sandbox" || options.MCPServers[1].Headers[0].Value != extra.Headers[0].Value || !strings.Contains(options.Instructions, mcpInboxContract) || strings.Contains(options.Instructions, "inbox read") {
			t.Error("missing MCP session identity/guidance")
		}
		tokens = append(tokens, options.MCPServers[0].Headers[0].Value)
		return &adapterComponent{}, nil
	}}
	for _, resume := range []bool{false, true} {
		b.recover = resume
		session, err := b.OpenSession(h.ctx, opts)
		if err != nil {
			t.Fatal(err)
		}
		if len(a.mcpExecutions) != 1 {
			t.Fatal("no MCP credential")
		}
		if err := session.Close(h.ctx); err != nil {
			t.Fatal(err)
		}
		if len(a.mcpExecutions) != 0 || len(a.executions) != 0 {
			t.Fatal("credential survived close")
		}
	}
	if tokens[0] == tokens[1] {
		t.Fatal("resume reused credential")
	}
	b.open = func(context.Context, dsh.Options) (dshComponent, error) {
		return nil, errors.New("offline failed open")
	}
	if _, err := b.OpenSession(h.ctx, opts); err == nil || len(a.mcpExecutions) != 0 || len(a.executions) != 0 {
		t.Fatal("failed open leaked authority", err)
	}
}

func TestMCPInboxExcludedPageStillConfirmsAndInternalEventLinks(t *testing.T) {
	h, svc, a, token := mcpInboxFixture(t)
	h.store.notificationAdapters = contractNotificationCapabilities
	source := contractSource("quiet", "quiet")
	receiveReference(t, h, source.ItemKey, source)
	svc.resolveInboxAdmission = func(_ context.Context, s platform.SourceRef) (json.RawMessage, error) {
		return json.Marshal(struct{ Account, Object platform.ExternalID }{s.Reference.RecipientID, s.Reference.Object.ID})
	}
	receiveTestTaskEvent(t, h, taskEventFixture("mcp-internal"))
	inboxRunning(t, h, 1, 2)
	_, excluded := mustInboxCall(t, a, token, 1, "", 1)
	if len(excluded.Entries) != 0 || excluded.Through != 1 || excluded.EmptyReason == "" || !excluded.HasMore {
		t.Fatal("excluded page lost range", excluded)
	}
	if status := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); status.ConsumedThrough != 1 {
		t.Fatal("excluded page did not confirm", status)
	}
	_, internal := mustInboxCall(t, a, token, 1, excluded.Next, 1)
	if len(internal.Entries) != 1 || internal.Through != 2 {
		t.Fatal(internal)
	}
	if status := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); status.ConsumedThrough != 2 {
		t.Fatal("internal event did not link/confirm", status)
	}
}

func TestMCPInboxUnconfirmedRestartReissuesOnlyOnNewRunningTurn(t *testing.T) {
	h, svc, a, token := mcpInboxFixture(t)
	receiveReference(t, h, "one", referenceTestSource("one"))
	inboxRunning(t, h, 1, 1)
	short := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder(), write: func(p []byte) (int, error) { return len(p) - 1, nil }}
	callInbox(t, a, token, map[string]any{"turn_number": 1}, short)
	before := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if before.ConsumedThrough != 0 {
		t.Fatal(before)
	}
	h.store.finishMCPTurn(h.id, 1)
	state, _ := h.store.LoadAgent(h.id)
	state.Turn.Status = "interrupted"
	if err := h.store.SaveAgent(state); err != nil {
		t.Fatal(err)
	}
	// Simulate a fresh process: old access and cache have no authority in it.
	h.reload(t, true)
	restoredRoom, restoreErr := NewRoom(h.store)
	if restoreErr != nil {
		t.Fatal(restoreErr)
	}
	h.roomHarness.addr, restoreErr = h.rt.Spawn(restoredRoom)
	if restoreErr != nil {
		t.Fatal(restoreErr)
	}
	svc = referenceTestService(h)
	svc.info.ServiceEpoch = "fresh-epoch"
	svc.referenceResolver = referenceTestResolver
	fresh, _, _ := accessFixture(t)
	if _, err := callInbox(t, fresh, token, map[string]any{"turn_number": 1}, nil); err == "" {
		t.Fatal("old process token accepted")
	}
	newToken, _ := issueInboxCredential(t, fresh, svc, h.id, true)
	if _, err := callInbox(t, fresh, newToken, map[string]any{"turn_number": 1}, nil); err == "" {
		t.Fatal("completed turn supplemented confirmation")
	}
	referenceRunning(t, h, 2, 0, 1)
	_, page := mustInboxCall(t, fresh, newToken, 2, "", 25)
	after := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if len(page.Entries) != 1 || after.ConsumedThrough != 1 || after.Batch.ID != before.Batch.ID || after.Batch.Receipt != before.Batch.Receipt {
		t.Fatal("restart lost original batch", before, after)
	}
}

func TestMCPInboxCachedOutputRejectsRetiredAuthority(t *testing.T) {
	for _, change := range []string{"credential", "generation", "membership", "service epoch"} {
		t.Run(change, func(t *testing.T) {
			h := mcpProbeInbox(t)
			svc := referenceTestService(h)
			svc.info.ServiceEpoch = "epoch"
			svc.referenceResolver = referenceTestResolver
			a, _, _ := accessFixture(t)
			token, revoke := issueInboxCredential(t, a, svc, h.id, true)
			short := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder(), write: func(p []byte) (int, error) { return len(p) - 1, nil }}
			callInbox(t, a, token, map[string]any{"turn_number": 1}, short)
			switch change {
			case "credential":
				revoke()
			case "generation":
				path, _ := h.store.dshPath(h.id, "session.json", true)
				ref := sessionReference{Version: formatVersion, MemberIdentity: h.id, Directory: filepath.Base(dshStateDir(path)) + "-" + strings.Repeat("b", 32)}
				if err := h.store.save(path, ref, maxPrivateFileBytes); err != nil {
					t.Fatal(err)
				}
			case "membership":
				state, _ := h.store.LoadRoom()
				state.Members = state.Members[1:]
				if err := h.store.SaveRoom(state); err != nil {
					t.Fatal(err)
				}
			case "service epoch":
				svc.info.ServiceEpoch = "changed"
			}
			if _, err := callInbox(t, a, token, map[string]any{"turn_number": 1}, nil); err == "" {
				t.Fatal("retired cached page supplied")
			}
			var consumption inboxConsumption
			if err := readSnapshot(filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, "consumption.json"), &consumption, maxPrivateFileBytes); err != nil || consumption.Through != 0 {
				t.Fatal("retired authority consumed", consumption, err)
			}
		})
	}
}

func TestMCPInboxUncertainConfirmationFreezesWithoutNewPage(t *testing.T) {
	h, svc, a, token := mcpInboxFixture(t)
	receiveReference(t, h, "one", referenceTestSource("one"))
	receiveReference(t, h, "two", referenceTestSource("two"))
	inboxRunning(t, h, 1, 2)
	original := h.store.ops.rename
	var fail atomic.Bool
	h.store.ops.rename = func(from, to string) error {
		if fail.Load() && strings.HasSuffix(to, "consumption.json") {
			return os.ErrPermission
		}
		return original(from, to)
	}
	t.Cleanup(func() { h.store.ops.rename = original })
	w := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder(), flush: func() error { fail.Store(true); return nil }}
	result, err := callInbox(t, a, token, map[string]any{"turn_number": 1, "limit": 1}, w)
	if err != "" {
		t.Fatal(err)
	}
	view := inboxToolView(t, result)
	session := svc.store.mcpOperations.inboxes[h.id]
	if !errors.Is(session.confirmationError(), ErrCommitUnknown) {
		t.Fatal("uncertain commit hidden", session.confirmationError())
	}
	if _, err := callInbox(t, a, token, map[string]any{"turn_number": 1, "cursor": view.Next}, nil); err == "" || !strings.Contains(err, "reopen") {
		t.Fatal("uncertain commit allowed new page", err)
	}
	if status := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); status.Batch.PageThrough != 1 || status.ConsumedThrough != 0 {
		t.Fatal("unknown confirmation advanced", status)
	}
}

func TestMCPInboxImageFailureAndByteBudgetsPreserveText(t *testing.T) {
	h, svc, a, token := mcpInboxFixture(t)
	svc.imageMaterials = imageMaterialConfig{TotalBytes: 8, ImageBytes: 8}
	var calls int
	svc.materialAttachments = &materialAttachments{download: func(_ context.Context, req attachmentRequest, _ *attachmentReader, limits attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
		calls++
		if strings.HasSuffix(req.Path, "missing.png") {
			return nil, "", nil, attachment.ErrUnavailable
		}
		if limits.MaxBytes != 8 {
			t.Errorf("wrong remaining byte budget %d", limits.MaxBytes)
		}
		return []byte("12345678"), "image/png", &attachmentAccess{}, nil
	}, check: func(context.Context, ServiceInfo, attachmentReader, TurnRecord, []materialNotice, []*attachmentAccess) error {
		return nil
	}}
	q := h.receive("partial")
	q.Item.Source = referenceTestSource("partial")
	q.Item.Text = "saved truncated text; missing remains unverified ![missing](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/missing.png) ![ok](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/ok.png)"
	inboxCall[InboxReceipt](t, h, q)
	q = h.receive("later")
	q.Item.Source = referenceTestSource("later")
	q.Item.Text = "![other](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/other.png)"
	inboxCall[InboxReceipt](t, h, q)
	inboxRunning(t, h, 1, 2)
	result, first := mustInboxCall(t, a, token, 1, "", 1)
	if len(result.Content) != 3 || first.Images[0].Status != "unavailable" || first.Images[1].Status != "supplied" {
		t.Fatal(first, result)
	}
	_, second := mustInboxCall(t, a, token, 1, first.Next, 1)
	if calls != 2 || second.Images[0].Status != "omitted" || !strings.Contains(second.Images[0].Reason, "byte budget") {
		t.Fatal(calls, second)
	}
}

func TestMCPInboxProductionDefaultAndExplicitCLIFallback(t *testing.T) {
	h, svc, access, _ := mcpInboxFixture(t)
	svc.access = access
	opts := adapterOptions(h.store, h.id)
	for _, mode := range []string{"", "http", "cli"} {
		svc.inboxMode = mode
		backend := svc.memberBackend(RunnerConfig{})
		options, err := backend.componentOptions(opts, sessionReference{Directory: "session.json.dsh"})
		if err != nil {
			t.Fatal(err)
		}
		if mode == "cli" {
			if backend.inboxMCP != nil || len(options.MCPServers) != 0 || !strings.Contains(options.Instructions, `"$TEAMX_CLI" inbox read`) {
				t.Fatal("explicit fallback not selected", mode)
			}
		} else if backend.inboxMCP != svc || len(options.MCPServers) != 1 || !strings.Contains(options.Instructions, "next_cursor") || strings.Contains(options.Instructions, `"$TEAMX_CLI" inbox read`) {
			t.Fatal("default MCP not selected", mode)
		}
	}
	for _, mode := range []string{"http", "cli", "stdio", "automatic"} {
		config, err := loadStartupConfig(writeStartupConfig(t, t.TempDir(), "[server]\ninbox_mode = \""+mode+"\"\n"), true)
		if mode == "http" || mode == "cli" {
			if err != nil || config.Server.InboxMode != mode {
				t.Fatal(mode, err)
			}
		} else if err == nil {
			t.Fatal("unsupported mode accepted", mode)
		}
	}
}
