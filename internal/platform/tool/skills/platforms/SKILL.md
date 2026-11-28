# Connect platforms and configure member tools or notifications

Use `"$TEAMX_CLI" platforms skill` for a real human conversation request to connect
Feishu/GitLab, configure your own Feishu/GitLab/Scion notifications/tools, or show an account, group
or project selection card. It works before any
connection or tool binding. Known total-switch tasks can read
`platforms skill --topic notifications` directly; selection cards and their
asynchronous results can read `platforms skill --topic selection` directly. Each
topic includes its own boundaries and recovery; no index/full read is needed
first. This complete guide retains connection, customization and tool repair.
Use current `platforms --help` and subcommand help for field contracts.
Every `teamx` example stands for `"$TEAMX_CLI"`; replace uppercase placeholders with
verified values. Connect/selection commands already return JSON; omit `--json`.
Scion first login uses the account card's protected PAT field or Settings → Platforms → Scion; existing verified Scion accounts
use the member/selection commands below. Other unsupported settings use Settings → Platforms. For interface
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
Apply the latest human corrections and revocations before continuing; an operation
result never revives cancelled intent.

## Conversational onboarding

A plain request to connect, enable or use Feishu/GitLab (for example, “帮我接入飞书”
or “连接 GitLab”) means make it usable for this member: reuse or connect the account
and enable both notifications and tools, including previously disabled switches.
These verbs do not select different setup modes. Explain this briefly and proceed
without another enable question. Explicit limits (“只连接账号”, “不要通知”, “仅工具”),
source restrictions and later corrections take precedence. Opening or refreshing
a conversation/settings page is not a setup request and never reopens switches.

Reuse the existing workflows below:

1. Resolve the addressed member and exact verified account. Reuse an unambiguous
   account directly; only missing access or real account/instance ambiguity needs
   connection/selection. Keep the original human seq across asynchronous results.
2. Enable notifications through the existing total switch. First use has manual
   defaults: Feishu personnel private chats and group @me/@all, or GitLab personal
   to-dos with project activity off, starting now. Existing reception ranges,
   filters and start are retained. Explicit source requests use customization
   instead of broad defaults. State the actual scope briefly before applying.
3. Read member get for the tool binding. If `binding_revision=0` and no tool is
   enabled, use the existing initial setup command directly:
   `teamx platforms member tool --platform-id ID --platform-version V --binding-revision 0 --operation-id TOOL_STEP_ID --source-seq SEQ --enabled true`.
   Otherwise use the short tool switch below, retaining saved grants. Do not try
   the short switch for a missing binding or add defaults to saved grants.
4. Use the common lifecycle, per-step receipts and recovery; finish follow-on
   settings before acknowledging the connection result. Publish one short outcome
   through the existing `teamx send` entry; ordinary final output is private.
   State the account, actual notification/tool results (including partial failure)
   and one useful next request. Omit operation IDs/revision numbers unless asked.
   Full settings is optional for adjustment or recovery.

Onboarding is local setup, not authority for external business writes, collaboration
changes or reminder rules. Reuse existing configuration operations, not a new flow.

## Common lifecycle

1. Read the real human message and retain its `seq`. Choose the requested path:
   combined onboarding → reuse/connect the account, then use the existing
   notification switch and saved/initial tool paths above;
   notification or existing tool total switch → submit directly (the server recovers its receipt);
   source/tool customization → recover member status and get observed versions;
   connection or selection → recover only that workflow by original source-seq.
   Only `operation_not_found` establishes absence. Failed/partial reads do not.
   An existing account needs no new connection. Do not insert get/status before
   a pure switch merely to fill parameters.
2. Use a verified exact account already in context, or read `platforms list` to
   match the provider, instance, account and connection version. Resolve real ambiguity through the fixed forms below.
   Do not switch accounts after failure or guess from a matching display name.
3. Give each connection, selection and complex member configuration step its own
   stable 32-character hexadecimal operation ID. Pure notification/existing-tool switches
   use the server-derived ID; do not generate one. Repeated ID/input recovers a receipt;
   changed input conflicts. Keep the original human source across events and
   continuation. Never replace an ID or invent a source to retry unknown work.
