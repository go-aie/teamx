package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/attachment"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/mark3labs/mcp-go/mcp"
)

func historyImageItem(s *inputSession, id, text string) notification.Item {
	item := storedGitLab(s.storage.id, id, "events", "7", "42", text)
	item.Source.ProjectID = 7
	item.Source.URL = "https://gitlab.example.com/group/repo/-/merge_requests/42"
	return item
}

func historyAccess(t *testing.T, s *inputSession) (*serverAccess, string, func()) {
	t.Helper()
	a, _, _ := accessFixture(t)
	token, revoke, err := a.issueInputBinding(s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(revoke)
	return a, token, revoke
}

func historyMCPCall(t *testing.T, a *serverAccess, token, call, tool string, args any) (*mcp.CallToolResult, historyBody) {
	t.Helper()
	w := httptest.NewRecorder()
	result := simpleMCPRequest(t, a, token, call, tool, args, w)
	if result == nil || result.IsError {
		t.Fatal(w.Body.String())
	}
	var body historyBody
	if err := json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &body); err != nil {
		t.Fatal(err)
	}
	return result, body
}

func TestHistoryMCPModesSchemasAndNoConsumption(t *testing.T) {
	parallelFixtureTest(t)
	for _, mode := range []string{"http", "cli"} {
		t.Run(mode, func(t *testing.T) {
			h, s := unifiedReaderFixture(t, 3)
			s.service.inboxMode = mode
			a, token, _ := historyAccess(t, s)
			req := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("X-Teamx-Call", "discover")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
			w := httptest.NewRecorder()
			a.serveMCP(w, req)
			var discovered struct {
				Result struct {
					Tools []mcp.Tool `json:"tools"`
				} `json:"result"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &discovered); err != nil {
				t.Fatal(err)
			}
			names := map[string]bool{}
			for _, tool := range discovered.Result.Tools {
				names[tool.Name] = true
				if strings.HasPrefix(tool.Name, "history_") && (tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint) {
					t.Fatal(tool)
				}
			}
			expected := 2
			if mode == "http" {
				expected = 4
			}
			if len(names) != expected || !names["history_list"] || !names["history_read"] || names["inbox_read"] != (mode == "http") {
				t.Fatal(names)
			}
			before := historyTree(t, h.store.dir)
			args := map[string]any{"stream_ref": roomStreamRef(s.storage.id), "limit": 1}
			first, body := historyMCPCall(t, a, token, "one", "history_read", args)
			if !reflect.DeepEqual(historySeqs(body), []uint64{3}) || body.Next == "" {
				t.Fatal(body)
			}
			roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: "late", Text: "late"})
			retry, _ := historyMCPCall(t, a, token, "one", "history_read", args)
			if !reflect.DeepEqual(first, retry) {
				t.Fatal("call retry changed snapshot")
			}
			cursor := map[string]any{"cursor": body.Next}
			second, older := historyMCPCall(t, a, token, "two", "history_read", cursor)
			again, _ := historyMCPCall(t, a, token, "cursor-retry", "history_read", cursor)
			if !reflect.DeepEqual(second, again) || !reflect.DeepEqual(historySeqs(older), []uint64{2}) {
				t.Fatal(older)
			}
			if h.list(t).Members[0].ReadThrough != 0 {
				t.Fatal("history consumed")
			}
			// The only file changes above belong to the explicit Room append.
			afterAppend := historyTree(t, h.store.dir)
			historyMCPCall(t, a, token, "fresh", "history_read", args)
			if reflect.DeepEqual(before, afterAppend) || !reflect.DeepEqual(afterAppend, historyTree(t, h.store.dir)) {
				t.Fatal("history changed storage")
			}
			for i, bad := range []any{map[string]any{"cursor": body.Next, "limit": 1}, map[string]any{"stream_ref": roomStreamRef(s.storage.id), "query": "x"}, map[string]any{"cursor": "invalid"}} {
				w := httptest.NewRecorder()
				result := simpleMCPRequest(t, a, token, string(rune('a'+i)), "history_read", bad, w)
				if result != nil && !result.IsError {
					t.Fatal("accepted invalid args", bad)
				}
			}
			if mode == "cli" {
				w := httptest.NewRecorder()
				r := simpleMCPRequest(t, a, token, "forbidden", "inbox_read", args, w)
				if r != nil && !r.IsError {
					t.Fatal("CLI admitted MCP consumption")
				}
			}
		})
	}
}

func TestHistoryMCPImagePositionsDedupAndIndependentBudget(t *testing.T) {
	parallelFixtureTest(t)
	h, s := unifiedReaderFixture(t, 0)
	s.images = true
	a, token, _ := historyAccess(t, s)
	text := "![one](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/one.png) ![duplicate](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/one.png)"
	ref := receiveHistoryItem(t, s, historyImageItem(s, "older", text))
	receiveHistoryItem(t, s, historyImageItem(s, "latest", text))
	var downloads, checks int
	s.service.materialAttachments = &materialAttachments{download: func(_ context.Context, req attachmentRequest, reader *attachmentReader, _ attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
		if req.Range == nil || req.Partition == "" || req.SourceRef != "" || reader.MemberIdentity != s.storage.id {
			t.Fatal(req)
		}
		downloads++
		return []byte("fixture-pixels"), "image/png", &attachmentAccess{}, nil
	}, check: func(context.Context, ServiceInfo, attachmentReader, TurnRecord, []materialNotice, []*attachmentAccess) error {
		checks++
		return nil
	}}
	before := historyTree(t, h.store.dir)
	args := map[string]any{"stream_ref": ref, "limit": 2}
	first, body := historyMCPCall(t, a, token, "images", "history_read", args)
	if len(first.Content) != 2 || first.Content[1].(mcp.ImageContent).MIMEType != "image/png" || downloads != 1 {
		t.Fatal(first, downloads)
	}
	for _, e := range body.Entries {
		images := e.(map[string]any)["images"].([]any)
		if len(images) != 2 {
			t.Fatal(images)
		}
		for i, v := range images {
			m := v.(map[string]any)
			if m["content_index"] != float64(1) || m["position"] != float64(i+1) || m["reason"] != nil || m["status"] != nil {
				t.Fatal(m)
			}
		}
	}
	charged := s.storage.history.bytes
	historyMCPCall(t, a, token, "images", "history_read", args)
	if downloads != 1 || checks < 2 || s.storage.history.bytes != charged {
		t.Fatal("replay did not reuse/revalidate")
	}
	for i := range 4 {
		historyMCPCall(t, a, token, string(rune('a'+i)), "history_read", args)
	}
	if downloads != 4 || s.storage.history.images != 4 {
		t.Fatal("image allowance refreshed", downloads)
	}
	missing, page := historyMCPCall(t, a, token, "exhausted", "history_read", args)
	if len(missing.Content) != 1 || !strings.Contains(string(mustJSON(t, page)), "image count budget") {
		t.Fatal(page)
	}
	if !reflect.DeepEqual(before, historyTree(t, h.store.dir)) {
		t.Fatal("history persisted material/consumption")
	}
	state, err := s.inputState(h.ctx)
	if err != nil || state.Used != 0 || state.MaterialImages != 0 || len(state.Attempts) != 0 {
		t.Fatal(state, err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestHistoryMCPMissingImagesKeepText(t *testing.T) {
	parallelFixtureTest(t)
	for _, kind := range []string{"text-only", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			_, s := unifiedReaderFixture(t, 0)
			s.images = kind != "text-only"

			ref := receiveHistoryItem(t, s, historyImageItem(s, "gitlab", "saved fact ![x](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png)"))
			s.service.materialAttachments = &materialAttachments{download: func(context.Context, attachmentRequest, *attachmentReader, attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
				return nil, "", nil, attachment.ErrUnavailable
			}}
			a, token, _ := historyAccess(t, s)
			result, body := historyMCPCall(t, a, token, "missing", "history_read", map[string]any{"stream_ref": ref})
			raw := string(mustJSON(t, body))
			expected := map[string]string{"text-only": "text-only", "unavailable": "attachment unavailable"}[kind]
			if !strings.Contains(raw, expected) {
				t.Fatal("wrong missing reason", raw)
			}
			if len(result.Content) != 1 || !strings.Contains(raw, "saved fact") || !strings.Contains(raw, "reason") || strings.Contains(raw, "content_index") {
				t.Fatal(raw)
			}
		})
	}
}

func TestHistoryMCPRevokeAndCloseWaitForOutput(t *testing.T) {
	parallelFixtureTest(t)
	h, s, _, shared := sharedReaders(t)
	receiveSharedFixture(t, s, "one", "42")
	receiveSharedFixture(t, s, "two", "42")
	ref := sharedRef(t, s)
	a, token, revoke := historyAccess(t, s)
	entered, finish := make(chan struct{}), make(chan struct{})
	var writes atomic.Int32
	w := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder(), write: func(b []byte) (int, error) { writes.Add(1); close(entered); <-finish; return len(b), nil }}
	done := make(chan struct{})
	go func() {
		defer close(done)
		simpleMCPRequest(t, a, token, "output", "history_read", map[string]any{"stream_ref": ref, "limit": 1}, w)
	}()
	<-entered
	revoked := make(chan error, 1)
	go func() { revoked <- s.service.closeSharedMember(h.ctx, s.storage.id) }()
	closed := make(chan struct{})
	go func() { revoke(); close(closed) }()
	select {
	case err := <-revoked:
		t.Fatal("revoke bypassed admitted output", err)
	case <-time.After(25 * time.Millisecond):
	}
	select {
	case <-closed:
		t.Fatal("close bypassed admitted output")
	case <-time.After(25 * time.Millisecond):
	}
	close(finish)
	<-done
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	<-closed
	if writes.Load() != 1 || (s.storage.history != nil && len(s.storage.history.queries) != 0) || len(h.store.roomHistoryPins) != 0 || len(shared.view.Load().root.Pages) != 0 {
		t.Fatal("history lifetime leaked")
	}
	args, _ := json.Marshal(historyReadRequest{Stream: ref})
	if _, err := s.historyCall(h.ctx, "revoked", "history_read", args); !errors.Is(err, errHistoryStream) {
		t.Fatal(err)
	}
}

func TestHistoryMCPShortOutputRetryAndTurnClose(t *testing.T) {
	parallelFixtureTest(t)
	h, s := unifiedReaderFixture(t, 2)
	a, token, _ := historyAccess(t, s)
	args := map[string]any{"stream_ref": roomStreamRef(s.storage.id), "limit": 1}
	short := &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder(), write: func([]byte) (int, error) { return 0, io.ErrShortWrite }}
	simpleMCPRequest(t, a, token, "short", "history_read", args, short)
	charged := s.storage.history.bytes
	_, page := historyMCPCall(t, a, token, "short", "history_read", args)
	if !reflect.DeepEqual(historySeqs(page), []uint64{2}) || charged != s.storage.history.bytes || h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal(page)
	}
	h.store.finishMCPTurn(s.storage.id, 1)
	if s.storage.history != nil {
		t.Fatal("turn retained pixels")
	}
	if _, err := s.historyCall(h.ctx, "late", "history_list", []byte(`{}`)); !errors.Is(err, ErrClosing) {
		t.Fatal(err)
	}
}

func TestHistoryMCPRevocationDuringPreparationRejectsPixels(t *testing.T) {
	parallelFixtureTest(t)
	h, s, _, _ := sharedReaders(t)
	item := historyImageItem(s, "shared-image", "![image](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png)")
	item.Route.Target = notification.Target{Mode: "shared_inbox", InboxID: sharedProjectInbox}
	if err := s.service.receiveInbox(h.ctx, item.Route.Target, item); err != nil {
		t.Fatal(err)
	}
	s.images = true
	started, finish := make(chan struct{}), make(chan struct{})
	s.service.materialAttachments = &materialAttachments{download: func(context.Context, attachmentRequest, *attachmentReader, attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
		close(started)
		<-finish
		return []byte("pixels"), "image/png", &attachmentAccess{}, nil
	}}
	a, token, _ := historyAccess(t, s)
	ref := sharedRef(t, s)
	w := httptest.NewRecorder()
	done := make(chan *mcp.CallToolResult, 1)
	go func() {
		done <- simpleMCPRequest(t, a, token, "during-download", "history_read", map[string]any{"stream_ref": ref}, w)
	}()
	<-started
	if err := s.service.closeSharedMember(h.ctx, s.storage.id); err != nil {
		t.Fatal(err)
	}
	close(finish)
	if result := <-done; result != nil && !result.IsError {
		t.Fatal("revoked preparation supplied", result)
	}
	if strings.Contains(w.Body.String(), "image/png") {
		t.Fatal("revoked image escaped")
	}
}

func TestHistoryMCPCancelDownloadReleasesPinsAndCache(t *testing.T) {
	parallelFixtureTest(t)
	h, s := unifiedReaderFixture(t, 0)
	s.images = true
	ref := receiveHistoryItem(t, s, historyImageItem(s, "cancel-image", "![image](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png)"))
	started := make(chan struct{})
	s.service.materialAttachments = &materialAttachments{download: func(ctx context.Context, _ attachmentRequest, _ *attachmentReader, _ attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
		close(started)
		<-ctx.Done()
		return nil, "", nil, ctx.Err()
	}}
	a, token, revoke := historyAccess(t, s)
	done := make(chan struct{})
	go func() {
		defer close(done)
		simpleMCPRequest(t, a, token, "cancel", "history_read", map[string]any{"stream_ref": ref}, &mcpFaultWriter{ResponseRecorder: httptest.NewRecorder()})
	}()
	<-started
	revoke()
	<-done
	if (s.storage.history != nil && len(s.storage.history.queries) != 0) || len(h.store.roomHistoryPins) != 0 {
		t.Fatal("canceled preparation retained history")
	}
	release, err := s.acquire(h.ctx)
	if err != nil {
		t.Fatal("operation lease leaked", err)
	}
	release()
}

func TestHistoryMCPLarkImagesAndCursorReplayChargeSharedAllowance(t *testing.T) {
	parallelFixtureTest(t)
	h, s := unifiedReaderFixture(t, 0)
	s.images = true
	ref := receiveHistoryItem(t, s, storedLark(s.storage.id, "older", "oc_saved", "Saved chat", "omt", "older ![image](lark-image:img_saved)"))
	receiveHistoryItem(t, s, storedLark(s.storage.id, "latest", "oc_saved", "Saved chat", "omt", "latest ![image](lark-image:img_saved)"))
	downloads := 0
	s.service.materialAttachments = &materialAttachments{download: func(_ context.Context, req attachmentRequest, _ *attachmentReader, _ attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
		if req.Path != "lark-image:img_saved" || req.Range.sourceRef.Provider != "lark" {
			t.Fatal(req)
		}
		downloads++
		return []byte("pixels"), "image/png", &attachmentAccess{}, nil
	}}
	a, token, revoke := historyAccess(t, s)
	first, body := historyMCPCall(t, a, token, "first", "history_read", map[string]any{"stream_ref": ref, "limit": 1})
	if len(first.Content) != 2 || body.Next == "" {
		t.Fatal(body)
	}
	args := map[string]any{"cursor": body.Next}
	second, _ := historyMCPCall(t, a, token, "older", "history_read", args)
	third, _ := historyMCPCall(t, a, token, "again", "history_read", args)
	if len(second.Content) != 2 || !reflect.DeepEqual(second, third) || downloads != 2 || s.storage.history.images != 3 {
		t.Fatal(downloads, s.storage.history.images)
	}
	historyMCPCall(t, a, token, "last", "history_read", args)
	_, stop := historyMCPCall(t, a, token, "budget", "history_read", args)
	if stop.Stop != "history_budget" || s.storage.history.images != 4 || downloads != 2 {
		t.Fatal(stop, downloads)
	}
	charged := s.storage.history.bytes
	revoke()
	if s.storage.history.bytes != charged || s.storage.history.images != 4 || len(s.storage.history.queries) != 0 {
		t.Fatal("transport close reset allowance")
	}
	token, nextRevoke, err := a.issueInputBinding(s)
	if err != nil {
		t.Fatal(err)
	}
	defer nextRevoke()
	result, missing := historyMCPCall(t, a, token, "new-transport", "history_read", map[string]any{"stream_ref": ref, "limit": 1})
	if len(result.Content) != 1 || !strings.Contains(string(mustJSON(t, missing)), "image count budget") || s.storage.history.bytes <= charged {
		t.Fatal(missing)
	}
	state, err := s.inputState(h.ctx)
	if err != nil || state.MaterialImages != 0 {
		t.Fatal(state, err)
	}
}

func TestHistoryMCPServiceImageLimitAppliesAcrossQueries(t *testing.T) {
	parallelFixtureTest(t)
	_, s := unifiedReaderFixture(t, 0)
	s.images = true
	s.service.imageMaterials = imageMaterialConfig{MaxImages: 1}
	ref := receiveHistoryItem(t, s, historyImageItem(s, "limited", "![image](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/image.png)"))
	downloads := 0
	s.service.materialAttachments = &materialAttachments{download: func(context.Context, attachmentRequest, *attachmentReader, attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
		downloads++
		return []byte("pixels"), "image/png", &attachmentAccess{}, nil
	}}
	a, token, _ := historyAccess(t, s)
	args := map[string]any{"stream_ref": ref}
	first, _ := historyMCPCall(t, a, token, "first", "history_read", args)
	second, body := historyMCPCall(t, a, token, "new-query", "history_read", args)
	if len(first.Content) != 2 || len(second.Content) != 1 || downloads != 1 || !strings.Contains(string(mustJSON(t, body)), "image count budget") {
		t.Fatal(body, downloads)
	}
}

func TestHistoryMCPStorageFailureHidesPrivatePaths(t *testing.T) {
	parallelFixtureTest(t)
	h, s := unifiedReaderFixture(t, 0)
	ref := receiveHistoryItem(t, s, historyImageItem(s, "missing-record", "retained fact"))
	files, err := filepath.Glob(filepath.Join(h.store.dir, "inboxes", s.storage.id.MembershipID, "gitlab", "records", "*"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	a, token, _ := historyAccess(t, s)
	w := httptest.NewRecorder()
	result := simpleMCPRequest(t, a, token, "missing-file", "history_read", map[string]any{"stream_ref": ref}, w)
	raw := w.Body.String()
	if (result != nil && !result.IsError) || strings.Contains(raw, h.store.dir) || !strings.Contains(raw, "storage_") {
		t.Fatal(raw)
	}
}
