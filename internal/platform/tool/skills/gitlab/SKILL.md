---
name: gitlab
description: GitLab personal to-dos, verified discussions, development and exact-version review.
---

# GitLab workflows

Reuse the known exact account entry; discover it with `"$TEAMX_CLI" tools list`
only when missing. The service substitutes that entry in this Skill's commands.
Read `"$TEAMX_CLI" tools run gitlab --help` when native usage is missing. Work under the selected account's current
permissions and the user's actual authorization. Notifications and Workspace do
not enable tools or authorize writes. Never inspect tokens, credential files,
.env or environment dumps, run the native CLI directly, or switch accounts after
failure. For onboarding or your notification/tool settings, read `platforms skill`.

Read the section matching the task. Use `matters`, `actions` and `policy` help for
current JSON fields. Structured writes accept `--request-file PATH` for actions
and `--change-file PATH` for matters/policy; - reads stdin. File and inline forms
are exclusive and preserve the same validation. Prefer files or quoted heredocs
for multiline bodies. Placeholder projects, IIDs, user IDs and SHAs require verified
values from this instance.

For a direct MR comment/reply request with a full URL, start at **Managed MR
comments and discussion replies** below. Its resolve verifies the account,
project and exact MR/thread; no preliminary native `mr view`, note listing or
ID lookup is needed. The context-reading steps below apply when the task itself
requires investigating remote content.

## Personal to-dos and everyday assistance

1. Read a known Inbox stream directly through the current Session transport;
   list only for missing choices. Room human intent takes priority. Handle Room
   operation-result events by querying their original operation; they are not GitLab objects or fresh human authority.
   For platform entries, identify instance, receiving account, project and object.
   Direct human requests may also start work without any notification.
2. Read a known matter directly; use `matters list` only to find missing refs.
   For missing original evidence, use `matters source --source SOURCE_REF` directly
   when known. Otherwise use `matters sources [--object OBJECT_REF]`; continue with
   `--cursor NEXT_CURSOR` and the same object filter, never Inbox `--seq`/`--after`.
   A verified object_label or object_url aids display;
   neither replaces native identity or the exact discussion reply target. Inspect
   `requires_read` and re-read after revision changes. `matters read --detail`
   restores omitted inspection/authority snapshots; all action outcomes and
   question/instruction relationships remain in the normal view.
3. Read current remote content and complete discussions using this account. Saved
   evidence is not a fresh permission/state check. Do not infer API type from a
   /work_items/ web URL or confuse project-local IID, REST ID and GraphQL ID.
   Partial results, denied access and not-found responses do not establish absence.
4. Judge the work against the user's request and all applicable long-term rules.
   Summarize, prepare a draft, or perform an available authorized action. For a
   needed human decision on an existing matter, use `matters notify`, then
   `matters read` to associate a real human answer with explicit reply_to.
   Use `matters reply` only for an otherwise unlinked real answer with clear
   contextual evidence. Save an instruction only when intent must persist across
   turns; a successful managed action referencing it automatically marks it fulfilled.
   Acknowledge local progress
   without inventing a platform write or asking the same resolved question again.
5. Re-read subsequent replies before responding. Use the managed comment workflow
   below for available MR targets. Open a separate discussion followup with
   `matters ensure --source SOURCE_REF --target TARGET_REF --turn TURN` only when
   independent handling is needed; retry insufficient source resolution explicitly
   with `matters resolve --source SOURCE_REF --turn TURN`, never polling or invented Inbox IDs.
6. Report managed results as described below. Verify other writes against their
   actual remote result. Save progress and publish a useful local update with send. Quote report text literally (single quotes or a quoted
   heredoc); never interpolate Markdown backticks in a double-quoted shell string. Only after the business issue is handled and verified should
   you complete its exact personal to-do: query `todo list` or the explicit-instance
   API to verify to-do ID, receiving account and target. Never derive it from an
   item_key or bulk-clear the list. To-do completion is not business completion.

## Managed MR comments and discussion replies

