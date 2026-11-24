package room

import "strings"

const rootCLIHelp = `Usage: teamx COMMAND [--help]
常用维护：teamx service status | logs | upgrade
  teamx service upgrade         升级到最新稳定版，自动备份
  teamx service upgrade --help  查看指定版本、离线升级与备份选项

  service                       Manage the background service (teamx service --help)
  init | doctor                 Prepare files/auth or diagnose a distribution (--bundle-root, --home; model setup is in the browser)
  access init --config FILE     Initialize the fixed operator and private admin-token file
  serve                         Start the authenticated service
  extensions check              Check selected declarations without starting services
  skill usage                   Read TeamX interface and execution guidance
  platforms skill/list/connect/selection/chats/projects/member  Selected account integrations and current-member setup
  platforms upgrade           Upgrade platform model and references offline (--rooms-dir ROOT)
  member add/list/remove/run/pause/session rotate/status
  calendar list/get/update/claim/finish  Direct-conversation scheduled tasks (--help)
  send                          Publish Room messages
  inbox read                    Room messages/events and saved notifications
  matters list/read/sources/source/resolve/ensure/update/notify/reply/instruct/instruction-status
  actions resolve/execute/read/list Managed platform actions
  policy read/update/publish     Member long-term rules
  tools list/run/skill           Enabled platform and standalone tools
Human CLI: --token-file FILE or TEAMX_ADMIN_TOKEN_FILE; token values are never command-line arguments.
Use teamx COMMAND SUBCOMMAND --help for parameters and constraints.
`

const serviceCLIHelp = `Usage: teamx service COMMAND [OPTIONS]
  status | inspect              查看服务状态或安装登记
  logs [--lines 100] [--follow]  查看日志
  stop | start | restart        停止、启动或重启已安装服务
  disable | enable              禁用或启用自启动（disable 不停止当前服务）
  upgrade                       升级到最新稳定版，自动创建一致备份
  upgrade --version vX.Y.Z       升级到指定正式版本
  upgrade --archive FILE        离线升级；摘要读取 FILE.sha256
  backup --backup-dir DIR       停服并创建一致备份，完成后保持停止
  restore --from DIR --backup-dir DIR  恢复备份，并保存恢复前状态
  uninstall                     停止并注销服务，保留数据、版本和命令
  install --archive FILE        安装/重新登记本机 standard 包（安装器调用）
通用参数：--server-root DIR；默认从已安装的 teamx 命令发现。
使用 teamx service COMMAND --help 查看该命令的参数和完整示例。
macOS 使用已登录普通用户，不加 sudo；Linux 管理命令前加 sudo。
首次安装使用官方 install.sh，安装完成后在网页配置模型。
`

type referenceCommandHelp struct {
	usage, purpose, parameters, result, example, constraints string
}

const unitChangeHelp = "object_ref, unit_ref, unit_version (the returned unit revision), turn_number (current_turn). "

