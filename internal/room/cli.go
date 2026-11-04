package room

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type cliClient struct {
	endpoint string
	http     *http.Client
}

// Only connection and member identity values are replaced for Room tools.
// Other TEAMX_* settings, including model credentials, remain inherited.
func isRoomToolEnv(entry string) bool {
	name, _, _ := strings.Cut(entry, "=")
	switch name {
	case "TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID":
		return true
	default:
		return false
	}
}

func (c cliClient) request(ctx context.Context, path string, req any, out any) error {
	var body io.Reader
	method := http.MethodGet
	if req != nil {
		data, err := json.Marshal(req)
		if err != nil {
			return err
		}
		body, method = bytes.NewReader(data), http.MethodPost
	}
	request, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return fmt.Errorf("HTTP response too large")
	}
	if response.StatusCode != http.StatusOK {
		var failure apiError
		if json.Unmarshal(data, &failure) == nil && failure.Error != "" {
			return fmt.Errorf("%s: %s", failure.Code, failure.Error)
		}
		return fmt.Errorf("HTTP %s", response.Status)
	}
	return json.Unmarshal(data, out)
}

func (c cliClient) identity(ctx context.Context, roomID string, env func(string) string) (apiRequest, error) {
	agent, membership := env("TEAMX_AGENT_ID"), env("TEAMX_MEMBERSHIP_ID")
	if agent != "" || membership != "" {
		info := ServiceInfo{RoomIdentity: RoomIdentity{RoomID: env("TEAMX_ROOM_ID"), RoomInstance: env("TEAMX_ROOM_INSTANCE")}, ServiceEpoch: env("TEAMX_SERVICE_EPOCH")}
		if env("TEAMX_ENDPOINT") == "" || agent == "" || membership == "" || info.RoomID == "" || info.RoomInstance == "" || info.ServiceEpoch == "" || (roomID != "" && roomID != info.RoomID) {
			return apiRequest{}, fmt.Errorf("member tools require all six TEAMX_* environment values and the bound Room")
		}
		return apiRequest{ServiceInfo: info, Caller: Caller{Author: Author{Kind: "agent", ID: agent}, MembershipID: membership}}, nil
	}
	var info ServiceInfo
	if err := c.request(ctx, "/v1/info", nil, &info); err != nil {
		// Managed services have no implicit Room. Resolve an explicitly named
		// live instance from the catalog; ordinary requests still carry its full
		// identity and never start a replacement service.
		var catalog roomsResponse
		if catalogErr := c.request(ctx, "/v1/rooms", nil, &catalog); catalogErr != nil {
			return apiRequest{}, err
		}
		if roomID == "" {
			return apiRequest{}, fmt.Errorf("%w: managed service requires --room", ErrRequest)
		}
		for _, room := range catalog.Rooms {
			if room.RoomID == roomID && room.Service != nil {
				info = *room.Service
				break
			}
		}
		if info == (ServiceInfo{}) {
			return apiRequest{}, ErrIdentity
		}
	}
	if roomID != "" && roomID != info.RoomID {
		return apiRequest{}, ErrIdentity
	}
	user := env("USER")
	if user == "" {
		user = "operator"
	}
	return apiRequest{ServiceInfo: info, Caller: Caller{Author: Author{Kind: "human", ID: user}}}, nil
}

