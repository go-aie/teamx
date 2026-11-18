package room

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/mark3labs/mcp-go/mcp"
)

func inputListSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"limit":{"type":"integer","minimum":1,"maximum":20},"cursor":{"type":"string","minLength":1,"maxLength":128}},"oneOf":[{"not":{"required":["cursor"]}},{"required":["cursor"],"not":{"required":["limit"]}}]}`)
}
func inputReadSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["stream_ref"],"properties":{"stream_ref":{"type":"string","minLength":1,"maxLength":128},"limit":{"type":"integer","minimum":1,"maximum":10000}}}`)
}
func inputToolDescription(name string) string {
	if name == "inbox_list" {
		return "List saved readable input streams without consuming. First request: optional limit (default 10, max 20). More: only the returned directory cursor. First-screen streams can be read directly. Empty arguments refresh the directory."
	}
	return "Formally read the oldest pending entries of one selected stream. Only stream_ref and optional limit. Repeat after success for the next batch; choose another stream when appropriate. remaining_entries counts entries still pending in this stream's fixed observation. Reading is distinct from business completion or action authority."
}
func (h *mcpHTTP) addInputTools() {
	h.addOutputTool(mcp.NewToolWithRawSchema("inbox_read", inputToolDescription("inbox_read"), inputReadSchema()), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, func(context.Context) error, func(), error) {
		data, err := json.Marshal(req.GetRawArguments())
		if err != nil {
			return nil, nil, nil, err
		}
		s, err := inputSessionFrom(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		call, _ := ctx.Value(inputCallKey{}).(string)
		return s.readStream(ctx, call, data)
	})
	listTool := mcp.NewToolWithRawSchema("inbox_list", inputToolDescription("inbox_list"), inputListSchema())
	listTool.Annotations.ReadOnlyHint = mcp.ToBoolPtr(true)
	h.server.AddTool(listTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		data, err := json.Marshal(req.GetRawArguments())
		if err != nil {
			return nil, err
		}
		s, err := inputSessionFrom(ctx)
		if err != nil {
			return nil, err
		}
		v, err := s.listInputStreams(ctx, data)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(v)
		return mcp.NewToolResultText(string(b)), err
	})
}
func (s *inputSession) listInputStreams(ctx context.Context, data []byte) (any, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	q, err := inputstream.DecodeList(data)
	if err != nil {
		return nil, err
	}
	s.storage.discovery.Lock()
	defer s.storage.discovery.Unlock()
	if q.Cursor != "" {
		for i := len(s.storage.catalogs) - 1; i >= 0; i-- {
			v, err := s.storage.catalogs[i].ReadableList(q)
			if err == nil {
				return s.chargeDiscovery(v)
			}
			if !errors.Is(err, inputstream.ErrExpired) {
				return nil, err
			}
		}
		return nil, errors.New("Directory expired; call inbox_list with empty arguments to refresh")
	}
	c, err := s.catalog(ctx)
	if err != nil {
		return nil, err
	}
	s.storage.keepInputCatalog(c)
	v, err := c.ReadableList(q)
	if err != nil {
		return nil, err
	}
	return s.chargeDiscovery(v)
}
func (s *inputSession) chargeDiscovery(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if err := s.mutate(func(r *inputstream.Repository) error { return r.ChargeDiscovery(s.execution, len(b)) }); err != nil {
		if errors.Is(err, inputstream.ErrCapacity) {
			reason := "discovery_budget_exhausted"
			return map[string]string{"stop_reason": reason}, nil
		}
		return nil, err
	}
	s.rememberDirectory(v)
	return v, nil
}