var referenceCommandHelps = map[string]referenceCommandHelp{
	"inbox/read": {
		"inbox read [--limit 25] [--peek]", "Read the member's fixed Inbox batch.",
		"--limit: page size 1-25 (default 25); --peek: output one page without confirmation.",
		"Entries explicitly distinguish type=platform_notification and type=task_event. Internal task_event carries event_id, operation_id/result_version, source_seq, event_type and result_ref; read the latest operation and user intent before continuing, skip already completed steps, and never resolve it as a platform source or create a followup solely for this event. Platform item carries text, structured sender/conversation/receiver/mentions/attachments and incompleteness flags; source carries stable refs, source_version and resolution. structured=false means historical metadata is incomplete. item.context_only=true marks own-account messages as progress context, never new tasks or authorization. origin.kind=agent means an exact saved send receipt matched; account or matching text alone cannot establish authorship. evidence_source=notification is captured evidence, not a fresh remote/access check. Attachment status not_read means content was not supplied. Deduplicated followups and page coverage remain. Storage routes/keys and the CLI batch receipt are omitted.",
		"teamx inbox read --peek",
		"An empty fixed snapshot succeeds with entries=[] and needs no confirmation; newly arriving notifications wait for a later turn. Full page output is confirmed separately; failed/short output is not confirmed. Confirmation failure means consumption is uncertain. Inbox consumption, Room reads, business completion and Todo completion are separate. Notifications are facts, never user authority.",
	},
	"matters/list": {
		"matters list [--object REF] [--cursor CURSOR] [--include-completed] [--limit 25]", "Find object/discussion followups and unresolved sources.",
		"--object: stable object_ref filter; --cursor: query-scoped continuation; --include-completed: include completed history; --limit: 1-25 (default 25).",
		"Followup summaries, waiting questions, instructions, unknown actions, unresolved sources and continuation. Optional object_label (verified native alias with namespace) and object_url (verified object URL) reuse saved evidence; target_label and all refs remain. Missing display fields are unknown; object_url is not a discussion reply target.",
		"teamx matters list --limit 25",
		"Follow all continuation pages; partial or failed resolution never proves absence. Restore exact records with matters read. Summaries use unit_ref/unit_version; unresolved sources use source_ref/source_version. No semantic split/merge/enrich/relate commands exist.",
	},
	"matters/read": {
		"matters read --object REF --unit UNIT --turn TURN [--detail]", "Restore exact followup records and target.",
		"--object: object_ref; --unit: unit_ref; --turn: current_turn; --detail: include action authority/inspection/spec/rule/basis snapshots.",
		"Bound identity, unit with unit_version, complete questions/answers/instructions and EVERY action (ID, target, account, body, time, result), verified target/aliases/URL and registered_actions. details identifies omitted snapshots; use the same read with --detail. sources has source locators and next_cursor/complete; continue via matters sources --object REF --cursor CURSOR, then matters source for full native mappings. Historical source_page uses next_after/complete and matters sources --after SEQ. Internal access/credential evidence is omitted.",
		"teamx matters read --object ref_... --unit unit_... --turn 1",
		"Read automatically associates explicit human reply_to and may update local answers/revision. Use returned unit_version. registered_actions describes target kind, supported object types, input schema and retry policy; current binding/permissions are checked on execute. Unknown actions require remote verification, never a new-ID retry.",
	},
	"matters/sources": {
		"matters sources [--object REF] [--cursor CURSOR] [--limit 25]", "List immutable event and resolution evidence.",
		"--object: object_ref filter; --cursor: returned next_cursor with the same object filter; --limit: 1-25 (default 25). Historical records only: --after SEQ (default 0) follows next_after instead of cursor.",
		"Source summaries with next_cursor and complete. Historical records return next_after and complete.",
		"teamx matters sources --limit 25\nContinue: teamx matters sources --cursor RETURNED_CURSOR --limit 25",
		"Continue through all pages using the returned continuation and the same object filter. Unified input does not accept nonzero --after. Partial/failed lookups never prove absence; use matters source for original evidence and resolve for an explicit retry.",
	},
	"matters/source": {
		"matters source --source SRC", "Read one original event and its resolution evidence.",
		"--source: returned read-only source_ref. Historical records only: also supply the matching --seq SEQ.",
		"Original entry, immutable source, resolution status/reason and verified references.",
		"teamx matters source --source src_...",
		"Unresolved/partial evidence is retained and does not prove absence. Native object, discussion and message identities are distinct. Reading does not retry resolution.",
	},
	"matters/resolve": {
		"matters resolve --source SRC --turn TURN", "Explicitly retry resolution of one retained source.",
		"--source: returned source_ref; --turn: current_turn. Historical records only: also supply the matching --seq SEQ.",
		"Source resolution status/reason and verified object/target references.",
		"teamx matters resolve --source src_... --turn 1",
		"Only the specified source is retried. Lark rebuilds captured native identity from the saved notification without remote reads or a member binding; use tools run ENTRY read for current content/context on the exact bound account. Failed/partial lookups retain evidence and never prove absence. Do not fabricate Inbox sequences or native identities.",
	},
	"matters/ensure": {
		"matters ensure --source SRC --target REF --turn TURN", "Open a discussion followup on demand.",
		"--source: returned source_ref; --target: verified native discussion target_ref from that source; --turn: current_turn. Historical records only: use --seq SEQ instead of --source.",
		"unit_ref, object_ref, unit_version, status and requires_read; restore the business view with matters read.",
		"teamx matters ensure --source src_... --target ref_... --turn 1",
		"Use only when independent discussion handling is needed. No caller-defined unit IDs. Discussion identity must be verified; never fall back to an ordinary object comment.",
	},
	"matters/update": {
		"matters update --change JSON", "Save followup progress.",
		"--change: JSON with " + unitChangeHelp + "status, summary, next, evidence.",
		"Committed receipt: unit_ref, unit_version, status, changed progress/questions/instructions/actions, reused and requires_read. Re-read when requires_read=true before deciding with this version.",
		`teamx matters update --change '{"object_ref":"ref_...","unit_ref":"unit_...","unit_version":1,"turn_number":1,"status":"in_progress","summary":"Investigating"}'`,
		"States: pending/in_progress/waiting_user/waiting_followup/completed/archived/unknown. Waiting/unknown requires next; completed requires evidence and no unresolved questions/instructions/actions. Success, business completion, Inbox consumption and Todo completion are separate.",
	},
	"matters/notify": {
		"matters notify --change JSON", "Save a stable question, then publish it in Room.",
		"--change: JSON with " + unitChangeHelp + "text, optional message_refs (verified native message refs), proposed {operation,body or constraints}.",
		"Receipt with exact saved question, publication and send_receipt only when confirmed. publication=unconfirmed means saved, not published; published_link_pending means send confirmed but local linkage needs recovery. reused identifies question reuse; requires_read=true requires a fresh read.",
		`teamx matters notify --change '{"object_ref":"ref_...","unit_ref":"unit_...","unit_version":1,"turn_number":1,"text":"May I send this?","proposed":{"operation":"object.comment","body":"Received"}}'`,
		"Question is saved BEFORE public send. Identical requests reuse the question/receipt; reconcile uncertain sends with the same request. Read before asking again. Keep routine results private; important results use send with a stable ID and update evidence.",
	},
	"matters/reply": {
		"matters reply --change JSON", "Associate an unambiguous real human answer.",
		"--change: JSON with " + unitChangeHelp + "question_ref, source_seq (human message), basis.",
		"Committed receipt with changed questions/answers, progress, unit_version and requires_read.",
		`teamx matters reply --change '{"object_ref":"ref_...","unit_ref":"unit_...","unit_version":2,"turn_number":1,"question_ref":"question_...","source_seq":18,"basis":"Explicit answer to the question"}'`,
		"Explicit reply_to is automatically associated by matters read. Without reply_to, associate only unambiguous human context. For action authority.room_seq use questions[].answers[].message.seq, never the Agent question's room_seq.",
	},
	"matters/instruct": {
		"matters instruct --change JSON", "Persist exact human intent for work across turns.",
		"--change: JSON with " + unitChangeHelp + "source_seq, intent {operation,body or constraints}, basis; optional question_ref, message_refs, expires_at (RFC3339).",
		"Committed receipt with changed instructions, exact human evidence, unit_version and requires_read.",
		`teamx matters instruct --change '{"object_ref":"ref_...","unit_ref":"unit_...","unit_version":2,"turn_number":1,"source_seq":18,"intent":{"operation":"object.comment","body":"Received"},"basis":"Exact human instruction"}'`,
		"question_ref is optional for a direct human task. Immediate execution of an exact human answer needs no extra instruction. Target/messages remain scoped; a saved instruction is single-use and does not establish a standing rule.",
	},
	"matters/instruction-status": {
		"matters instruction-status --change JSON", "Record fulfillment or human revocation of an instruction.",
		"--change: JSON with " + unitChangeHelp + "instruction_ref, instruction_version (observed instruction version), status (fulfilled/revoked), evidence; source_seq for human revocation only.",
		"Committed receipt with changed instructions/status/version, unit_version and requires_read.",
		`teamx matters instruction-status --change '{"object_ref":"ref_...","unit_ref":"unit_...","unit_version":3,"turn_number":1,"instruction_ref":"instruction_...","instruction_version":1,"status":"fulfilled","evidence":"Verified result"}'`,
		"Revocation requires the real human correction. This records business evidence, not an action execution receipt. Re-read on version conflict; never silently substitute current versions.",
	},
	"actions/resolve": {
		"actions resolve --request JSON [--tool ENTRY]", "Verify a target and action contract. Use root actions resolve/execute --tool ENTRY across platforms for an explicitly selected exact entry; saved target/context can restore its account without --tool. Reuse a verified target and valid contract without resolving again.",
		"--request: {locator,turn_number}; GitLab: {url:FULL_HTTPS_PROJECT_ISSUE_MR_OR_WORK_ITEM_URL} (optional #note_ID, #discussion_FULL_ID or /notes/ID) or {target_ref:REF}. Lark: {self:true}, {query:{object_type:chat,text:NAME}}, returned candidates[i].locator unchanged, or {target_ref:REF}. Root requests without --tool also accept provider; account_owner_seq is optional human account-ownership evidence.",
		"resolution: account, target/label or candidates[].{label,locator}, status, complete, candidates_incomplete and reason. target_ref plus actions: input_schema, required request fields, authority contract, executable and repair reason/next_action.",
		`teamx actions resolve --tool ENTRY --request '{"locator":{"url":"https://gitlab.example/group/project/-/merge_requests/42#note_99"},"turn_number":1}'`,
		"GitLab URL resolution needs no notification, matter or Workspace; select the exact account entry. A project issue/MR/work item URL selects object.comment; a comment or reply URL retains its note and selects discussion.reply in its verified thread. Ordinary comments can become threads; system notes cannot be replied to. Unsupported/missing anchors never fall back to an object comment; clarify an unspecified thread first. Candidates are unverified; resolve the selected locator on the same entry with the CURRENT turn. Clarify duplicate names; retain incompleteness. Reuse an existing valid target_ref. Target access is not sending authority.",
	},
	"actions/execute": {
		"actions execute --request JSON [--tool ENTRY]", "Submit one complete action on the exact tools-list entry. Input and required evidence come from its resolved action.",
		"--request: {target_ref,turn_number,id,operation,input,context?}. Self message.send_self omits policy_token/authority. Group message.send requires current policy_token and authority:{type:human_reply,room_seq:ACTUAL_HUMAN_SEQ,reason:INTERPRETATION}. Other actions use rule/instruction {type,id,version,reason,constraints?} or human_reply {type,room_seq,reason,question_ref?,message_refs?,constraints?} as declared; rule forbids constraints. Optional context:{object_ref,unit_ref,unit_version} associates a matter, never grants authority. Legacy unit requests remain accepted.",
		"id, status, attempts, target_ref and result. Lark sends and GitLab project comments/replies include operation, complete input, authority when required, exact account/target and result.verified plus any known result_id/reason. Full frozen evidence is available with actions read --id ID.",
		`teamx actions execute --tool ENTRY --request '{"target_ref":"ref_...","turn_number":1,"id":"save-1","operation":"message.send_self","input":{"body":"保存这段文字"}}'`,
		"Follow input_schema: body is exact text; images, only when declared, are ordered {message_seq,attachment_id} from current Room human messages/attachments[].id. No extra attachment lookup or read receipt. One action/ID sends the entire selection; never split captions/images or fall back to a subset. Lark message replies remain text only. GitLab images are available only when input_schema declares them; GitLab image comments/replies require authority:{type:human_reply,room_seq,reason} and current policy_token, one attempt; rule/instruction/question authority is not accepted with images. Group sends require the direct Room instruction; self evidence grants no group permission. Reuse stable IDs; changed target/input/evidence conflicts. source/upload failure means nothing sent; unknown may already be sent: query actions read --id ORIGINAL_ID, never resend with a new ID/account/Session. Use error reason/next_action for the same account; platforms skill supplies requested repairs. Success means verified delivery, not read/business completion. Publish outcomes locally with send.",
	},
	"actions/read": {
		"actions read --id ID [--object REF --unit UNIT]", "Restore one action and its complete frozen execution evidence.",
		"--id: stable action ID; --object/--unit: explicit context only to disambiguate historical unit-scoped IDs.",
		"Platform/native target/account, input, typed authority, original human/rule evidence, retry policy, attempts, result, context and detail snapshot.",
		"teamx actions read --id reply-1",
		"Read-only; no resend or recovery queue. Multiple historical matches require explicit context. New action IDs are unique within this membership.",
	},
	"actions/list": {
		"actions list [--object REF --unit UNIT] [--cursor CURSOR] [--limit 25]", "List member actions, with or without followups.",
		"--object/--unit: optional business context filter; --cursor: returned member/query-scoped continuation; --limit: 1-25.",
		"Bounded action records and next/complete; actions read returns full evidence for each ID.",
		"teamx actions list --limit 25",
		"Follow continuation pages. Historical and new records remain distinct. Listing never retries writes.",
	},
	"policy/read": {
		"policy read [--id ID [--detail]]", "Restore effective long-term rules or one saved record.",
		"--id: exact saved policy ID (including suggested/revoked); omit for effective view. --detail requires --id and includes original source/conversation/confirmation evidence.",
		"Effective view with revision, current_turn and policy_token; --id returns the record and policy revision. Human source text appears once; conversation retains interpretation/reply_to/time/proposal. Full evidence: policy read --id ID --detail.",
		"teamx policy read --id usage-answers",
		"effective is a full replacement, including an empty set. Active means supplied context, not blanket permission; latest explicit restrictions/revocations win. Use runtime token/current_turn; restore missing effective body here. Record version and policy revision are distinct from unit_version/unit_ref.",
	},
	"policy/update": {
		"policy update --change JSON", "Save a conversation-backed long-term rule.",
		"--change (or --change-file PATH, - for stdin): {type,id,version?,source_seq,turn_number,basis,body?,proposal_seq?}. Always supply stable id, real human source_seq, runtime current_turn as turn_number, observed exact rule version for amendments, and basis (your interpretation). New records: omit version (defaults to 0). Amend existing: supply observed version and full body. save/suggest: body {text,expires_at?}; expires_at is an optional RFC3339 timestamp. confirm: existing version + proposal_seq, no body. revoke: existing version, no body/proposal_seq. Server restores the exact saved body for confirm/revoke. Removed kind/scope/scope_from/actions/conditions/exceptions fields are rejected, including null.",
		"Effective policy view, revision, current_turn and policy_token, plus record: the exact durably saved ID/version/status/text and evidence, including suggested/revoked. Reusing an old request returns its original record alongside current policy context; record need not be current or effective. Use record.id/version to publish a new suggestion; policy read --id ID restores the current saved version.",
		`New long-term rule (version defaults to 0):
teamx policy update --change '{"type":"save","id":"reply-style","body":{"text":"Keep replies concise"},"source_seq":18,"turn_number":1,"basis":"Explicit lasting human reply requirement"}'
Handling convention (knowledge alone does not authorize sending a message):
teamx policy update --change '{"type":"save","id":"contact-priority","body":{"text":"For AgenticHub errors of this kind, first suggest hongwei and luojun; ask before contacting them unless the human has explicitly authorized it."},"source_seq":18,"turn_number":1,"basis":"Human explicitly asked to remember this error-handling convention"}'
Owner knowledge uses the same body shape: {"text":"AgenticHub product owner is huayi"}.
Suggest: use type suggest with the same full-body shape; it does not activate the record.
Amend: use type save with the existing id, observed record version, full body and new human source_seq.
Confirm a published suggestion:
teamx policy update --change '{"type":"confirm","id":"contact-priority","version":1,"source_seq":24,"turn_number":2,"proposal_seq":22,"basis":"Human confirms the exact published suggestion"}'
Revoke:
teamx policy update --change '{"type":"revoke","id":"contact-priority","version":2,"source_seq":28,"turn_number":3,"basis":"Human explicitly revokes this rule"}'
Replace example sequences/versions/turns with actual observed values. File revision is read-only and rejected on writes, including 0/null. Changes to other rules do not invalidate the observed record version.`,
		"Keep applicability, requirements, permission and exceptions together in text. Do not invent project limits or treat missing limits as arbitrary permission. Active means supplied context, not blanket permission: the Agent judges the text against the exact task and all current restrictions. Reply style, owner knowledge and contact priority alone do not authorize external messages. The server verifies rule lifecycle and exact execution identity, not natural-language applicability. save establishes explicit lasting intent as active; suggest saves suggested only; confirm activates the exact saved/published version using real human reply and proposal_seq, without changing text/expiry; revoke preserves saved body and requires a new human revocation source. Server reads human originals; never submit source_text. Keep interpretation in basis, preserve limits and ambiguous references without guessing. One-off consent never activates standing rules. Ask only about ambiguity affecting the current target, permission or result; independent work and unambiguous rule saving may proceed. UI is optional; users normally manage rules in Room. Rules stay in this Room/Membership; saving starts no listener or schedule. Report success only after durable save; reconcile unknown saves.",
	},
	"policy/publish": {
		"policy publish --id ID --version VERSION --turn TURN", "Publish the exact saved suggestion for human confirmation.",
		"--id: saved suggestion ID; --version: expected record version; --turn: current_turn.",
		"Stable Room PostReceipt with sequence and client message ID.",
		"teamx policy publish --id usage-answers --version 1 --turn 1",
		"policy read --id ID restores the exact suggestion. Identical publication reuses the stable receipt. Publication does not activate a suggestion; confirm requires the exact version, proposal_seq and real human answer.",
	},
}