4. Start long human interactions with `--notify inbox`. Tell the human to use the
   original message's fixed component; authorization links and QR codes stay in
   that component, not chat history or private files. Full settings shares its
   receipt; closing settings does not cancel the conversation operation.
5. Confirm the receipt and result subscription, then end the turn; do not wait
   or poll for clicks/authorization. Use `connect wait` at most once per turn only
   when the human explicitly asks to wait briefly or for bounded recovery/diagnosis.
   Pending or CLI interruption does not cancel work.
   Do not loop to keep a turn alive. The human may speak again to continue.
6. Wait, user continuation and Inbox events use the same original receipt. Read
   latest status, newer user intent and existing member steps; apply only missing
   requested work. `completed` plus `connection.status=connected` establishes a
   saved connection; app setup and authorization have separate outcomes. Before dependent follow-on changes, check current access/configuration through
   list/member get; a completed pure-switch receipt needs no extra get.
7. Connection and selection results require acknowledgment; member saves have
   their own completed receipt and no acknowledge step. After reconciling a
   connection/selection result and its follow-on step, use `connect acknowledge
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

## Notification total switch

For a plain request to enable or disable Feishu or GitLab notifications with an
exact verified platform ID, directly submit the original human message seq and switch:

```sh
teamx platforms member notifications --platform-id ID --source-seq SEQ --enabled true
```

Submit this command immediately after verifying the account. Do not prepend
get/status or generate an operation ID. Use get for a requested state query,
complex edits, conflicts or historical-receipt reconciliation, not as a switch
preflight. The server
observes current state, freezes the target and recovers the original receipt on
repeat submission. Keep the original seq across turns; never choose the latest
message automatically. A different switch for the same source conflicts. For an
optional current read, use `platforms member get --platform-id ID`; an explicit
`--platform-version V` remains an exact version read and must be nonzero.

Use false to disable. First enable uses the same platform defaults as manual
setup; an existing configuration restores its saved sources, ranges, filters,
interval and reception start. Do not expand this plain request into source flags
or add a group/project choice. If saved settings have no enabled reception source,
ask which source to enable instead of replacing them with first-use defaults.
Closing an absent configuration creates no subscription or reception start.
Already applied unchanged settings return preserved without a new version.
Verify the returned receipt before reporting its saved result. Historical receipts
do not establish current settings; read get when current state is needed. Conflicts
and unknown/partial results require inspection through original status/get and
settings recovery; do not automatically retry or create a new source.

For source customization, follow the provider section. New customization enables
only explicitly requested sources; omitted sources in saved settings are preserved.
An exclusive request such as “只接收这个群” after a verified group choice means
receive only that selected group: use specified group scope and disable private
reception. In contrast, “群只接收这个群，其他设置保留” changes only group scope.
Use the original selected native ID, not a new search or another selection card.
An explicit total false overrides source enables in the same operation. A source
enable that would also awaken another saved dormant source still needs explicit
choices for both; the pure total switch intentionally restores all saved sources.

## Selection cards and editable drafts

For “飞书选择群的卡片”, “GitLab 项目选择卡片” or “Scion 仓库选择卡片”, recover selection status by the
original human seq, verify the exact account, then create the existing component:

```sh
teamx platforms selection start --kind groups --platform-id ID --platform-version V --operation-id PICK_ID --source-seq SEQ --notify inbox
teamx platforms selection start --provider gitlab --kind projects --platform-id ID --platform-version V --operation-id PICK_ID --source-seq SEQ --notify inbox
```

Search, pagination, retained choices and retry belong to the component. Do not
first enumerate chats/projects, check business sending grants, search source code,
or present a text list/chat_id paste instructions as a card. Verify the creation
receipt and result subscription, briefly point to the card and end this turn.
For a card-only request, after submission verify the selected values, acknowledge
the exact result_version and report the choice. That finishes the original request;
do not wait for another configuration instruction before acknowledging, or apply
notifications. Showing a card alone authorizes no notification change. If the original request
also asked to subscribe, reconcile that intent and newer messages after submit,
then apply only the requested complex change with observed versions. Switching
all groups to specified requires explicit complete replacement intent; unchecking
a card does not remove saved subscriptions.

Publish forms only for a real choice or missing field; an unambiguous explicit ID
needs no redundant confirmation. Recover original selection steps before starting.

| Choice | Start fields after `platforms selection start` |
| --- | --- |
| Feishu account/new connection name | `--provider lark --kind account --name NAME` (name may be empty) |
| Feishu groups | `--provider lark --kind groups --platform-id ID --platform-version V --query NAME` |
| GitLab verified accounts on one instance | `--provider gitlab --kind account --origin URL` |
| Scion verified existing accounts | `--provider scion --kind account [--origin URL]` |
| Scion repositories on one account | `--provider scion --kind repositories --platform-id ID --platform-version V --query NAME` |
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
Then branch on the original human request, not your own earlier offer or promise:

- A request only to display a selection card authorizes choosing a value. Verify
  the submitted values, acknowledge the exact result_version and report them.
  Do not read member configuration or call member notifications for this path.
  An earlier plain notification-enable request does not authorize enabling a new
  source or replacing its scope after a later card-only request.
- Only an explicit configuration request (for example, choose a project **and
  subscribe to its activity**) continues into member configuration. Inspect the
  current account/configuration and retained observed version. If that version
  changed, reconcile the changed scope before constructing a new request. Apply
  only the requested change under its own stable ID, then acknowledge the result.
  Full settings may already satisfy the step: verify exact member/account/scope
  before skipping it. Selection itself never applies configuration.
When a handled connection opens a group/project form, acknowledge that connection
before ending the turn; the form has its own independent continuation and receipt.

## Scion existing connections, tools and Issue notifications

Scion uses the same member configuration and account selection mechanisms. A single
human request can enable both tools and notifications: retain the same original
human SEQ, but use separate stable TOOL_STEP_ID and NOTIFICATION_STEP_ID receipts.
Report each actual result, including partial success; there is no combined rollback.
A plain request to connect/enable/use Scion means make it usable for this member:
enable tools and notifications, respecting explicit limits. First reception requires
an explicit repository. If it is missing, finish the authorized tool setup and
show its repository selection card; never infer it from Workspace or claim notification success.
An existing subscription can be restored with the common total switch.

Use `platforms list` to find the exact verified existing Scion connection.
The result's `scion_enabled` is the current Space/deployment capability. When false,
report that Scion is unavailable here; do not offer PAT login, open a card or promise
that retrying/waiting will restore it. `space_capability_unavailable` means the same
admission restriction, not an expired account. Personal Spaces keep separate Scion
connections and credentials; never reuse another Space's connection ID or profile.

Use the listed verified Scion accounts. If several
accounts are plausible or no verified account is available, use `selection start --provider scion --kind account
[--origin URL] --operation-id PICK_ID --source-seq SEQ --notify inbox`, following
selection recovery below. Selection saves no tools or notifications. Missing or
invalid accounts use the account card's protected PAT field and “登录并继续” button.
The card links to the instance's PAT management page and asks for the instance only
when it is unknown. An empty verified account list is sufficient evidence to show this
card: point to its PAT login field and end this step; do
not try placeholder IDs, inspect local files/environment or probe member writes.
PATs never belong in
chat messages, model arguments or member JSON; the password control submits directly
to the existing human-only login API. Do not use `platforms connect start` for Scion;
it does not support Scion login. The human confirms “登录并继续” once; the page
logs in, saves and verifies the connection, then the original card submits that exact
connection/version and delivers its normal selection result via Inbox. Recover and
verify that original result before continuing only the still-current request.
Unsubmitted PAT text is cleared on page exit and never saved in the conversation.
If the browser reloads
before submission, reuse the original card to select the saved account; do not
create a replacement or repeat a login whose result is unknown.

Read `member get --platform-id ID`: `scion_notifications`, `subscription_id`,
`notification_version`, `baseline`, `binding_revision` and `tool_enabled` describe
this member's actual settings. For initial tool setup use the complete member tool
command below with its own ID and observed platform version. For an existing binding
use the short tool switch. Scion rejects Feishu action flags. Tool enable refreshes
available tools through the existing Session lifecycle; saved config alone is not
proof of an actual native call. Tool and notification changes remain independent.

When the repository is unspecified, or the human requests a repository picker,
use `platforms selection start --provider scion --kind repositories` with the exact
connection ID/version, original source SEQ and its own stable PICK_ID. Reuse the
selection workflow above; never enumerate repositories or ask for pasted paths
in place of the requested card. The component filters names/descriptions within
a bounded list, supports native organization/repository prefixes and marks
truncation. On completion verify `draft.repository.name` and `created_at_ms`,
then use that name as `--scion-repository` only if the original request authorized
notification configuration. A card-only request ends after acknowledging the
selection; it creates no subscription. Repository draft edits require both
`--repository OWNER/REPO --repository-created-at-ms N`.

For first Issue reception, use the complete command:

```sh
teamx platforms member notifications --platform-id ID --platform-version V --notification-version 0 --operation-id NOTIFICATION_STEP_ID --source-seq SEQ --enabled true --scion-repository OWNER/REPO --scion-object-type issue --scion-backfill-start RFC3339
```

Omit the start for now, or use `--scion-backfill-seconds 86400` for the last day;
never supply both. Interpret “today” in the user's timezone, submit midnight with
an explicit RFC3339 offset/UTC, not a bare local date. The server verifies the
repository on that instance and freezes the start once. Review, comments and review
activity are unavailable. Only open Issue creations visible at polling time are
covered; retention/expiry can narrow historical reception. Notifications use this
member's private Inbox and need neither tools nor Workspace. No shared Inbox.

After first save repository, object types and reception start are fixed. Interval
edits use complete observed mode with only `--scion-interval-seconds 30..3600`;
omitted settings are retained. Pause/resume uses the common short notifications
switch without versions or a client ID, preserving progress and start. A first
pure enable without a repository returns “请选择仓库”; it creates no subscription.
Keep conflicts and unknown operations under the original ID/SEQ; status never
reapplies them. Do not start `while scn ...` or another polling shell. Formal
input read/ack is separate from subscription configuration.

## Feishu connection and selected groups

Reuse an exact verified connection by including its ID/version/name; otherwise:

```sh
teamx platforms connect start --provider lark --name NAME --operation-id ID --source-seq SEQ --notify inbox
```

For reuse, add `--platform-id ID --platform-version V`. The message component may
have application setup and user authorization stages. In a CLI-only client,
observe a live `interaction.authorization_url` without persisting it. Report a
connection only after the common lifecycle checks. An explicit connection-only
request ends there; otherwise continue the conversational onboarding path above.

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
`image.upload` is optional resource readiness, not a member action or grant.
Its absence must not block enabling or using text actions. Repair it only when
the user requests image permission repair; never pass it as a member --action.
User image upload requires application and user `im:resource`. A ready upload
permission does not open image sending: use only a resolved send action whose
input_schema explicitly declares images. Keep the original account, member grants
and notification choices during repair.
Same-account OAuth restores only enabled credential-paused subscriptions and keeps
progress/backlog. Other members' grants are untouched; old self evidence becomes
stale and requires their own explicit repair. Completed partial configurations
need a new human repair request, not automatic replay of the old operation.

For a requested selection card, use the selection section directly. The following
search path is for resolving a named group in a configuration request:

1. Recover prior member steps, then read `platforms member get --platform-id ID
   --platform-version V`. Retain the independent `notification_version` and
   `binding_revision`; verify the account and full member identity.
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

For source/range/filter changes in an unambiguous real human settings request, recover original member steps and
read member get, then directly submit one notification operation with all requested
fields. Do not reconnect, enable tools, create an editing draft or ask for redundant
confirmation. Preserve exact member/account, notification version, original source
and stable operation ID. Unspecified fields, tool grants and reception start stay
unchanged. An exclusive reception request such as “只接收这个群” includes disabling
other reception branches explicitly; a request confined to the group range and
preserving other settings does not. The server does not infer that exclusion from
a group ID. An existing selection card does not turn a request about saved settings
into a draft edit; reconcile newer human intent before applying a late old selection.

| Requested change | Flags on `platforms member notifications` |
| --- | --- |
| Add / remove followed groups | Repeat `--add-chat-id ID` / `--remove-chat-id ID` |
| Replace the group range | `--group-scope specified` and repeat `--group-chat-id ID` |
| Receive only this group, excluding private reception | Complete `--group-scope specified --group-chat-id ID` plus `--private-enabled false` |
| Clear the specified group list | A single `--group-chat-id ''` |
| All groups | `--group-scope all` |
| Group reception switch | `--group-enabled true\|false` |
| Only @me | `--group-message-mode mentions --group-at-me true --group-at-all false` |
| Only @all | `--group-message-mode mentions --group-at-me false --group-at-all true` |
| All messages in the selected groups | `--group-message-mode all` |
| Private reception switch | `--private-enabled true\|false` |
| All accessible person-to-person chats | `--private-scope all` and `--private-enabled true` when enabling is requested |
| Replace specified personnel chats | `--private-scope specified` and repeat `--private-chat-id CHAT_ID` |
| Clear the specified private list | A single `--private-chat-id ''` |

These customized notification changes require the explicit observed `--notification-version N`: 0 means absent; omission is rejected. Never refresh it automatically after a conflict, including complete range replacements.
All changes also need the usual exact platform ID/version, notification version,
operation ID and real human source sequence. Add/remove and complete replacement
are mutually exclusive; never add and remove the same ID. A replacement list must
include every intended group, including choices not on the current candidate page.
Scope and condition flags alone do not enable reception. Explicit branch enable
resumes the total switch unless the human explicitly keeps `--enabled false`.
To replace and enable a group range, also use `--group-enabled true`. When a branch
enable would activate another dormant branch, obtain the human's source choice and include
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
specified private range. Clearing the last specified private chat also disables that
branch and, if no branch remains enabled, total reception. Explicitly enabling an
empty specified list conflicts. Switching all private chats to specified requires
the explicit scope and complete list. Polling interval changes still use full settings.
No Workspace is required.

For specified people, use `platforms chats --platform-id ID --platform-version V
--type p2p --query NAME`; omit query to browse the private directory. Only proven
personnel conversations are returned; bots, groups and self chats are unsupported.
Compare names and exact native `chat_id` values. For same-name candidates, show the
distinct IDs and clarify before submitting; never pick the first result or use a
person's `open_id` as a conversation ID. Do not add a personnel selection card.
Follow directory `has_more`/`page_token` even when the filtered page is empty.
Name search cannot paginate and returns `refine_query` when more matches exist;
ask for a more precise name or browse the directory. Preserve `search_warnings`:
permission failures, unverified relationships or partial results do not establish
absence. Explain missing access or the need for an existing verifiable personnel
conversation; never send a message to create one. Exact IDs are re-read and their
same-account peer relationship verified again at save time. Include the complete
intended list, not just the current page, and `--private-enabled true` only when
enabling is requested. Combine all requested group/private changes in one operation.

Mentions mode requires at least one selected mention condition when explicitly
editing those conditions. Do not silently turn @me back on after the human asked
for both to be off. All-message mode retains mention preferences without filtering.
The simple mentions-mode command still defaults to @me if neither was selected.

Use existing account/group/project/repository cards only for choices they already represent;
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

Read `platforms list` before asking for an instance address. It includes
`gitlab_managed`, `gitlab_enabled` and public `gitlab_applications` with IDs,
canonical origins and compatibility reasons. Match the expected username when
the human specifies one; do not ask for it merely to fill an optional field.
Application references, connection IDs and account identities are distinct.

When `gitlab_managed=true`, the administrator owns application configuration.
If `gitlab_enabled=false`, explain that this deployment has not enabled GitLab.
Otherwise reuse an unambiguous verified account. For a new connection, use the
only compatible service application and its origin directly, including when the
human only says "connect GitLab"; no instance-address or application-setup
question is needed. If several compatible applications remain, ask the human to
choose among the actual configured candidates, respecting an explicit instance.
If none matches, explain the returned availability/compatibility issue and ask
the human to contact the administrator. Never ask a managed user to create or
import an application, provide Client ID/Secret, or supply an arbitrary instance
address as a workaround. Continue with the existing authorization component.

For personal applications (`gitlab_managed=false`), reuse an unambiguous known
account/application origin. Ask for the instance only if it remains unknown or
ambiguous. Canonical `--origin` has no trailing slash.

```sh
# Reuse the exact verified account; no fresh OAuth is needed.
teamx platforms connect start --provider gitlab --origin URL --name NAME --platform-id PLATFORM_ID --platform-version V --account-username USER --operation-id OP_ID --source-seq SEQ --notify inbox
# New account with an explicitly chosen compatible application.
teamx platforms connect start --provider gitlab --origin URL --name NAME --application-id APP_ID --account-username USER --operation-id OP_ID --source-seq SEQ --notify inbox
```

A unique compatible application on the exact instance needs no redundant approval.
For personal applications, when none is compatible, or the human must choose/configure one, omit
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
selects a replacement. Apply the common result/continuation checks. A connection
receipt alone does not enable notifications or tools: finish the requested member
steps through the conversational onboarding path above. External replies and
to-do completion still require their own business authorization.

## GitLab personal to-dos, project activity and filters

For source/filter changes, read member status for original steps, then
`platforms member get --platform-id ID`. Check account/member, notification/tool versions and
`gitlab_notifications`: desired config, pending sources, errors and applying state.
Version 0 with no collector subscription IDs is an unsaved draft, even if its
default switches are true. A saved desired version with pending/errors is a partial application; retain its original receipt and inspect per-source state through member get. Use the existing explicit settings retry-apply entry to apply pending sources; never resubmit the original intent to repair them.

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

For a requested project card, use selection directly. For resolving a named project
in a configuration request, search through `platforms projects --platform-id ID --platform-version V
--query NAME [--page-token PAGE]`. Follow numeric `next_page` until 0, including empty
pages. Compare native `id` and full `path_with_namespace`. Visibility does not grant
business write permission. Use the project form below for an actual choice.

Collect all requested source/filter changes into one notification step for the same
request/account. Omitted dimensions are preserved. If enabling one source would
also enable another dormant source under the shared switch, ask for the explicit
source choice and submit both switches together; do not rewrite unrelated intent.
GitLab rejects Feishu private/group flags. For a plain total switch, use the
common notification toggle above rather than changing individual source switches.

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
teamx platforms member tool --platform-id ID --source-seq SEQ --enabled true
```

