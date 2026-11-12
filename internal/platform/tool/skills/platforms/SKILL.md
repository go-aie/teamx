# Connect Feishu or GitLab to TeamX

Use `"$TEAMX_CLI" platforms skill` for a real human conversation request to connect
Feishu/GitLab or configure your own notifications/tools. It works before any
connection or tool binding. Read the common lifecycle, then the matching provider
section. Use current `platforms --help` and subcommand help for field contracts.
Every `teamx` example stands for `"$TEAMX_CLI"`; replace uppercase placeholders with
verified values. Other providers and unsupported settings use Settings → Platforms. For interface
or execution questions, read `"$TEAMX_CLI" skill usage`.

Connections are shared; notifications, tools and rules belong to the exact current
Room/Agent/Membership. Reading this Skill or enabling collaboration authorizes no
configuration. Do not impersonate a human, change your bound identity, invoke native
login commands or read credentials. Use protected settings for secrets. Requests
for another member belong in that conversation or the human management interface.

In a shared Room, read the whole conversation but apply a member configuration
request only when it is addressed to you. Explicit names/@mentions and context
identify the intended member; a request for another member does not change your
own settings. If "your" remains ambiguous among members, clarify before changing
settings. For an explicit request to several members, handle only your own part.
Confirm applied changes with your member name, exact platform and changed setting.

## Common lifecycle

1. Read the real human message and retain its `seq`. Recover `platforms connect
   status --source-seq SEQ`, `platforms selection status --source-seq SEQ` and
   `platforms member status --source-seq SEQ` as relevant. Only
   `operation_not_found` establishes absence. A failed or partial read does not.
   For an existing account and a settings-only request, go directly to member
   configuration; no new connect operation is needed.
2. Read `platforms list` and match the exact provider, instance, account and
   connection version. Resolve real ambiguity through the fixed forms below.
   Do not switch accounts after failure or guess from a matching display name.
3. Give each connection, selection and member configuration step its own stable
   32-character hexadecimal operation ID. Repeated ID/input recovers a receipt;
   changed input conflicts. Keep the original human source across events and
   continuation. Never replace an ID or invent a source to retry unknown work.
4. Start long human interactions with `--notify inbox`. Tell the human to use the
   original message's fixed component; authorization links and QR codes stay in
   that component, not chat history or private files. Full settings shares its
   receipt; closing settings does not cancel the conversation operation.
5. Confirm the subscription, then end the turn. For a short interaction, try
   `connect wait --operation-id ID --timeout 20s` or the equivalent `selection
   wait` at most once per turn. Pending or CLI interruption does not cancel work.
   Do not loop to keep a turn alive. The human may speak again to continue.
6. Wait, user continuation and Inbox events use the same original receipt. Read
   latest status, newer user intent and existing member steps; apply only missing
   requested work. `completed` plus `connection.status=connected` establishes a
   saved connection; app setup and authorization have separate outcomes. Current
   access/configuration must still be checked through list/member get.
7. After reconciling a result and its follow-on step, use `connect acknowledge
   --operation-id ID --result-version V` or `selection acknowledge` for the exact
   result version. Check the receipt before one completion report. If
   `continuation.handled_version` already covers it, skip handled work and
   duplicate feedback; consume a late Inbox event normally. After interruption,
   inspect history and step receipts to finish only missing work.

`connect notify --operation-id ID --mode inbox|none` and `selection notify` change
only result delivery. Subscribing after completion delivers the same stable event;
none cannot retract an already queued one. Read `continuation.state` and
`blocked_reason`: pause, admission, capacity, failure recovery and Session rotation
still apply. Explain the actual block and active-continuation option; never promise
immediate wake-up. Delivery, consumption, acknowledgment and business completion
are independent. A failed Agent turn follows ordinary recovery, not event replay.

Cancel only on the human's explicit request: `connect cancel --operation-id ID`
or `selection cancel --operation-id ID --version V`. Applied effects remain;
late callbacks cannot revive cancelled operations. Unknown submissions require
receipt/current-state verification and explicit recovery through settings. After
restart, temporary OAuth sessions are not restored; first inspect saved effects.
A terminal failed/expired selection needs a new human request before a new one.

## Feishu connection and selected groups

Reuse an exact verified connection by including its ID/version/name; otherwise:

```sh
teamx platforms connect start --provider lark --name NAME --operation-id ID --source-seq SEQ --notify inbox
```

For reuse, add `--platform-id ID --platform-version V`. The message component may
have application setup and user authorization stages. In a CLI-only client,
observe a live `interaction.authorization_url` without persisting it. Report a
connection only after the common lifecycle checks. A connection-only request ends
there; do not add groups, notifications or tools.