func referenceHelpText(command, op string) string {
	if op == "" {
		var b strings.Builder
		b.WriteString("Usage: teamx " + command + " SUBCOMMAND [--help]\n")
		// Keep the directory in workflow order, independent of map iteration.
		for _, name := range strings.Fields(map[string]string{"inbox": "read", "matters": "list read sources source resolve ensure update notify reply instruct instruction-status", "actions": "resolve execute read list", "policy": "read update publish"}[command]) {
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
	result := "Usage: teamx " + h.usage + "\n" + h.purpose + "\nParameters: " + h.parameters + "\nCommon: --endpoint URL (bound TEAMX_ENDPOINT); --json (JSON output, default true). Requires the six bound TEAMX_* identity values.\nReturns: " + h.result + "\nExample: " + h.example + "\nConstraints: " + h.constraints + "\n"
	if command == "actions" && op == "execute" {
		result += gitlabImageActionExamples
	}
	return result
}

// Transport flags differ, while the structured action contract and diagnostics
// are shared. Do not advertise root routing flags on an already selected entry.
func builtinActionHelpText(entry, op string) string {
	h := referenceHelpText("actions", op)
	h, _, _ = strings.Cut(h, "\nGitLab image examples")
	if op == "resolve" {
		h = strings.ReplaceAll(h, `{"locator":{"url":"https://gitlab.example/group/project/-/merge_requests/42#note_99"},"turn_number":1}`, `{"locator":{"self":true},"turn_number":1}`)
	}
	h = strings.ReplaceAll(h, "teamx actions", "teamx tools run "+entry+" actions")
	h = strings.ReplaceAll(h, " [--tool ENTRY]", "")
	h = strings.ReplaceAll(h, " --tool ENTRY", "")
	h = strings.ReplaceAll(h, "Common: --endpoint URL (bound TEAMX_ENDPOINT); --json (JSON output, default true). Requires the six bound TEAMX_* identity values.", "Common: --json (JSON output, default true). The selected entry fixes the execution account.")
	return h
}

const sendCLIHelp = `Usage: teamx send [ROOM] [--room ROOM] --text TEXT [--id ID] [--reply-to SEQ] [--json] [--endpoint URL]
Publish a Room message; ordinary Agent output stays private.
--id: stable retry ID (generated if omitted); --reply-to: exact Room message sequence.
Returns: committed sequence and client_message_id; --json writes the complete receipt.
Example: teamx send --id result-1 --text 'Verified result' --json
Constraints: retain the receipt ID. After failure/uncertain output, retry the same ID and content; changed content conflicts.
`

const gitlabImageActionExamples = `
GitLab image examples (when declared by input_schema):
Use root actions execute --tool ENTRY; GitLab tools run ENTRY remains the native CLI proxy.
Ordinary MR comment:
teamx actions execute --tool ENTRY --request '{"target_ref":"ref_MR","turn_number":1,"id":"mr-images-1","operation":"object.comment","input":{"body":"问题截图与修复效果","images":[{"message_seq":120,"attachment_id":"img_0123456789abcdef0123456789abcdef"}]},"policy_token":"CURRENT_TOKEN","authority":{"type":"human_reply","room_seq":122,"reason":"用户明确要求向该 MR 发送说明与这张图"}}'
Exact discussion reply:
teamx actions execute --tool ENTRY --request '{"target_ref":"ref_EXACT_DISCUSSION","turn_number":1,"id":"thread-images-1","operation":"discussion.reply","input":{"images":[{"message_seq":121,"attachment_id":"img_fedcba9876543210fedcba9876543210"}]},"policy_token":"CURRENT_TOKEN","authority":{"type":"human_reply","room_seq":122,"reason":"用户明确要求把第二张图回复到该准确讨论"}}'
authority.room_seq is the sending instruction, not the image source. Reuse context:{object_ref,unit_ref,unit_version} for an existing matter without duplicate top-level matter fields. Resolve a full GitLab project issue/MR/work item/comment URL with actions resolve --tool ENTRY --request '{"locator":{"url":"https://gitlab.example/group/project/-/merge_requests/42"},"turn_number":1}' when no usable ref exists. Copy actual refs/current turn/token; do not construct native-ID locators. Submit the whole selection once, then report the receipt. On ambiguity clarify; on unknown read the original ID and never resend.
`