// runCLI accepts a writer so the output/confirmation boundary can be tested
// with a real HTTP peer, including short writes and broken pipes.
func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string) error {
	// Git invokes GIT_ASKPASS with only a prompt. Keep the executable path free
	// of embedded flags, so spaces and quotes in paths work without a shell.
	if len(args) == 1 && env("TEAMX_GITLAB_TOOLS") != "" && (strings.HasPrefix(args[0], "Username for '") || strings.HasPrefix(args[0], "Password for '")) {
		return runWorkspaceGitCLI(ctx, []string{"askpass", "--binding", filepath.Join(env("TEAMX_GITLAB_TOOLS"), "binding.json"), args[0]}, stdout, stderr, env)
	}

	if len(args) == 0 {
		return fmt.Errorf("usage: teamx serve | member add/list/remove/run/pause/session rotate | send | read | tools list/run")
	}
	command, args := args[0], args[1:]
	if command == "workspace-git" {
		return runWorkspaceGitCLI(ctx, args, stdout, stderr, env)
	}
	if command == "tools" {
		return runToolsCLI(ctx, args, os.Stdin, stdout, stderr, env)
	}
	operation := ""
	if command == "member" {
		if len(args) == 0 {
			return fmt.Errorf("usage: teamx member add/list/remove/run/pause [ROOM] --agent ID")
		}
		operation, args = args[0], args[1:]
		if operation == "session" {
			if len(args) == 0 || args[0] != "rotate" {
				return fmt.Errorf("usage: teamx member session rotate [ROOM] --agent ID [--cancel] [--run]")
			}
			operation, args = "session/rotate", args[1:]
		}
		if operation != "add" && operation != "list" && operation != "remove" && operation != "run" && operation != "pause" && operation != "session/rotate" {
			return ErrRequest
		}
	} else if command != "send" && command != "read" {
		return fmt.Errorf("unknown command %q", command)
	}
	roomID := env("TEAMX_ROOM_ID")
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		roomID, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := env("TEAMX_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://127.0.0.1:8088"
	}
	flags.StringVar(&endpoint, "endpoint", endpoint, "local Room HTTP endpoint")
	flags.StringVar(&roomID, "room", roomID, "explicit Room to address (required in management mode)")
	asJSON := flags.Bool("json", false, "write JSON (one object per read page)")
	var agent, runner, profile, text, messageID string
	var retry, cancelTurn, resume bool
	var replyTo uint64
	var all, unread, peek bool
	var latest, limit int
	switch command {
	case "member":
		if operation != "list" {
			flags.StringVar(&agent, "agent", "", "member identity")
		}
		if operation == "add" {
			flags.StringVar(&runner, "runner", "dsh", "agent backend (dsh)")
			flags.StringVar(&profile, "profile", "", "service-side DSH profile name")
		}
		if operation == "run" {
			flags.BoolVar(&retry, "retry", false, "offer current unread again without replaying an old call stack")
		}
		if operation == "pause" {
			flags.BoolVar(&cancelTurn, "cancel", false, "cancel current turn and wait for settlement before any later turn")
		}
		if operation == "session/rotate" {
			flags.BoolVar(&cancelTurn, "cancel", false, "cancel old turn and wait for full cleanup")
			flags.BoolVar(&resume, "run", false, "resume scheduling after rotation; does not replay offered unread")
		}
	case "send":
		flags.StringVar(&text, "text", "", "message text")
		flags.StringVar(&messageID, "id", "", "stable retry ID")
		flags.Uint64Var(&replyTo, "reply-to", 0, "referenced message sequence")
	case "read":
		flags.BoolVar(&all, "all", false, "preview entire fixed snapshot")
		flags.BoolVar(&unread, "unread", false, "consume unread messages")
		flags.BoolVar(&peek, "peek", false, "preview unread without confirming")
		flags.IntVar(&latest, "latest", 0, "preview latest N messages")
		flags.IntVar(&limit, "limit", 20, "maximum other-author unread messages")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return ErrRequest
	}
	if command == "member" && operation == "add" && !(RunnerConfig{Kind: runner, Profile: profile}).valid() {
		return fmt.Errorf("%w: member add requires a DSH profile", ErrRequest)
	}
	client := cliClient{endpoint: strings.TrimRight(endpoint, "/"), http: &http.Client{Timeout: 10 * time.Second}}
	req, err := client.identity(ctx, roomID, env)
	if err != nil {
		return err
	}
	if command == "member" {
		if req.Caller.Kind != "human" {
			return fmt.Errorf("member management requires operator context")
		}
		req.AgentID, req.Runner = agent, RunnerConfig{Kind: runner, Profile: profile}
		req.Retry, req.Cancel, req.Resume = retry, cancelTurn, resume
		switch operation {
		case "list":
			var result serviceMembers
			if err := client.request(ctx, "/v1/member/list", &req, &result); err != nil {
				return err
			}
			if *asJSON {
				return writeJSON(stdout, result)
			}
			var output bytes.Buffer
			fmt.Fprintf(&output, "Room %s instance=%s service_epoch=%s revision=%d\n", result.RoomID, result.RoomInstance, result.ServiceEpoch, result.Revision)
			for _, m := range result.Members {
				fmt.Fprintf(&output, "%s membership=%s unread=%d read_through=%d status=%s", m.AgentID, m.MembershipID, m.UnreadCount, m.ReadThrough, m.Status)
				if m.Notification != nil {
					fmt.Fprintf(&output, " notification_revision=%d notification_unread=%d", m.Notification.Revision, m.Notification.UnreadCount)
				}
				if m.Execution != nil {
					fmt.Fprintf(&output, " paused=%t active=%t turn=%d error=%q", m.Execution.Paused, m.Execution.Active, m.Execution.Agent.TurnNumber, m.Execution.Error)
					if b := m.Execution.TurnBudget; b != nil {
						fmt.Fprintf(&output, " turns_used=%d turn_limit=%d", b.Used, b.Limit)
					}
					if r := m.Execution.Rotation; r != nil {
						fmt.Fprintf(&output, " rotation=%s private_context_reset=%t rotation_error=%q", r.Status, r.ContextReset, r.Error)
					}
				}
				fmt.Fprintln(&output)
			}
			return writeOutput(stdout, output.Bytes())
		case "remove", "run", "pause", "session/rotate":
			var list serviceMembers
			if err := client.request(ctx, "/v1/member/list", &req, &list); err != nil {
				return err
			}
			for _, m := range list.Members {
				if m.AgentID == agent {
					req.MembershipID = m.MembershipID
					break
				}
			}
			if req.MembershipID == "" {
				return ErrIdentity
			}
		}
		if operation == "run" || operation == "pause" || operation == "session/rotate" {
			var result MemberStatus
			if err := client.request(ctx, "/v1/member/"+operation, &req, &result); err != nil {
				return err
			}
			if *asJSON {
				return writeJSON(stdout, result)
			}
			if operation == "session/rotate" {
				return writeOutput(stdout, []byte(fmt.Sprintf("rotated %s; private context reset; Room history and cursor preserved; paused=%t (use read --all/--latest for shared context)\n", agent, result.Paused)))
			}
			return writeOutput(stdout, []byte(fmt.Sprintf("%s %s status=%s paused=%t active=%t\n", operation, agent, result.Status, result.Paused, result.Active)))
		}
		var result MemberInfo
		if err := client.request(ctx, "/v1/member/"+operation, &req, &result); err != nil {
			return err
		}
		if *asJSON {
			return writeJSON(stdout, result)
		}
		return writeOutput(stdout, []byte(fmt.Sprintf("%s %s membership=%s\n", operation, result.AgentID, result.MembershipID)))
	}
	if command == "send" {
		if messageID == "" {
			messageID, err = newOpaqueID()
			if err != nil {
				return err
			}
		}
		req.ClientMessageID, req.Text, req.ReplyTo = messageID, text, replyTo
		var result PostReceipt
		if err := client.request(ctx, "/v1/send", &req, &result); err != nil {
			return fmt.Errorf("send failed or outcome uncertain; client_message_id=%s (retry the same ID and content): %w", messageID, err)
		}
		if *asJSON {
			err = writeJSON(stdout, result)
		} else {
			err = writeOutput(stdout, []byte(fmt.Sprintf("Room %s instance=%s seq=%d client_message_id=%s\n", result.RoomID, result.RoomInstance, result.Seq, result.ClientMessageID)))
		}
		if err != nil {
			return fmt.Errorf("message committed; seq=%d client_message_id=%s; receipt output failed: %w", result.Seq, result.ClientMessageID, err)
		}
		return nil
	}
	modeCount := 0
	if all {
		modeCount++
	}
	if unread {
		modeCount++
	}
	if latest != 0 {
		modeCount++
	}
	if modeCount > 1 || latest < 0 || limit < 1 || limit > maxMessages || (peek && !unread) {
		return ErrRequest
	}
	req.Mode, req.Latest = ReadLatest, latest
	if req.Latest == 0 {
		req.Latest = 20
	}
	if all {
		req.Mode, req.Latest = ReadAll, 0
	}
	if unread {
		req.Mode, req.Latest, req.Limit, req.Peek = ReadUnread, 0, limit, peek
	}
	return readCLI(ctx, client, req, stdout, *asJSON)
}

