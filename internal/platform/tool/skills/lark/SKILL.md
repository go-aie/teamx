# Feishu messages and actions

Read this Skill for Feishu/Lark work through a member's enabled platform entry.
Use `"$TEAMX_CLI" tools list` and its exact account entry; the service substitutes
that entry in the commands below. This is a bounded built-in dispatcher, not
arbitrary lark-cli access. For connecting an account or changing your subscriptions
and tools, read `"$TEAMX_CLI" platforms skill` without requiring a tool binding.

## Understand and follow up

1. Read the offered Inbox batch. Platform notifications contain captured content
   and resolution evidence at entries[].source; internal task_event entries use
   the original local operation workflow, never remote message resolution.
   Preserve sender, receiver, chat, mention and incompleteness metadata. Own-account
   context_only messages show progress and are not new tasks or automatic replies.
2. Judge relevance against the user's request and current rules. A saved message
   can be understood without platform tools. For unfinished work, recover existing
   `matters list/read`; use `matters sources/source` for original evidence. Reuse
   returned object_ref/target_ref instead of deriving identity from names or URLs.
   Explicitly resolve an unresolved retained source with matters resolve only when
   needed. Failed or partial resolution does not establish absence.
3. When fresh text or surrounding context is necessary, use the matching enabled
   entry and saved native message references:

```sh
"$TEAMX_CLI" tools run lark --help
"$TEAMX_CLI" tools run lark read --object OBJECT_REF --target TARGET_REF --scope content
"$TEAMX_CLI" tools run lark read --object OBJECT_REF --target TARGET_REF --scope context
```

Context and pages are bounded. Continue with the returned cursor while incomplete,
keeping the same target/account; do not describe one page as the full conversation.
Check later replies before reminding or replying. Captured evidence is not a fresh
access check; an attachment marked not_read has not been understood. Do not infer
image contents from its key or preview URL, or claim an unread attachment was read.

4. If the user needs to decide, publish a concise local question with the original
   context/link and your proposed next step; for an existing matter use its notify
   and reply-association workflow. For a lasting reminder request, preserve source,
   condition and local destination through the common policy workflow. Subscription,
   rule save, actual arrival and a published reminder are separate results. A local
   reminder uses send and needs no additional external sending grant.

## Reply or send through declared actions

A tool binding is not business permission. Use only the exact target, currently
executable action and applicable human instruction/rule. First resolve:

```sh
"$TEAMX_CLI" tools run lark actions resolve --request '{"locator":{"target_ref":"ref_..."},"turn_number":1}'
```

Replace example refs/turns with returned real values. For supported direct sends,
verified group messages or self-chat, use the action help's declared locator;
resolve and verify any candidates, account ownership and completeness requirements.
Never guess an identity, treat directory metadata as send permission or silently
substitute a recipient/account after failure.

Execute with `tools run lark actions execute --request JSON`, using the current
`actions resolve/execute --help` schema, stable action ID, policy token and typed
authority. Optional matter context cannot replace target_ref. Do not fall back to
native IM/login commands when a capability or grant is missing. Explain the
returned limitation and retain a draft or local next step instead.

Verify the returned receipt and actual result before reporting a remote reply or
send. Unknown results require receipt/state checks; no new ID, entry or backend
may resend them. Save useful progress in the existing matter. Publish user-facing
results with `"$TEAMX_CLI" send`; ordinary private model output is not a reply.
