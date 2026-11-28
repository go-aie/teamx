## Show an account, group, project or repository selection card

For a real request such as “飞书选择群的卡片”, “GitLab 项目选择卡片” or “Scion 仓库选择卡片”, use the
fixed component directly. Retain the original human `SEQ`. If the original
selection is not already known, recover it before starting:

```sh
teamx platforms selection status --source-seq SEQ
```

Reuse any matching receipt; only `operation_not_found` establishes absence.
Failed or partial reads do not. Each new selection step uses its own stable
32-character hexadecimal `PICK_ID`. Same ID/input recovers its receipt; changed
input conflicts. Groups/projects/repositories require an exact verified account ID and
nonzero connection version from retained context or `platforms list`.

```sh
teamx platforms selection start --provider lark --kind groups --platform-id ID --platform-version V --operation-id PICK_ID --source-seq SEQ --notify inbox
teamx platforms selection start --provider gitlab --kind projects --platform-id ID --platform-version V --operation-id PICK_ID --source-seq SEQ --notify inbox
teamx platforms selection start --provider scion --kind repositories --platform-id ID --platform-version V --operation-id PICK_ID --source-seq SEQ --notify inbox
```

For account cards, use the following fields instead of group/project fields:

| Choice | Fields after `platforms selection start` |
| --- | --- |
| Feishu account/new connection name | `--provider lark --kind account --name NAME` (name may be empty) |
| Verified existing Scion accounts | `--provider scion --kind account [--origin URL]` |
| Verified GitLab account on one instance | `--provider gitlab --kind account --origin URL` (canonical instance URL) |

Every start also needs `--operation-id PICK_ID --source-seq SEQ --notify inbox`.
An account card selects verified existing accounts; it does not create an account,
application or connection. Empty Scion candidates require Settings → Platforms → Scion login (PAT stays in
protected settings); after login re-read connection/version and continue the original
request. Other empty candidates use the complete platforms connection guide. An unambiguous explicit ID needs no redundant card,
unless the human requested the card itself.

Let the component search, paginate and retain choices; `--query NAME` can seed a
group/project/repository search. Scion filters a bounded visible list by name or
description; an organization/repository prefix narrows the native query. A
truncated list is incomplete; let the card load more or refine the prefix. Do not first enumerate chats/projects/repositories, check business sending
grants, present text/paste instructions instead of the card, or read credentials.
Only the original human submits, through the original message component or full
settings. Confirm the creation receipt and `continuation.mode=inbox`, briefly
point to the card, then end the turn without waiting for clicks. No extra notify,
draft update or polling is needed. Closing settings does not cancel the operation.
Scheduling blocks and pause/rotation still apply; subscription alone cannot
promise immediate wake-up. The human may continue through the normal conversation.

## Asynchronous result and interrupted-turn recovery

A Room operation-result event with `result_ref=platforms/selection/PICK_ID` points
to the original selection. Read the original status, rather than creating a new
card or using the event's Room sequence as human authority:

```sh
teamx platforms selection status --operation-id PICK_ID
```

Recover the retained original human request, latest intent and any existing
follow-on receipts. Use `status --source-seq ORIGINAL_SEQ` when the ID is lost.
Check the exact submitted account/group/project/repository: account `draft.platform_id`,
`draft.platform_version`, name and account identity; group `draft.chat_ids` with
verified chats; project `draft.project_id`, `draft.project_path` with the verified
project and optional events filter; Scion repository `draft.repository.name` and
`draft.repository.created_at_ms`, reverified on submission against the pinned account. Display names are not substitute IDs.

For a card-only request, `status=completed` plus verified submitted values finishes
the choice. Do not read member configuration, connect the account or change
notifications/tools. An earlier enable request, an Agent offer or unchecked card
items do not authorize a new source, removal or scope replacement. After verifying
the choice, acknowledge its exact stable completion `result_version`:

```sh
teamx platforms selection acknowledge --operation-id PICK_ID --result-version RESULT_VERSION
```

`result_version` is not the editable draft `version`. Confirm that
`continuation.handled_version` covers the result before one completion report.
If it already covers this version, consume a late event normally and skip handled
work and duplicate feedback. Inbox delivery/consumption/status do not acknowledge
or apply settings. After an interrupted turn inspect history and original step
receipts; finish only missing work, including an unsent report.

Only an explicit configuration request, such as “选择项目并订阅其动态”, permits a
follow-on save. Use the complete platforms skill for that complex operation:
verify current account/member settings, retain observed notification versions,
reconcile newer intent/conflicts and inspect original member step receipts before
applying only missing requested work under its own stable ID and original source.
Full settings may already have satisfied it. Then acknowledge the selection result
and report once. A completed member save itself needs no acknowledge. If a handled
connection opens this card, acknowledge that connection's exact result separately;
the selection has its own receipt and continuation. `platforms/connect/ID` events
use the complete skill's connection recovery, not selection status.

## Failure, editing and cancellation

After an uncertain start or submission, query the original ID/source; never create
a replacement to bypass unknown work. A stale account/draft version requires
inspection and reconciliation, not silently refreshing observations or choosing
another account. Cancel only on an explicit human request, using the observed
draft version:

```sh
teamx platforms selection cancel --operation-id PICK_ID --version DRAFT_VERSION
```

Applied effects remain and late callbacks cannot revive cancellation. Drafts
expire after 24 hours; a terminal failed/expired card needs a new human request
before a new selection. Submitted values are immutable. Only when the human asks
the Agent to edit a draft, use `selection update --help`: updates replace the
complete draft under its observed version and do not submit or configure it.
`selection notify --operation-id PICK_ID --mode inbox|none` changes result delivery
only; subscribing late delivers the same stable event, and none cannot retract
queued events. Read continuation state/blocked_reason for the actual runtime block.
