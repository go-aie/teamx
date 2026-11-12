package room

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const platformConnectHelp = `Usage:
  teamx platforms skill
  teamx platforms list
  teamx platforms selection --help
  teamx platforms connect start --provider lark|gitlab --name NAME [--origin URL --application-id ID --account-username USER] --operation-id ID --source-seq SEQ [--platform-id ID --platform-version VERSION] [--notify inbox|none]
  teamx platforms connect authorize --platform-id ID --platform-version V --operation-id ID --source-seq SEQ [--notify inbox|none]
  teamx platforms connect status --operation-id ID | --source-seq SEQ [--intent authorize --platform-id ID]
  teamx platforms connect wait --operation-id ID [--timeout 20s]
  teamx platforms connect cancel --operation-id ID
  teamx platforms connect notify --operation-id ID --mode inbox|none
  teamx platforms connect acknowledge --operation-id ID --result-version V
  teamx platforms chats --platform-id ID --platform-version V [--type group|p2p] [--query NAME --page-token TOKEN]
  teamx platforms projects --platform-id ID --platform-version V [--query NAME --page-token PAGE]
  teamx platforms member get --platform-id ID --platform-version V
  teamx platforms member notifications --platform-id ID --platform-version V --notification-version N --operation-id ID --source-seq SEQ [--add-chat-id CHAT_ID] [--enabled true|false] [--group-message-mode all|mentions]
  teamx platforms member notifications --platform-id ID --platform-version V --notification-version N --operation-id ID --source-seq SEQ [--remove-chat-id CHAT_ID] [--group-enabled true|false --group-scope all|specified --group-chat-id CHAT_ID] [--group-at-me true|false --group-at-all true|false] [--private-enabled true|false --private-scope all|specified --private-chat-id CHAT_ID]
  teamx platforms member notifications --platform-id ID --platform-version V --notification-version N --operation-id ID --source-seq SEQ --todos-enabled true|false
  teamx platforms member notifications --platform-id ID --platform-version V --notification-version N --operation-id ID --source-seq SEQ [--events-enabled true|false --project-id N --project-path PATH] [--event-type TYPE] [--todo-reason REASON --todo-type TYPE]
  teamx platforms member tool --platform-id ID --platform-version V --tool-revision N --operation-id ID --source-seq SEQ --enabled true|false [--action OP ...]
  teamx platforms member status --operation-id ID | --source-seq SEQ
  teamx platforms upgrade --rooms-dir ROOT

Connection commands require current bound Agent identity, independent of platform
tool bindings. ID is a stable 32-character hexadecimal operation ID. Same ID and
fixed input reuse the receipt; changed input conflicts. Status by original human
source-seq recovers a lost ID. Output is JSON; authorization URLs are live only.
wait polls status with a bounded total timeout (maximum 2m) and per-request timeout;
pending and CLI interruption do not cancel server work. Explicit cancel stops
remaining work, preserves applied effects and cannot undo a saved connection.
Stages: application, authorization, connection, complete. Status: running, waiting,
cancelling, completed, cancelled, expired, failed, unknown. Only completed means connected.
Result versions are stable completion references; version tracks receipt changes.
GitLab requires a canonical --origin. Use an explicit compatible --application-id
for new login or --platform-id/--platform-version for reuse. Omit --application-id
to wait for the original human to configure/select an application in full settings.
The setup projection includes the official management URL, actual callback, required
scopes and compatibility reasons; the same operation resumes after human submission. list includes public
GitLab application compatibility and existing account names; no credentials.
The browser authorization component establishes its own session; Agent commands
never receive cookies, state or PKCE material. Missing/incompatible applications
use the operation's Configure application button to open secure Settings → Platforms.
Closing settings does not cancel conversation work. selection supports GitLab account/project forms; chats is Feishu-only.
Feishu login requests the complete current permission set supported by its app.
authorize repairs an exact existing Feishu account/application/profile and keeps
connection identity, tool entries, member grants and notification progress. It
shares status/wait/cancel/notify/acknowledge. Source lookup defaults to connect;
use --intent authorize --platform-id ID for authorization recovery. One source
can connect and authorize; the same intent/target cannot start twice under new IDs.
A connect command alone does not enable group reception, tools or member actions.
notify changes only result delivery policy. Subscribing after completion delivers
the same stable result event; none cannot retract an already queued event. Inbox
delivery, consumption, result acknowledgment and business completion are separate.
continuation reports mode, handoff state, handled_version and any scheduling block.
After checking latest intent and step receipts, acknowledge the exact result version
before one completion report. A late event whose version is already handled only
needs normal Inbox consumption; never repeat configuration or completion feedback.
Paused/blocked members wait under ordinary scheduling. Notification errors do not
invalidate a saved connection; users can continue by speaking and querying status.

Member configuration requires a verified connection and current member identity;
it needs no prior tool binding. chats defaults to groups (including topic groups);
--type p2p lists/searches verified personnel conversations, excluding bots and self.
Results carry exact chat_ids, connection version and continuation; same names are
not selections. Follow returned directory tokens even on empty filtered pages.
Personnel name search cannot paginate: refine the query on refine_query warnings.
Keep search_warnings; permission_denied or conversation_unverified and partial/empty
pages do not prove absence. No message is sent to create a conversation.
get reads current notification configuration, baseline, tool revision and availability.
GitLab get includes gitlab_notifications with desired config, per-source pending/errors
and application state. GitLab notifications changes explicit --todos-enabled,
--events-enabled, project ID/path and filters; omitted fields are preserved;
new subscriptions cover all projects accessible to that personal account from save
time, with project activity disabled. Existing source settings, filters and reception
start remain intact. Enabling todos conflicts if it would also enable dormant project
activity; explicitly choose both sources to resolve that conflict. --enabled, private and group
flags apply only to Feishu notifications. Tool enable/disable supports both providers.
Feishu notifications adds or replaces exact groups, rechecking their metadata and
account. --remove-chat-id repeats followed IDs and needs no remote read; removing
the last specified group closes group reception, and no remaining source closes
the total switch. Closing an absent subscription preserves version 0 without
creating one. --group-chat-id repeats a complete replacement list; a single empty
value clears it. Do not mix replacement with add/remove, or empty with nonempty IDs.
Switching all groups to specified requires explicit --group-scope specified and a
complete --group-chat-id list. All groups has no exclusion list. --group-enabled
and --private-enabled independently enable/disable reception branches, preserving
their ranges; enabling resumes total reception unless --enabled false is explicit.
Resuming both dormant branches requires explicit choices for both, not an incidental
activation of the other source. Range/condition edits alone do not enable reception.
--private-scope all is explicit all-person reception; enabling an existing specified
private range retains it. --private-scope specified with repeated --private-chat-id
replaces the complete personnel list, verifying native chat_id and same-account
relationship before saving; open_id, groups, bots and self chats are rejected.
Switching all to specified needs a complete list. A single empty private-chat-id
clears it; do not mix empty/nonempty values. Clearing the last specified private
chat closes that branch and total reception if no branch remains enabled. Explicit
enable with an empty specified list conflicts. Range edits alone do not enable it.
--group-at-me/--group-at-all set independent mention conditions;
use mentions mode and true/false for only @me. Both false conflicts in mentions mode;
all-message mode retains those values without filtering. Existing settings and
reception baseline are preserved; new explicitly enabled subscriptions start at
save time. Legacy add alone enables only selected groups on new subscriptions.
No notification editing card or mixed conversation/browser draft is introduced.
Tool changes are separate and
do not grant external-write authority. Explicit Feishu enable adds the fixed current
reply, group-send and self-delivery grants; repeated enable repairs missing intent
and evidence, and is preserved only when unchanged. --action OP adds only selected
Feishu actions (message.reply, message.send, message.send_self); it requires enabled
true and never removes other grants. Partial capability readiness is reported in
the receipt. user_authorization_required / not_applied means no binding was saved;
complete authorization on the same account before retrying. unknown submissions
must be inspected with their original operation ID and never replayed.
projects searches the exact GitLab account with numeric --page-token pagination;
permission errors do not establish absence. Project activity supports one project
per member/account: supply its native --project-id and exact --project-path.
Metadata is rechecked before submission; ID/path conflicts never select a substitute.
--event-type repeats activity filters (empty clears); --todo-reason/--todo-type
repeat personal filters (empty selects none). Omitted filter dimensions are preserved.
Personal todos retain all-accessible-project semantics; no Workspace is changed.
Collect all requested notification fields into one operation for the same source-seq
and account. Separate later requests use their own source; do not invent a new source
or operation ID to bypass unknown work. Each configuration step uses its own stable ID and observed config version. status
is pure read; source-seq returns all member configuration steps for that request.
Completed receipts describe historical results; use get to verify current settings.
Unknown submissions never replay, even with the same ID: inspect status/get and
resolve through platform settings. Never change IDs to retry an unknown operation.
`

