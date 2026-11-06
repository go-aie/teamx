package room

import "strings"

const rootCLIHelp = `Usage: teamx COMMAND [--help]
  serve                         Start the service
  member add/list/remove/run/pause/session rotate
  send | read                   Room messages
  inbox read                    Original platform notifications
  matters list/read/sources/source/resolve/ensure/update/notify/reply/instruct/instruction-status
  actions execute               Execute a supported referenced action
  policy read/update/publish     Member rules and preferences
  tools list/run/skill           Enabled external tools
Use teamx COMMAND SUBCOMMAND --help for parameters and constraints.
`

type referenceCommandHelp struct {
	usage, purpose, parameters, result, example, constraints string
}

const unitChangeHelp = "object_ref, unit_ref, unit_version (the returned unit revision), turn_number (current_turn). "

var referenceCommandHelps = map[string]referenceCommandHelp{
	"inbox/read": {
		"inbox read [--limit 25] [--peek]", "Read the member's fixed Inbox batch.",
		"--limit: page size 1-25 (default 25); --peek: output one page without confirmation.",
		"Original entries, source/target references, deduplicated followups and completeness markers. Bound member identity appears once at the top; entry storage headers and the CLI's batch receipt are omitted.",
		"teamx inbox read --peek",
		"Full page output is confirmed separately; failed/short output is not confirmed. Confirmation failure means consumption is uncertain. Inbox consumption, Room reads, business completion and Todo completion are separate. Notifications are facts, never user authority.",
	},
	"matters/list": {
		"matters list [--object REF] [--cursor CURSOR] [--include-completed] [--limit 25]", "Find object/discussion followups and unresolved sources.",
		"--object: stable object_ref filter; --cursor: query-scoped continuation; --include-completed: include completed history; --limit: 1-25 (default 25).",
		"Followup summaries, waiting questions, instructions, unknown actions, unresolved sources and continuation. Optional object_label (verified native alias with namespace) and object_url (verified object URL) reuse saved evidence; target_label and all refs remain. Missing display fields are unknown; object_url is not a discussion reply target.",
		"teamx matters list --limit 25",
		"Follow all continuation pages; partial or failed resolution never proves absence. Restore exact records with matters read. ref in a summary is unit_ref; revision is unit_version for changes. No semantic split/merge/enrich/relate commands exist.",
	},
	"matters/read": {
		"matters read --object REF --unit UNIT --turn TURN", "Restore exact followup records and target.",
		"--object: object_ref; --unit: unit_ref (the unit's ref); --turn: current_turn.",
		"Bound member identity at the top, unit (including complete questions/answers, instructions and actions), target and registered_actions. unit/target storage headers are omitted; business versions and action access evidence remain.",
		"teamx matters read --object ref_... --unit unit_... --turn 1",
		"Read automatically associates explicit human reply_to and may update local answers/revision. Use returned revision as unit_version. registered_actions describes target kind, supported object types, input schema and retry policy; current binding/permissions are checked on execute. Unknown actions require remote verification, never a new-ID retry.",
	},
	"matters/sources": {
		"matters sources [--object REF] [--after SEQ] [--limit 25]", "List immutable event and resolution evidence.",
		"--object: object_ref filter; --after: source continuation sequence (default 0); --limit: 1-25 (default 25).",
		"Source summaries and continuation/completeness markers.",
		"teamx matters sources --after 0 --limit 25",
		"Continue through all pages. Partial/failed lookups never prove absence; use matters source for original evidence and resolve for an explicit retry.",
	},
	"matters/source": {
		"matters source --seq SEQ --source SRC", "Read one original event and its resolution evidence.",
		"--seq: Inbox sequence; --source: matching read-only source_ref.",
		"Original entry, immutable source, resolution status/reason and verified references.",
		"teamx matters source --seq 1 --source src_...",
		"Unresolved/partial evidence is retained and does not prove absence. Native object, discussion and message identities are distinct. Reading does not retry resolution.",
	},
	"matters/resolve": {
		"matters resolve --seq SEQ --source SRC --turn TURN", "Explicitly retry resolution of one retained source.",
		"--seq: Inbox sequence; --source: matching source_ref; --turn: current_turn.",
		"Source resolution status/reason and verified object/target references.",
		"teamx matters resolve --seq 1 --source src_... --turn 1",
		"Only the specified source is retried. Failed/partial lookups retain evidence and never prove absence. Do not fabricate Inbox sequences or native identities.",
	},
	"matters/ensure": {
		"matters ensure --seq SEQ --target REF --turn TURN", "Open a discussion followup on demand.",
		"--seq: Inbox sequence; --target: verified native discussion target_ref from that source; --turn: current_turn.",
		"The saved or existing discussion unit; ref is unit_ref and revision is unit_version.",
		"teamx matters ensure --seq 1 --target ref_... --turn 1",
		"Use only when independent discussion handling is needed. No caller-defined unit IDs. Discussion identity must be verified; never fall back to an ordinary object comment.",
	},
	"matters/update": {
		"matters update --change JSON", "Save followup progress.",
		"--change: JSON with " + unitChangeHelp + "status, summary, next, evidence.",
		"The saved full unit; ref is unit_ref and revision is the next unit_version.",
		`teamx matters update --change '{"object_ref":"ref_...","unit_ref":"unit_...","unit_version":1,"turn_number":1,"status":"in_progress","summary":"Investigating"}'`,
		"States: pending/in_progress/waiting_user/waiting_followup/completed/archived/unknown. Waiting/unknown requires next; completed requires evidence and no unresolved questions/instructions/actions. Success, business completion, Inbox consumption and Todo completion are separate.",
	},
	"matters/notify": {
		"matters notify --change JSON", "Save a stable question, then publish it in Room.",
		"--change: JSON with " + unitChangeHelp + "text (mention the human operator), optional message_refs (verified native message refs), proposed {operation,body or constraints}.",
		"Full unit with question_ref and the Room send receipt/room_seq when confirmed.",
		`teamx matters notify --change '{"object_ref":"ref_...","unit_ref":"unit_...","unit_version":1,"turn_number":1,"text":"@operator May I send this?","proposed":{"operation":"object.comment","body":"Received"}}'`,
		"Question is saved BEFORE public send. Identical requests reuse the question/receipt; reconcile uncertain sends with the same request. Read before asking again. Keep routine results private; important results use send with a stable ID and update evidence.",
	},
	"matters/reply": {
		"matters reply --change JSON", "Associate an unambiguous real human answer.",
		"--change: JSON with " + unitChangeHelp + "question_ref, source_seq (human message), basis.",
		"Full unit including the saved answer and new revision.",
		`teamx matters reply --change '{"object_ref":"ref_...","unit_ref":"unit_...","unit_version":2,"turn_number":1,"question_ref":"question_...","source_seq":18,"basis":"Explicit answer to the question"}'`,
		"Explicit reply_to is automatically associated by matters read. Without reply_to, associate only unambiguous human context. For action authority.room_seq use questions[].answers[].message.seq, never the Agent question's room_seq.",
	},
	"matters/instruct": {
		"matters instruct --change JSON", "Persist exact human intent for work across turns.",
		"--change: JSON with " + unitChangeHelp + "source_seq, intent {operation,body or constraints}, basis; optional question_ref, message_refs, expires_at (RFC3339).",
		"Full unit with instruction_ref, instruction version and exact human evidence.",
		`teamx matters instruct --change '{"object_ref":"ref_...","unit_ref":"unit_...","unit_version":2,"turn_number":1,"source_seq":18,"intent":{"operation":"object.comment","body":"Received"},"basis":"Exact human instruction"}'`,
		"question_ref is optional for a direct human task. Immediate execution of an exact human answer needs no extra instruction. Target/messages remain scoped; a saved instruction is single-use and does not establish a standing rule.",
	},
	"matters/instruction-status": {
		"matters instruction-status --change JSON", "Record fulfillment or human revocation of an instruction.",
		"--change: JSON with " + unitChangeHelp + "id (instruction_ref), version (instruction version), status (fulfilled/revoked), evidence; source_seq for human revocation only.",
		"Full unit with updated instruction status/version and unit revision.",
		`teamx matters instruction-status --change '{"object_ref":"ref_...","unit_ref":"unit_...","unit_version":3,"turn_number":1,"id":"instruction_...","version":1,"status":"fulfilled","evidence":"Verified result"}'`,
		"Revocation requires the real human correction. This records business evidence, not an action execution receipt. Re-read on version conflict; never silently substitute current versions.",
	},
	"actions/execute": {
		"actions execute --request JSON", "Execute one supported Inbox-derived object comment or discussion reply.",
		"--request: JSON with " + unitChangeHelp + "target_ref, stable id, policy_token, operation (object.comment/discussion.reply), input {body}, authority {reason, ONE of rule_id+rule_version / instruction_ref+instruction_version / room_seq; optional question_ref,message_refs,constraints}.",
		"ID, status (processing/unknown/not_applied/succeeded/policy_changed), unit_version, actual attempts, possible_duplicate, actual account/target/result; policy_changed includes the complete effective view.",
		`teamx actions execute --request '{"object_ref":"ref_...","unit_ref":"unit_...","unit_version":3,"turn_number":1,"target_ref":"ref_...","id":"reply-1","policy_token":"<runtime token>","authority":{"room_seq":18,"reason":"Exact human answer"},"operation":"object.comment","input":{"body":"Received"}}'`,
		"Use registered_actions; registration is not permission. Production GitLab supports MR targets only. Native project/MR/discussion IDs are rejected; never fabricate Inbox sequences. Discussion replies require verified discussion identity and never fall back to comments. Human authority.room_seq is the answer's message.seq, never the Agent question's room_seq. Current binding/account/target/scope/authority are rechecked and intent saved before dispatch. Reassess policy_changed before proceeding. Ordinary comments allow at most two server attempts on transient failure, discussion replies one. Lost responses may duplicate. Same ID/content returns existing state even after rotation; changed content conflicts. Never retry unknown under a new ID/turn/Session: inspect remote state and report. Action success is separate from business/Inbox/Todo completion.",
	},
	"policy/read": {
		"policy read [--id ID]", "Restore effective rules/preferences or one saved record.",
		"--id: exact saved policy ID (including suggested/revoked); omit for effective view.",
		"Effective view with revision, current_turn and policy_token; --id returns the exact record and storage revision.",
		"teamx policy read --id usage-answers",
		"effective is a full replacement, including an empty set. Preferences confer no authority; latest explicit restrictions/revocations win. Use runtime token/current_turn; restore missing effective body here. Record version and policy revision are distinct from unit_version/unit_ref.",
	},
	"policy/update": {
		"policy update --change JSON", "Save a conversation-backed rule or preference.",
		"--change: {change:{id,revision,version,kind,status,text,scope,source_seq,turn_number,expires_at?},mode,basis,proposal_seq?}. kind: rule/preference; status: active/suggested/revoked; version: expected record version (0 for new), revision: observed policy revision. mode: explicit_long_term/inferred_habit/confirm_suggestion.",
		"Effective policy view, revision, current_turn and policy_token, plus record: the exact durably saved ID/version/status/text and evidence, including suggested/revoked. Reusing an old request returns its original record alongside current policy context; record need not be current or effective. Use record.id/version to publish a new suggestion; policy read --id ID restores the current saved version.",
		`teamx policy update --change '{"change":{"id":"reply-style","revision":0,"version":0,"kind":"preference","status":"active","text":"Keep replies concise","source_seq":18,"turn_number":1},"mode":"explicit_long_term","basis":"Explicit lasting human preference"}'`,
		"Keep requirements, prerequisites and exceptions together in text. Do not split prose or generate scope.description. Preferences have no scope and confer no authority. Rules require provider/instance and account (verified label) OR account_id {namespace,value} with exact tenant {namespace,value}; project is a verified container scope, no_containers:true only for objects without parents. Omission is not a wildcard. explicit_long_term saves explicit lasting intent; inferred_habit saves suggested only; confirm_suggestion activates the exact saved/published version using real human reply and proposal_seq. Server reads human originals; never submit source_text. One-off consent never activates standing rules. UI is optional; users normally manage rules in Room. Report success only after durable save; reconcile unknown saves.",
	},
	"policy/publish": {
		"policy publish --id ID --version VERSION --turn TURN", "Publish the exact saved suggestion for human confirmation.",
		"--id: saved suggestion ID; --version: expected record version; --turn: current_turn.",
		"Stable Room PostReceipt with sequence and client message ID.",
		"teamx policy publish --id usage-answers --version 1 --turn 1",
		"policy read --id ID restores the exact suggestion. Identical publication reuses the stable receipt. Publication does not activate a suggestion; confirm_suggestion requires the exact version, proposal_seq and real human answer.",
	},
}

