# Connect Feishu or GitLab to TeamX

Use this workflow only for a real human Room request to connect Feishu/GitLab or configure
the current member's Feishu/GitLab notifications/tools. It is
available before any platform connection or member tool binding exists.

## Scope and discovery

Read this embedded workflow with `"$TEAMX_CLI" platforms skill`, independently of
`tools list` or `tools skill TOOL`. Use `"$TEAMX_CLI"` for the commands below;
the executable and member identity are supplied by TeamX. Consult `platforms
--help` and the relevant subcommand help for the installed contract.

This workflow supports Feishu (`lark`) connection/member configuration and GitLab
(`gitlab`) connection, account/project forms, personal to-do and single-project
activity notifications, existing filters and independent member tools. GitLab first
application setup returns to the original operation through secure Settings → Platforms;
credentials are entered only in that existing protected form.
Read the GitLab connection section below before starting a GitLab operation. Other
providers use Settings → Platforms where supported.
A missing command, dependency error or unavailable capability must be reported
as returned, with the unfinished step and the full management/recovery entry.
Do not bypass the service with native login commands or credential access. An
unavailable Inbox subscription does not prevent a human from speaking again:
query the same operation on continuation, never promise automatic wake-up without
a confirmed subscription and eligible member runtime.

Connections are shared by conversations in this data directory. Notifications,
tools and rules are scoped to the exact current Room/Agent/Membership. For another
conversation/member, direct the human to that conversation or its management
entry; never silently apply that request to yourself or change your identity.
Enabling a member or reading this skill is not human authorization to configure
anything. Neither timers nor cross-conversation delegation are provided here.

## Connect or recover a connection

For an already connected account and a request that only changes member settings,
go directly to the matching provider member section below: "Configure GitLab personal
to-do notifications for the current member" or the Feishu selected-group section.

1. Read the human Room message; retain its real `seq`. First query
   `teamx platforms connect status --source-seq SEQ` to recover any prior operation.
   Only an explicit `operation_not_found` permits starting a new operation for that
   source. Permission failures, incomplete reads and transport failures do not
   establish absence or cancel a previous operation.
2. Run `teamx platforms list`. Reuse an explicitly selected, connected account
   with its exact `id`, `version`, and name. Use the fixed account form below when a choice or a new connection name is needed;
   never guess an account or change accounts following failure.
3. Start once with a stable 32-character hexadecimal operation ID:
   `teamx platforms connect start --provider lark --name NAME --operation-id ID --source-seq SEQ --notify inbox`.
   To reuse a verified connection also provide `--platform-id ID --platform-version VERSION`.
   The server reads and retains the original human message. Never invent human
   evidence, submit credentials or execute native login commands.
4. The successful `start` publishes a durable connection interaction reference,
   associated with the original human message. TeamX's conversation UI displays
   its authorization button/QR code and live progress, including the second
   authorization stage. Tell the human to use that component before waiting.
   Do not paste temporary authorization URLs into Room history or private files.
   The component is available while this turn waits and survives refresh or room
   switching. Full Settings → Platforms shows the same operation and receipt;
   closing Settings does not cancel it. In a CLI-only client, use the live
   `interaction.authorization_url` from status without persisting it.
5. Human authorization normally outlives this turn: subscribe with `--notify inbox`
   on start, or `teamx platforms connect notify --operation-id ID --mode inbox`.
   Confirm the subscription response before ending the turn. A lost response is
   recovered with status; repeat the same notify command if needed, never start
   a new connection. Completion before, during or after subscription uses the
   same result version and stable event, including after a restart.
   For a short wait use `teamx platforms connect wait --operation-id ID --timeout 20s`
   at most once in this turn. Pending or CLI interruption leaves the operation
   running. Publish the interaction before waiting. Do not loop to keep a turn
   alive. To opt out of automatic continuation use `notify --mode none`; this
   cannot retract an event already in Inbox.
   Inbox delivery follows normal enable/pause, admission, budget and Session
   rotation rules. Read `continuation.state` and `blocked_reason`: a saved
   connection may wait for capacity, a paused member, blocked source verification
   or recovery. Explain the actual block and the option to speak again; never
   promise immediate wake-up. Users may always continue by speaking: query the
   original operation, even if its event has not arrived.
