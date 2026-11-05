package room

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const referenceHelp = `Member object/discussion followups (schema 2, JSON output):
  teamx inbox read [--limit 25] [--peek]
  teamx matters list [--object REF] [--cursor CURSOR] [--include-completed] [--limit 25]
  teamx matters read --object REF --unit UNIT --turn TURN
  teamx matters sources [--object REF] [--after SEQ] [--limit 25]
  teamx matters source --seq SEQ --source SRC
  teamx matters resolve --seq SEQ --source SRC --turn TURN
  teamx matters ensure --seq SEQ --target REF --turn TURN
  teamx matters update --change JSON
  teamx matters notify --change JSON
  teamx matters reply --change JSON
  teamx matters instruct --change JSON
  teamx matters instruction-status --change JSON
  teamx actions execute --request JSON
  teamx policy read [--id ID]
  teamx policy update --change JSON
  teamx policy publish --id ID --version VERSION --turn TURN
Inbox pages contain original entries, source_ref/target_ref and deduplicated followups. Full output is confirmed separately; --peek never confirms. Unresolved sources remain discoverable in matters list; resolve explicitly retries one source. No semantic split/merge/enrich/relate commands exist.
Read returns unit, target and registered_actions (target kind, supported object types, input schema and retry policy; current binding/permissions are checked on execute); sources/source retain immutable delivery and resolution evidence. Agent read associates explicit human reply_to automatically. Reuse returned revision as unit_version after each mutation. Discussion ensure is on demand using a verified native target_ref from a source. No caller-defined unit IDs.
All unit changes carry object_ref, unit_ref, unit_version, turn_number.
Update: status, summary, next, evidence. States: pending/in_progress/waiting_user/waiting_followup/completed/archived/unknown. Completion requires evidence and no unresolved questions/instructions/actions.
Notify: text (mention the human operator), message_refs (verified native message refs), proposed {operation,body or constraints}. Saves a stable question_ref BEFORE public send; identical requests reconcile the same question and Room receipt. Important results use send with a stable ID and save evidence in update. Routine results stay private.
Reply: question_ref, source_seq, basis. Without reply_to, associate only unambiguous real human context. Read before asking again. For an answered question, authority.room_seq is unit.questions[].answers[].message.seq (the human answer), NOT the question's room_seq (the Agent's public question).
Instruct: question_ref (optional for a direct human task), source_seq, message_refs, intent {operation,body or constraints}, basis, expires_at (optional). Only persist when work spans turns; an immediate exact human answer needs no separate decision. instruction-status: id (instruction_ref), version, status (fulfilled/revoked), evidence, source_seq (human revocation only).
Execute: object_ref, unit_ref, unit_version, turn_number, target_ref, stable id, policy_token, authority {reason, ONE of rule_id+rule_version / instruction_ref+instruction_version / room_seq; optional question_ref,message_refs,constraints}, operation (object.comment or discussion.reply), input {body}. Native project/MR/discussion IDs are rejected. Use supported Inbox-derived targets from registered_actions; current production GitLab writes support MR targets only. Do not fabricate Inbox sequences. Discussion replies require verified discussion identity and never fall back to ordinary comments. Current account, binding, target, scope and authority are rechecked. Intent is saved before dispatch; actual account/target/result ID/attempts are retained. policy_changed returns the complete effective view: reassess scope before continuing.
Ordinary comments allow at most two server attempts on transient failures; discussion replies allow one. Lost responses may duplicate. Same ID/content reads existing state even after rotation; changed content is rejected. Never retry unknown under a new ID/turn/Session. Inspect remote state and report findings. Success, business completion, Inbox consumption and Todo completion are separate.
Policy update: {change:{id,revision,version,kind,status,text,scope,actions,conditions,exceptions,source_seq,turn_number,...},mode,basis,proposal_seq?}. scope uses description/provider/instance/object_type and either account (verified label) or account_id {namespace,value} with exact tenant {namespace,value}; use project for a verified container scope, or no_containers:true only for objects with no parent containers. Omission is not a wildcard. mode explicit_long_term directly saves explicit lasting human intent; inferred_habit saves suggested only; confirm_suggestion activates the exact published suggestion/version using the real human reply and proposal_seq. Publish returns the stable Room receipt for a saved suggestion (id is the saved suggestion ID; policy read --id ID restores that exact record). Human originals are read server-side; never submit source_text. Preferences confer no authority. One-off consent never activates a standing rule. UI is optional management; users normally answer and manage rules in Room conversation.
Use runtime policy_token/current_turn. Restore missing effective body with policy read. Authority remains scoped to the accurate target/messages. Frozen older instructions are superseded by this contract. Unadapted capabilities remain tools run under actual user authority.
`

