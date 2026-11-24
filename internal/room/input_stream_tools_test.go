package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/mark3labs/mcp-go/mcp"
)

func marshalMCPToolDefinitions(t *testing.T, h *mcpHTTP) []byte {
	return marshalMCPToolsWithPrefix(t, h, "")
}

func marshalMCPToolsWithPrefix(t *testing.T, h *mcpHTTP, prefix string) []byte {
	t.Helper()
	registered := h.server.ListTools()
	definitions := make([]mcp.Tool, 0, len(registered))
	for name, tool := range registered {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		definitions = append(definitions, tool.Tool)
	}
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].Name < definitions[j].Name })
	data, err := json.Marshal(definitions)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readInputStream(t *testing.T, s *inputSession, call, stream string, limit int, receive bool) inputBody {
	t.Helper()
	args := map[string]any{"stream_ref": stream}
	if limit > 0 {
		args["limit"] = limit
	}
	data, _ := json.Marshal(args)
	result, flush, release, err := s.readStream(context.Background(), call, data)
	if release != nil {
		defer release()
	}
	if err != nil {
		t.Fatal(err)
	}
	var v inputBody
	body := result.Content[0].(mcp.TextContent).Text
	if err = json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(err)
	}
	if err = flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if release != nil {
		release()
	}
	if receive {
		if err = s.receiveCall(context.Background(), call); err != nil {
			t.Fatal(err)
		}
	}
	return v
}
func TestInputToolsMinimalContract(t *testing.T) {
	parallelFixtureTest(t)
	h := newInputMCPHTTP(true)
	if len(h.server.ListTools()) != 2 {
		t.Fatal("input tool count")
	}
	for _, name := range []string{"inbox_list", "inbox_read"} {
		if h.server.ListTools()[name] == nil {
			t.Fatal("missing input tool", name)
		}
	}
	if hint := h.server.ListTools()["inbox_list"].Tool.Annotations.ReadOnlyHint; hint == nil || !*hint {
		t.Fatal("list must be read-only")
	}
	for _, name := range []string{"inbox_stats", "inbox_preview", "inbox_find", "inbox_overview"} {
		if h.server.ListTools()[name] != nil {
			t.Fatal(name)
		}
	}
	schema := marshalMCPToolsWithPrefix(t, h, "inbox_")
	for _, word := range []string{"turn_number", "read_id", "within", "filter", "query", "blocked_reason", "scope_ref"} {
		if bytes.Contains(schema, []byte(word)) {
			t.Fatal("schema leak", word)
		}
	}
	var definitions []mcp.Tool
	if err := json.Unmarshal(schema, &definitions); err != nil {
		t.Fatal(err)
	}
	for _, tool := range definitions {
		if tool.Name != "inbox_read" {
			continue
		}
		read := tool.InputSchema
		if len(read.Properties) != 2 || read.Properties["stream_ref"] == nil || read.Properties["limit"] == nil ||
			len(read.Required) != 1 || read.Required[0] != "stream_ref" {
			t.Fatal("unexpected inbox_read schema", read)
		}
	}
	for _, bad := range []string{`null`, `{"limit":null}`, `{"limit":0}`, `{"limit":21}`, `{"cursor":""}`, `{"cursor":"abc","limit":1}`, `{"turn_number":1}`, `{"query":"x"}`, `{"limit":1,"limit":2}`} {
		if _, err := inputstream.DecodeList([]byte(bad)); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	for _, bad := range []string{`{}`, `{"stream_ref":" "}`, `{"stream_ref":"x","limit":10001}`, `{"stream_ref":"x","cursor":"x"}`, `{"stream_ref":"x","read_id":"x"}`, `{"stream_ref":"x","turn_number":1}`, `{"stream_ref":"x","consume":false}`} {
		if _, err := inputstream.DecodeStreamRead([]byte(bad)); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	for _, good := range []string{`{}`, `{"limit":2}`, `{"cursor":"returned"}`} {
		if _, err := inputstream.DecodeList([]byte(good)); err != nil {
			t.Fatal(err)
		}
	}
	guide := unifiedAgentGuide()
	// Input takes only stream_ref/limit; business actions still require the
	// current turn_number. Check that field in the intake guidance only.
	intake, _, found := strings.Cut(guide, "\n\nAfter Session rotation")
	if !found || strings.Contains(intake, "turn_number") {
		t.Fatal("input guidance requires a business turn parameter")
	}
	for _, word := range []string{"read_id", "inbox_stats", "inbox_preview", "scope_ref", "filter", "blocked_streams"} {
		if strings.Contains(guide, word) {
			t.Fatal("guide leak", word)
		}
	}
}
func TestInputCallsReceiptReplayAndRoomSnapshot(t *testing.T) {
	parallelFixtureTest(t)
	h, s := unifiedReaderFixture(t, 45)
	ref := roomStreamRef(s.storage.id)
	first := readInputStream(t, s, "call-a", ref, 10, false)
	if len(first.Entries) != 10 || first.Remaining != 35 || h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal(first)
	}
	// A transport retry and a later failed-call retry both recover the old page.
	retry := readInputStream(t, s, "call-a", ref, 10, false)
	if !reflect.DeepEqual(first, retry) {
		t.Fatal("transport replay changed")
	}
	recovered := readInputStream(t, s, "call-retry", ref, 1, true)
	if !reflect.DeepEqual(first, recovered) {
		t.Fatal("failed page replaced by limit change")
	}
	roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: "late", Text: "later input"})
	next := readInputStream(t, s, "call-b", ref, 15, true)
	if len(next.Entries) != 15 || next.Remaining != 20 {
		t.Fatal(next)
	}
	last := readInputStream(t, s, "call-c", ref, 10000, true)
	if len(last.Entries) != 20 || last.Remaining != 0 {
		t.Fatal(last)
	}
	late := readInputStream(t, s, "call-d", ref, 0, true)
	if len(late.Entries) != 1 || late.Remaining != 0 {
		t.Fatal(late)
	}
	// An old logical call remains a replay, even after successful later reads.
	old := readInputStream(t, s, "call-a", ref, 10, true)
	if !reflect.DeepEqual(first, old) {
		t.Fatal("old call became new read")
	}
	if h.list(t).Members[0].ReadThrough != 46 || h.list(t).Members[1].ReadThrough != 0 {
		t.Fatal("independent member cursor")
	}
}
func TestInputPlatformRemainingQuotaAndCrossStream(t *testing.T) {
	parallelFixtureTest(t)
	h, s := unifiedReaderFixture(t, 0)
	s.service.referenceResolver = referenceTestResolver
	for i := 0; i < 29; i++ {
		item := storedGitLab(s.storage.id, fmt.Sprint(i), "events", "7", "42", fmt.Sprintf("body %d", i))
		receivePlatform(t, s.storage, item)
	}
	receivePlatform(t, s.storage, storedGitLab(s.storage.id, "other", "events", "7", "56", "other body"))
	// Start the next legal turn so its fixed platform bounds include these inputs.
	agent, _ := h.store.LoadAgent(s.storage.id)
	agent.TurnNumber = 2
	agent.Turn.Number = 2
	if err := h.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	s = startPlatformReader(t, h, s.storage, s.service, 2)
	a := streamFor(t, s, "body 0")
	b := streamFor(t, s, "other body")
	first := readInputStream(t, s, "a1", a, 2, true)
	if first.Remaining != 27 || len(first.Entries) != 2 {
		t.Fatal(first)
	}
	readInputStream(t, s, "b1", b, 1, true)
	next := readInputStream(t, s, "a2", a, 10000, true)
	if len(next.Entries) != 22 || next.Remaining != 5 || next.Stop != "read_budget_exhausted" {
		t.Fatal(next)
	}
	stop := readInputStream(t, s, "a3", a, 1, true)
	if len(stop.Entries) != 0 || stop.Remaining != 5 || stop.Stop != "read_budget_exhausted" {
		t.Fatal(stop)
	}
	state, _ := s.storage.repository.State(s.execution)
	if state.Used != 25 {
		t.Fatal(state.Used)
	}
	body, _ := json.Marshal(first)
	for _, word := range []string{`"stream"`, `"has_more"`, `"next_cursor"`, `"item"`, `"source"`, `"source_version"`, `"structured"`, `"namespace"`} {
		if bytes.Contains(body, []byte(word)) {
			t.Fatal("body leaked", word, string(body))
		}
	}
	if !bytes.Contains(body, []byte("source_ref")) {
		t.Fatal("lost source evidence")
	}
}
func TestInputDirectoryFirstPageContinuationAndRefresh(t *testing.T) {
	parallelFixtureTest(t)
	h, s := unifiedReaderFixture(t, 1)
	for i := 0; i < 26; i++ {
		receivePlatform(t, s.storage, storedGitLab(s.storage.id, fmt.Sprint(i), "events", "7", fmt.Sprint(100+i), fmt.Sprint(i)))
	}
	agent, _ := h.store.LoadAgent(s.storage.id)
	agent.TurnNumber = 2
	agent.Turn.Number = 2
	if err := h.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	s = startPlatformReader(t, h, s.storage, s.service, 2)
	c, err := s.catalog(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := c.Directory()
	if err != nil || len(d.Streams) > 5 || d.Pending != 27 {
		t.Fatal(d, err)
	}
	s.storage.catalogs = append(s.storage.catalogs, c)
	seen := map[string]bool{}
	for _, v := range d.Streams {
		seen[v.Ref] = true
	}
	cursor := d.Next
	for cursor != "" {
		data, _ := json.Marshal(inputstream.ListRequest{Cursor: cursor})
		v, err := s.listInputStreams(h.ctx, data)
		if err != nil {
			t.Fatal(err)
		}
		page := v.(inputstream.Directory)
		for _, v := range page.Streams {
			if seen[v.Ref] {
				t.Fatal("duplicate")
			}
			seen[v.Ref] = true
		}
		cursor = page.Next
	}
	if len(seen) != 27 {
		t.Fatal("tail truncated", len(seen))
	}
	readInputStream(t, s, "room", roomStreamRef(s.storage.id), 1, true)
	v, err := s.listInputStreams(h.ctx, []byte(`{}`))
	if err != nil || v.(inputstream.Directory).Pending != 26 {
		t.Fatal(v, err)
	}
	b, _ := json.Marshal(v)
	if bytes.Contains(b, []byte("scope_ref")) || bytes.Contains(b, []byte("blocked")) {
		t.Fatal(string(b))
	}
	state, _ := s.storage.repository.State(s.execution)
	if err := s.mutate(func(r *inputstream.Repository) error {
		return r.ChargeDiscovery(s.execution, (128<<10)-state.DiscoveryBytes)
	}); err != nil {
		t.Fatal(err)
	}
	stop, err := s.listInputStreams(h.ctx, []byte(`{}`))
	if err != nil || stop.(map[string]string)["stop_reason"] != "discovery_budget_exhausted" {
		t.Fatal(stop, err)
	}
}
func simpleMCPRequest(t *testing.T, a *serverAccess, token, call, tool string, args any, w http.ResponseWriter) *mcp.CallToolResult {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "transport-id", "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	req := httptest.NewRequest("POST", "http://localhost/mcp", bytes.NewReader(b))
	req.Host = "localhost"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Teamx-Call", call)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
	a.serveMCP(w, req)
	if out, ok := w.(*httptest.ResponseRecorder); ok {
		var v struct {
			Result *mcp.CallToolResult `json:"result"`
		}
		if json.Unmarshal(out.Body.Bytes(), &v) != nil {
			t.Fatal(out.Code, out.Body.String())
		}
		return v.Result
	}
	return nil
}
func TestInputMCPWaitsForFrameworkReceipt(t *testing.T) {
	parallelFixtureTest(t)
	h, s := unifiedReaderFixture(t, 2)
	a, _, _ := accessFixture(t)
	token, revoke, err := a.issueInputBinding(s)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	args := map[string]any{"stream_ref": roomStreamRef(s.storage.id), "limit": 1}
	out := httptest.NewRecorder()
	first := simpleMCPRequest(t, a, token, "logical-a", "inbox_read", args, out)
	if first == nil || first.IsError || h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal(out.Body.String())
	}
	if err := s.receiveCall(h.ctx, "logical-a"); err != nil {
		t.Fatal(err)
	}
	out = httptest.NewRecorder()
	retry := simpleMCPRequest(t, a, token, "logical-a", "inbox_read", args, out)
	if !reflect.DeepEqual(first, retry) {
		t.Fatal("lost-response replay")
	}
	out = httptest.NewRecorder()
	second := simpleMCPRequest(t, a, token, "logical-b", "inbox_read", args, out)
	if reflect.DeepEqual(first, second) {
		t.Fatal("same successful args failed to advance")
	}
	if err := s.receiveCall(h.ctx, "logical-b"); err != nil {
		t.Fatal(err)
	}
	revoke()
	req := httptest.NewRequest("POST", "http://localhost/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token)
	out = httptest.NewRecorder()
	a.serveMCP(out, req)
	if out.Code != 401 {
		t.Fatal("old binding accepted")
	}
}
func TestInputCLIRejectsOldFlags(t *testing.T) {
	parallelFixtureTest(t)
	for _, args := range [][]string{{"inbox", "stats"}, {"inbox", "preview"}, {"inbox", "read", "--stream", "x", "--turn", "1"}, {"inbox", "read", "--stream", "x", "--read-id", "id"}, {"inbox", "list", "--query", "x"}, {"inbox", "list", "--cursor", "x", "--limit", "1"}} {
		if err := runInputCLI(context.Background(), args, io.Discard, func(context.Context, string, []byte) error { t.Fatal("accepted", args); return nil }); err == nil {
			t.Fatal(args)
		}
	}
}

func TestInputNormalBodyAndContextReplay(t *testing.T) {
	parallelFixtureTest(t)
	h, s := unifiedReaderFixture(t, 2)
	c, err := s.catalog(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := c.Directory()
	if err != nil {
		t.Fatal(err)
	}
	s.rememberDirectory(d)
	result, _, release, err := s.readStream(h.ctx, "known", []byte(fmt.Sprintf(`{"stream_ref":%q,"limit":1}`, roomStreamRef(s.storage.id))))
	if release != nil {
		release()
	}
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &obj)
	if len(obj) != 2 || obj["entries"] == nil || string(obj["remaining_entries"]) != "1" {
		t.Fatal(obj)
	}
	if err = s.receiveCall(h.ctx, "known"); err != nil {
		t.Fatal(err)
	}
	s.knownMu.Lock()
	s.known = nil
	s.knownMu.Unlock()
	first := readInputStream(t, s, "opaque", roomStreamRef(s.storage.id), 1, false)
	if first.Context == "" {
		t.Fatal("lost readable context after recovery")
	}
	s.rememberDirectory(d)
	replay := readInputStream(t, s, "opaque", roomStreamRef(s.storage.id), 1, true)
	if !reflect.DeepEqual(first, replay) {
		t.Fatal("context changed during replay", first, replay)
	}
}

func TestInputReceiptRequiresFinalBodyProof(t *testing.T) {
	parallelFixtureTest(t)
	h, s := unifiedReaderFixture(t, 2)
	result, _, release, err := s.readStream(h.ctx, "shell/one", []byte(fmt.Sprintf(`{"stream_ref":%q,"limit":1}`, roomStreamRef(s.storage.id))))
	if release != nil {
		release()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = s.receiveGroupBodies(h.ctx, "shell", nil); err == nil {
		t.Fatal("empty/redirected output acknowledged")
	}
	if h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal("unreceived body consumed")
	}
	wrong, _ := inputBodyDigest([]byte(`{"entries":[],"remaining_entries":1}`))
	if err = s.receiveGroupBodies(h.ctx, "shell", []string{wrong}); err == nil {
		t.Fatal("replaced body acknowledged")
	}
	body := result.Content[0].(mcp.TextContent).Text
	digest, err := inputBodyDigest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.receiveGroupBodies(h.ctx, "shell", []string{digest}); err != nil {
		t.Fatal(err)
	}
	if h.list(t).Members[0].ReadThrough != 1 {
		t.Fatal("verified body not confirmed")
	}
	if err = s.receiveGroupBodies(h.ctx, "shell", []string{digest}); err != nil {
		t.Fatal("lost receipt response cannot replay", err)
	}
}

func TestInputMCPRejectsDuplicateArgumentsBeforeBinding(t *testing.T) {
	parallelFixtureTest(t)
	h, s := unifiedReaderFixture(t, 1)
	a, _, _ := accessFixture(t)
	token, revoke, err := a.issueInputBinding(s)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	stream := roomStreamRef(s.storage.id)
	args := json.RawMessage(fmt.Sprintf(`{"stream_ref":%q,"stream_ref":%q}`, stream, stream))
	w := httptest.NewRecorder()
	result := simpleMCPRequest(t, a, token, "duplicate", "inbox_read", args, w)
	if result != nil && !result.IsError {
		t.Fatal("duplicate argument accepted", w.Body.String())
	}
	state, err := s.storage.repository.State(s.execution)
	if err != nil || len(state.Calls) != 0 || len(state.Attempts) != 0 || h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal(state, err)
	}
}