6. Report connected only for `status=completed` with `connection.status=connected`.
   Application setup, authorization and connection saving have separate receipts.
   Preserve partial success. For unknown results query the original operation;
   do not start another ID or repeat app registration. After a service restart,
   unresolved authorization requires explicit recovery through platform settings.
   Completed receipts are historical results; `platforms list` shows current access.
   For all three paths (wait, user continuation, Inbox event), first read status,
   the latest human intent and existing member-step receipts. If
   `continuation.handled_version` covers the result version, skip already handled
   steps and completion feedback. Consume late Inbox events normally; do not
   replay the original request. If intent changed, follow the latest instruction.
   After reconciling the result and any explicitly requested member configuration,
   run `teamx platforms connect acknowledge --operation-id ID --result-version V`
   before one final report. Check its receipt; on an unknown response query status.
   Acknowledgment records only result handling, not Inbox consumption or business
   completion. If interrupted after acknowledgment, inspect the Room history and
   existing step receipts before reporting anything still missing. Event delivery
   is not a reason to retry a failed turn; use ordinary explicit failure recovery.
7. Cancel only on an explicit user request with
   `teamx platforms connect cancel --operation-id ID`. Cancellation stops remaining
   work and retains effects already applied; it does not revoke an existing app
   configuration, login, or saved connection. Late results never revive it.

If the request only adds a platform, stop after verifying the connection. Do not
enable notifications or tools as a side effect.

## GitLab connection and first application setup

Use the same recover/start/wait/notify/status/acknowledge lifecycle above. First
query `connect status --source-seq SEQ`; only `operation_not_found` establishes
absence. Run `platforms list` for existing connections and public
`gitlab_applications` references, instance origins and compatibility reasons.
Match the user's exact GitLab instance and account username. For ambiguous verified accounts on an exact instance, use the GitLab fixed
account selector below. Ask for a missing instance or a real application choice;
do not guess among candidates or send Feishu chats/groups commands to GitLab.

- Reuse an explicitly selected connected account at its exact ID/version/name:
  `teamx platforms connect start --provider gitlab --origin https://gitlab.example
  --name NAME --platform-id ID --platform-version V --account-username USER
  --operation-id OP_ID --source-seq SEQ --notify inbox`.
- For a new connection with an explicitly selected compatible verified application:
  `teamx platforms connect start --provider gitlab --origin https://gitlab.example
  --name NAME --application-id APP_ID --account-username USER
  --operation-id OP_ID --source-seq SEQ --notify inbox`.
  Use the canonical instance origin without a trailing slash. Application references
  are distinct from connection IDs and credentials. A unique compatible application
  on the exact requested instance can be used without a redundant confirmation.
- If there is no compatible application, or the human must choose among applications,
  start the same GitLab operation **without** `--application-id` (keep origin, name,
  expected username, operation ID and original source-seq). The operation waits at
  `application` and returns `setup` with the official applications URL, actual service
  callback, required `api` scope and compatibility reasons. Tell the human to use
  **Configure GitLab application** on the original message: it opens full settings
  for that exact instance, imports/selects the app, then verifies native ownership
  through the existing OAuth callback. Never collect Secret/Token in chat. A changed
  or incompatible explicitly bound application cannot be replaced through this flow.
  Closing/reopening settings retains the operation; do not create a replacement ID.
  After configuration, use the same status/wait/Inbox continuation and member steps.
- The fixed GitLab button on the original human message creates the OAuth browser
  session. Tell the user to click it and authorize the expected account. Agent
  commands receive no OAuth URL, cookies, state, code or PKCE material; do not
  impersonate a human, call the browser endpoint or read credential files.
  Tabs in the original browser reuse the session; another browser cannot take it
  over. First application setup uses the linked full settings form; only public
  setup requirements appear in the conversation component.
