# Running Room

Production members use `MemberAgent` with the DSH Backend. Room owns shared history, membership and read progress; each member owns a private Session. Agents access Room through the TeamX CLI. Ordinary model output stays private; only an explicit send publishes a message. Notification observers, virtual Sessions and the deterministic demo are compiled only into tests.

Without `--room` / `--state-dir`, `teamx serve` reads `~/.teamx/config.toml` and uses adjacent `teams/` and `data/` directories. Select a project configuration with `--config`; `server.teams_dir` and `server.data_dir` select directories. Readiness reports `data_dir` and `teams_dir` as absolute paths. Prepare configuration and [team templates](../../.teamx/teams/README.md) using the [root README](../../README.md#快速开始). The managed entry point rejects `--dsh-config`. See [Room architecture](../../docs/room-architecture.zh.md).

```sh
teamx serve --config .teamx/config.toml
# From another working directory:
teamx serve --config /absolute/config.toml
```

Use the left-hand **＋** to select a team and optional room name. Each team has one shared runtime and member roles. Creation saves runtime settings, expanded instructions and resource copies, with independent member identities, Sessions, cursors, workspaces and persistent budgets. The opening message remains an operator draft. Template edits take effect for new rooms after a service restart; existing rooms restore their saved configuration even if the template is removed. Missing external dependencies and unsafe recovery evidence still block execution.

[Workspace](../../docs/workspace.zh.md) settings below the composer manage plain directories, an optional HTTPS repository and GitLab authentication. New rooms start with plain member directories; template suggestions stay inactive. Applying settings settles old execution, prepares and commits the target, then updates existing Session generations. Files and public history survive; old work is never automatically replayed. PATs use a write-only request and private credential references. Existing repositories/projects are read-only, and revisits only check local state.

The management APIs are `GET /v1/workspace`, `POST /v1/workspace/apply` and `POST /v1/workspace/retry`. They use stable operation IDs and configuration versions. Query after a timeout; explicitly retry the original target after failure. Partial or uncertain commits block execution. See the [Workspace API guide](../../docs/workspace.zh.md#管理接口). Runtime and Workspace migration commands have been removed; managed rooms require complete runtime and Workspace snapshots.

Each tab keeps its own selection in sessionStorage. Switching or closing a tab leaves background turns, tools and notifications running. Rooms run concurrently with one execution slot per room; revisits reuse healthy instances and Sessions without resetting state or budgets. Lifecycle conflicts block only their owning room, while shared connection changes still settle every affected call. Drafts, quotes, mention progress and uncertain sends are isolated by room and operator in tab-local storage. Pending sends keep their original full Room/service identity and message ID; after identity expiry, inspect history before discarding a pending retry. Late responses cannot overwrite another room's view. Lost create/open responses retain deduplication IDs for explicit retry. Reloading never replays management operations; rooms whose opening failed remain listed.

Click the header room name or its edit icon to edit it inline. Enter saves and Escape cancels; adjacent Save/Cancel buttons offer the same actions. Saving updates the header and navigation. `POST /v1/rooms/rename` requires the catalog `service_epoch`, a human `caller`, exact `room_id` / `room_instance`, `name`, and the original `expected_name`. The trimmed name must be nonempty and at most 256 UTF-8 bytes; a changed original name produces a conflict. Only the display name in `rooms.json` is atomically updated; creation receipts and frozen configs keep their original names. History, drafts, members, workspaces and Session identities remain intact, with no execution restart. Read back the catalog after an uncertain response; mutations are never automatically replayed.

Unseen mentions of the operator give the whole message a yellow background. In a visible, focused page, a message is automatically marked viewed after at least half of it stays in view continuously for one second; messages taller than the viewport require half the viewport instead. Brief glimpses and background pages do not count. Clicking the reminder navigates to the oldest unseen mention, which follows the same visibility and dwell rule. Viewed messages lose their background and reduce the count individually, leaving earlier unseen messages highlighted. The @username keeps its yellow mark even after viewing, identifying the mention independently of its unread state. Viewing progress survives room switching and tab reloads, without advancing Agent unread cursors.

Use **删除** (Delete) beside a room in the list and confirm to stop it, verify current and historical private Sessions have settled, and permanently remove its directory, including public history, member state, every private Session, diagnostics, copied resources and workspace files. Team templates and other rooms survive. Deleting the selected room leaves no room selected. Operator-only `POST /v1/rooms/delete` requires the catalog `service_epoch`, a human `caller`, and exact `room_id` / `room_instance`. The service retains the background operation after HTTP disconnects. Observe `GET /v1/rooms`: per-room `busy`, room `status=deleting`, `deletion_phase` (`closing` / `removing`) and `error`; disappearance from the catalog confirms completion. Failed or interrupted deletions require explicit retry and never resume automatically on reload or service restart. Settlement is persisted before entering `removing`, allowing partial removal to be reopened and explicitly retried.

Startup settings and default runtime live in `config.toml`; see the [root README](../../README.md#快速开始) and [example](../../.teamx/config.example.toml). Teams may omit runtime or replace it completely. Existing Rooms retain their frozen runtime. The collaboration flow below retains explicit single-room mode.

`server.time_zone` defaults to `Asia/Shanghai`. Settings → General saves an IANA time zone to the startup configuration without a restart, shared by all Rooms. Page timestamps, day groups and date/time inputs use this zone; stored instants and message text stay intact. The browser-zone button only fills the form. DST gaps and ambiguous wall times require correction; resolved inputs retain their instant across zone changes, while unresolved drafts need confirmation. Manual configuration edits require a restart. Agents receive the current local time each turn and a time zone convention on their first turn and after a zone change. Changes take effect on the next normal turn; source timestamps retain their original representation.

## Start and collaborate

Requirements: Go 1.27+, a complete DSH installation, loopback listening, process observation and model credentials/network access. Follow the [Agent runtime guide](../../docs/agent-runtime.zh.md). Explicit single-room mode requires a JSON file with named `reviewer` and `observer` profiles, each specifying `binary_path`, `workspace`, `instructions` and `model`; see [dshProfile](dsh.go) for the fields. Use local absolute paths and create the workspaces first. The service inherits credentials and does not automatically load `.env`. Actorx is pinned in `go.mod` and downloaded by Go.

```sh
go build -o /tmp/teamx ./cmd/teamx
/tmp/teamx serve --room design-review --state-dir ./.teamx/data/dsh-team \
  --listen 127.0.0.1:8088 --dsh-config /absolute/dsh-team.json --session-max-turns 16
```

Open **http://127.0.0.1:8088/** for the embedded Room UI. The service also prints `Room UI:` to stderr; use that address with an automatically allocated port. No frontend build or separate process is required.

The UI displays public messages and explicit Agent replies, supports sending and quoting messages, older history, adding members from configured profiles, enabling/pausing collaboration and cancelling a turn. Member state includes unread counts, read cursors, turn budgets and errors. Messages and status refresh approximately every 1.5 seconds; returning to the tab refreshes immediately. Add and enable members before posting a task: pre-join history is not unread. Ordinary model output stays private; silence is a valid turn outcome.

Team selection lives in the new-room dialog. The right panel displays saved members and collaboration state.

UI previews use the operator identity and never consume member unread messages. The 16 KiB body limit still applies. An uncertain send retains its original ID and content for explicit retry, with the pending request in tab-local sessionStorage. Restart refreshes the displayed identity; existing pending sends retain their original full service identity rather than acquiring the new epoch. Session rotation and member removal remain available through the CLI.

The operator-only public clear API `POST /v1/messages/clear` still requires `snapshot_seq`. The UI places **重新开始** under the **···** menu beside the message count; confirmation starts the full reset described below.

**重新开始讨论** (Start a new discussion) pauses every member, cancels and joins old turns, rotates every private Session, then clears public history and resumes collaboration. New sends, clearing and membership changes are blocked during the operation; observation and progress polling remain available. Membership, roles, workspace files, lifetime turn counts and budgets survive. Members with exhausted budgets remain paused. Fresh Sessions open lazily without replaying old turns; old Session directories are retained. Existing workspace files remain readable by fresh Sessions.

The service owns the background batch even after a browser closes or an HTTP request times out. Rotation failure retains public history and leaves the team paused. Session references commit independently, so some members may already have reset; there is no cross-member rollback. A durable `discussion-restart.json` marker prevents automatic team resume after an unfinished or failed batch across service restarts. Inspect the failure before explicitly retrying. Operator-only `POST /v1/discussion/restart` requires a 32-character lowercase hexadecimal `restart_id` and the last observed batch ID as `previous_restart_id` (empty initially). Repeating the current ID returns its existing result; stale requests conflict. `/v1/member/list` exposes phases, outcome, errors and budget-exhausted members in `discussion_restart`.

In the message composer, Enter sends; Shift + Enter and Ctrl + Enter (⌘ + Enter on Mac) insert a newline. Confirming text with an input method does not send.

The room header has one team icon that toggles a shared right sidebar. Clicking a member’s information area, avatar or name in a message, or processing/waiting hint opens Agent details in that sidebar. The header shows the member, status, and execution controls. Details open on the activity tab; managed rooms also provide a tool settings tab for enabling room tools for the selected member. Account, permission and scope changes remain in room tool management, accessible from that tab; returning reloads the member settings. Switching to tool settings stops private activity reads; **返回成员** (Back to members) or Escape returns to the list. A selector switches members within details. Both views share the same width. On desktop, drag the sidebar’s left edge to resize it; the focused separator also supports Left/Right and Home/End. Width is bounded to leave room for messages, adapts to window size, and is retained in tab-local storage. Collapsing the sidebar stops activity reads; reopening shows the member list. While a turn runs, the drawer polls a bounded memory snapshot about every 1.5 seconds. Thought, tool activity and internal replies appear in notification arrival order, with each span independently collapsible. Consecutive text chunks of the same kind are combined; consecutive updates of the same tool share a row, while updates separated by other activity appear at their arrival position. Internal replies open by default; thought and tools start collapsed. Refreshes and turn completion preserve disclosure choices. The drawer follows the latest activity by default; scrolling up pauses following, and scrolling back to the bottom or clicking **回到最新** (Back to latest) resumes it. Tool rows include names, status, inputs (up to 2 KiB) and text results (up to 4 KiB). The flow retains up to 256 spans within 256 KiB of encoded data, with up to 16 KiB of text per span; truncation is marked. After completion it shows the saved flow, stop reason and error. Diagnostics without the ordered events format are rejected by the activity UI. The current DSH emits ACP notifications for committed model output and tool events, so this does not guarantee token-by-token updates or expose thought the model did not provide. Closing the drawer stops reads. Inspection never starts a turn, consumes unread messages or posts private data to public history. Live snapshots are not persisted; a service restart retains only the last completed diagnostic.

Embedded assets live at `/` and `/ui/*`; `GET /v1/ui` returns `managed=true` in management mode; `GET /v1/rooms` supplies the catalog, operator, safe team metadata and each room’s `runtime_status`, `busy`, `runtime_error`, available `service` identity and saved member names. There is no global active instance. `POST /v1/rooms/open` opens by full Room identity with a stable `open_id`; `GET /v1/rooms/open` queries its receipt without replay. The old activation endpoint is removed. Managed CLI calls require an explicit `--room ROOM_ID` or ROOM positional argument and address an already-open instance. Single-room mode retains the original bootstrap metadata. Neither exposes runtime settings or credentials. Management mode rejects roster mutations. Operator-only `POST /v1/member/activity` validates the current Room/service and membership generations, reads the current bounded memory snapshot while running and the bounded `dsh-turn.json` on demand while idle, and returns `Cache-Control: no-store`. Member tools cannot access it, and private output is excluded from member lists and public history. Profile contents, credentials and full DSH conversation files are not exposed. Same-origin access retains the trusted-local usage boundary.

Alternatively, in an operator terminal:

```sh
export TEAMX_ENDPOINT=http://127.0.0.1:8088
/tmp/teamx member add --agent reviewer --profile reviewer
/tmp/teamx member add --agent observer --profile observer
/tmp/teamx send --text 'Review shutdown ordering; observer may read silently.' --id task-1
/tmp/teamx member run --agent reviewer
/tmp/teamx member run --agent observer
/tmp/teamx member list --json
/tmp/teamx read --latest 10 --json
/tmp/teamx read --all --json
```

Member addition defaults to `--runner dsh` and requires a configured profile. New members start paused at the history tail; joining does not create old unread messages or open a Session. Each member has independent roles, workspace, process, private state and turn budget. Human operators can post and preview without joining as a simulated member.

`unread_count` comes from Room; `notification` is the newest processed metadata and can briefly lag. Notifications never contain bodies. `execution` exposes pause/activity, turn, error, budget and rotation state. Readiness is one stdout JSON line; logs go to stderr. `--listen 127.0.0.1:0` allocates a port.

## Turns, budgets and rotation

```sh
/tmp/teamx member pause --agent reviewer
/tmp/teamx member pause --agent reviewer --cancel
/tmp/teamx member run --agent reviewer
/tmp/teamx member run --agent reviewer --retry
/tmp/teamx member session rotate --agent reviewer --cancel
/tmp/teamx member remove --agent reviewer
```

Each Room grants one execution slot, covering lazy Session opening, the entire turn and settlement of its tools. Requests enter FIFO order as received by its scheduler Actor, without role priority. Queued members show `waiting` (等待执行), with `active=false`, unchanged turn numbers and no budget charge. A grant rechecks membership and current unread state. A settled holder with more input rejoins the tail. Different Rooms have independent slots; messages, reading, user input and member management remain available.

A member runs at most one turn. Notifications merge while busy; after settlement, newer input may trigger a follow-up. Choosing not to read or reply completes normally and does not repeatedly trigger the same unread watermark. `run --retry` explicitly reoffers unread input without replaying an old call stack or tool effects. Cancellation alone is not settlement: unfinished cancellation retains `active=true`, the slot and a diagnostic reason. Pause, removal and rotation withdraw waiting requests; shutdown stops new grants first. The queue is not persisted, and restart retains the existing Session recovery requirements.

User input during a turn continues to merge without interrupting that turn. Serial execution does not impose a public-message limit or guarantee convergence or token savings. Send-time freshness checks and discussion budgets remain unimplemented.

`serve --session-max-turns N` / `server.session_max_turns` requires a positive threshold, default 16 admitted attempts per DSH Session. Room and Inbox share the count; failures and cancellations count, rejected task admission does not. The Nth turn ends normally; the next executable input triggers background rotation before taking a model execution slot. Idle members neither rotate nor run. No enable or additional allowance is needed at a threshold. `execution.session_turns` reports Session used / limit and cumulative total. Both standalone and managed DSH counts survive normal restart. Managed rooms freeze the creation-time threshold; changing the service default affects only new rooms. Standalone mode uses startup configuration. The retired `max_turns` / `--max-turns` names fail with a migration hint.

Existing room configuration and collaboration snapshots upgrade once by version, preserving their threshold, cumulative count and latest pause intent. An old Session without a trustworthy baseline must rotate before its next normal input; a member without saved enable intent stays paused. Corrupt new snapshots block execution. Backends without rotation capability retain their existing cumulative limit. Automatic rotation never reoffers previously offered input, resets consumption cursors or retries failed work.

Rotation pauses new turns while notifications keep merging, joins the current turn (optionally requesting `--cancel`), then closes and joins the old Session, processes and workers before committing a new directory reference. The new Session opens lazily. Membership, history, cursor and cumulative count stay intact. The default leaves the member paused; `--run` explicitly resumes scheduling without retrying previously offered input. A pause command clears a pending rotation's resume request.

**Private context resets; there is no automatic summary or memory migration.** The new Session receives its configured role/model, current CLI identity, reset notice and history-preview instructions (`read --all` / `--latest`). Preview does not change unread state. Existing posts and external effects remain and must not be replayed automatically. Retired private directories remain on disk. `dsh-turn.json` is the most recent turn diagnostic, not a handoff summary.

The rotation command waits about five seconds, then reports incomplete / outcome unknown if work remains owned. `member session status --agent ID --json` returns the operation ID, fixed from/to directories and last confirmed phase (`prepared → old_closed → completed`). Repeating the same ID and parameters observes the original operation; `--continue` resumes it after a known failure. Conflicting parameters and stale generations are rejected. An unhealthy Session may be explicitly discarded once every owned process, tool, reader and worker has stopped, even if original-session recovery is unavailable. Unconfirmed settlement or uncertain persistence still forbids replacement execution.

After a fault, **启用协作** (Enable collaboration) explicitly resumes work. Nearby text explains any Session reset, which clears private context. A blocked member must pass settlement checks and rotate before scheduling resumes. Failed resets retain the block. Automatic threshold rotation preserves the latest enable/pause intent; pause is available during cleanup. Enabling also reoffers unread input left by a failed or interrupted turn, without replaying the old call stack. Recognized model rate limits show a fixed HTTP 429 hint without exposing remote error bodies or credentials. Wait for provider capacity before retrying; there is no automatic retry loop.

The DSH turn timeout defaults to 15 minutes, with 64 KiB retained output. Output-token limits follow model configuration; model request retries are zero. A fixed turn threshold does not measure context capacity. There is no monetary cap, exact token accounting or private summary migration.

## CLI identity and reading

The adapter binds `TEAMX_ENDPOINT`, `TEAMX_ROOM_ID`, `TEAMX_ROOM_INSTANCE`, `TEAMX_SERVICE_EPOCH`, `TEAMX_AGENT_ID` and `TEAMX_MEMBERSHIP_ID`, plus the absolute `TEAMX_CLI` path. Agent tools must preserve these bindings and cannot manage members. Operators only need the endpoint. This is a trusted local protocol, not multi-user authentication.

Agents consume with `read --unread --limit 20 --json`. The CLI delivers each page, confirms the cursor, then continues. Pages contain at most 100 messages and 1 MiB of body text. Each member has one pending unread receipt, expiring after 30 seconds. Competing consumption returns busy; previews remain available. Failed stdout or confirmation cannot advance beyond an undelivered page. Committed progress never rolls back; restart expires old receipts.

`--latest N`, `--all` and `--unread --peek` preview without moving cursors. Snapshot reads exclude later arrivals. Self-posts do not count as the author's unread and do not clear older unread input. Send deduplicates by author and client ID; identical retries return the same sequence, while conflicting content is rejected. Retry uncertain sends with the original ID and content.

Room owns one bounded notification batch, waiting at most 100 ms per target. Failures retry about every second; reconciliation runs every five seconds. Members validate full identity and revision instead of accumulating notification counts. Delivery failure does not roll back committed messages.

## Recovery and shutdown

Restart with the same room and state directory. Startup locks and validates state, restores checkpoints, reattaches Actors, then publishes readiness. RoomInstance, MembershipID, sequences, revision and cursors survive; ServiceEpoch changes and stale tools fail. Orphan files cannot revive removed members.

Only verified normal shutdown and matching artifact/workspace/role/model configuration authorize original DSH Session resume. Completed members resume scheduling lazily; failed/interrupted members stay paused. Leftover running checkpoints become interrupted. Resume failure never creates a replacement or replays a prompt. Missing profiles or shutdown evidence block only the affected member; corrupt authoritative data prevents readiness.

Recovery after a completed rotation follows the latest saved enable/pause intent and the existing backend recovery gates. A completed managed Workspace application restores intent only for its exact recorded generation; incomplete or interrupted work still requires explicit continuation. A normally closed rotated Session retains original-session recovery. `settled.json` records complete execution shutdown for explicit resets, independently of normal recovery eligibility, and is consumed before another launch. Directories without shutdown evidence are not marked settled. Unresolved ownership cannot use rotation as crash takeover; host-crash takeover is unsupported.

Ctrl-C / SIGTERM stops admission, joins management and member tasks/Sessions, then closes Room, Runtime and Store. Removal commits membership deletion first, rejects stale tools, then cancels and joins resources. Cleanup timeouts retain ownership and report incomplete work; a cancellation or termination request does not prove cleanup.

Managed startup restores each room’s persisted collaboration intent independently of browser selection. Interrupted turns remain paused; fully paused, settled rooms stay lazy. Shutdown freezes every instance before stopping rooms concurrently, uses one shared 30-second deadline, records per-room settlement, and retains ownership and the root lock while anything remains unsettled. Single-room shutdown also uses a 30-second wait budget. DSH cancellation, protocol close, and natural process-tree exit each use the cancellation timeout (five seconds each by default). An interrupted member whose cancellation and shutdown settle normally remains eligible for original-session recovery, but stays paused after restart until explicitly continued. Close also reports the cause when unsettled cancellation, protocol failure, or lost process observation disables a Session. Forced cleanup and unsuccessful process exits likewise report failure and do not write normal-shutdown evidence. Missing evidence from older sessions is not retroactively granted.

## Storage and recovery

Version 1 stores shared history, runner/profile and cursors in `room.json`; scheduling checkpoints in `agents/<MembershipID>/agent.json`; the DSH directory reference in `session.json`; and backend.json, home and model history inside that referenced directory. Rotation uses a unique directory generation and optional pending flag, consumed durably before launch. Rotation observations are process-local; the durable reference identifies the generation after restart.

Only the current DSH runner and strictly decoded snapshots are supported. Catalog indexes use version 3. Legacy matter graphs, decisions, actions, index migration and unreferenced DSH-directory recovery have been removed. Create Rooms in a fresh data directory; existing data is never deleted automatically. Normal original-session recovery for current referenced DSH generations remains supported.

macOS/Linux use nonblocking directory locks; other platforms report unsupported. Writes use same-directory temporary files, file sync, atomic replacement and directory sync, with 0600 files and 0700 directories. There is no cross-file transaction. Uncertain replacement or directory sync freezes Store writes; settle ownership, close, reload and inspect. Invalid versions, identities, unknown fields or corruption never create an empty Room.

Limits: 32 members, 10,000 messages, 16 KiB per body and 64 MiB of total bodies; full capacity rejects new writes while retaining history. Encoded limits are 512 MiB per Room snapshot and 1 MiB per private snapshot. Arbitrary external tools are not exactly-once.

## Validation

```sh
go test -race -timeout 15m ./...
go vet ./...
node --test internal/web/tests/web*_test.mjs
go test -tags=e2e ./... -run '^$'
```

Run `go generate ./internal/room` after changing Actor handlers. Local checks require Go and Node; the last command only checks that acceptance tests compile. Testing real models or connected platforms requires explicit opt-in. See the [validation guide](README.zh.md#验证入口) and [open checks](README.zh.md#当前待验项).

The [Chinese guide](README.zh.md#服务配置) covers configuration and user-directory setup; [Room architecture](../../docs/room-architecture.zh.md) covers message, scheduling and persistence design.