func referenceHelpText(command, op string) string {
	if op == "" {
		var b strings.Builder
		b.WriteString("Usage: teamx " + command + " SUBCOMMAND [--help]\n")
		// Keep the directory in workflow order, independent of map iteration.
		for _, name := range strings.Fields(map[string]string{"inbox": "read", "matters": "list read sources source resolve ensure update notify reply instruct instruction-status", "actions": "execute", "policy": "read update publish"}[command]) {
			h := referenceCommandHelps[command+"/"+name]
			b.WriteString("  " + name + "  " + h.purpose + "\n")
		}
		b.WriteString("Use teamx " + command + " SUBCOMMAND --help for its parameters, JSON contract and constraints.\n")
		return b.String()
	}
	h := referenceCommandHelps[command+"/"+op]
	if name := referencePayloadFlag(command, op); name != "" {
		h.usage = strings.Replace(h.usage, "--"+name+" JSON", "(--"+name+" JSON | --"+name+"-file PATH)", 1)
		h.parameters += " --" + name + "-file PATH: read the same JSON from a file; - reads stdin. Mutually exclusive with --" + name + ". Existing endpoint/body and business field limits still apply."
		h.example += "; or teamx " + command + " " + op + " --" + name + "-file payload.json (use - for stdin)"
	}
	return "Usage: teamx " + h.usage + "\n" + h.purpose + "\nParameters: " + h.parameters + "\nCommon: --endpoint URL (bound TEAMX_ENDPOINT); --json (JSON output, default true). Requires the six bound TEAMX_* identity values.\nReturns: " + h.result + "\nExample: " + h.example + "\nConstraints: " + h.constraints + "\n"
}

