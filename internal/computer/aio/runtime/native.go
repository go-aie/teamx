package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-aie/teamx/internal/computer"
)

// nativeClient is per screen epoch. It forwards complete JSON documents and
// never retries tools/call, including after HTTP or JSON-RPC errors.
type nativeClient struct {
	url, session string
	http         *http.Client
	next         atomic.Uint64
}

func (n *nativeClient) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := n.next.Add(1)
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if n.session != "" {
		req.Header.Set("Mcp-Session-Id", n.session)
	}
	res, err := n.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, computer.ResponseLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > computer.ResponseLimit {
		return nil, fmt.Errorf("native response exceeds 8 MiB")
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("native MCP HTTP %d", res.StatusCode)
	}
	var envelope struct {
		ID     uint64          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err = json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("native MCP requires bounded JSON response: %w", err)
	}
	if envelope.ID != id || len(envelope.Result) == 0 || (len(envelope.Error) > 0 && string(envelope.Error) != "null") {
		return nil, fmt.Errorf("native MCP did not return a verified result")
	}
	if method == "initialize" {
		n.session = res.Header.Get("Mcp-Session-Id")
	}
	return envelope.Result, nil
}
func (n *nativeClient) initialize(ctx context.Context) error {
	if _, err := n.rpc(ctx, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "teamx-computer", "version": "1"}}); err != nil {
		return err
	}
	raw := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if n.session != "" {
		req.Header.Set("Mcp-Session-Id", n.session)
	}
	res, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, err = io.Copy(io.Discard, io.LimitReader(res.Body, 65536))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("native initialize notification HTTP %d", res.StatusCode)
	}
	return err
}

func discover(ctx context.Context, c *nativeClient) (map[string]json.RawMessage, error) {
	discovered := map[string]json.RawMessage{}
	cursor := ""
	for page := 0; page < 16; page++ {
		params := map[string]string{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := c.rpc(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var result struct {
			Tools []json.RawMessage `json:"tools"`
			Next  string            `json:"nextCursor"`
		}
		if err = json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
		for _, doc := range result.Tools {
			var tool struct {
				Name string `json:"name"`
			}
			if err = json.Unmarshal(doc, &tool); err != nil {
				return nil, err
			}
			if _, exists := discovered[tool.Name]; exists {
				return nil, fmt.Errorf("duplicate native tool")
			}
			discovered[tool.Name] = doc
		}
		cursor = result.Next
		if cursor == "" {
			break
		}
	}
	if cursor != "" {
		return nil, fmt.Errorf("native tool catalog pagination limit")
	}

	return discovered, nil
}

// Startup discovery is read-only. Bound each probe independently so a cold
// upstream MCP server cannot consume the entire screen startup budget in one
// request. Native tools/call retains its single-attempt execution contract.
func awaitNativeCatalog(ctx context.Context, c *nativeClient, contract computer.Contract) (computer.Contract, error) {
	for {
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		discovered, err := discover(probe, c)
		cancel()
		if err == nil {
			complete := true
			for _, tool := range contract.Tools {
				definition, ok := discovered[tool.NativeName]
				if !ok {
					complete = false
					continue
				}
				if computer.Hash(definition) != computer.Hash(tool.Definition) {
					return computer.Contract{}, computer.Fail(computer.ContractMismatch, "native definition differs from pinned complete schema: "+tool.NativeName)
				}
			}
			if complete {
				return contract.Clone(), nil
			}
		}
		select {
		case <-ctx.Done():
			return computer.Contract{}, computer.Fail(computer.Unavailable, "native catalog did not become ready within screen startup budget")
		case <-time.After(250 * time.Millisecond):
		}
	}
}
