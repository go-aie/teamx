package room

const platformConnectHelp = `Usage: teamx platforms COMMAND [--help]
  skill [--topic notifications|selection]  Read the requested setup workflow (omit topic for complete guidance)
  list                        Find existing connections and application compatibility
  connect                     Connect or repair an account (connect --help)
  selection                   Show an account/group/project selection card (--help)
  chats | projects            Search metadata on an exact verified account (--help)
  member                      Configure the current member (--help)
Use teamx platforms COMMAND SUBCOMMAND --help for its parameters and recovery.
Available integrations follow the selected declarations; list shows existing accounts.
Setup needs current bound member identity, independently of tool bindings.
`

const platformConnectDirectory = `Usage: teamx platforms connect COMMAND [--help]
  start          Connect or reuse an account
  authorize      Repair authorization on an exact existing account
  status         Read an operation by ID or original human source
  wait           Explicit bounded wait (advanced; normal start uses --notify inbox)
  cancel         Stop remaining work
  notify         Change result delivery policy
  acknowledge    Record reconciliation of an exact result version
Use teamx platforms connect COMMAND --help for parameters and recovery.
`

const platformSelectionHelp = `Usage: teamx platforms selection COMMAND [--help]
  start          Show an account/group/project selection card
  status         Read a selection by ID or original human source
  update         Replace a draft using its observed version (advanced)
  wait           Explicit bounded wait (advanced; normal start uses --notify inbox)
  cancel         Cancel the exact observed draft
  notify         Change result delivery policy
  acknowledge    Record reconciliation of an exact result version
Use teamx platforms selection COMMAND --help for parameters and recovery.
`

const platformOperationIdentity = "Requires current bound member identity. Operation IDs are stable 32-character hexadecimal values; changed input under an existing ID conflicts. "

