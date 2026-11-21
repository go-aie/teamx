# Feishu messages and actions

Use `"$TEAMX_CLI" tools list` and the exact enabled account entry. The service
substitutes that entry for `lark` below. Reuse an already known entry/target;
never switch accounts after failure. For requested setup or repair, read
`"$TEAMX_CLI" platforms skill`; follow the error's reason/next_action.
Use only the declared TeamX tools for this workflow, without native platform
commands or local file creation/search. Action receipts retain progress.

## Understand a saved message

Read the offered Inbox batch and preserve sender, chat, mentions and completeness.
Own-account context_only messages are progress, not new tasks. Internal task_event
entries use their local workflow. For unfinished work recover `matters list/read`
and `matters sources/source`; reuse saved object_ref/target_ref. Resolve a retained
unresolved source with matters resolve only when needed.

For fresh content or context on the same account:

```sh
"$TEAMX_CLI" tools run lark read --object OBJECT_REF --target TARGET_REF --scope content
"$TEAMX_CLI" tools run lark read --object OBJECT_REF --target TARGET_REF --scope context
```

Follow returned cursors while incomplete. Check later replies before reminding or
replying. A captured message is not fresh access evidence; an unread attachment
has not been understood. Publish questions/reminders locally with `send`; use an
existing matter's notify/reply workflow when it needs a human decision.

## Confirm target → select content → execute → report

Use current runtime turn_number (and current policy_token for a group). A binding,
candidate, display name or accessible target is not business permission.

For “my Feishu”, resolve the bound account's self-chat:

```sh
"$TEAMX_CLI" tools run lark actions resolve --request '{"locator":{"self":true},"turn_number":1}'
```

For a named group, query once, choose the exact candidate, then pass its WHOLE
`candidates[i].locator` unchanged as the next request's locator on the SAME entry.
The service rechecks the target. After clarification use the NEW current turn.

```sh
"$TEAMX_CLI" tools run lark actions resolve --request '{"locator":{"query":{"object_type":"chat","text":"项目群"}},"turn_number":1}'
```

Candidates are under resolution.candidates. Clarify duplicate names or multiple
plausible source images in the Room with `send`; retain candidates_incomplete.
Do not ask again after the user has chosen. Reuse a valid target_ref directly;
for a saved message reply resolve `{locator:{target_ref:REF},turn_number:TURN}`.
Use only returned executable actions and their input_schema/required/authority.
Read `tools run lark actions resolve --help` or `execute --help` when needed.
For multiline JSON, use `--request-file -` with a here-document on stdin. Never
stage requests in local files.

## Send selected Room images as one message

Take actual human message seq and attachments[].id from Inbox or History.
Sources must be retained images published by a human in this Room; destinations
are the bound user's self-chat or one verified supported group.
Previously consumed images can be forwarded while retained, even without image
recognition. Never claim to understand an unseen image. Include images only when
the current input_schema declares them; ordinary replies remain text only.
Format and limits come from that schema. No attachment lookup is required.

Self example (replace refs, turn, ID and content with actual values):

```sh
"$TEAMX_CLI" tools run lark actions execute --request '{"target_ref":"ref_FROM_SELF","turn_number":1,"id":"save-selected","operation":"message.send_self","input":{"body":"说明原文","images":[{"message_seq":100,"attachment_id":"img_0123456789abcdef0123456789abcdef"}]}}'
```

Self uses its saved capability; omit policy_token and authority. Group example:

```sh
"$TEAMX_CLI" tools run lark actions execute --request '{"target_ref":"ref_FROM_GROUP","turn_number":2,"id":"send-selected","operation":"message.send","policy_token":"CURRENT_RUNTIME_TOKEN","authority":{"type":"human_reply","room_seq":102,"reason":"按此直接指令向选定群发送完整内容"},"input":{"body":"说明原文","images":[{"message_seq":100,"attachment_id":"img_0123456789abcdef0123456789abcdef"}]}}'
```

room_seq is the real direct sending instruction, which can differ from the image
source seq. Self permission does not grant group permission. For text omit images;
for images without a caption omit body. Preserve exact text and image order.
Submit the ENTIRE selection in ONE action with ONE stable intent ID: text first,
then ordered images. Never split captions/images, add a remote explanation, pass
paths/URLs/base64/image keys/frame indices, or substitute text/a subset on failure.

Report the receipt locally with `"$TEAMX_CLI" send --text ...`. Succeeded means the
exact account, destination and complete content were verified; upload success
alone is not delivery. Source/upload failure means no message was sent. For
unknown or a restart, query `"$TEAMX_CLI" actions read --id ORIGINAL_ID`; retain any
known remote ID and never resend under another ID, account or Session. Existing
receipts restore complete evidence. A new explicit retry after proven not_applied
can create a new complete action. Delivery does not mean read or task completion.
