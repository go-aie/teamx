package room

const calendarCLIHelp = `Usage: teamx calendar COMMAND [--help]
Direct-conversation scheduled tasks: once/daily/weekly, at minute precision.
  list      Find tasks by state or bounded date range
  get       Read a definition, run or explicit history/result
  update    Create/edit a schedule or change its lifecycle
  claim     Claim the supplied due run before business work
  finish    Save and publish the complete execution report
Use teamx calendar COMMAND --help for its parameters, JSON and recovery.
List/get are read-only. Due events supply run_id; claim it directly.
`

const calendarMutationHelp = `Agent mutations require --turn CURRENT_TURN from the current policy context.
The runtime supplies private execution credentials; do not inspect, copy or invent them.
Session credentials alone cannot mutate Calendar or reopen a cancelled turn.
Common: --endpoint URL, --room ID and --json (default true).
`

const calendarUpdateHelp = `Usage: teamx calendar update --turn CURRENT_TURN (--request-file FILE|- | --request JSON)
Create/edit/pause/resume/cancel/skip_once/retry_run with business JSON.
Use a file or stdin for multiline text; request and request-file are mutually exclusive.
Example: teamx calendar update --turn CURRENT_TURN --request-file task.json
Returns: the saved task/run receipt; report its actual rule_summary and next_at.
` + calendarMutationHelp + `
Update JSON:
  Every action requires action, a stable operation_id (1-128 characters), and for Agents
  human_source:{message_seq:ACTUAL_HUMAN_MESSAGE_SEQ}. Only message_seq may be submitted.
  A real human scheduling instruction authorizes creation without generic confirmation;
  discussions, quotes, platform inputs and due events do not authorize changes.

  create: definition:{title,instruction,schedule,scope?,misfire?}; optional state:active|needs_info.
    Do not supply expected_version, task_id, run_id or changes.
    title: 1-256 characters; instruction: complete business work, up to 16 KiB.
    The service replies here, so instruction need not describe how to send a report.
    Scope must come from verified platform/input/reference reads or an established Workspace;
    never guess IDs. Missing necessary scope/time: save state=needs_info and ask;
    never announce an active arrangement.
  edit: task_id, expected_version, changes:{title?,instruction?,scope?,schedule?,misfire?}.
    Each supplied field replaces its whole value; omitted fields stay unchanged.
    scope:null clears scope; misfire:null resets the default; required fields cannot be null.
    Do not supply definition or run_id. Only needs_info can activate via edit state=active;
    paused tasks require resume.
  pause/resume/cancel: task_id, expected_version; no definition/changes/state/run_id.
    Resume starts strictly after commit, without catching up paused cycles.
  skip_once: task_id, expected_version, local_date (future YYYY-MM-DD in the rule zone).
    Preserves history. local_date belongs only to skip_once.
  retry_run: run_id, expected_version (the run version), after verifying a settled
    failed/blocked/skipped result. No task_id/definition/changes/state. Unknown results
    never authorize business replay; no automatic retries after failed/unknown.

Schedule:
  frequency: once|daily|weekly; local_time: HH:MM; time_zone: optional IANA zone.
  once requires start_date (YYYY-MM-DD); weekly requires unique ISO weekdays (1-7).
  Recurring start_date defaults to the commit date in the saved zone; the first valid
  occurrence is strictly after commit. The zone defaults to the system IANA zone and
  stays fixed when display settings change.
  day_filter defaults to all_days (includes weekends/holidays).
  weekdays means Monday-Friday, including weekday holidays.
  business_days requires work_calendar:"CN": mainland China including makeup weekends;
  state this explicitly. Do not infer country solely from time zone. Unknown coverage
  blocks execution: explain blocked_reason and coverage, never fall back to weekdays.
  Optional end_date is inclusive. excluded_dates (up to 366 unique YYYY-MM-DD dates)
  skip without shifting. Recurring repeated local times run the first occurrence once;
  missing wall times skip. once on a DST gap/fold requires clarification.
  misfire defaults to latest_within_one_hour; skip is also supported.
  The service must remain running. After downtime only the latest occurrence may catch
  up within one hour; older cycles are recorded as missed ranges.

Receipts and recovery:
  Return the actual saved rule_summary and next_at (zone, date filter, first time, misfire).
  Include [查看日历](#calendar/task/<task_id>) using the returned task_id; opening the link
  does not start work. Existing-resource changes require the observed expected_version.
  A lost/unknown update reply must replay the original operation_id and unchanged fields,
  even without task_id. Never recompute defaults or bypass a frozen store. Reused receipts
  describe the original operation. Version conflicts include target/actual_version:
  get the necessary definition and decide again; never silently refresh and overwrite.
`