var platformConnectCommandHelps = map[string]referenceCommandHelp{
	"start": {
		"platforms connect start --provider PROVIDER --name NAME --operation-id ID --source-seq SEQ [--origin URL --application-id ID --account-username USER] [--platform-id ID --platform-version V] [--notify inbox|none]",
		"Connect an account, or reuse an explicit existing connection.",
		"--provider: selected connection provider; --name: connection name; --operation-id: stable ID; --source-seq: real human request. --platform-id/--platform-version: observed connection to reuse. --notify inbox: resume through normal input after completion. --json is optional; output is JSON.",
		"Operation ID, stage/status, public setup/authorization link and continuation. Only completed means connected; no credentials are returned.",
		"teamx platforms connect start --provider lark --name Work --operation-id ID --source-seq SEQ --notify inbox",
		platformOperationIdentity + "Same ID and unchanged input recover the receipt. For GitLab use canonical --origin; choose a compatible --application-id or omit it for the original human's Configure application flow. --account-username checks the expected account. Feishu login requests the app's current full permission set. Authorization URLs are live only. Connecting alone enables no notifications, tools or external writes. After confirming the receipt/subscription, end the turn; continue when the result arrives. For repair use authorize --help; for lost IDs use status --help.",
	},
	"authorize": {
		"platforms connect authorize --platform-id ID --platform-version V --operation-id ID --source-seq SEQ [--notify inbox|none]",
		"Repair authorization on an exact existing Feishu account/application/profile.",
		"--platform-id/--platform-version: exact observed connection; --operation-id: stable ID; --source-seq: real human request; --notify inbox: resume through normal input. --json is optional.",
		"Operation stage/status and public authorization flow; existing connection identity and grants remain associated.",
		"teamx platforms connect authorize --platform-id ID --platform-version V --operation-id OP --source-seq SEQ --notify inbox",
		platformOperationIdentity + "Keep the same account, tool entries, grants and notification progress. A repair does not grant sending authority. Same intent/target/source cannot restart under a new ID. End the turn after the receipt; recover with status --source-seq SEQ --intent authorize --platform-id ID. No cookies, state or PKCE material are returned.",
	},
	"status": {
		"platforms connect status (--operation-id ID | --source-seq SEQ [--intent authorize --platform-id ID])",
		"Inspect the original connection or authorization operation without replaying it.",
		"Use its operation ID, or the original human source-seq. Source lookup defaults to connect; authorize lookup requires --intent authorize and the exact --platform-id. --json is optional.",
		"Original status/stage, stable completion result_version, saved effects and continuation including handled_version and scheduling blocks.",
		"teamx platforms connect status --source-seq SEQ",
		"Requires current bound member identity. running/waiting/cancelling are pending; completed/cancelled/expired/failed/unknown require their actual result to be read. Only completed means connected. Inspect latest human intent and existing step receipts before continuing. unknown never permits a new-ID retry. Status alone neither acknowledges a result nor changes settings; acknowledge --help describes reconciliation.",
	},
	"wait": {
		"platforms connect wait --operation-id ID [--timeout 20s]", "Explicitly wait for an existing operation within a bounded timeout.",
		"--operation-id: original ID; --timeout: 0 to 2m (default 20s); --json is optional.", "pending plus the last observed operation, if available.",
		"teamx platforms connect wait --operation-id ID --timeout 20s",
		"Normal asynchronous setup uses start/authorize --notify inbox and ends the turn. This advanced wait polls with bounded individual requests; timeout or interruption does not cancel server work. Inspect the original status after uncertainty; never start a replacement operation.",
	},
	"cancel": {
		"platforms connect cancel --operation-id ID", "Stop remaining connection work.", "--operation-id: original ID; --json is optional.", "Current operation status and already applied effects.",
		"teamx platforms connect cancel --operation-id ID", "Requires current bound member identity. Cancellation cannot undo a saved connection or applied effects. Closing a browser/settings page does not cancel work; inspect the original status after an uncertain reply.",
	},
	"notify": {
		"platforms connect notify --operation-id ID --mode inbox|none", "Change an existing operation's result delivery policy.", "--operation-id: original ID; --mode: inbox or none; --json is optional.", "Original operation and continuation delivery state.",
		"teamx platforms connect notify --operation-id ID --mode inbox", "Requires current bound member identity. start/authorize --notify inbox already subscribes; no extra notify call is needed. Subscribing after completion delivers the same stable result event; none cannot retract an already queued event. Delivery, consumption, acknowledgment and business completion are separate.",
	},
	"acknowledge": {
		"platforms connect acknowledge --operation-id ID --result-version V", "Record that an exact connection result has been reconciled with current intent and subsequent steps.",
		"--operation-id: original ID; --result-version: exact stable completion result_version, not the changing receipt version; --json is optional.", "Continuation handled_version for the same operation/result.",
		"teamx platforms connect acknowledge --operation-id ID --result-version V", "Requires current bound member identity. Read the latest human intent, operation result and step receipts first; acknowledge before one completion report. Status or Inbox consumption does not reconcile results. A late event already covered by handled_version only needs normal consumption; never repeat settings or feedback. Notification errors do not invalidate a saved connection; paused/blocked members follow ordinary scheduling.",
	},
	"list": {
		"platforms list", "Find existing accounts and public application compatibility.", "Optional --json; current bound member identity is required.", "Verified connection IDs/versions, names and public application compatibility; no credentials.",
		"teamx platforms list", "An account's existence does not establish tool binding or external-write authority. Available providers follow the selected composition; reuse an existing suitable connection. For a requested card use platforms selection start --help.",
	},
	"skill": {
		"platforms skill [--topic notifications|selection]", "Read embedded account setup guidance for the current task.", "--topic notifications: pure notification total switch; --topic selection: account/group/project cards and asynchronous result recovery. Omit --topic for complete connection, complex settings and tool recovery guidance. Optional --json; current bound member identity is required.", "One self-contained workflow as Markdown, or the complete Skill when topic is omitted.", "teamx platforms skill --topic notifications\nteamx platforms skill --topic selection\nteamx platforms skill", "Choose the known task directly; no index or complete-Skill read is needed first. Unknown topics are rejected. No prior connection or tool binding is required. Reading changes no settings and grants no authority.",
	},
}

