# Scion native repositories and Review

Read this Skill for native Scion work. First use `"$TEAMX_CLI" tools list`, then
`tools run scion --help` and `tools skill scion` with its exact enabled entry; the
service substitutes that entry here. Commands run in the current member's effective
Workspace on the server. A shell's temporary cd/env is not forwarded; use native
-C for subdirectories. Missing programs, login or mismatched identity need operator
recovery, not credential searches, direct scn execution or another account.

## Read and assist

The fixed tool is scn 0.1.13, with native JSON, stdout/stderr and exit status.
The selected connection supplies instance and Hub. There is no GitLab MR/PR shim.
For reads without a work copy, name the verified repository explicitly:

```sh
"$TEAMX_CLI" tools run scion --json --repo OWNER/REPOSITORY repo info OWNER/REPOSITORY
"$TEAMX_CLI" tools run scion --json --repo OWNER/REPOSITORY review ls --state all --limit 100
"$TEAMX_CLI" tools run scion --json --repo OWNER/REPOSITORY review show NUMBER
"$TEAMX_CLI" tools run scion --json --repo OWNER/REPOSITORY review comments NUMBER
```

Verify the repository and Review, read discussions and check current revisions
before summarizing, drafting a response or recommending a next step. Native tool
access is not authority to publish. Scion has no TeamX notification collector;
do not promise automatic Review reception or treat reading this Skill as a watcher.
Use the common guide for user questions, durable progress and local send replies.

truncated=true means incomplete coverage, never absence. review show exposes
Review fields at the JSON top level, plus revisions/checks/verdicts/timeline;
read base from the matching revision. Retain canonical repository, Review number,
actual link, target, revision and full native base/head ObjectId. Changeset IDs
in this fixed version use c1 plus 64 hexadecimal characters; preserve the prefix.
They are not 40-character Git SHAs. Review JSON omits a web URL; after verifying
instance/canonical repo/number, the normal route is
INSTANCE/OWNER/REPO/reviews/NUMBER. A missing returned URL is not creation failure.

## Workspace and development

Verify the work copy, target branch, baseline and test entry first. Scion uses
.scion/, not .git/. Do not run Git commands, including status, fetch, checkout or
MR refs: Git might discover an unrelated parent repository. The human can explicitly
apply a Scion Workspace with a verified connection, giving each membership its
own copy. Do not overwrite a nonempty directory. A clone connection does not enable
member tools; daily commands still need explicit binding. Never hand off files
through another member's directory.

Use a unique task branch, `switch -c NAME`, modify/test, then
`commit -m TEXT --author 'Name <email>' PATH...` and `land`. The author must be
explicitly supplied; authenticated subject and commit author are separate identities.
There is no staging area: omitting paths commits all changes, and add is not an
isolation step. Preserve edits, pending stacks and conflicts. Separate branch
names by Room, membership and task.

scn land publishes the task branch and can rebase/rewrite IDs; verify the published
head/tree and retest as needed. scn review land N merges a Review into its target;
it needs the user's specific decision. The current supported workflow uses ordinary
versioned comments, not automatic approve/request-changes, merge or close. Explain
this workflow's version/authorization limits if such an action is requested;
an exposed native verb alone does not establish a supported guarded operation.

After publishing, query for an existing task Review before creating one from the
actual head. For corrections, keep the branch and Review: commit/land, then
`review push NUMBER FULL_HEAD`, read the new revision and hand off again. review
update can also change the version; previous conclusions no longer cover it.

## Independent review

Read the Review and complete discussions. Confirm your copy is clean and pending
state/conflicts permit switching, then `switch --detach FULL_HEAD`. Verify
`status --no-ahead-behind`: actual base equals expected head, detached=true and no
pending state. Inspect the exact base/head diff and complete files; independently
run checks. Re-read the Review before publishing; changed revisions invalidate an
old-version conclusion. Version ordinary comments explicitly with
`review comment NUMBER 'BODY' --revision REVISION`. Read actual comment ID, author,
body and revision before publishing a local review result.

Handoffs include instance, canonical repo, Review number/link, target, revision,
full base/head, actual tests, findings and unverified areas. Different members using
the same account may have the same remote author; separate copies are not proof of
separate accounts. Review approval does not authorize merge or close.

## Uncertain results

Interpret exit status by operation: 0 still needs result verification; 1 is general
failure, 2 usage, 3 conflict, 4 temporary failure, 5 may be authentication/permission.
For interrupted/timed-out creation, comments, push or land, retain target, time,
version and returned IDs; read remote author/body/revision/head before deciding.
Never automatically resend, change backend or fall back to REST. Review land may
still be queued after acceptance; report landed only after reading actual state.
