package room

func inputCLIHelpText(op string) (string, bool) {
	switch op {
	case "":
		return `Usage: teamx inbox COMMAND [--help]
  list [--limit N | --cursor REF]   Discover readable input streams without consuming
  read --stream REF [--limit N]     Formally deliver one stream's oldest pending entries
Use teamx inbox COMMAND --help for parameters and delivery semantics.
Use this CLI only when the Session selects CLI input transport. MCP Sessions use
the discovered inbox_list/inbox_read tools. Follow the Session's transport instructions.
`, true
	case "list":
		return scopedCommandHelp(referenceCommandHelp{
			"inbox list [--limit N | --cursor REF] [--json]", "Discover saved readable input streams without consuming them.",
			"First request: optional --limit 1-20 (default 10). Continue using only the returned --cursor, without --limit. --json is optional; output is JSON.",
			"pending_streams, streams with stream_ref/label/hint, optional next_cursor and stop_reason.",
			"teamx inbox list --limit 10", "First-screen or already known relevant stream_ref can be read directly; listing is not a prerequisite. An empty request refreshes the directory. Discovery grants no authority and consumes no input. Requires the Session's CLI input transport and execution binding for actual reads; help is offline. MCP Sessions use discovered inbox_list.",
		}), true
	case "read":
		return scopedCommandHelp(referenceCommandHelp{
			"inbox read --stream REF [--limit N] [--json]", "Formally read the oldest pending entries of one selected stream.",
			"--stream: returned stream_ref; optional --limit 1-10000 (default 20). --json is optional; output is JSON.",
			"entries, remaining_entries in this stream's fixed observation, optional context/stop_reason/followups and images.",
			"teamx inbox read --stream RETURNED_STREAM_REF --limit 20", "Execute one inbox read per shell tool call and wait for its result. Full delivery is confirmed through the Session's execution bridge; failed or short output is not consumption. Repeat the same stream after success for the next batch; do not retry a successful read expecting the same batch. Reading differs from business completion and action authority. Requires the Session's CLI input transport and execution binding; MCP Sessions use discovered inbox_read. No peek, read-id, turn, filter or read cursor arguments.",
		}), true
	default:
		return "", false
	}
}
