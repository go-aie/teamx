package lark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/go-aie/teamx/internal/app/platformtools"
)

type toolRuntime struct{ host platformtools.Host }

func BindTool(host platformtools.Host) platformtools.Runtime { return toolRuntime{host} }

func actionHelp(args []string) (string, bool) {
	if len(args) == 2 && args[0] == "actions" && (args[1] == "--help" || args[1] == "-h") {
		return "", true
	}
	if len(args) == 3 && args[0] == "actions" && (args[1] == "resolve" || args[1] == "execute") && (args[2] == "--help" || args[2] == "-h") {
		return args[1], true
	}
	return "", false
}
func (toolRuntime) Help(args []string) bool {
	_, action := actionHelp(args)
	return len(args) == 0 || len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") || action || len(args) == 2 && args[0] == "read" && (args[1] == "--help" || args[1] == "-h")
}
func toolHelp(entry string) string {
	return fmt.Sprintf("Usage:\n  teamx tools run %s read --object REF --target REF [--scope content|context] [--cursor CURSOR]\n  teamx tools run %s actions resolve --request JSON\n  teamx tools run %s actions execute --request JSON\nUse the same entry with actions resolve/execute --help for parameters and examples. Query receipts with teamx actions read --id ID (read-only, including during Session recovery). Arbitrary CLI commands and authentication are unavailable.\n", entry, entry, entry)
}
func invalid(field, message string) error {
	return &platformtools.Diagnostic{Field: field, Message: message}
}
func validRef(s string) bool {
	if !strings.HasPrefix(s, "ref_") {
		return false
	}
	b, err := hex.DecodeString(strings.TrimPrefix(s, "ref_"))
	return err == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}
func (r toolRuntime) Run(ctx context.Context, entry string, args []string) ([]byte, error) {
	if len(args) > 4096 {
		return nil, invalid("", "invalid request")
	}
	if r.Help(args) {
		if op, ok := actionHelp(args); ok {
			return []byte(r.host.ActionHelp(entry, op)), nil
		}
		return []byte(toolHelp(entry)), nil
	}
	var result json.RawMessage
	var err error
	switch args[0] {
	case "read":
		var q platformtools.ReadRequest
		f := flag.NewFlagSet("read", flag.ContinueOnError)
		f.SetOutput(io.Discard)
		f.StringVar(&q.ObjectRef, "object", "", "object_ref")
		f.StringVar(&q.TargetRef, "target", "", "target_ref")
		f.StringVar(&q.Scope, "scope", "content", "content or context")
		f.StringVar(&q.Cursor, "cursor", "", "continuation")
		if f.Parse(args[1:]) != nil || f.NArg() != 0 || !validRef(q.ObjectRef) || !validRef(q.TargetRef) || q.Scope != "content" && q.Scope != "context" {
			return nil, invalid("", "invalid request")
		}
		result, err = r.host.Read(ctx, q)
	case "actions":
		if len(args) < 2 || args[1] != "resolve" && args[1] != "execute" {
			return nil, invalid("command", "expected actions resolve or actions execute; use actions --help")
		}
		f := flag.NewFlagSet("actions "+args[1], flag.ContinueOnError)
		f.SetOutput(io.Discard)
		payload := f.String("request", "", "structured request")
		_ = f.Bool("json", true, "JSON output")
		if err := f.Parse(args[2:]); err != nil {
			return nil, invalid("arguments", err.Error())
		}
		if f.NArg() != 0 {
			return nil, invalid("arguments", "unexpected positional arguments; use --help")
		}
		if args[1] == "resolve" {
			result, err = r.host.Resolve(ctx, json.RawMessage(*payload))
		} else {
			result, err = r.host.Execute(ctx, json.RawMessage(*payload))
		}
	default:
		return nil, invalid("", "invalid request")
	}
	if err != nil {
		return nil, err
	}
	return append(result, '\n'), nil
}
