package lark

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/app/platformtools"
)

type invocationHost struct {
	helps              []bool
	reads              []platformtools.ReadRequest
	resolves, executes []string
	closed             bool
}

func (h *invocationHost) Admit(ctx context.Context, help bool) (context.Context, func(), error) {
	h.helps = append(h.helps, help)
	return ctx, func() { h.closed = true }, nil
}
func (h *invocationHost) ActionHelp(entry, op string) string { return entry + " actions " + op }
func (h *invocationHost) Read(ctx context.Context, q platformtools.ReadRequest) (json.RawMessage, error) {
	h.reads = append(h.reads, q)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"complete":true}`), nil
}
func (h *invocationHost) Resolve(_ context.Context, q json.RawMessage) (json.RawMessage, error) {
	h.resolves = append(h.resolves, string(q))
	return json.RawMessage(`{"target_ref":"original"}`), nil
}
func (h *invocationHost) Execute(_ context.Context, q json.RawMessage) (json.RawMessage, error) {
	h.executes = append(h.executes, string(q))
	return json.RawMessage(`{"status":"unknown"}`), nil
}

// Exercise the application boundary without HTTP, CLI processes or Room internals.
func TestToolInvocationHelpAndOwnerCalls(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}, {"read", "--help"}, {"actions", "--help"}, {"actions", "resolve", "--help"}, {"actions", "execute", "-h"}} {
		h := &invocationHost{}
		call, err := platformtools.Prepare(context.Background(), "platform-entry", args, BindTool, h)
		if err != nil {
			t.Fatal(err)
		}
		data, err := call.Run()
		call.Close()
		if err != nil || !strings.Contains(string(data), "platform-entry") || len(h.helps) != 1 || !h.helps[0] || !h.closed || len(h.reads)+len(h.executes)+len(h.resolves) != 0 {
			t.Fatal(args, string(data), h, err)
		}
	}
	h := &invocationHost{}
	ref := "ref_" + strings.Repeat("a", 64)
	call, err := platformtools.Prepare(context.Background(), "platform-entry", []string{"read", "--object", ref, "--target", ref, "--scope", "context", "--cursor", "continuation", "--json"}, BindTool, h)
	if err != nil {
		t.Fatal(err)
	}
	data, err := call.Run()
	call.Close()
	if err != nil || string(data) != "{\"complete\":true}\n" || len(h.reads) != 1 || h.reads[0].Cursor != "continuation" || h.helps[0] || !h.closed {
		t.Fatal(h, string(data), err)
	}
	for _, operation := range []string{"resolve", "execute"} {
		// The unchanged body reaches the existing owner/strict decoder; it cannot
		// become a different action request or be retried after an unknown outcome.
		body := `{"id":"same-id","input":{"body":"完整正文"}}`
		h := &invocationHost{}
		call, err := platformtools.Prepare(context.Background(), "platform-entry", []string{"actions", operation, "--request", body}, BindTool, h)
		if err != nil {
			t.Fatal(err)
		}
		_, err = call.Run()
		call.Close()
		got := append(h.resolves, h.executes...)
		if err != nil || len(got) != 1 || got[0] != body || h.helps[0] || !h.closed {
			t.Fatal(h, err)
		}
	}
}
func TestToolInvocationRejectsCommandsAndKeepsCancellation(t *testing.T) {
	for _, args := range [][]string{{"api", "POST", "/messages"}, {"read", "--object", "ref_native", "--target", "ref_native"}, {"actions", "execute", "unexpected"}, {"actions", "read", "--id", "receipt"}, {"actions", "execute", "--request-file", "/server/path"}} {
		h := &invocationHost{}
		call, err := platformtools.Prepare(context.Background(), "platform-entry", args, BindTool, h)
		if err != nil {
			t.Fatal(err)
		}
		_, err = call.Run()
		call.Close()
		if err == nil || len(h.reads)+len(h.resolves)+len(h.executes) != 0 || !h.closed {
			t.Fatal(args, h, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := &invocationHost{}
	ref := "ref_" + strings.Repeat("b", 64)
	call, err := platformtools.Prepare(ctx, "platform-entry", []string{"read", "--object", ref, "--target", ref}, BindTool, h)
	if err != nil {
		t.Fatal(err)
	}
	_, err = call.Run()
	call.Close()
	if !errors.Is(err, context.Canceled) || !h.closed {
		t.Fatal(err, h)
	}
}