- Confirm Inbox subscription and end the turn during human authorization. No new
  chat is needed after callback completion if the current member is eligible.
  Synchronous wait and user continuation query the very same receipt. Callback
  verifies the instance/application/account and saves the connection once; account
  mismatch or already-connected conflict never silently changes targets.
- Verify `completed`, exact account and `connection.status=connected`, acknowledge
  the result version, then report the connection. Keep failures, cancellation,
  expiration and `unknown` explicit. After restart inspect the original receipt;
  never replay an authorization code or start another ID for an unknown result.
  Cancellation preserves already saved effects and never revokes another account.

GitLab connection alone does not enable notifications or tools. For explicit
personal to-do notification/tool requests, continue with the member steps below.
GitLab account/project selection and single-project activity use the commands below.
First application setup still uses Settings → Platforms. Never run Feishu `chats`
or `selection --kind groups` against GitLab.

## Configure GitLab personal to-do notifications for the current member

1. After connection completion, recover `teamx platforms member status --source-seq SEQ`.
   Skip completed steps. Only `operation_not_found` proves absence; unknown or failed
   reads never justify a new operation ID. Use the original human request retained
   in the connection, check newer intent, and reconcile late events before acting.
2. Select the exact verified GitLab connection from `platforms list` or the connection
   receipt. Read `teamx platforms member get --platform-id ID --platform-version V`.
   Check account, full Room/Agent/Membership, `notification_version`, `tool_revision`
   and `gitlab_notifications` (desired config, per-source pending/errors, applying).
   Version 0 with no events/todos subscription IDs is an unsaved first-use draft,
   even when its default switches are true; it is not an existing subscription.
   Multiple ambiguous accounts require the fixed GitLab account selector; never guess.
3. For an explicit personal to-do request, submit once:
   `teamx platforms member notifications --platform-id ID --platform-version V
   --notification-version N --operation-id STEP_ID --source-seq SEQ --todos-enabled true`.
   Use `false` to disable only personal todos. New subscriptions cover all accessible
   projects for that personal account, receive from save time, and leave project
   activity disabled. Existing project settings, filters, intervals and original
   reception start are preserved. No project selector or Workspace change is needed.
   GitLab rejects Feishu group flags and notification `--enabled`; it only changes
   the requested todos source. If enabling the shared switch would also enable dormant
   project activity, collect the user's explicit source choice before submitting both
   source switches in the same request. Do not disable/rewrite activity to bypass it.
4. Enable or disable GitLab tools only on a separate explicit request:
   `teamx platforms member tool --platform-id ID --platform-version V --tool-revision N
   --operation-id OTHER_STEP_ID --source-seq SEQ --enabled true|false`.
   The operation is independent of notifications. Repeated enable preserves existing
   grants; a new binding grants no external writes, replies or todo completion.
5. Check each step's completed receipt, then read `member get` again to verify exact
   account/member, actual config/version and absence of pending/errors. Notification
   and tool outcomes are separate: report partial success honestly. On a version
   conflict refresh and reassess; on an unknown submission inspect the same receipt
   and current settings, then use full settings for explicit recovery. Never resubmit
   unknown work under a new ID or replay configuration on a late connection event.
6. When there is a connection operation, acknowledge its handled result only after
   reconciling these steps. Direct use of an already verified account needs no new
   connection operation, authorization or connection-result acknowledgment.
   Saved notifications do not prove a real todo arrived or a reminder was delivered.
   Only connection requests stop after connection verification. For lasting reminder
   behavior, reconcile natural-language rules as described below, using the original
   human source; a subscription starts reception, while a rule describes when to act.

## GitLab account/project choice and scoped activity

Use `selection status --source-seq SEQ` first. Reuse existing steps and their
result versions; all selection lifecycle, draft conflict, notification, bounded
wait and acknowledgment rules below also apply to GitLab. Keep the original real
human source across selection events; a selection never applies configuration.