func runReferenceCLI(ctx context.Context, command string, args []string, stdout, stderr io.Writer, env func(string) string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return writeOutput(stdout, []byte(referenceHelp))
	}
	op := args[0]
	allowed := map[string]string{"inbox": "read", "matters": "list read sources source resolve ensure update notify reply instruct instruction-status", "actions": "execute", "policy": "read update publish"}
	if !strings.Contains(" "+allowed[command]+" ", " "+op+" ") {
		return ErrRequest
	}
	f := flag.NewFlagSet(command+" "+op, flag.ContinueOnError)
	f.SetOutput(stderr)
	f.Usage = func() { _ = writeOutput(stdout, []byte(referenceHelp)) }
	endpoint := f.String("endpoint", env("TEAMX_ENDPOINT"), "bound Room endpoint")
	payloadName := "change"
	if command == "actions" {
		payloadName = "request"
	}
	payload := f.String(payloadName, "", "structured request; see --help")
	_ = f.Bool("json", true, "JSON output")
	q := followupRequest{}
	f.StringVar(&q.ObjectRef, "object", "", "stable object_ref")
	f.StringVar(&q.UnitRef, "unit", "", "stable unit_ref")
	f.StringVar(&q.TargetRef, "target", "", "verified target_ref")
	f.StringVar(&q.SourceRef, "source", "", "read-only source_ref")
	f.Uint64Var(&q.Seq, "seq", 0, "source Inbox sequence")
	f.Uint64Var(&q.TurnNumber, "turn", 0, "current_turn")
	f.StringVar(&q.Cursor, "cursor", "", "query-scoped continuation")
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
	if f.NArg() != 0 || *limit < 1 || *limit > 25 {
		return ErrRequest
	}
	timeout := 10 * time.Second
	if command == "actions" || command == "inbox" || op == "resolve" {
		timeout = actionClientTimeout
	}
	client := cliClient{endpoint: strings.TrimRight(*endpoint, "/"), http: &http.Client{Timeout: timeout}}
	req, err := client.identity(ctx, env("TEAMX_ROOM_ID"), env)
	if err != nil {
		return err
	}
	if req.Caller.Kind != "agent" {
		return ErrIdentity
	}
	req.Limit, req.After, req.TurnNumber = *limit, after, q.TurnNumber
	req.Followup = &q
	var target any
	switch command + "/" + op {
	case "actions/execute":
		req.ReferenceAction = &referenceActionRequest{}
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
		req.ConversationPolicy = &conversationPolicyChange{}
		target = req.ConversationPolicy
	case "matters/update", "matters/instruction-status":
		target = &q
	}
	if target != nil {
		dec := json.NewDecoder(strings.NewReader(*payload))
		dec.DisallowUnknownFields()
		if dec.Decode(target) != nil || dec.Decode(new(any)) != io.EOF {
			return ErrRequest
		}
	} else if *payload != "" {
		return ErrRequest
	}
	if command != "inbox" {
		var result any
		if err := client.request(ctx, "/v1/"+command+"/"+op, &req, &result); err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	for {
		var page referenceInboxPage
		if err := client.request(ctx, "/v1/inbox/read", &req, &page); err != nil {
			return fmt.Errorf("inbox read incomplete: %w", err)
		}
		if err := writeJSON(stdout, page); err != nil {
			return fmt.Errorf("page output incomplete; page not confirmed: %w", err)
		}
		if *peek {
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
