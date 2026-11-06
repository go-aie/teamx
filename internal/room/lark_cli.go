package room

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func runLarkCLI(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string) error {
	const help = "Usage: teamx lark read --object REF --target REF [--scope content|context] [--cursor CURSOR]\nRead a saved native Lark target using this member's explicit lark binding. Context is bounded to +/-5 minutes and 25 messages per page; follow next when incomplete. Attachments are not read. No reply or arbitrary CLI access.\n"
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return writeOutput(stdout, []byte(help))
	}
	if args[0] != "read" {
		return fmt.Errorf("%s", help)
	}
	f := flag.NewFlagSet("lark read", flag.ContinueOnError)
	f.SetOutput(stderr)
	f.Usage = func() { _ = writeOutput(stdout, []byte(help)) }
	var q larkTargetRequest
	f.StringVar(&q.ObjectRef, "object", "", "object_ref")
	f.StringVar(&q.TargetRef, "target", "", "target_ref")
	f.StringVar(&q.Scope, "scope", "content", "content or context")
	f.StringVar(&q.Cursor, "cursor", "", "continuation from the same target/account")
	_ = f.Bool("json", true, "JSON output")
	if err := f.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || !validOpaqueRef(q.ObjectRef, "ref_") || !validOpaqueRef(q.TargetRef, "ref_") || (q.Scope != "content" && q.Scope != "context") {
		return ErrRequest
	}
	endpoint := strings.TrimRight(env("TEAMX_ENDPOINT"), "/")
	if endpoint == "" {
		return ErrIdentity
	}
	client := cliClient{endpoint: endpoint, http: &http.Client{Timeout: actionClientTimeout}}
	identity, err := client.identity(ctx, "", env)
	if err != nil {
		return err
	}
	if identity.Caller.Kind != "agent" {
		return ErrIdentity
	}
	var page json.RawMessage
	if err = client.request(ctx, "/v1/platform/lark/target", larkReadRequest{ServiceInfo: identity.ServiceInfo, Caller: identity.Caller, Target: &q}, &page); err != nil {
		return err
	}
	return writeJSON(stdout, page)
}
