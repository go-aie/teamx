# Use TeamX

Read this guide with `"$TEAMX_CLI" skill usage` when explaining TeamX's interface,
setup prerequisites or execution controls. It is available to a current bound
Agent without platform connections or tool bindings. Reading guidance changes no
configuration and grants no management authority. Reply in the user's language.

## Explain capabilities and choose an entry

Start with the user's goal and the shortest useful path. Use everyday language
and two or three examples when introducing your capabilities. Distinguish what
an assistant can do now from work needing a platform connection, a tool, a
Workspace or business authorization. Verify current availability before claiming
an account or action is ready; a general introduction needs no tool inventory.

- **Personal help:** use the default Personal Assistant private conversation to
  clarify requests, organize work, summarize information and prepare content.
- **Team work:** the sidebar's + menu creates a private conversation or a team
  room. Rooms use team templates with separate member roles. Explain the entry;
  the current Agent cannot create a team, delegate or manage its other members.
- **Platform setup:** for an explicit Feishu/GitLab connection or current-member
  notification/tool request, read `"$TEAMX_CLI" platforms skill`. Other supported
  providers and full account management use Settings → Platforms.
- **Platform work:** discover enabled accounts and actual capabilities with
  `"$TEAMX_CLI" tools list`; use its exact entry with `tools skill ENTRY` and
  `tools run ENTRY --help`. A connection or subscription does not enable tools.
- **Files and code:** work in the current member's Workspace. To bind a repository,
  the human explicitly applies Workspace settings below the composer. API reads
  and notifications do not require a clone; missing Workspace is not permission
  to clone elsewhere or overwrite a directory.

Useful example requests include "Help me organize today's work", "Connect
Feishu and follow this group; remind me here when I need to act", and "Connect
my GitLab account and receive my personal to-dos". Adapt them to the user's goal;
do not imply a timer, delegation service or unavailable integration.

## Conversations and visibility

Private conversations have one assistant; ordinary rooms use team members.
Each has independent messages, member identities, Sessions, Inbox, rules and work
directories. Shared platform connections do not share member subscriptions or
rules. A manually created assistant starts paused; the default assistant is
initialized once with its initial enable intent. Opening or switching a page
creates no user task and does not resume a paused or failed member.

The empty private-conversation suggestions only fill a draft; the human sends it.
Normal model output is private. Publish an answer with `"$TEAMX_CLI" send` so it
appears in the conversation. Room messages and a member's Inbox are separate
inputs. A platform notification is context, not a new human instruction.

Typing @ in the composer opens the current member list; type part of a name to
filter, then click or use Up/Down and Enter/Tab to insert it. Escape dismisses the
list. Choosing a member fills the draft without sending. Backspace at the end of
a complete @member (including its following space), or Delete at its start,
removes that mention as a unit; editing inside it remains ordinary text editing.
Known member mentions have a subtle green highlight in the draft and sent message;
mentions of the current human retain their yellow reminder style after sending.
All Room members still see the message; @ identifies who is addressed and does not make it private. For
member configuration, name the intended member, for example "@reviewer, turn off
your GitLab notifications"; each addressed member changes only their own settings.

Tabs select conversations independently. Switching conversations or closing a
page does not stop server work. Explain where the human should go for another
conversation's settings; never switch your bound identity to act there.

## Notifications, tools and rules

The member detail panel has Platforms, Rules, Inbox and Activity tabs. In a
private conversation these belong to its single assistant.

- **Platforms:** add or connect an account; receiving notifications and using
  tools are separate choices. Inspect that platform's settings for reception
  scope, filters and start time. Later toggles preserve unrelated choices.
- **Rules:** users normally establish, amend or revoke lasting instructions in
  conversation. Save explicit lasting intent through `policy update`; the Rules
  view allows inspection and correction. A one-time request is not a standing rule.
- **Inbox:** inspect captured notifications and internal operation results.
  Reception, delivery, consumption, business completion and an actual reminder
  are different facts. A saved notification can be understood without enabling
  platform tools; fresh reads and external actions require their own access.
- **Activity:** inspect member progress, thoughts and tool calls. Published
  conversation replies remain distinct from private activity.

GitLab personal to-dos cover projects accessible to the selected personal account;
project activity currently uses one project per member/account. Feishu group
scope and mention filters control reception. Use the setup Skill and returned
configuration for exact supported choices. Scion currently has no notification
collector. Account permissions, enabled tool capabilities and user authorization
are separate prerequisites for an external write.

## Execution and recovery

Pause or enable collaboration through member controls. Members follow normal
admission, failure recovery and Session rotation rules; saved Inbox input may
wait while a member is paused, blocked or recovering. Do not promise an immediate
reminder or bypass a pause. A saved rule or unfinished task creates no timer.

Session rotation resets private context while preserving conversation history,
Workspace and identity; recover durable matters before continuing. The service
normally rotates at its configured Session turn threshold. A room's Restart
conversation action clears public messages and resets private Sessions while
preserving Workspace; it is not a way to reload a newly edited role template.

The default assistant is a protected entry: it can be renamed, paused or
restarted, but cannot be deleted. Manual conversations can be deleted by the
human; deletion removes their messages, Sessions and Workspace. An older deleted
default can be restored only through the explicit Restore default assistant
entry, creating a new conversation without its previous rules or subscriptions.
Do not suggest deletion as routine troubleshooting.

For platform authorization, use the original message's component and secure
settings form; never collect secrets in chat. Closing settings does not cancel a
conversation operation. Read the original receipt and latest user intent before
continuing; unknown effects must be verified before dependent work. Report the
completed part, actual blocker and next user step. Use Settings → General for
system settings such as the service time zone; model/runtime setup belongs to
the operator's startup configuration. Do not invent an Agent command for a
human-only interface or promise that a frozen existing role updates on restart.

Feishu connections request the supported current permission set in one flow.
OAuth does not enable a member's tools or authorize a business write. For explicit
same-account repair, read platforms skill and use `platforms connect authorize`;
never ask for a tool off/on cycle. Use structured capability reasons: app permission
gaps require the original application's official settings, user gaps require its
"补全授权" component, missing self evidence requires that member's explicit repair,
and unsupported hosts cannot send. `tools list` availability expires after 60s;
configured grants and historical receipts alone do not establish current readiness.