New login requests the complete current TeamX permission set supported by the
selected application, including search and sending dependencies. OAuth permission
is separate from member action grants and business write authority. Reuse performs
a bounded capability check; it may briefly remain running before completion.

For an explicitly requested repair of an existing Feishu account, use:

```sh
teamx platforms connect authorize --platform-id ID --platform-version V --operation-id AUTH_ID --source-seq SEQ --notify inbox
```

This freezes the original account/application/profile and changes no connection
name, tool entry, notification scope or member grants. It shares status/wait/cancel,
Inbox result and acknowledge with connect. Source recovery is
`connect status --source-seq SEQ --intent authorize --platform-id ID`; the plain
source lookup means connect. The same source can have one connect and one authorize
per exact existing platform, never two operations for the same intent/target.
Do not change IDs to bypass an unknown submission. Use the original message's
"补全授权" component; no extra human chat is needed for subscribed continuation.

Use `member get` / `tools list` availability: configured grants describe intent;
capabilities are recent observations (60 seconds), not perpetual execution rights.
Unchecked is not ready. `authorize_user` uses the operation above;
`configure_application` uses the exact official application settings link;
`verify_self` requires a new explicit repair of that member's already granted
self action (`member tool --enabled true --action message.send_self`);
`enable_member_tool` uses explicit member enable; `revalidate_account` checks the
original account in settings; `retry_check` refreshes the original connection.
An unsupported host cannot send; repeating login or the tool switch cannot fix it.
Same-account OAuth restores only enabled credential-paused subscriptions and keeps
progress/backlog. Other members' grants are untouched; old self evidence becomes
stale and requires their own explicit repair. Completed partial configurations
need a new human repair request, not automatic replay of the old operation.

For explicitly selected groups:

1. Recover prior member steps, then read `platforms member get --platform-id ID
   --platform-version V`. Retain the independent `notification_version` and
   `tool_revision`; verify the account and full member identity.
2. Search with `platforms chats --platform-id ID --platform-version V --query NAME`.
   Continue the same query with returned `--page-token` while `has_more=true`,
   including empty pages. Preserve `search_warnings`; incomplete permissions do
   not establish absence. Compare exact `chat_id`, name and `chat_mode`; use a
   group form for a real choice. Metadata does not grant sending permission.
3. Submit the requested groups once:

```sh
teamx platforms member notifications --platform-id ID --platform-version V --notification-version N --operation-id STEP_ID --source-seq SEQ --add-chat-id CHAT_ID
```

Repeat `--add-chat-id` for additional explicit groups. New subscriptions receive
only selected groups from save time, with group message mode `all`. Existing
settings, groups, mode and reception start are preserved. Change
`--group-message-mode all|mentions` only when requested; mentions enables @me if
neither mention filter was enabled. `--enabled true|false` changes the subscription
switch; adding groups without it enables reception. An existing group scope of
all requires an explicit specified scope and complete replacement list, not silent
replacement by an add command. See notification changes below.

Verify each completed receipt and `result.status=applied|preserved`, then current
member get. Unknown work is not replayed by status or resubmission. Keep unrelated
settings and the reception baseline intact.

## Feishu notification changes

For an unambiguous real human settings request, recover original member steps and
read member get, then directly submit one notification operation with all requested
fields. Do not reconnect, enable tools, create an editing draft or ask for redundant
confirmation. Preserve exact member/account, notification version, original source
and stable operation ID. Unspecified fields, tool grants and reception start stay
unchanged. An existing selection card does not turn a request about saved settings
into a draft edit; reconcile newer human intent before applying a late old selection.

| Requested change | Flags on `platforms member notifications` |
| --- | --- |
| Add / remove followed groups | Repeat `--add-chat-id ID` / `--remove-chat-id ID` |
| Replace the group range | `--group-scope specified` and repeat `--group-chat-id ID` |
| Clear the specified group list | A single `--group-chat-id ''` |
| All groups | `--group-scope all` |
| Group reception switch | `--group-enabled true\|false` |
| Only @me | `--group-message-mode mentions --group-at-me true --group-at-all false` |
| Only @all | `--group-message-mode mentions --group-at-me false --group-at-all true` |
| All messages in the selected groups | `--group-message-mode all` |
| Private reception switch | `--private-enabled true\|false` |
| All accessible person-to-person chats | `--private-scope all` and `--private-enabled true` when enabling is requested |

All changes also need the usual exact platform ID/version, notification version,
operation ID and real human source sequence. Add/remove and complete replacement
are mutually exclusive; never add and remove the same ID. A replacement list must
include every intended group, including choices not on the current candidate page.
Scope and condition flags alone do not enable reception. Explicit branch enable
resumes the total switch unless the human explicitly keeps `--enabled false`.
To replace and enable a group range, also use `--group-enabled true`. When resuming
would activate another dormant branch, obtain the human's source choice and include
both branch switches; do not silently disable or enable the other source.