- For an actual choice among verified accounts on one known instance:
  `teamx platforms selection start --provider gitlab --kind account
  --origin https://gitlab.example --operation-id PICK_ID --source-seq SEQ --notify inbox`.
  Candidates show username, native user ID and connection ID/version on that
  instance. A submitted receipt freezes `draft.platform_id/platform_version/name`
  and `account`. No matching verified account means connect using a compatible
  application; a selector does not create an application or another account.
- Search accessible projects with `teamx platforms projects --platform-id ID
  --platform-version V --query NAME [--page-token PAGE]`. Read `projects` and
  numeric `next_page`; continue until 0, including empty pages. Compare full
  `path_with_namespace` and native `id`, not just a display name. Permission
  failures/partial reads are not absence and do not authorize switching accounts.
  Project visibility never grants business write permission.
- For an ambiguous project or an explicit request to choose a project, publish
  `teamx platforms selection start --provider gitlab --kind projects
  --platform-id ID --platform-version V --query NAME --operation-id PICK_ID
  --source-seq SEQ --notify inbox`.
  The fixed form selects one project and optionally activity type. Natural-language
  draft updates replace the full draft with `selection update --operation-id PICK_ID
  --version V --project-id N --project-path namespace/project [--event-type TYPE]`.
  Retain other draft fields, including every filter type. Only the original human
  submits; stale edits/renamed projects/account changes conflict. Do not guess a
  replacement or edit a completed target. Human settings and chat use the same draft.
- On completion read `draft.project_id/project_path`, verified `project` and
  optional `draft.events_filter`. Refresh member get and reconcile latest intent
  before applying. For an explicit activity subscription submit:
  `teamx platforms member notifications --platform-id ID --platform-version V
  --notification-version N --operation-id STEP_ID --source-seq SEQ
  --events-enabled true --project-id PROJECT_ID --project-path namespace/project`.
  Add repeated `--event-type` only if explicitly chosen; empty clears to all activity.
  The service rechecks the native ID and exact path under the same account, then
  uses the existing single-project subscription service. This replaces the current
  activity project for this member/account; explain that limitation if multiple
  projects are requested, do not claim additive multi-project support. Disabling
  activity uses `--events-enabled false` and preserves todos.
- Collect account/project choices before applying one combined notifications step
  for this request and account. Add `--todos-enabled true|false` for explicitly
  requested todos changes. Omitted sources remain as saved; enabling a shared
  switch cannot silently activate another dormant source. On conflict request the
  source choice; do not bypass it by changing operation IDs or inventing source-seq.
  Filters use repeated `--todo-reason mentioned|assigned|review_requested|other`,
  `--todo-type merge_request|work_item` and `--event-type note|push|merge_request.opened|
  merge_request.merged|merge_request.closed`. Omitted dimensions preserve existing
  filters; empty personal dimension selects none, empty event types selects all.
- Verify completed member receipts, then current member get, exact project binding,
  per-source pending/errors, baseline, untouched sources and tool state. Handle
  unknown work under the same ID. A full-settings change can satisfy a step only
  after verifying its exact account/member/project/filter. Acknowledge handled
  selection results after reconciling configuration; consume late events normally.
  Do not repeat configuration or completion reports. No additional human message
  is needed after selection submission when ordinary Inbox admission permits it.

Personal todos currently cover all projects accessible to the selected personal
account. There is no separate current-Workspace todo scope in this implementation.
If a request requires unsupported Workspace scope or missing prerequisites, explain
it and direct the user to Settings for supported configuration; do not clone a
repository, change Workspace or silently claim an all-project subscription is scoped.
Connection-only requests still stop after connection verification. A saved activity
subscription proves neither a real event arrival nor a Room reminder.

## Follow selected groups and configure the current member

1. Recover earlier configuration steps with
   `teamx platforms member status --source-seq SEQ`. The response contains all
   steps for that original request. Skip completed steps; inspect unknown ones
   with status and current settings. Only `operation_not_found` establishes
   absence. Never change operation IDs to retry unknown submissions.