Use this short command for an existing exact binding; false disables it. Do not
first read member get/status, fill versions or generate an operation ID. The server
freezes the current account/binding and restores only saved grants, never default
or newly added actions. Disable retains dormant grants for that account and waits
for active calls to settle; capability proof is cleared and checked again on enable.
An already selected state is preserved. Initial setup, missing saved authorization,
or an account replacement requires the complete workflow below. A user OAuth gap
returns `not_applied / user_authorization_required`; repair that exact connection.
A failed restore does not authorize adding defaults or new actions. Use complete
setup/grant changes only when the human's actual request covers those actions.
No notification source, filter or subscription is changed by a tool switch.

Repeat identical input with the original human seq only to recover its receipt;
changing the switch under the same seq conflicts. New human intent uses its new
seq. Receipts are historical, not proof of current configuration. For interrupted
or unknown submissions, inspect `member status --source-seq SEQ` and authoritative
member settings; never resubmit under a new source/ID or automatically refresh a
conflict. A completed save needs no extra get/status. No extra acknowledgment is
required. Later explicit corrections and revocations still win.

Initial setup or explicit grant changes use the complete command:

```sh
teamx platforms member tool --platform-id ID --platform-version V --operation-id TOOL_STEP_ID --source-seq SEQ --enabled true
```

Complete enable/disable is an absolute intent for this exact binding and needs no
Room tool revision. Independent tools/members can be configured from the same
human request. To protect an earlier observation, pass `--binding-revision N`
from member get's `binding_revision`; explicit 0 means never configured, while
omission lets the server freeze the current binding at admission. Never refresh
that observation automatically after a conflict. `--tool-revision` is retired.
The operation ID fixes business input (including the optional observation), not
the server's internal snapshot. Repeat the same ID only with identical input;
status reads never apply or repair settings. Unknown results remain fenced after
restart. A timeout plus one missing status record does not prove non-application:
wait for settlement and inspect authoritative state before recovery. A conflict
is not evidence of past authorization. Check later human instructions yourself.

Any explicit version, binding-revision, operation-id or action selects complete
mode and requires platform-version plus operation-id. Use false to disable.
Complete Feishu enable without action configures the fixed current reply,
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