const calendarFinishHelp = `Usage: teamx calendar finish --turn CURRENT_TURN (--request-file FILE|- | --request JSON)
Save the complete result and publish its report through the sole report path.
Use a file or stdin for multiline reports; request and request-file are mutually exclusive.
Example: teamx calendar finish --turn CURRENT_TURN --request-file report.json
Returns: run publication status and report_seq when publication is confirmed.
` + calendarMutationHelp + `
Finish JSON: {run_id,outcome,report,evidence?}; outcome: succeeded|blocked|failed.
  report must be complete UTF-8 text, nonempty, up to 16 KiB.
  Optional evidence:{source_refs:[...]}: at most 16 verified src_ references / 4 KiB;
  do not duplicate report prose. External writes retain existing action authorization.
  status=publishing means the service continues publishing the saved report: do not
  also send it, repeat business work or poll for normal publication. Only a terminal
  status with report_seq proves publication. Get is for explicit queries or recovery.
  Private output is never completion.
`

func calendarHelpText(op string) (string, bool) {
	switch op {
	case "":
		return calendarCLIHelp, true
	case "list":
		return scopedCommandHelp(referenceCommandHelp{
			"calendar list [--state STATE] [--from RFC3339 --to RFC3339] [--cursor CURSOR]",
			"Find scheduled tasks through a read-only query without consuming input or starting work.",
			"--state: task state; --from/--to: positive date range, at most 42 days; --cursor: returned continuation. Common: --endpoint URL, --room ID, --json (default true).",
			"One row per task, at most 50 rows per page and continuation; default states are active/paused/needs_info without a date limit.",
			"teamx calendar list --state active",
			"completed/cancelled require an explicit --state. Continue with the identical query/snapshot; changed snapshots fail explicitly. No mutation, human source, execution credential or turn argument is needed.",
		}), true
	case "get":
		return scopedCommandHelp(referenceCommandHelp{
			"calendar get (--task-id ID [--history [--cursor CURSOR]] | --run-id ID [--result])",
			"Read a task definition or a frozen run; request history or report bodies only when needed.",
			"--task-id: task definition; --history: paginated task history (at most 50 rows, without report bodies); --cursor: returned history continuation. --run-id: frozen run; --result: include its report/evidence. Common: --endpoint URL, --room ID, --json (default true).",
			"Editable task definition or frozen definition/version/status/receipt; explicit history/result when requested.",
			"teamx calendar get --run-id ID --result",
			"Read-only: this neither consumes input nor starts work. Continue history with the identical query/snapshot; changed snapshots fail explicitly. Use observed versions when subsequently updating resources. A due event already supplies run_id; normal execution claims it directly without a preliminary get.",
		}), true
	case "update":
		return calendarUpdateHelp, true
	case "claim":
		return scopedCommandHelp(referenceCommandHelp{
			"calendar claim --turn CURRENT_TURN --run-id ID",
			"Claim the supplied due run before business reads, at most one per turn.",
			"--run-id: actual ID from calendar_due; --turn: authenticated current_turn. Common: --endpoint URL, --room ID, --json (default true).",
			"Frozen instruction, scope, definition version, due time, time zone and misfire policy.",
			"teamx calendar claim --turn CURRENT_TURN --run-id ID",
			"Read latest Room human intent through Inbox first and handle cancellations first. calendar_due is a signal, not new authority. Claim directly without list/get, then use the frozen instruction/scope/time zone/misfire. Pending work never overrides input/Session gates; external writes retain existing authorization. The runtime supplies private execution credentials; do not inspect or invent them. Complete through calendar finish --help; private output is not completion.",
		}), true
	case "finish":
		return calendarFinishHelp, true
	default:
		return "", false
	}
}