1. Confirm the selected account, exact MR/thread and complete content. Reuse an
   available target_ref and its current contract across turns, including when
   switching back from a discussion to its MR. A new turn alone does not require
   another resolve; use current turn_number/policy_token. When the contract is missing,
   use root `actions resolve --tool ENTRY --request JSON`. For example:
   `actions resolve --tool ENTRY --request '{"locator":{"target_ref":"REF"},"turn_number":1}'`.
   Substitute the actual ref and current turn number; locator/turn_number are JSON
   fields, never separate CLI flags. `tools run gitlab` remains the native CLI proxy. A
   full MR URL can establish a new managed target without notifications, a matter
   or Workspace. Use `actions resolve --tool ENTRY --request '{"locator":{"url":"https://gitlab.example/group/project/-/merge_requests/42"},"turn_number":1}'`.
   Use the supplied #note_ID or #discussion_FULL_ID anchor for an exact existing
   replyable thread; never remove an unsupported anchor to send a plain comment.
   Select ENTRY from tools list on the URL's instance. Do not look up native IDs
   with glab or manufacture a matter to send a comment.
   For an existing target_ref/context that already restores the account, omit
   --tool if the exact entry is unknown; never substitute a provider alias or
   list tools merely to repeat the recovered account.
2. Submit one complete request with root `actions execute --tool ENTRY --request JSON`:
   object.comment for the MR, discussion.reply for the exact discussion. Follow
   its dynamic input_schema and authorization. Direct requests with an available
   target need no new matter, saved instruction, or receipt precheck; do not query
   a new action ID before executing it. If a matter
   already applies, reuse its context and current version; context can supply the
   matching target_ref, without repeating top-level matter fields.
   If the requested thread or image set is unspecified, ask before any
   resolve/execute; these commands do not search for missing choices. GitLab
   resolve verifies a saved ref or a full URL, not an unspecified different discussion.
3. Report account, MR/thread, complete content and verification from the returned
   receipt; query detail only for uncertainty, recovery or an explicit request.
   For unknown, query
   `actions read --id ORIGINAL_ID`; retain any known note ID and explain what is
   unverified. A known note ID comes from the platform response, not a guess;
   existence/content/thread may still be unverified. A new receipt-query request
   needs an actual read. Never change ID, account or CLI to resend uncertain work;
   apparent absence alone does not authorize a new attempt. Durable
   action receipts save progress; do not create local progress files.

Managed MR comments and exact discussion replies support images when the current
instance's input_schema declares images. Select ordered Room human attachments from the
normal Inbox/History message seq and attachment ID. Keep body exactly as the
user requested. For image-only, omit body entirely; never send an empty string.
The service generates image Markdown.
Never append Room image links, attachment labels or upload paths to body.
The actual direct Room human
instruction must cover the target and selected complete content; supply its
room_seq/reason and current policy_token. Text rules, saved instructions and
question grants do not authorize images. Clarify ambiguous targets or selection,
then retain confirmed choices using current turn/policy values. Submit body and
all selected images together, once; never send a subset or fall back to text.
Dynamic schema supplies limits; `actions execute --help` has full MR-comment and
exact-thread examples. No separate image resolve or upload call is needed. On
not_applied, explain the reason and any unused-upload side effect, then stop;
a fresh complete attempt requires the human's explicit retry request.

MR/Issue reads use native commands where available. WorkItem extensions include
`work-items view`, `work-items discussions list`, `work-items note`; Issue complete
discussions use `issue discussions`. Follow native pagination. The extensions
read all pages before output and fail without partial output; their view and
discussions commands have no explicit pagination flags. View exposes
id/iid/title/type/description/state/assignees: null assignees means unsupported;
[] means supported with nobody assigned. Discussion output preserves native id,
replyId and notes with id/body/system/author. Use verified reply IDs, not labels.

Closing, deleting or changing assignees requires the user's specific intent.
Assignee add/remove preserve others, replace replaces all, and clear empties the
list. Resolve numeric user IDs through this exact instance. Keep rule conditions,
permissions and exceptions together in natural-language policy bodies. Explicit
lasting instructions may be saved; inferred habits need suggestion/confirmation.
One task's consent is not a standing rule. The common guide owns rule lifecycle.

## Workspace and network Git

