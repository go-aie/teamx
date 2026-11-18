package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"github.com/mark3labs/mcp-go/mcp"
	"io"
	"net/http"
	"strings"
	"time"
)

type inputCLIEnvelope struct {
	ServiceInfo
	Caller    Caller          `json:"caller"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
	Execution string          `json:"execution"`
	Call      string          `json:"call"`
}

func runInputHTTPCLI(ctx context.Context, args []string, out io.Writer, env func(string) string) error {
	client := cliClient{endpoint: strings.TrimRight(env("TEAMX_ENDPOINT"), "/"), http: &http.Client{Timeout: time.Minute}}
	id, err := client.identity(ctx, env("TEAMX_ROOM_ID"), env)
	if err != nil {
		return err
	}
	var binding struct {
		Token string `json:"token"`
		Call  string `json:"call"`
	}
	if json.Unmarshal([]byte(env("DSH_TEAMX_INPUT_BINDING")), &binding) != nil || binding.Token == "" || binding.Call == "" {
		return ErrIdentity
	}
	suffix, err := randomAccessToken()
	if err != nil {
		return err
	}
	flush := func() error {
		if f, ok := out.(interface{ Flush() error }); ok {
			return f.Flush()
		}
		return nil
	}
	return runInputCLI(ctx, append([]string{"inbox"}, args...), func(ctx context.Context, name string, data []byte) error {
		req := inputCLIEnvelope{ServiceInfo: id.ServiceInfo, Caller: id.Caller, Tool: name, Arguments: data, Execution: binding.Token, Call: binding.Call + "/" + suffix}
		var result json.RawMessage
		if err := client.request(ctx, "/v1/inputs", &req, &result); err != nil {
			return err
		}
		if err := writeInputJSON(out, flush, result); err != nil {
			return err
		}
		return nil
	})
}
func (s *service) serveInputCLI(w http.ResponseWriter, r *http.Request) {
	var req inputCLIEnvelope
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if !decodeToolRequest(w, r, &req) {
		return
	}
	if !s.unifiedInputs || s.inboxMode != "cli" || s.access == nil || req.ServiceInfo != s.info || req.Caller.Kind != "agent" {
		writeAPIError(w, ErrIdentity)
		return
	}
	s.access.mu.Lock()
	input := s.access.inputBindings[sha256.Sum256([]byte(req.Execution))]
	s.access.mu.Unlock()
	if input == nil || input.service != s || agentCallerIdentity(input.storage.id) != req.Caller || input.check(r.Context()) != nil {
		writeAPIError(w, ErrIdentity)
		return
	}
	if req.Tool == "list" {
		v, err := input.listInputStreams(r.Context(), req.Arguments)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(v)
		return
	}
	if req.Tool != "read" {
		writeAPIError(w, ErrRequest)
		return
	}
	result, _, release, err := input.readStream(r.Context(), req.Call, req.Arguments)
	if release != nil {
		defer release()
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var view map[string]any
	block, ok := result.Content[0].(mcp.TextContent)
	decoder := json.NewDecoder(bytes.NewReader([]byte(block.Text)))
	decoder.UseNumber()
	if !ok || decoder.Decode(&view) != nil {
		writeAPIError(w, ErrInvalid)
		return
	}
	if len(result.Content) > 1 {
		view["content"] = result.Content[1:]
	}
	_ = json.NewEncoder(w).Encode(view)
}
