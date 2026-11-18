package room

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/go-aie/teamx/internal/inputstream"
	frozen "github.com/go-aie/teamx/internal/inputstreamtest/frozen"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/mark3labs/mcp-go/mcp"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
)

func (s *inputSession) legacyRunCLI(ctx context.Context, args []string, out io.Writer, flush func() error) error {
	if len(args) < 2 || args[0] != "inbox" || (args[1] != "read" && args[1] != "stats") {
		return ErrRequest
	}
	flags := flag.NewFlagSet("frozen read", flag.ContinueOnError)
	turn := flags.Uint64("turn", 0, "")
	stream := flags.String("stream", "", "")
	id := flags.String("read-id", "", "")
	cursor := flags.String("cursor", "", "")
	limit := flags.Int("limit", 0, "")
	filter := flags.String("filter", "", "")
	_ = flags.Bool("json", false, "")
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if args[1] == "stats" {
		b, _ := json.Marshal(map[string]any{"turn_number": *turn, "filter": json.RawMessage(*filter)})
		_, err := s.legacyDiscover(ctx, "inbox_stats", b)
		return err
	}
	b, _ := json.Marshal(inputstream.ReadRequest{Turn: *turn, Stream: *stream, ID: *id, Cursor: *cursor, Limit: *limit})
	return s.writeCLI(ctx, b, out, flush)
}

