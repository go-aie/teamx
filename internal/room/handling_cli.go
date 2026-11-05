package room

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"strings"
	"time"
)

const handlingHelp = `Member actions and matter communication (JSON output):
  teamx actions execute --request JSON
  teamx matters notify --change JSON
  teamx matters reply --change JSON
Use runtime-supplied policy_token/current_turn and the effective decisions returned with inbox read or matters list. Restore missing policy body with policy read only as needed.
Execute (GitLab MR ordinary comments on existing Inbox matters only): seq, matter_id, matter_version, turn_number, id, policy_token, authority {reason, ONE of rule_id+rule_version / decision_id+decision_version / room_seq}, verification, operation:"gitlab.mr.comment", target {project_id:"native REST ID",mr_iid:"project-local IID"}, input {body:"exact comment"}.
The server resolves the bound account, verifies native object identities, saves intent BEFORE dispatch, and records the actual outcome. policy_changed includes the complete effective view: reassess before a new request. A logical comment makes at most two attempts on transient failures; replies may duplicate after a lost response. Cancellation, permission/input denial and stale authority stop attempts. Same ID/content returns the saved receipt (or processing), even with an old turn/version. Changed content under the same ID is rejected. Unknown results are never automatically resumed, including with a new ID; inspect remote state and report findings. Unrelated work may continue. Success is distinct from business completion; do not complete work dependent on an unknown result.
Notify/reply require seq, matter_id, matter_version, turn_number from current records.
Notify: id, kind (question/result/alert), text. Questions must mention the human operator. Reuse the same id and text to reconcile a pending public send; never resend via ordinary send.
Reply: question_id, source_seq, basis. Copies a real human Room reply; linkage alone grants no permission.
Only adapted MR comments use execute. Other tool capabilities remain under tools run; no general write adapter or enforced bypass prevention is provided.
`

func runHandlingCLI(ctx context.Context, command string, args []string, stdout, stderr io.Writer, env func(string) string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return writeOutput(stdout, []byte(handlingHelp))
	}
	op := args[0]
	if command == "actions" && op != "execute" || command == "matters" && op != "notify" && op != "reply" {
		return ErrRequest
	}
	f := flag.NewFlagSet(command+" "+op, flag.ContinueOnError)
	f.SetOutput(stderr)
	endpoint := f.String("endpoint", env("TEAMX_ENDPOINT"), "bound Room endpoint")
	payloadFlag := "change"
	if command == "actions" {
		payloadFlag = "request"
	}
	change := f.String(payloadFlag, "", "structured member business request")
	_ = f.Bool("json", true, "JSON output")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return ErrRequest
	}
	timeout := 10 * time.Second
	if command == "actions" {
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
	var target any
	switch command + "/" + op {
	case "actions/execute":
		req.ActionRequest = &ActionRequest{}
		target = req.ActionRequest
	case "matters/notify":
		req.Notice = &NoticeChange{}
		target = req.Notice
	case "matters/reply":
		req.MatterReply = &MatterReplyChange{}
		target = req.MatterReply
	}
	dec := json.NewDecoder(strings.NewReader(*change))
	dec.DisallowUnknownFields()
	if dec.Decode(target) != nil || dec.Decode(new(any)) != io.EOF {
		return ErrRequest
	}
	if command == "actions" {
		var result ActionReceipt
		if err := client.request(ctx, "/v1/actions/execute", &req, &result); err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	var result agentMatterRecord
	if err := client.request(ctx, "/v1/"+command+"/"+op, &req, &result); err != nil {
		return err
	}
	return writeJSON(stdout, result)
}
