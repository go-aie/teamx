package room

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func runInboxCLI(ctx context.Context, command string, args []string, stdout, stderr io.Writer, env func(string) string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return writeOutput(stdout, []byte("Member Inbox and matter commands (JSON output):\n  teamx inbox read [--limit 25] [--peek]\n  teamx matters list [--object OBJECT_KEY] [--after SEQ] [--limit 25]\n  teamx matters update --seq SEQ --id main --version VERSION --turn TURN --status STATUS --summary TEXT [--next TEXT] [--evidence TEXT]\n  teamx matters notify --change JSON\n  teamx matters reply --change JSON\n  teamx matters context\n  teamx matters enrich --change JSON\n  teamx matters relate --change JSON\n  teamx actions --help\nInbox read confirms each fully written page. Use --peek for an unconfirmed page. New concern IDs use --version 0. See actions --help for handling, public questions/results and reply linkage.\n"))
	}
	op, args := args[0], args[1:]
	if command == "inbox" && op != "read" || command == "matters" && op != "list" && op != "update" {
		return ErrRequest
	}
	flags := flag.NewFlagSet(command+" "+op, flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("endpoint", env("TEAMX_ENDPOINT"), "bound Room HTTP endpoint")
	_ = flags.Bool("json", true, "write structured JSON")
	limit, peek, object, after := 25, false, "", uint64(0)
	u := MatterUpdate{}
	if command == "inbox" || op == "list" {
		flags.IntVar(&limit, "limit", 25, "maximum entries in one bounded batch/page (1-25)")
	}
	if command == "inbox" {
		flags.BoolVar(&peek, "peek", false, "read Inbox page without confirming it")
	}
	if op == "list" {
		flags.StringVar(&object, "object", "", "exact object_key from a matter record")
		flags.Uint64Var(&after, "after", 0, "list records after this Inbox sequence")
	}
	if op == "update" {
		flags.Uint64Var(&u.Seq, "seq", 0, "source Inbox sequence")
		flags.StringVar(&u.ID, "id", "main", "stable concern ID; a new ID creates a separate matter")
		flags.Uint64Var(&u.Version, "version", 0, "expected matter version (0 creates)")
		flags.Uint64Var(&u.TurnNumber, "turn", 0, "current execution turn from inbox read")
		flags.StringVar(&u.Status, "status", "", "pending/in_progress/waiting_user/waiting_followup/completed/archived/unknown")
		flags.StringVar(&u.Summary, "summary", "", "verified finding or request")
		flags.StringVar(&u.Next, "next", "", "next step, question or follow-up condition")
		flags.StringVar(&u.Evidence, "evidence", "", "actual read-only verification result and source")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || limit < 1 || limit > 25 {
		return ErrRequest
	}
	client := cliClient{endpoint: strings.TrimRight(*endpoint, "/"), http: &http.Client{Timeout: 10 * time.Second}}
	req, err := client.identity(ctx, env("TEAMX_ROOM_ID"), env)
	if err != nil {
		return err
	}
	if req.Caller.Kind != "agent" {
		return ErrIdentity
	}
	req.Limit, req.ObjectKey, req.After = limit, object, &after
	if command == "matters" {
		req.Matter = &u
		var result any
		if err := client.request(ctx, "/v1/matters/"+op, &req, &result); err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	// Exactly one fixed batch per invocation. New arrivals belong to a later
	// turn. Each page is confirmed only after its entire output succeeds.
	for {
		var page agentInboxBatchPage
		if err := client.request(ctx, "/v1/inbox/read", &req, &page); err != nil {
			return fmt.Errorf("inbox read incomplete: %w", err)
		}
		if err := writeJSON(stdout, page); err != nil {
			return fmt.Errorf("page output incomplete; page not confirmed: %w", err)
		}
		if peek {
			return nil
		}
		confirm := req
		confirm.TurnNumber, confirm.ReadReceipt = page.Batch.TurnNumber, page.Batch.Receipt
		var result InboxStatus
		if err := client.request(ctx, "/v1/inbox/confirm", &confirm, &result); err != nil {
			return fmt.Errorf("page output complete; confirmation failed or uncertain (receipt=%s turn=%d): %w", page.Batch.Receipt, page.Batch.TurnNumber, err)
		}
		if !page.HasMore {
			return nil
		}
		req.TurnNumber = page.Batch.TurnNumber
	}
}