2. Use `platforms list` to select the exact connected account and current version,
   then `teamx platforms member get --platform-id ID --platform-version V`.
   Retain its `notification_version` and `tool_revision`. These are independent
   of the connection version; stale versions conflict. This configures only
   your complete current Room/Agent/Membership identity, never another member.
3. Search groups with `teamx platforms chats --platform-id ID --platform-version V
   --query NAME`. Without a query it lists group metadata. Continue with the same
   query and returned `--page-token` while `has_more=true`, including empty pages.
   Preserve `search_warnings`; partial permission failures do not prove absence.
   Results include stable `chat_id`, `name`, `chat_mode` and connection version.
   When names match multiple groups, use the fixed group form below with the same query
   for the human to select. Never guess a group, create a chat, switch accounts,
   or treat directory metadata as message/sending permission.
4. For an explicit selected group, submit once using a fresh stable ID for this
   configuration step:
   `teamx platforms member notifications --platform-id ID --platform-version V
   --notification-version N --operation-id STEP_ID --source-seq SEQ --add-chat-id oc_GROUP`.
   Repeat `--add-chat-id` for multiple explicitly selected groups. The server
   rechecks each exact group and the account before applying. New subscriptions
   receive only those groups from save time, with group message mode `all`.
   Existing subscriptions retain other settings, selected groups, message mode
   and original reception start. Set `--group-message-mode all|mentions` only
   when explicitly requested; `mentions` enables @me if neither mention filter
   was enabled. `--enabled true|false` changes the subscription
   switch; omitting it while adding groups enables their reception. If the
   existing group scope is `all`, use Settings → Platforms for an explicit scope
   adjustment; do not silently replace the existing range.
5. Enable tools only when requested, independently of notification setup:
   `teamx platforms member tool --platform-id ID --platform-version V --tool-revision N
   --operation-id OTHER_STEP_ID --source-seq SEQ --enabled true`.
   Use `false` for explicit disable. A repeated enable preserves existing grants;
   a new binding does not create external-write authorization. Read `member get`
   to distinguish saved tool intent from dependency/capability availability.
6. Check each `status=completed` receipt and `result.status=applied|preserved`,
   then read `member get` to verify the actual target, configuration and versions.
   Keep the notification and tool results separate. On a version conflict, read
   current settings and reassess the original request; never overwrite unrelated
   changes. `unknown` means submission may have applied: query the same ID and
   inspect current settings; use full settings for explicit recovery. Neither
   status nor a repeated submission replays an unknown operation, including
   after restart. Do not claim the original business request finished merely
   because a configuration was saved or Inbox reception was enabled.

Account/group forms publish immutable human selection receipts. Connection and
selection operations support asynchronous result subscriptions; member configuration
returns its own receipts synchronously. A connection or
tool binding alone does not grant sending capability or external-write permission.
Use Settings → Platforms for full management and exceptional recovery.

If the human explicitly requests lasting behavior, such as "when this group
needs my attention, remind me here", reconcile the current member's existing
long-term rules using the public guide and `policy read/update`. Preserve the
exact group, condition and current-conversation destination in natural language;
use the real human source and current turn. Reuse matching rules, retain unrelated
rules and verify the saved receipt before reporting a rule saved. A one-time
connection or group-follow request does not establish a standing rule. A
subscription only enables Inbox reception: it neither saves this behavior nor
proves that any reminder has been delivered. Future relevant Inbox input follows
the ordinary member scheduler and rules; publish a necessary reminder with
`send` in the current conversation. This needs no extra platform tool or external
send grant. Report connection, reception, rule save and any runtime block
separately; do not promise reminders while paused, blocked or budget exhausted.

Read `teamx platforms connect --help` for the exact CLI contract. Human management
endpoints remain human-only; never impersonate an operator to call them.


## Fixed account/group forms and editable drafts

