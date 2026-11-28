## Notification total switch

Scion uses this same switch for an existing Issue subscription, preserving its
repository, start and progress. A first enable needs an explicit repository: read
`platforms skill` for Scion initial setup and the complete flags. Missing verified
accounts use the Scion account card's login/relogin entry through the complete
platforms skill; never put PATs in chat. Review
and comment reception remain unsupported. Tools are a separate member operation;
a single human request can authorize both, with separate receipts and outcomes.

For “开启飞书通知”, “关闭 GitLab 通知” and equivalent plain total-switch requests,
verify the exact existing account, then submit directly:

```sh
teamx platforms member notifications --platform-id ID --source-seq SEQ --enabled true
```

Use `false` to disable. Do not first read member get/status, generate an operation
ID, supply versions, enumerate groups/projects or show a selection card. Do not
connect a second account, enable tools, alter Workspace or create a lasting rule.
If there is no suitable connection, use the complete platforms skill for the
requested connection flow; do not guess an ID.

GitLab/Feishu first enable uses the same defaults as manual setup: Feishu's manual reception
defaults, or GitLab personal to-dos over accessible projects with project activity
off. Existing settings restore their saved sources, scopes, filters, interval and
reception start. If saved settings have no enabled source, ask which source to
enable; do not replace them with first-use defaults. Closing an absent setting
creates no subscription or reception start. Unchanged saved settings are preserved.

The server observes current versions, freezes the target and derives the stable
receipt from the original human source. Repeating the same switch/source recovers
that receipt; changing the switch under the same source conflicts. Never use a
newer message as the source to retry uncertain work.

## Completion and recovery

Check `status=completed` and the result's `applied|preserved` state and exact
member/account. A completed save needs no acknowledge or extra get. These member
receipts have no connection/selection `result_version` acknowledgment. Saving a
subscription does not prove a platform event has arrived or a reminder was sent.

On timeout, interruption, conflict, unknown or partial application, inspect the
original receipt and, when current state is needed, the current configuration:

```sh
teamx platforms member status --source-seq ORIGINAL_SEQ
teamx platforms member get --platform-id ORIGINAL_ID
```

Use `--operation-id ORIGINAL_OPERATION_ID` for status if it is already known.
Status does not replay work. A historical receipt describes its original operation,
not necessarily current settings; explicit current-state queries use get.
An absent/failed/partial read after a timeout does not prove non-application.
Preserve the original evidence and inspect authoritative state before recovery;
never retry unknown work under a new source or ID. Pending/error application uses
the existing settings retry-apply recovery, not resubmission of the old intent.

This topic covers only the total switch. A request to add/remove sources, select
groups/projects, change scopes or filters uses the complete platforms skill and
observed configuration versions. A requested selection card instead uses
`teamx platforms skill --topic selection` directly. Do not expand a plain switch
into either workflow.