const readCLIHelp = `Usage: teamx read [ROOM] [--room ROOM] [--all | --unread | --latest N] [--peek] [--limit N] [--json] [--endpoint URL]
Read Room messages in a fixed snapshot (default: latest 20).
--all: preview all; --latest N: preview latest N; --unread: consume unread; --peek: preview unread without confirming; --limit: maximum other-author unread messages in this fixed read, 1-10000 (default 20).
Returns: messages with seq/author/text/source/reply_to/time and page coverage; --json writes one object per page. Agent JSON omits the CLI's read_receipt/expires_at; confirmation failure includes receipt recovery details in the error.
Example: teamx read --unread --json
Constraints: modes are mutually exclusive; --peek requires --unread. Preview does not consume. Only full output is confirmed; short output is not confirmed. Confirmation failure means consumption is uncertain. Complete every page; Room reads do not consume Inbox or complete business work.
`

const sendCLIHelp = `Usage: teamx send [ROOM] [--room ROOM] --text TEXT [--id ID] [--reply-to SEQ] [--json] [--endpoint URL]
Publish a Room message; ordinary Agent output stays private.
--id: stable retry ID (generated if omitted); --reply-to: exact Room message sequence.
Returns: committed sequence and client_message_id; --json writes the complete receipt.
Example: teamx send --id result-1 --text 'Verified result' --json
Constraints: retain the receipt ID. After failure/uncertain output, retry the same ID and content; changed content conflicts. Mention the human operator when asking for a decision, with enough context to answer.
`