func runPlatformsCLI(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string) error {
	if len(args) > 0 && args[0] == "upgrade" {
		return runPlatformsUpgrade(args, stdout, stderr)
	}
	timeout := 5 * time.Second
	if len(args) > 0 && (args[0] == "chats" || args[0] == "projects" || args[0] == "member") {
		timeout = 45 * time.Second // Includes bounded metadata verification and settlement.
	}
	return runPlatformConnectWithClient(ctx, args, stdout, stderr, env, &http.Client{Timeout: timeout})
}
func runPlatformConnectWithClient(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string, h *http.Client) error {
	if len(args) > 0 && args[0] == "selection" {
		return runPlatformSelectionCLI(ctx, args[1:], stdout, stderr, env, h)
	}
	if len(args) > 0 && (args[0] == "chats" || args[0] == "projects" || args[0] == "member") {
		return runPlatformMemberCLI(ctx, args, stdout, stderr, env, h)
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return writeOutput(stdout, []byte(platformConnectHelp))
	}
	action := args[0]
	args = args[1:]
	if action == "connect" {
		if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
			return writeOutput(stdout, []byte(platformConnectHelp))
		}
		action, args = args[0], args[1:]
		if action != "start" && action != "authorize" && action != "status" && action != "wait" && action != "cancel" && action != "notify" && action != "acknowledge" {
			return ErrRequest
		}
	} else if action != "skill" && action != "list" {
		return ErrRequest
	}
	f := flag.NewFlagSet("platforms "+action, flag.ContinueOnError)
	f.SetOutput(stderr)
	f.Usage = func() { _ = writeOutput(stdout, []byte(platformConnectHelp)) }
	req := platformConnectRequest{}
	input := platformConnectInput{}
	notify := ""
	timeout := 20 * time.Second
	// Register only fields supported by this operation so none can be ignored.
	if action == "start" || action == "authorize" || action == "status" || action == "wait" || action == "cancel" || action == "notify" || action == "acknowledge" {
		f.StringVar(&req.OperationID, "operation-id", "", "stable operation ID")
	}
	if action == "start" || action == "authorize" || action == "status" {
		f.Uint64Var(&req.SourceSeq, "source-seq", 0, "original human Room message sequence")
	}
	if action == "authorize" {
		input.Intent, input.Provider = "authorize", "lark"
		f.StringVar(&notify, "notify", "", "result notification: inbox or none")
		f.StringVar(&input.PlatformID, "platform-id", "", "exact existing connection")
		f.Uint64Var(&input.PlatformVersion, "platform-version", 0, "observed connection version")
		req.Input = &input
	}
	if action == "status" {
		f.StringVar(&req.Intent, "intent", "", "source lookup intent: authorize")
		f.StringVar(&req.PlatformID, "platform-id", "", "source lookup authorization target")
	}
	if action == "start" {
		f.StringVar(&notify, "notify", "", "result notification: inbox or none")
		f.StringVar(&input.Provider, "provider", "", "lark or gitlab")
		f.StringVar(&input.Origin, "origin", "", "GitLab instance origin")
		f.StringVar(&input.ApplicationID, "application-id", "", "verified compatible GitLab OAuth application ID")
		f.StringVar(&input.AccountUsername, "account-username", "", "expected GitLab username; mismatch is rejected")
		f.StringVar(&input.Name, "name", "", "connection name")
		f.StringVar(&input.PlatformID, "platform-id", "", "reuse explicit connection ID")
		f.Uint64Var(&input.PlatformVersion, "platform-version", 0, "observed connection version")
		req.Input = &input
	}
	if action == "notify" {
		f.StringVar(&notify, "mode", "", "result notification: inbox or none")
	}
	if action == "acknowledge" {
		f.Uint64Var(&req.ResultVersion, "result-version", 0, "exact reconciled result version")
	}
	if action == "wait" {
		f.DurationVar(&timeout, "timeout", timeout, "bounded total wait (0 to 2m)")
	}
	_ = f.Bool("json", true, "JSON output")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || timeout < 0 || timeout > 2*time.Minute {
		return ErrRequest
	}
	f.Visit(func(flag *flag.Flag) {
		if flag.Name == "notify" || flag.Name == "mode" {
			req.Notify = &notify
		}
	})
	if action == "notify" && req.Notify == nil || req.Notify != nil && notify != "inbox" && notify != "none" || action == "acknowledge" && req.ResultVersion == 0 {
		return ErrRequest
	}
	endpoint := strings.TrimRight(env("TEAMX_ENDPOINT"), "/")
	if endpoint == "" {
		return fmt.Errorf("platform connection commands require bound Agent context")
	}
	client := cliClient{endpoint: endpoint, http: h}
	identity, err := client.identity(ctx, "", env)
	if err != nil {
		return err
	}
	if identity.Caller.Kind != "agent" {
		return ErrIdentity
	}
	req.ServiceInfo, req.Caller = identity.ServiceInfo, identity.Caller
	if action == "skill" {
		// This is the same embedded read-only resource mechanism as tools skill,
		// with member validation but no platform-binding prerequisite.
		data, err := platformSkillRequest(ctx, client, req)
		if err != nil {
			return err
		}
		return writeOutput(stdout, data)
	}
	path := "/v1/platforms/connect/" + action
	if action == "list" {
		path = "/v1/platforms/list"
		var result any
		if err := client.request(ctx, path, req, &result); err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	if action != "wait" {
		var op platformConnectOperation
		if err := client.request(ctx, path, req, &op); err != nil {
			return fmt.Errorf("operation_id=%s source_seq=%d; query status before retry: %w", req.OperationID, req.SourceSeq, err)
		}
		return writeJSON(stdout, op)
	}
	if req.OperationID == "" {
		return ErrRequest
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last *platformConnectOperation
	for {
		// Bound a single observation independently of the overall wait budget.
		readCtx, stop := context.WithTimeout(waitCtx, 5*time.Second)
		var op platformConnectOperation
		err := client.request(readCtx, "/v1/platforms/connect/status", req, &op)
		stop()
		if err != nil {
			if waitCtx.Err() != nil && ctx.Err() == nil {
				return writeJSON(stdout, struct {
					Pending     bool                      `json:"pending"`
					OperationID string                    `json:"operation_id"`
					Operation   *platformConnectOperation `json:"operation,omitempty"`
				}{true, req.OperationID, last})
			}
			return err
		}
		last = &op
		if connectTerminal(op.Status) {
			return writeJSON(stdout, struct {
				Pending   bool                     `json:"pending"`
				Operation platformConnectOperation `json:"operation"`
			}{false, op})
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return writeJSON(stdout, struct {
				Pending   bool                     `json:"pending"`
				Operation platformConnectOperation `json:"operation"`
			}{true, op})
		case <-timer.C:
		}
	}
}
func platformSkillRequest(ctx context.Context, c cliClient, req platformConnectRequest) ([]byte, error) {
	// Reuse the bounded raw skill response path, preserving server errors.
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1/platforms/skill", strings.NewReader(string(data)))
	if err != nil {
		return nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(r)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, toolResponseError(response)
	}
	return io.ReadAll(io.LimitReader(response.Body, 64<<10))
}