var platformSelectionCommandHelps = map[string]referenceCommandHelp{
	"start": {
		"platforms selection start --kind account|groups|projects --operation-id ID --source-seq SEQ [--provider PROVIDER --origin URL] [--platform-id ID --platform-version V --query NAME] [--name NAME] [--notify inbox|none]",
		"Show an account, group or project selection card for the original human.",
		"--kind: form kind; --operation-id: stable ID; --source-seq: real human request. Group/project forms require exact --platform-id/--platform-version; --query is initial search; --name is the initial connection name. --provider defaults to lark. GitLab account choice requires --provider gitlab --origin URL; projects require --provider gitlab. Scion existing account choice uses --provider scion --kind account with optional --origin URL; missing accounts use Settings → Platforms → Scion login, never a PAT in chat. Selection alone enables no tools or notifications.",
		"Selection ID/status/version, draft, card and continuation. Output is already JSON; omit --json.",
		"teamx platforms selection start --kind groups --platform-id ID --platform-version V --operation-id OP --source-seq SEQ --notify inbox",
		platformOperationIdentity + "Read platforms skill --topic selection directly for the card workflow and result recovery. Let the component search/paginate; do not enumerate candidates first or replace the card with text. Only the original human submits. A submitted selection is not a connection or notification change; verify latest intent and use existing connect/member commands afterward. Same ID/input recover the receipt; unknown requires status, never a replacement. After confirming receipt/subscription, end the turn without polling for clicks. Forms expire after 24h.",
	},
	"status": {
		"platforms selection status (--operation-id ID | --source-seq SEQ)", "Read an original selection and its exact submitted result.",
		"Use the original operation ID, or original human source-seq to recover all selection steps.", "Status, observed draft version, immutable submitted result/result_version and continuation including handled_version. Output is JSON.",
		"teamx platforms selection status --source-seq SEQ", "Requires current bound member identity. Read latest intent and existing step receipts before applying a submitted result. Same names are not stable selections; keep exact account/group/project identities. Empty/partial candidate pages, permission failures or stale versions never select a substitute. Status neither configures settings nor acknowledges the result; see acknowledge --help. Unknown submission requires the original ID, never a new card.",
	},
	"update": {
		"platforms selection update --operation-id ID --version V [--selected-platform-id ID --selected-platform-version V --name NAME] [--chat-id ID --group-message-mode all|mentions] [--project-id N --project-path PATH --event-type TYPE]",
		"Replace an existing draft when the human explicitly asks for an Agent edit.",
		"--version: observed draft version. --chat-id repeats the complete selected group list; --event-type repeats project activity filters (empty clears). Other fields replace the corresponding draft choice.",
		"Updated draft/version; output is JSON.", "teamx platforms selection update --operation-id ID --version V --chat-id VERIFIED_CHAT_ID",
		"Requires current bound member identity. Human and Agent edits share compare-and-swap versions; never silently refresh a stale draft. Initial start input is immutable and submitted results cannot change. Use verified IDs on the exact account; projects require native ID and exact path. Updating a draft grants no authority and does not submit/configure it. Normal card display needs no update call.",
	},
	"wait": {
		"platforms selection wait --operation-id ID [--timeout 20s]", "Explicit bounded wait for an existing card result.", "--operation-id: original selection; --timeout: 0 to 2m (default 20s).", "pending and the last observed selection, if available; output is JSON.",
		"teamx platforms selection wait --operation-id ID --timeout 20s", "Normal card flow uses start --notify inbox and ends the turn without polling. Timeout/interruption does not cancel the card. Inspect the same ID after uncertainty; never create a replacement to retry unknown work.",
	},
	"cancel": {
		"platforms selection cancel --operation-id ID --version V", "Cancel an exact observed selection draft.", "--operation-id: original selection; --version: observed draft version.", "Selection status; output is JSON.",
		"teamx platforms selection cancel --operation-id ID --version V", "Requires current bound member identity. Cancellation applies no settings. A submitted result is immutable; do not substitute a newer draft version after a conflict.",
	},
	"notify": {
		"platforms selection notify --operation-id ID --mode inbox|none", "Change delivery for the original selection result.", "--operation-id: original selection; --mode: inbox or none.", "Continuation delivery state; output is JSON.",
		"teamx platforms selection notify --operation-id ID --mode inbox", "Requires current bound member identity. start --notify inbox already subscribes. Late subscription delivers the same stable result; none cannot retract queued events. Delivery and consumption do not acknowledge or apply a selection.",
	},
	"acknowledge": {
		"platforms selection acknowledge --operation-id ID --result-version V", "Record reconciliation of an exact submitted selection result.", "--operation-id: original selection; --result-version: exact stable completion result_version, not the editable draft version.", "Continuation handled_version; output is JSON.",
		"teamx platforms selection acknowledge --operation-id ID --result-version V", "Requires current bound member identity. Check latest human intent, submitted choice and downstream step receipts before acknowledgment and one completion report. Status/Inbox reads do not acknowledge. Already handled late events only need normal consumption; never repeat configuration or feedback. A selection alone grants no notification or external-write authority.",
	},
}

func platformConnectHelpText(action string) (string, bool) {
	h, ok := platformConnectCommandHelps[action]
	return scopedCommandHelp(h), ok
}

func platformSelectionHelpText(action string) (string, bool) {
	h, ok := platformSelectionCommandHelps[action]
	return scopedCommandHelp(h), ok
}
