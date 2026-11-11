---
name: gitlab
description: GitLab personal to-dos, verified discussions, development and exact-version review.
---

# GitLab workflows

Use the exact account entry from `"$TEAMX_CLI" tools list`; the service substitutes
that entry in this Skill's `tools run gitlab` examples. Discover commands through
`"$TEAMX_CLI" tools run gitlab --help`. Work under the selected account's current
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

## Personal to-dos and everyday assistance

1. Read the offered Inbox batch. Handle internal task_event entries by querying
   their original operation; they are not GitLab objects or fresh human authority.
   For platform entries, identify instance, receiving account, project and object.
   Direct human requests may also start work without any notification.
2. Recover relevant progress with `matters list/read`; inspect original evidence
   through `matters sources/source` and follow all pagination. Source resolutions
   are at entries[].source. A verified object_label or object_url aids display;
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
   `matters read/reply` to associate the real answer. Acknowledge local progress
   without inventing a platform write or asking the same resolved question again.
5. Re-read subsequent replies before responding. Existing Inbox MR comments use
   managed `actions execute` with object.comment; exact native discussions use
   discussion.reply. First resolve the saved target_ref and use the returned
   schema and current authorization. Optional matter context associates the work
   but cannot replace the execution target. Do not fall back to a general comment
   when a discussion reply is unavailable. Open a separate discussion followup
   with `matters ensure` only when independent handling is needed; unresolved
   sources use explicit `matters resolve`, never polling or invented Inbox IDs.
6. Verify the actual remote result, then save progress and publish a useful local
   update with send. Only after the business issue is handled and verified should
   you complete its exact personal to-do: query `todo list` or the explicit-instance
   API to verify to-do ID, receiving account and target. Never derive it from an
   item_key or bulk-clear the list. To-do completion is not business completion.

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
Inbox-associated comments use the managed action path above. A direct Room task
with no Inbox matter may use native `mr note create --resolvable=false` under its
actual authority; do not fabricate a matter. Include full base/head, findings,
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
business receipts. Managed object comments may make at most two attempts for a
temporary fault and may duplicate; discussion replies have their own returned
retry policy. Do not add Agent retries. Check exact note ID, author, body and
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