// New-format composition shares the authenticated member/turn service with CLI.
// Legacy Rooms continue through the prior MCP contract until data upgrade.
func (h *mcpHTTP) addLegacyPlatformInputTools(s *inputSession) {
	schema := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"turn_number": map[string]any{"type": "integer", "minimum": 1}, "stream_ref": map[string]any{"type": "string", "minLength": 1}, "read_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 10000}, "cursor": map[string]any{"type": "string", "minLength": 1}},
		"required":   []string{"turn_number"},
		"oneOf":      []any{map[string]any{"required": []string{"stream_ref", "read_id"}, "not": map[string]any{"required": []string{"cursor"}}}, map[string]any{"required": []string{"cursor"}, "not": map[string]any{"anyOf": []any{map[string]any{"required": []string{"stream_ref"}}, map[string]any{"required": []string{"read_id"}}, map[string]any{"required": []string{"limit"}}}}}},
	}
	raw, _ := json.Marshal(schema)
	h.addOutputTool(mcp.NewToolWithRawSchema("inbox_read", "Read one selected saved stream in order. Requires current turn, stream_ref and stable read_id; retry identical arguments. Continuations accept only turn_number and cursor. Source association precedes output; consumption confirms only after complete output and flush. Platform limit is 1-25, default 25; all platform streams share 25 entries per turn. Room default 20, explicit limit up to 10000 with fixed pagination; Room uses no platform quota. This is formal consumption, not preview or business completion.", raw), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, func(context.Context) error, func(), error) {
		data, err := json.Marshal(req.GetArguments())
		if err != nil {
			return nil, nil, nil, err
		}
		session := s
		if session == nil {
			session, err = legacyInputSessionFrom(ctx, data)
			if err != nil {
				return nil, nil, nil, err
			}
		}
		return session.read(ctx, data)
	})
	// The service dispatcher is also usable by the CLI's JSON transport. Native
	// fields never appear in this schema; codecs own filter interpretation.
	for _, name := range []string{"inbox_stats", "inbox_list", "inbox_preview"} {
		name := name
		h.server.AddTool(mcp.NewToolWithRawSchema(name, legacyInputToolDescription(name), legacyInputDiscoverySchema(name)), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			data, err := json.Marshal(req.GetArguments())
			if err != nil {
				return nil, err
			}
			session := s
			if session == nil {
				session, err = legacyInputSessionFrom(ctx, data)
				if err != nil {
					return nil, err
				}
			}
			v, err := session.legacyDiscover(ctx, name, data)
			if err != nil {
				return nil, err
			}
			b, err := json.Marshal(v)
			return mcp.NewToolResultText(string(b)), err
		})
	}
}
func (s *inputSession) legacyDiscover(ctx context.Context, tool string, data []byte) (result any, err error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	if err := inputstream.ValidateRequest(data); err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil {
		return nil, ErrRequest
	}
	var turn uint64
	if json.Unmarshal(raw["turn_number"], &turn) != nil || turn != s.execution.Turn {
		return nil, ErrIdentity
	}
	// Corrective errors may contain source choices or native usage. Charge that
	// information to the same durable budget as successful discovery results.
	defer func() {
		if err == nil {
			return
		}
		original := err
		charged, chargeErr := s.legacyChargeDiscovery(map[string]string{"error": original.Error()}, tool == "inbox_preview")
		if chargeErr != nil {
			result, err = nil, chargeErr
			return
		}
		if stop, ok := charged.(map[string]string); ok && stop["stop_reason"] != "" {
			result, err = stop, nil
		}
	}()
	delete(raw, "turn_number")
	for key, value := range raw {
		switch key {
		case "within", "cursor", "stream_ref":
			var ref string
			if json.Unmarshal(value, &ref) != nil || strings.TrimSpace(ref) == "" || len(ref) > 128 {
				return nil, inputstream.ErrInvalid
			}
		case "limit":
			var n int
			maxLimit := 20
			if tool == "inbox_preview" {
				maxLimit = 3
			}
			if json.Unmarshal(value, &n) != nil || n < 1 || n > maxLimit {
				return nil, inputstream.ErrInvalid
			}
		case "query":
			var query string
			if json.Unmarshal(value, &query) != nil || strings.TrimSpace(query) == "" || len(query) > 4096 {
				return nil, inputstream.ErrInvalid
			}
		case "kind", "state", "attention", "sort":
			var v string
			if json.Unmarshal(value, &v) != nil || v == "" {
				return nil, inputstream.ErrInvalid
			}
		}
	}
	// Catalog instances retain bounded fixed pagination across calls. Refresh a
	// first request after consumption; cursor requests retain their original view.
	frozenCatalogMu.Lock()
	defer frozenCatalogMu.Unlock()
	var cursor string
	_ = json.Unmarshal(raw["cursor"], &cursor)
	catalog := frozenSessionCatalog[s]
	if cursor == "" || catalog == nil {
		var err error
		catalog, err = s.legacyCatalog(ctx)
		if err != nil {
			return nil, err
		}
		frozenSessionCatalog[s] = catalog
		frozenSessionCatalogs[s] = append(frozenSessionCatalogs[s], catalog)
		if len(frozenSessionCatalogs[s]) > 4 {
			frozenSessionCatalogs[s] = frozenSessionCatalogs[s][1:]
		}
	}
	b, _ := json.Marshal(raw)
	switch tool {
	case "inbox_list":
		var q frozen.Query
		if err := inputstream.StrictDecode(b, &q); err != nil {
			return nil, err
		}
		if cursor != "" {
			for i := len(frozenSessionCatalogs[s]) - 1; i >= 0; i-- {
				v, err := frozenSessionCatalogs[s][i].List(q)
				if err == nil {
					return s.legacyChargeDiscovery(v, false)
				}
				if !errors.Is(err, inputstream.ErrExpired) {
					return nil, err
				}
			}
			return nil, inputstream.ErrExpired
		}
		v, err := catalog.List(q)
		if err != nil {
			return nil, err
		}
		return s.legacyChargeDiscovery(v, false)
	case "inbox_stats":
		var q frozen.StatsQuery
		if err := inputstream.StrictDecode(b, &q); err != nil {
			return nil, err
		}
		if cursor != "" {
			for i := len(frozenSessionCatalogs[s]) - 1; i >= 0; i-- {
				v, err := frozenSessionCatalogs[s][i].Stats(q)
				if err == nil {
					return s.legacyChargeDiscovery(v, false)
				}
				if !errors.Is(err, inputstream.ErrExpired) {
					return nil, err
				}
			}
			return nil, inputstream.ErrExpired
		}
		v, err := catalog.Stats(q)
		if err != nil {
			return nil, err
		}
		return s.legacyChargeDiscovery(v, false)
	case "inbox_preview":
		var q struct {
			Stream string `json:"stream_ref"`
			Limit  int    `json:"limit,omitempty"`
		}
		if err := inputstream.StrictDecode(b, &q); err != nil {
			return nil, err
		}
		v, err := catalog.Preview(q.Stream, q.Limit)
		if err != nil {
			return nil, err
		}
		return s.legacyChargeDiscovery(v, true)
	default:
		return nil, ErrRequest
	}
}