For removal, match against saved group IDs first; use available names/search only
to disambiguate those IDs. A followed group can be removed even if it is no longer
remotely accessible. If its name cannot be mapped reliably, clarify rather than
guessing or treating failed access as absence. All-group reception has no exclusion
list: "all groups except A" requires clarification, not enumeration of visible groups.
Switching all to specified requires both the explicit scope and complete list.

Clearing/removing the last specified group disables that branch; if no branch
remains enabled, total reception closes. Report these consequences. Disabling a
branch retains its selection. Enabling private reception preserves an existing
specified private range; a new specified private range, private ID list or polling
interval change still uses full settings. Do not use group search to select people
or send a message to create a private conversation. No Workspace is required.

Mentions mode requires at least one selected mention condition when explicitly
editing those conditions. Do not silently turn @me back on after the human asked
for both to be off. All-message mode retains mention preferences without filtering.
The simple mentions-mode command still defaults to @me if neither was selected.

Use existing account/group/project cards only for choices they already represent;
unchecking an existing group-selection card does not remove a saved subscription.
Complete replacements may require query/clarification; do not reinterpret the old
card's unchecked items as removals. Browser changes are local until submission;
never claim to read those unsaved inputs or invent a natural-language draft-save
command. Full settings remains the entry for visual bulk editing and recovery.

Check completed/preserved receipts and current member get before reporting saved
changes. Preserved on an absent subscription leaves version 0 and creates no
collector. An unknown result is reconciled under its original ID, not replayed.
Configuration saved, platform event received and visible reminder delivered are
separate outcomes. Do not infer reception changes from vague requests about timing
or interruptions; clarify collection versus lasting reminder rules as needed.

## GitLab connection and first application

Match the canonical `--origin` without a trailing slash and expected username.
`platforms list` includes public `gitlab_applications` IDs and compatibility
reasons. Application references, connection IDs and account identities are distinct.

```sh
# Reuse the exact verified account; no fresh OAuth is needed.
teamx platforms connect start --provider gitlab --origin URL --name NAME --platform-id PLATFORM_ID --platform-version V --account-username USER --operation-id OP_ID --source-seq SEQ --notify inbox
# New account with an explicitly chosen compatible application.
teamx platforms connect start --provider gitlab --origin URL --name NAME --application-id APP_ID --account-username USER --operation-id OP_ID --source-seq SEQ --notify inbox
```

A unique compatible application on the exact instance needs no redundant approval.
When none is compatible, or the human must choose/configure one, omit
`--application-id`. The same operation waits at application and returns `setup`:
official management URL, actual callback, required `api` scope and compatibility.
Direct the human to Configure GitLab application on the original message, then
create/select/import the app through official pages and secure full settings.
The form opens authorization for the original operation; no extra chat or separate
connection is required. Closing/reopening settings preserves it. An explicitly
bound application cannot be silently replaced through draft edits.

The human's canonical browser establishes Cookie/state/PKCE, and the original
callback verifies the application/account and saves the connection once. Imported
apps also require native owner verification. Agent commands receive no browser
secrets or authorization URL. Tabs in the same browser reuse its session; another
browser cannot take over. Account mismatch or already-connected conflict never
selects a replacement. Apply the common result/continuation checks. Connecting
alone does not enable notifications, tools, external replies or to-do completion.

## GitLab personal to-dos, project activity and filters

Read member status for original steps, then `platforms member get --platform-id ID
--platform-version V`. Check account/member, notification/tool versions and
`gitlab_notifications`: desired config, pending sources, errors and applying state.
Version 0 with no collector subscription IDs is an unsaved draft, even if its
default switches are true. Resolve pending/errors through settings first.

```sh
# Personal to-dos only; false disables this source only.
teamx platforms member notifications --platform-id ID --platform-version V --notification-version N --operation-id STEP_ID --source-seq SEQ --todos-enabled true
# Activity for one exact project, verified under this account before submission.
teamx platforms member notifications --platform-id ID --platform-version V --notification-version N --operation-id STEP_ID --source-seq SEQ --events-enabled true --project-id PROJECT_ID --project-path namespace/project
```

New personal subscriptions cover all projects accessible to that account from save
time and leave project activity off. There is no separate Workspace to-do scope.
Do not clone a repository, change Workspace or claim all-project reception is
project-scoped. Existing source choices, intervals, filters and start are preserved.
Activity supports one project per member/account; a new project replaces that
activity project. Explain this if multiple projects are requested. Disable it
with `--events-enabled false`; preserve personal to-dos.

