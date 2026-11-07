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

func runReferenceCLI(ctx context.Context, command string, args []string, stdin io.Reader, stdout, stderr io.Writer, env func(string) string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return writeOutput(stdout, []byte(referenceHelpText(command, "")))
	}
	op := args[0]
	allowed := map[string]string{"inbox": "read", "matters": "list read sources source resolve ensure update notify reply instruct instruction-status", "actions": "resolve execute read list", "policy": "read update publish"}
	if !strings.Contains(" "+allowed[command]+" ", " "+op+" ") {
		return invalidField("command", "unknown subcommand; use teamx "+command+" --help")
	}
	f := flag.NewFlagSet(command+" "+op, flag.ContinueOnError)
	f.SetOutput(stderr)
	f.Usage = func() { _ = writeOutput(stdout, []byte(referenceHelpText(command, op))) }
	endpoint := f.String("endpoint", env("TEAMX_ENDPOINT"), "bound Room endpoint")
	payloadName := "change"
	if command == "actions" {
		payloadName = "request"
	}
	payload := f.String(payloadName, "", "structured request; see --help")
	var payloadFile string
	if referencePayloadFlag(command, op) != "" {
		f.StringVar(&payloadFile, payloadName+"-file", "", "JSON file path; - reads stdin (exclusive with --"+payloadName+")")
	}
	_ = f.Bool("json", true, "JSON output")
	q := followupRequest{}
	f.StringVar(&q.ObjectRef, "object", "", "stable object_ref")
	f.StringVar(&q.UnitRef, "unit", "", "stable unit_ref")
	f.StringVar(&q.TargetRef, "target", "", "verified target_ref")
	f.StringVar(&q.SourceRef, "source", "", "read-only source_ref")
	f.Uint64Var(&q.Seq, "seq", 0, "source Inbox sequence")
	f.Uint64Var(&q.TurnNumber, "turn", 0, "current_turn")
	f.StringVar(&q.Cursor, "cursor", "", "query-scoped continuation")
	f.BoolVar(&q.Detail, "detail", false, "include execution or policy source details")
	f.BoolVar(&q.IncludeCompleted, "include-completed", false, "include completed history")
	f.StringVar(&q.ID, "id", "", "policy key")
	f.Uint64Var(&q.Version, "version", 0, "expected policy version")
	limit := f.Int("limit", 25, "page limit 1-25")
	after := f.Uint64("after", 0, "source continuation sequence")
	peek := f.Bool("peek", false, "do not confirm Inbox output")
	if err := f.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return invalidField("arguments", "unexpected positional arguments; use --help")
	}
	provided := map[string]bool{}
	f.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	supported := referenceCLIFlags(command, op)
	for name := range provided {
		if !strings.Contains(" "+supported+" ", " "+name+" ") {
			return invalidField("--"+name, "not supported by this subcommand; use --help")
		}
	}
	if q.Detail && command == "policy" && q.ID == "" {
		return invalidField("--detail", "requires --id")
	}
	if *limit < 1 || *limit > 25 {
		return invalidField("limit", "must be between 1 and 25")
	}
	timeout := 10 * time.Second
	if command == "actions" || command == "inbox" || op == "resolve" {
		timeout = actionClientTimeout
	}
	var req apiRequest
	req.Followup = &q
	req.TurnNumber = q.TurnNumber
	switch command + "/" + op {
	case "inbox/read", "matters/list", "actions/list":
		req.Limit = *limit
	case "matters/sources":
		req.Limit, req.After = *limit, after
	}
	var target any
	var progress agentProgressChange
	var instructionStatus agentInstructionStatusChange
	switch command + "/" + op {
	case "actions/resolve":
		req.ActionResolve = &actionResolveRequest{}
		target = req.ActionResolve
	case "actions/execute":
		req.ReferenceAction = &agentActionRequest{}
		target = req.ReferenceAction
	case "matters/notify":
		req.Question = &followupQuestionChange{}
		target = req.Question
	case "matters/reply":
		req.FollowupReply = &followupReplyChange{}
		target = req.FollowupReply
	case "matters/instruct":
		req.Instruction = &followupInstructionChange{}
		target = req.Instruction
	case "policy/update":
		req.ConversationPolicy = &agentPolicyChange{}
		target = req.ConversationPolicy
	case "matters/update":
		req.Progress = &progress
		req.Followup = nil
		target = &progress
	case "matters/instruction-status":
		req.InstructionStatus = &instructionStatus
		req.Followup = nil
		target = &instructionStatus
	}
	if target != nil {
		req.Followup = nil
		provided := map[string]bool{}
		f.Visit(func(f *flag.Flag) { provided[f.Name] = true })
		if provided[payloadName] && provided[payloadName+"-file"] {
			return invalidField("--"+payloadName+"-file", "mutually exclusive with --"+payloadName)
		}
		inputFlag := "--" + payloadName
		input := *payload
		if provided[payloadName+"-file"] {
			inputFlag += "-file"
			var err error
			input, err = readCLIJSONFile(payloadFile, stdin)
			if err != nil {
				return fmt.Errorf("%s: %w", inputFlag, err)
			}
		}
		if err := decodeRequestJSON(input, target); err != nil {
			return fmt.Errorf("%s: %w", inputFlag, err)
		}
	} else if *payload != "" {
		return invalidField("--"+payloadName, "not supported by this subcommand; use --help")
	}
	client := cliClient{endpoint: strings.TrimRight(*endpoint, "/"), http: &http.Client{Timeout: timeout}}
	identity, err := client.identity(ctx, env("TEAMX_ROOM_ID"), env)
	if err != nil {
		return err
	}
	if identity.Caller.Kind != "agent" {
		return ErrIdentity
	}
	req.ServiceInfo, req.Caller = identity.ServiceInfo, identity.Caller

	if command != "inbox" {
		var result any
		if err := client.request(ctx, "/v1/"+command+"/"+op, &req, &result); err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	for {
		var page agentReferenceInboxPage
		if err := client.request(ctx, "/v1/inbox/read", &req, &page); err != nil {
			return fmt.Errorf("inbox read incomplete: %w", err)
		}
		output := page
		output.Batch.Receipt = ""
		if err := writeJSON(stdout, output); err != nil {
			return fmt.Errorf("page output incomplete; page not confirmed: %w", err)
		}
		if *peek || len(page.Entries) == 0 {
			return nil
		}
		confirm := req
		confirm.TurnNumber, confirm.ReadReceipt = page.Batch.TurnNumber, page.Batch.Receipt
		var status InboxStatus
		if err := client.request(ctx, "/v1/inbox/confirm", &confirm, &status); err != nil {
			return fmt.Errorf("page output complete; confirmation failed or uncertain (receipt=%s turn=%d): %w", page.Batch.Receipt, page.Batch.TurnNumber, err)
		}
		if !page.HasMore {
			return nil
		}
		req.TurnNumber = page.Batch.TurnNumber
	}
}

func referenceCLIFlags(command, op string) string {
	common := "endpoint json"
	if name := referencePayloadFlag(command, op); name != "" {
		return common + " " + name + " " + name + "-file"
	}
	return common + " " + map[string]string{
		"inbox/read": "limit peek", "matters/list": "object cursor include-completed limit", "matters/read": "object unit turn detail",
		"matters/sources": "object after limit", "matters/source": "seq source", "matters/resolve": "seq source turn", "matters/ensure": "seq target turn",
		"actions/read": "id object unit detail", "actions/list": "object unit cursor limit",
		"policy/read": "id detail", "policy/publish": "id version turn",
	}[command+"/"+op]
}