func readCLI(ctx context.Context, client cliClient, req apiRequest, stdout io.Writer, asJSON bool) error {
	for {
		var page ReadPage
		if err := client.request(ctx, "/v1/read", &req, &page); err != nil {
			return fmt.Errorf("read incomplete: %w", err)
		}
		var output bytes.Buffer
		if asJSON {
			if err := json.NewEncoder(&output).Encode(page); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(&output, "Room %s instance=%s snapshot=%d from=%d through=%d has_more=%t unread=%d\n", page.RoomID, page.RoomInstance, page.SnapshotSeq, page.FromExclusive, page.ScannedThrough, page.HasMore, page.UnreadCount)
			for _, m := range page.Messages {
				fmt.Fprintf(&output, "[%d] %s/%s: %s\n", m.Seq, m.Author.Kind, m.Author.ID, m.Text)
			}
		}
		if err := writeOutput(stdout, output.Bytes()); err != nil {
			return fmt.Errorf("page output incomplete; this page was not confirmed: %w", err)
		}
		if page.ReadReceipt != "" {
			confirm := req
			confirm.ReadReceipt = page.ReadReceipt
			var result ReadConfirmation
			if err := client.request(ctx, "/v1/read/confirm", &confirm, &result); err != nil {
				return fmt.Errorf("page output complete; confirmation failed or uncertain (receipt=%s through=%d); read incomplete: %w", page.ReadReceipt, page.ScannedThrough, err)
			}
		}
		if !page.HasMore {
			return nil
		}
		if page.NextAfter <= page.FromExclusive {
			return fmt.Errorf("read incomplete: non-advancing page")
		}
		next, snapshot := page.NextAfter, page.SnapshotSeq
		req.After, req.SnapshotSeq = &next, &snapshot
		if req.Mode == ReadUnread {
			req.Limit = page.Remaining
		} else {
			req.Mode, req.Latest = ReadRange, 0
		}
	}
}

func writeJSON(w io.Writer, value any) error {
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(value); err != nil {
		return err
	}
	return writeOutput(w, b.Bytes())
}

func writeOutput(w io.Writer, data []byte) error {
	n, err := w.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

// Run dispatches the TeamX Room CLI. The caller owns process exit reporting.
func Run(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "serve" {
		return serve(args[1:], stdout, stderr)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runCLI(ctx, args, stdout, stderr, os.Getenv)
}