Search projects through `platforms projects --platform-id ID --platform-version V
--query NAME [--page-token PAGE]`. Follow numeric `next_page` until 0, including empty
pages. Compare native `id` and full `path_with_namespace`. Visibility does not grant
business write permission. Use the project form below for an actual choice.

Collect all requested source/filter changes into one notification step for the same
request/account. Omitted dimensions are preserved. If enabling one source would
also enable another dormant source under the shared switch, ask for the explicit
source choice and submit both switches together; do not rewrite unrelated intent.
GitLab rejects Feishu group flags and notification `--enabled`.

Filter flags repeat: `--todo-reason mentioned|assigned|review_requested|other`,
`--todo-type merge_request|work_item`, and
`--event-type note|push|merge_request.opened|merge_request.merged|merge_request.closed`.
An explicitly empty personal dimension selects none; empty event types clears the
filter to all activity. Retain every selected type when replacing a form draft.
After a completed step, re-read member get: verify actual config, exact project,
baseline, untouched sources and no pending/errors. On conflicts, read/reassess;
on unknown results, reconcile the same receipt and use settings for recovery.

## Independent member tools and lasting reminders

Enable or disable tools only on an explicit request, independently of notifications:

```sh
teamx platforms member tool --platform-id ID --platform-version V --tool-revision N --operation-id TOOL_STEP_ID --source-seq SEQ --enabled true
```

Use false to disable. Explicit Feishu enable configures the fixed current reply,
group-send and self-delivery actions and prepares available proof. Repeated enable
adds missing defaults; preserved means the saved intent and proof are unchanged.
For a request limited to sending/replying, repeat `--action message.send` and/or
`--action message.reply` with `--enabled true`; only those actions are added, other
grants remain, and self delivery is not added. `--action message.send_self` explicitly
adds self delivery. These flags apply only to Feishu tools.

Read requested_actions, action_authorization, capabilities and result revision in
the receipt. Applied means settings were saved; a missing self relationship or app
permission can leave individual actions unavailable. A saved binding grants no
business write authority. `not_applied / user_authorization_required` is returned
before a configuration operation is reserved or a binding is written: complete
OAuth for this exact connection before retrying. Never replay an unknown submission
with a new operation ID. Member get reads saved settings; receipt capabilities are
observations at preparation time. Report notification and tool outcomes separately.

For a lasting request such as "remind me here when this needs my attention", use
the common guide's `policy read/update` workflow with real human evidence. Preserve
sources, conditions and current-conversation destination in natural language; reuse
matching rules and retain unrelated rules. A one-time connection/follow request
creates no standing rule. Verify the saved rule separately from reception. Future
Inbox input follows normal scheduling; a necessary local reminder uses send and
requires no extra external sending grant. A saved subscription/rule proves neither
an event arrived nor a reminder was delivered. Report runtime blocks accurately.

## Fixed choices and editable drafts

Publish forms only for a real choice or missing field; an unambiguous explicit ID
needs no redundant confirmation. Recover original selection steps before starting.

| Choice | Start fields after `platforms selection start` |
| --- | --- |
| Feishu account/new connection name | `--provider lark --kind account --name NAME` (name may be empty) |
| Feishu groups | `--provider lark --kind groups --platform-id ID --platform-version V --query NAME` |
| GitLab verified accounts on one instance | `--provider gitlab --kind account --origin URL` |
| GitLab projects on one account | `--provider gitlab --kind projects --platform-id ID --platform-version V --query NAME` |

Every start also needs its own `--operation-id PICK_ID --source-seq SEQ --notify inbox`.
GitLab account candidates are verified accounts with username/native ID and exact
connection ID/version. An empty result requires the connection flow, not guessed
accounts. The GitLab account form cannot create an account or application.

Only the original human submits through the message component or full settings.
Agent `selection update --operation-id PICK_ID --version V` replaces the complete
draft, never submits it. Feishu account fields are `--name` and optional
`--selected-platform-id/--selected-platform-version`; groups use repeated `--chat-id`
and optional `--group-message-mode`. GitLab projects use exact `--project-id`,
`--project-path` and repeated `--event-type`. Retain unchanged fields. Stale versions,
renamed projects or account changes conflict; read and reconcile instead of dropping
manual edits. Submitted targets are immutable. Forms expire after 24 hours.

On completion, verify the account's `draft.platform_id/platform_version/name` and
account, the group's `draft.chat_ids` and verified chats, or the project's
`draft.project_id/project_path`, verified project and optional events_filter.
Refresh current account/config versions, check latest intent and apply the requested
member change under its own stable ID. Selection alone applies no configuration.
Full settings may already satisfy a step: verify exact account/member/scope before
skipping it. Then acknowledge the selection result as in the common lifecycle.
When a handled connection opens a group/project form, acknowledge that connection
before ending the turn; the form has its own independent continuation and receipt.