func legacyInputDiscoverySchema(tool string) json.RawMessage {
	props := map[string]any{"turn_number": map[string]any{"type": "integer", "minimum": 1}, "cursor": map[string]any{"type": "string", "minLength": 1}, "within": map[string]any{"type": "string", "minLength": 1}, "filter": map[string]any{"type": "object"}, "kind": map[string]any{"enum": []string{"room", "notification"}}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 20}}
	if tool == "inbox_list" {
		props["query"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 4096}
		props["state"] = map[string]any{"enum": []string{"readable", "blocked", "all"}}
		props["attention"] = map[string]any{"enum": []string{"high", "normal", "low"}}
		props["sort"] = map[string]any{"enum": []string{"attention", "oldest"}}
	}
	required := []string{"turn_number"}
	if tool == "inbox_preview" {
		props = map[string]any{"turn_number": props["turn_number"], "stream_ref": map[string]any{"type": "string", "minLength": 1}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 3}}
		required = append(required, "stream_ref")
	}
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": props, "required": required}
	if tool != "inbox_preview" {
		// A cursor inherits every first-page condition, including its original limit.
		forbidden := []any{}
		keys := make([]string, 0, len(props))
		for key := range props {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if key != "turn_number" && key != "cursor" {
				forbidden = append(forbidden, map[string]any{"required": []string{key}})
			}
		}
		schema["oneOf"] = []any{map[string]any{"not": map[string]any{"required": []string{"cursor"}}}, map[string]any{"required": []string{"cursor"}, "not": map[string]any{"anyOf": forbidden}}}
	}
	b, _ := json.Marshal(schema)
	return b
}

func legacyInputToolDescription(name string) string {
	switch name {
	case "inbox_stats":
		return "Read-only saved source counts including blocked streams. Optional within binds exactly that source/container or one stream; a stream_ref never widens to the whole source. Use scope_ref from sources/list for broader source or project counts. Native filter requires within and adapter guidance, obtained with within and no filter. Reuse valid guidance directly. Cursor: only turn_number+cursor. No bodies, consumption or association."
	case "inbox_list":
		return "Discover relevant saved streams; optional query is literal AND over bounded saved labels/text, not semantic search. Within confines the returned source/container/stream. Native filter is adapter-owned; obtain missing local guidance with within and no filter, then reuse it. A returned scope_ref is broader than one candidate stream_ref. Default state readable; all includes blocked; sort attention or oldest; limit default10/max20. Continuation: only turn_number+cursor. No consumption, association or images."
	default:
		return "Read-only preview of the exact requested candidate: verify its label matches the requested context before previewing. Default 1, maximum 3 saved excerpts. Does not consume, associate answers or download images. Preview never substitutes for a later requested formal read."
	}
}

func legacyInputSessionFrom(ctx context.Context, data []byte) (*inputSession, error) {
	e, _ := ctx.Value(mcpExecutionKey{}).(*mcpExecution)
	if e == nil || e.inbox == nil || !e.inbox.service.unifiedInputs {
		return nil, ErrIdentity
	}
	if err := e.validate(); err != nil {
		return nil, err
	}
	var q struct {
		Turn uint64 `json:"turn_number"`
	}
	if json.Unmarshal(data, &q) != nil {
		return nil, ErrRequest
	}
	s, err := e.inbox.inputFor(ctx, q.Turn, e.identity.Generation)
	if err != nil {
		return nil, err
	}
	return s, nil
}

var frozenSessionCatalog = map[*inputSession]*frozen.Catalog{}
var frozenSessionCatalogs = map[*inputSession][]*frozen.Catalog{}
var frozenCatalogMu sync.Mutex

// writeCLI uses the same result as MCP. Flush is mandatory even for a successful
// full Write; callers supply their actual transport flush (or an unbuffered no-op).
func (s *inputSession) writeCLI(ctx context.Context, data []byte, w io.Writer, flush func() error) error {
	result, confirm, release, err := s.read(ctx, data)
	if release != nil {
		defer release()
	}
	if err != nil {
		return err
	}
	var view map[string]any
	if len(result.Content) == 0 {
		return ErrInvalid
	}
	text, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		return ErrInvalid
	}
	if err := json.Unmarshal([]byte(text.Text), &view); err != nil {
		return err
	}
	if len(result.Content) > 1 {
		view["content"] = result.Content[1:]
	}
	b, err := json.Marshal(view)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	n, err := w.Write(b)
	if err != nil {
		return err
	}
	if n != len(b) {
		return io.ErrShortWrite
	}
	if flush == nil {
		return ErrRequest
	}
	if err := flush(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return confirm(ctx)
}

func (s *inputSession) legacyChargeDiscovery(v any, preview bool) (any, error) {
	if preview {
		return v, nil
	}
	return s.chargeDiscovery(v)
}