Use a form only for an actual choice or a necessary field. An explicit account,
group ID or name in the latest human instruction needs no redundant confirmation.
Never replace an already submitted operation target by editing a draft.

1. First recover `teamx platforms selection status --source-seq SEQ`. It returns
   all account/group steps of the original request. Only `operation_not_found`
   permits a new selection for that step. Keep the original human source sequence
   across connection completion, new selection events and user continuation.
2. For an account choice or missing connection name publish:
   `teamx platforms selection start --kind account --operation-id PICK_ID
   --source-seq SEQ --name PROPOSED_NAME --notify inbox`.
   The form offers verified accounts with their stable account IDs/versions and
   a new connection name. Empty initial name is allowed when the name is missing.
   For a group choice on the exact verified account publish:
   `teamx platforms selection start --kind groups --operation-id GROUP_PICK_ID
   --source-seq SEQ --platform-id ID --platform-version V --query NAME --notify inbox`.
   The server supplies searchable/paginated group candidates and rechecks exact
   selected IDs on submit. Same names remain distinct. Permission warnings and
   empty pages do not prove absence. Selection alone does not change settings.
3. The original human can select, edit and submit in the message component or
   Settings → Platforms. Closing Settings leaves the operation active. Both use
   the same versioned draft; new messages/polls retain local input and focus.
   Natural-language edits use `selection update --operation-id PICK_ID --version V`
   with the complete replacement draft: account fields `--name NAME` and optional
   `--selected-platform-id ID --selected-platform-version V`, or group fields
   repeated `--chat-id ID` plus optional `--group-message-mode all|mentions`.
   Read status first and retain unchanged fields. Update never submits on behalf
   of a human. A version conflict requires reading/reconciling the latest draft;
   do not retry stale input against a newer version or discard manual changes.
4. Tell the human to use the published form before waiting. For short choices,
   try `selection wait --operation-id PICK_ID --timeout 20s` once. Long choices
   default to the confirmed Inbox subscription and ending this turn. Use
   `selection notify --operation-id PICK_ID --mode inbox|none` independently.
   Waiting never cancels. The user may speak to continue at any time. Inspect
   `continuation` for delivery/scheduling blocks; do not promise immediate wake-up.
5. All three continuation paths first read the original selection status, latest
   human intent, connect status and member-step receipts. Proceed only for
   `status=completed`. Account receipt: use `draft.platform_id/platform_version`
   and server-verified `draft.name` to reuse that exact account, or `draft.name`
   to start a new connection. Group receipt: use `input.platform_id/version`,
   `draft.chat_ids`, verified `chats` metadata and optional message mode. Re-read
   current account/configuration versions through list/member get; if the account
   version changed, reconcile explicitly rather than silently switching targets.
6. Apply through the existing stable connect/member operation IDs for the original
   source; inspect unknown submissions, never create another ID to retry them.
   Verify current settings after each completed configuration receipt. If full
   Settings already applied the requested change, verify its exact account/member,
   group IDs and configuration first, and skip any already satisfied step. A
   form receipt or result event is never proof that configuration was applied.
7. Acknowledge the exact selection result with `selection acknowledge
   --operation-id PICK_ID --result-version V` after reconciling/applying its step.
   A handled late event only needs normal Inbox consumption. When connection
   handling opens a later group form, acknowledge the handled connection result
   before ending that turn; the group form has its own continuation and receipt.
   Avoid premature connection acknowledgment while connection work is unresolved.
   Do not send repeated completion feedback for the same result. On interruption
   inspect history and existing receipts to complete only the missing work.
8. Cancel on explicit request with `selection cancel --operation-id PICK_ID
   --version V`. Cancelled/expired forms do not authorize any configuration. Forms
   expire after 24h and cannot edit a submitted target; retain their original
   receipts. Explain the outcome, query current settings, and require a new human
   request for a new selection when needed. Failed Agent turns use ordinary
   recovery; an event does not request automatic retry.

Read `teamx platforms selection --help` for the exact form/draft contract.