Verify your current member Workspace, origin, branch, remote project and clean
state before repository work. If it is not configured, use API reads or ask the
human to apply Workspace settings; do not clone elsewhere or overwrite files.

For an OAuth-bound GitLab Workspace, network operations use the managed entry:

```sh
"$TEAMX_CLI" workspace-git fetch main
"$TEAMX_CLI" workspace-git fetch refs/merge-requests/IID/head
"$TEAMX_CLI" workspace-git push TASK_BRANCH
"$TEAMX_CLI" workspace-git ls-remote --heads
```

The service supplies the bound repository and member directory; pass refs/approved
flags, not origin, another repository URL or credentials. OAuth requires explicit
write_repository scope in addition to API access. Missing scope requires human
application/configuration and reauthorization, not an agent login or automatic
scope expansion. Each call is bounded to five minutes. Local status, diff, branch,
checkout and commit remain ordinary Git in your own Workspace. Follow the injected
Workspace instructions; never configure ordinary network Git askpass yourself.
Network interruption after push needs remote verification before another write.

## Development and handoff

1. Verify the project/default or user-chosen target branch. Continue an existing
   task branch/MR when appropriate; otherwise create a unique branch for this
   Room/member/task. Current checkout is not automatically the intended MR target.
2. Modify, test and review the diff. Commit only requested files; push the task
   branch through the network entry above when authorized. Preserve unrelated
   edits. Do not force-push, deploy, delete remote branches, push the target branch
   directly, or use reset --hard/clean as routine task work.
3. Query existing MRs for that source branch before creating one. Discover native
   mr create parameters through help; supply source, target, title and description.
   Read back the exact MR state, URL and full base/head. Do not invent TeamX
   operation/config/source fields for native commands.
4. Publish a review handoff with requirement, implementation, actual tests and
   exact version. After corrections, verify the new version before requesting
   another review. No substantive progress means no repeated handoff.

## Review the exact version

Read the latest MR and full discussions; verify project, IID, opened state, target
and full base/head. Check your own Workspace before switching. Fetch
refs/merge-requests/IID/head using workspace-git, confirm FETCH_HEAD equals the
expected head, then detach checkout. Verify the complete base exists and re-read
the MR version before reviewing. Inspect the full diff/files and independently
run relevant checks; an API diff can be truncated. With no local Workspace, report
API-only coverage and do not claim local validation.

Before publishing, check that version/account for an existing equivalent review.
For covered MR comments and discussion replies, use the managed action path above,
including direct Room tasks without an Inbox matter; do not fabricate a matter.
Native CLI remains available for other authorized operations. Include full base/head, findings,
locations, checks and unverified areas. An ordinary review comment is not approval.
Read back the actual comment and current MR version before publishing the result.
Old-version conclusions do not establish that a changed MR passed.

## Merge and close

Require the user's specific decision and role permission for the exact object,
action and version. A notification, peer agreement or review conclusion is not
consent. Reviewers do not merge, close or approve. Recheck MR, target, full base/head,
the matching review and latest human intent. Native merge fixes `--sha HEAD
--auto-merge=false`; do not default to deleting branches. Acceptance or queuing is
not merged state: read the actual result. Pause does not mean close, and close
does not authorize deletion or merge.

## Result verification and uncertainty

Native tools preserve stdout/stderr/exit status; managed actions return durable
business receipts. Managed text-only object comments may make at most two attempts for a
temporary fault and may duplicate; image actions allow only one attempt.
Discussion replies follow their returned retry policy. Do not add Agent retries. Check exact note ID, author, body and
version; matching text alone is not this action's receipt or proof of authorship.
Do not blame attempts=1 on service retry or delete an unexplained duplicate.

For an unknown managed receipt, or an interrupted/timed-out native write, retain
object, action, time, branch/version and returned IDs. Read saved receipts and
remote state; never change ID, account or backend to repeat uncertain work. A 404
alone does not prove a deletion. Explain the verified part and unresolved outcome.
Use the exact instance for APIs without repository context, and explicit projects
for extensions. Report permission, dependency, credential and transport failures
accurately. Check requires_read before using updated matter versions; distinguish
saved questions from confirmed publication. Only send publishes a local answer;
normal model output is private.