// Historical result and request adapters are used by the frozen four-tool tests.
type inputReadStream struct {
	Ref   string `json:"stream_ref"`
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
}
type platformInputView struct {
	StreamRemaining int                            `json:"stream_remaining"`
	RoomEntries     []agentRoomMessage             `json:"-"`
	Stream          inputReadStream                `json:"stream"`
	Entries         []inputStreamNotificationEntry `json:"entries"`
	Followups       []agentFollowupSummary         `json:"followups,omitempty"`
	HasMore         bool                           `json:"has_more"`
	Next            string                         `json:"next_cursor,omitempty"`
	Remaining       int                            `json:"remaining_entries"`
	Stop            string                         `json:"stop_reason,omitempty"`
	Images          []streamImageCoverage          `json:"images,omitempty"`
}

func (v platformInputView) MarshalJSON() ([]byte, error) {
	v.Images = slices.Clone(v.Images)
	for i := range v.Images {
		coverage := &v.Images[i]
		if v.Stream.Kind == "room" {
			if coverage.Source != "" {
				_, _ = fmt.Sscanf(coverage.Source, "Room #%d", &coverage.RoomSeq)
			}
			coverage.Source = ""
		}
		if strings.HasPrefix(coverage.SharedSource, "Room #") {
			_, _ = fmt.Sscanf(coverage.SharedSource, "Room #%d", &coverage.SharedRoom)
			coverage.SharedSource = ""
		}
	}

	type plain platformInputView
	b, err := json.Marshal(plain(v))
	if err != nil || v.Stream.Kind != "room" {
		return b, err
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(b, &obj)
	obj["entries"], err = json.Marshal(v.RoomEntries)
	delete(obj, "remaining_entries")
	return json.Marshal(obj)
}
func (v *platformInputView) UnmarshalJSON(b []byte) error {
	type plain platformInputView
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	var stream inputReadStream
	if err := json.Unmarshal(obj["stream"], &stream); err != nil {
		return err
	}
	if stream.Kind == "room" {
		entries := obj["entries"]
		delete(obj, "entries")
		rest, _ := json.Marshal(obj)
		if err := json.Unmarshal(rest, (*plain)(v)); err != nil {
			return err
		}
		return json.Unmarshal(entries, &v.RoomEntries)
	}
	return json.Unmarshal(b, (*plain)(v))
}

// read holds the shared member operation lease until the output adapter calls
// release. Awaiting confirmation is durable BEFORE any content is handed out.
func (s *inputSession) read(ctx context.Context, data []byte) (*mcp.CallToolResult, func(context.Context) error, func(), error) {
	q, err := inputstream.DecodeRead(data)
	if err != nil {
		return nil, nil, nil, err
	}
	if q.Turn != s.execution.Turn {
		return nil, nil, nil, ErrIdentity
	}
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	result, confirm, err := s.readPage(ctx, q, s.renderFrozenInputPage)
	return result, confirm, release, err
}

func (s *inputSession) renderFrozenInputPage(a inputstream.ReadAttempt, page inputPageContent) (json.RawMessage, error) {
	state, err := s.storage.repository.State(s.execution)
	if err != nil {
		return nil, err
	}
	v := platformInputView{
		StreamRemaining: page.remaining, RoomEntries: page.roomEntries,
		Stream:  inputReadStream{Ref: a.Stream, Kind: "notification"},
		Entries: page.notifications, Followups: page.followups, Images: page.images,
		HasMore: a.HasMore, Next: a.Next, Remaining: inputstream.PlatformReadLimit - state.Used, Stop: page.stop,
	}
	v.Entries = slices.Clone(v.Entries)
	for i := range v.Entries {
		item := &v.Entries[i].Item
		item.Receiver = platform.AccountIdentity{}
		item.Tenant = platform.ExternalID{}
		item.ProjectID = 0
	}
	if a.Kind == "room" {
		v.Stream.Kind, v.Stream.Label = "room", "当前 Room"
		if a.HasMore && a.Next == "" {
			v.Stop = "read_limit_reached"
		}
	} else if len(page.notifications) > 0 {
		item := page.notifications[0].Item
		v.Stream.Label = item.Project
		if item.Context != nil && item.Context.Name != "" {
			v.Stream.Label = item.Context.Name
		}
		if item.Conversation != nil && item.Conversation.Name != "" {
			v.Stream.Label = item.Conversation.Name
		}
	}
	if len(page.roomEntries)+len(page.notifications) == 0 && v.Stop == "" {
		v.Stop = "stream_empty"
	}
	return json.Marshal(v)
}
