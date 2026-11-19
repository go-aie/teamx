package room

import (
	"context"
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
)

// Production composition: History is available independently of Inbox transport.
func newHistoryMCPHTTP(cliInbox bool) *mcpHTTP {
	h := newMCPTransport()
	if !cliInbox {
		h.addInputTools()
	}
	for _, tool := range historyToolDefinitions() {
		h.addOutputTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, func(context.Context) error, func(), error) {
			data, err := json.Marshal(req.GetRawArguments())
			if err != nil {
				return nil, nil, nil, err
			}
			s, err := inputSessionFrom(ctx)
			if err != nil {
				return nil, nil, nil, err
			}
			call, _ := ctx.Value(inputCallKey{}).(string)
			page, release, err := s.prepareHistory(ctx, call, req.Params.Name, data)
			if err != nil {
				return nil, nil, release, err
			}
			// Reuse verified HTTP framing and the operation lease, with no consumption
			// confirmation. The no-op only enables the existing output framing hook.
			return page.result(), func(context.Context) error { return nil }, release, nil
		})
	}
	return h
}
