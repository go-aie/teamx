package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const maxMCPResponseBytes = 8 << 20
const mcpProtocolVersion = "2025-11-25"

type mcpDeliveryKey struct{}
type mcpDelivery struct {
	header   http.Header
	expected []byte // SHA-256 only; no second retained copy of image data.
	bytes    int
	confirm  func(context.Context) error
	release  func()
}

// mcpHTTP owns one stateless, JSON-only server shared by every member.
type mcpHTTP struct {
	server *server.MCPServer
	http   *server.StreamableHTTPServer
}

func newMCPHTTP() *mcpHTTP { return newInputMCPHTTP(false) }
func newInputMCPHTTP(unified bool) *mcpHTTP {
	hooks := &server.Hooks{}
	hooks.AddOnSuccess(func(ctx context.Context, id any, method mcp.MCPMethod, _ any, result any) {
		d, _ := ctx.Value(mcpDeliveryKey{}).(*mcpDelivery)
		if d == nil || d.confirm == nil || method != mcp.MethodToolsCall {
			return
		}
		// This hook identifies the final successful reply; it does NOT confirm.
		// Compare its exact encoding with bytes actually written by the SDK.
		data, err := json.Marshal(struct {
			JSONRPC string `json:"jsonrpc"`
			ID      any    `json:"id"`
			Result  any    `json:"result"`
		}{"2.0", id, result})
		if err != nil || len(data)+1 > maxMCPResponseBytes {
			return
		}
		data = append(data, '\n') // SDK's json.Encoder.Encode delimiter.
		sum := sha256.Sum256(data)
		d.expected, d.bytes = sum[:], len(data)
		// Explicit framing lets a successful flush finish the HTTP response
		// body before confirmation, without a later chunk terminator on return.
		d.header.Set("Content-Length", strconv.Itoa(d.bytes))
	})
	s := server.NewMCPServer("teamx", "1", server.WithToolCapabilities(false), server.WithHooks(hooks), server.WithRecovery())
	h := &mcpHTTP{server: s, http: server.NewStreamableHTTPServer(s,
		server.WithEndpointPath("/mcp"), server.WithStateLess(true), server.WithDisableStreaming(true), server.WithMaxRequestBodyBytes(128<<10),
		// The verified output encoding is this protocol's JSON reply. Later
		// protocol eras decorate responses after OnSuccess; fail negotiation
		// rather than silently acknowledging a different output contract.
		server.WithStreamableHTTPProtocolVersions(mcpProtocolVersion))}
	if unified {
		h.addInputTools()
	} else {
		h.addInboxTool()
	}
	return h
}

// addOutputTool is internal: acceptance tools are registered only in _test.go.
// The operation lease covers preparation, output and confirmation, including errors.
func (h *mcpHTTP) addOutputTool(tool mcp.Tool, prepare func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, func(context.Context) error, func(), error)) {
	h.server.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		d, ok := ctx.Value(mcpDeliveryKey{}).(*mcpDelivery)
		if !ok {
			return nil, ErrRequest
		}
		result, confirm, release, err := prepare(ctx, req)
		d.release = release
		if err == nil && result != nil && !result.IsError {
			d.confirm = confirm
		}
		return result, err
	})
}

func (h *mcpHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d := &mcpDelivery{header: w.Header()}
	defer func() {
		if d.release != nil {
			d.release()
		}
	}()
	// Deliberately hide Flusher from the SDK: this endpoint cannot upgrade a
	// tools/call response to SSE or interleave progress notifications.
	out := &mcpOutputWriter{ResponseWriter: w, digest: sha256.New()}
	defer func() {
		if out.deadlineSet {
			_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		}
	}()
	h.http.ServeHTTP(out, r.WithContext(context.WithValue(r.Context(), mcpDeliveryKey{}, d)))
	if d.confirm == nil || d.bytes == 0 || out.err != nil || out.status != http.StatusOK ||
		out.Header().Get("Content-Type") != "application/json" || out.bytes != d.bytes ||
		!bytes.Equal(out.digest.Sum(nil), d.expected) || r.Context().Err() != nil {
		return
	}
	// Write may only fill net/http's buffer. FlushError detects failures while
	// draining it. Unsupported flush is a failed output gate, never a success.
	if err := http.NewResponseController(w).Flush(); err != nil || r.Context().Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	// Once output completed, cancellation cannot undo a successful confirm.
	// The caller retains responsibility for recording/reporting confirm errors.
	_ = d.confirm(ctx)
}

type mcpOutputWriter struct {
	http.ResponseWriter
	digest        hash.Hash
	status, bytes int
	err           error
	deadlineSet   bool
}

func (w *mcpOutputWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *mcpOutputWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if len(data) > maxMCPResponseBytes-w.bytes {
		w.err = errors.New("MCP response exceeds output limit")
		return 0, w.err
	}
	if !w.deadlineSet {
		// A revoked generation must be able to drain even when its client stops
		// reading a multi-image response. Start the budget after preparation.
		err := http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(15 * time.Second))
		if err == nil {
			w.deadlineSet = true
		} else if !errors.Is(err, http.ErrNotSupported) {
			w.err = err
			return 0, err
		}
	}
	n, err := w.ResponseWriter.Write(data)
	if n < 0 || n > len(data) {
		n, err = 0, io.ErrShortWrite
	}
	w.bytes += n
	_, _ = w.digest.Write(data[:n])
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}
