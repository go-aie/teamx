package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// The client hashes the final rendered business body. Canonicalization ignores
// object key order/whitespace while preserving all values and array order.
func inputBodyDigest(data []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var body any
	if err := decoder.Decode(&body); err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(body); err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.TrimSuffix(buffer.Bytes(), []byte("\n")))
	return hex.EncodeToString(sum[:]), nil
}
func (s *inputSession) receiveGroupBodies(ctx context.Context, group string, digests []string) error {
	if len(digests) > 256 {
		return ErrRequest
	}
	proof := map[string]bool{}
	for _, digest := range digests {
		if len(digest) != 64 {
			return ErrRequest
		}
		proof[digest] = true
	}
	state, err := s.storage.repository.State(s.execution)
	if err != nil {
		return err
	}
	calls := []string{}
	for call, binding := range state.Calls {
		if call != group && !strings.HasPrefix(call, group+"/") {
			continue
		}
		var page string
		for _, a := range state.Attempts {
			if a.Request == binding.Request && (a.Phase == "awaiting_confirmation" || a.Phase == "settled") {
				page = a.ID
				break
			}
		}
		if page == "" {
			// Binding can precede a safe failure before any result exists.
			continue
		}
		data, err := s.storage.repository.Result(s.execution, page)
		if err != nil {
			return err
		}
		var saved inputPageResult
		if err = json.Unmarshal(data, &saved); err != nil {
			return err
		}
		rendered, err := saved.mcpResult()
		if err != nil {
			return err
		}
		body, ok := rendered.Content[0].(mcp.TextContent)
		if !ok {
			return ErrInvalid
		}
		expected, err := inputBodyDigest([]byte(body.Text))
		if err != nil {
			return err
		}
		if !proof[expected] {
			return ErrRequest
		}
		calls = append(calls, call)
	}
	// Verify the whole group before any native confirmation can commit.
	for _, call := range calls {
		if err = s.receiveCall(ctx, call); err != nil {
			return err
		}
	}
	return nil
}
