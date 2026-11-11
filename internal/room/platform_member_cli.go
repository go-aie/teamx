package room

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func runPlatformMemberCLI(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string, h *http.Client) error {
	action := args[0]
	args = args[1:]
	if action == "member" {
		if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
			return writeOutput(stdout, []byte(platformConnectHelp))
		}
		action, args = args[0], args[1:]
		if action != "get" && action != "notifications" && action != "tool" && action != "status" {
			return ErrRequest
		}
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return writeOutput(stdout, []byte(platformConnectHelp))
	}
	f := flag.NewFlagSet("platforms "+action, flag.ContinueOnError)
	f.SetOutput(stderr)
	f.Usage = func() { _ = writeOutput(stdout, []byte(platformConnectHelp)) }
	req := platformMemberRequest{}
	in := platformMemberInput{}
	if action != "status" {
		f.StringVar(&in.PlatformID, "platform-id", "", "exact verified connection ID")
		f.Uint64Var(&in.PlatformVersion, "platform-version", 0, "observed connection version")
		req.Input = &in
	}
	if action == "status" || action == "notifications" || action == "tool" {
		f.StringVar(&req.OperationID, "operation-id", "", "stable configuration operation ID")
		f.Uint64Var(&req.SourceSeq, "source-seq", 0, "original human Room message sequence")
	}
	if action == "chats" {
		f.StringVar(&req.Query, "query", "", "group name search (optional)")
		f.StringVar(&req.PageToken, "page-token", "", "exact continuation token")
	}
	if action == "notifications" || action == "tool" {
		// A value is required so omitted and explicit false remain distinct.
		f.Func("enabled", "true or false", func(s string) error {
			if s != "true" && s != "false" {
				return ErrRequest
			}
			v, err := strconv.ParseBool(s)
			in.Enabled = &v
			return err
		})
	}
	if action == "notifications" {
		f.Uint64Var(&in.NotificationVersion, "notification-version", 0, "observed subscription version (0 if absent)")
		f.Func("add-chat-id", "exact selected group ID; repeat to add multiple groups", func(id string) error { in.AddChatIDs = append(in.AddChatIDs, id); return nil })
		f.StringVar(&in.GroupMessageMode, "group-message-mode", "", "all or mentions; omitted preserves existing mode")
	}
	if action == "tool" {
		f.Uint64Var(&in.ToolRevision, "tool-revision", 0, "observed Room tool revision")
	}
	_ = f.Bool("json", true, "JSON output")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return ErrRequest
	}
	endpoint := strings.TrimRight(env("TEAMX_ENDPOINT"), "/")
	if endpoint == "" {
		return fmt.Errorf("platform member commands require bound Agent context")
	}
	client := cliClient{endpoint: endpoint, http: h}
	identity, err := client.identity(ctx, "", env)
	if err != nil {
		return err
	}
	if identity.Caller.Kind != "agent" {
		return ErrIdentity
	}
	req.ServiceInfo, req.Caller = identity.ServiceInfo, identity.Caller
	path := "/v1/platforms/member/" + action
	if action == "chats" {
		path = "/v1/platforms/chats"
	}
	var result json.RawMessage // Preserve uint64 source sequences and revisions exactly.
	if err := client.request(ctx, path, req, &result); err != nil {
		return fmt.Errorf("operation_id=%s source_seq=%d; query member status and get before retry: %w", req.OperationID, req.SourceSeq, err)
	}
	return writeJSON(stdout, result)
}
