package room

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

const platformMemberHelp = `Usage:
  teamx platforms member notifications --platform-id ID --source-seq SEQ --enabled true|false
  teamx platforms member get --platform-id ID [--platform-version V]
  teamx platforms member notifications --help
  teamx platforms member tool --help
  teamx platforms member status --operation-id ID | --source-seq SEQ

Pure switches need no preliminary get/status, versions or client operation ID.
First enable uses manual setup defaults; existing settings restore saved sources.
For source/range/filter edits, see notifications --help. Selection cards use
platforms skill --topic selection. For pure switch workflow/recovery read
platforms skill --topic notifications directly; complex edits use platforms skill.
`

func platformMemberActionHelp(action string) string {
	switch action {
	case "notifications":
		return `Usage:
  teamx platforms member notifications --platform-id ID --source-seq SEQ --enabled true|false
  teamx platforms member notifications --platform-id ID --source-seq SEQ --platform-version V --notification-version N --operation-id ID [source/range/filter flags]

Pure switches observe current state and recover the original receipt in one call.
Use the original human seq; do not generate an operation ID or first get/status.
First enable uses manual defaults; existing settings retain sources, filters and
reception start. No source in saved settings requires an explicit source choice.
Historical receipts describe that operation, not necessarily current settings.
Customization requires all three observed fields (notification-version 0 means
absent). Any supplied version/operation-id selects complete mode. Preserve stale
observations on conflict; inspect unknown/partial receipts, never replay them.
Omitted source fields are preserved; first customization enables only requested
sources. Explicit enabled false overrides source enables. Collect all fields for
the same account/source in one operation. A pure switch may restore both sources;
single-source enable must not silently awaken another dormant source.
Feishu all→specified requires explicit scope and a complete replacement list;
empty lists close that branch. Personnel IDs must be verified p2p chat_ids.
GitLab personal todos cover accessible projects without project selection or a
Workspace; project activity needs a verified native ID/path. Pending/errors use
existing settings retry-apply recovery. Tools and external writes are separate.
For pure switch recovery read platforms skill --topic notifications; for a card
read platforms skill --topic selection. Complex edits use complete platforms skill.

Flags:
`
	case "get":
		return "Usage: teamx platforms member get --platform-id ID [--platform-version V]\n\nOmit version to read current verified account/configuration; explicit V must be nonzero and current. This read creates no subscription, receipt or reception start.\n\nFlags:\n"
	case "tool":
		return "Usage: teamx platforms member tool --platform-id ID --platform-version V --operation-id ID --source-seq SEQ --enabled true|false [--binding-revision N] [--action OP ...]\n\nUse observed account/binding state and a stable operation ID. Tool grants do not authorize external writes. Unknown submissions require original-receipt inspection, never replay. Read platforms skill for capability and authorization recovery.\n\nFlags:\n"
	case "status":
		return "Usage: teamx platforms member status --operation-id ID | --source-seq SEQ\n\nRead original member configuration receipts; this command never reapplies them.\n\nFlags:\n"
	case "chats":
		return "Usage: teamx platforms chats --platform-id ID --platform-version V [--type group|p2p] [--query NAME --page-token TOKEN]\nSearch the exact verified Feishu account without creating conversations. Default: groups, including topic groups; p2p excludes bots and self.\nReturns JSON with exact chat IDs, connection version, continuation and search_warnings. Follow tokens even on empty filtered pages; personnel name searches may require refining the query instead. Permission errors and partial pages do not prove absence; same names are not selections.\nExample: teamx platforms chats --platform-id ID --platform-version V --type group --query NAME\nFlags:\n"
	case "projects":
		return "Usage: teamx platforms projects --platform-id ID --platform-version V [--query NAME --page-token PAGE]\nSearch the exact verified GitLab account; reuse the returned numeric page token.\nReturns JSON with native project IDs, exact paths and continuation. Permission errors and partial pages do not prove absence. Project activity uses one verified project per member/account; personal todos need no project or Workspace selection.\nExample: teamx platforms projects --platform-id ID --platform-version V --query NAME\nFlags:\n"
	default:
		return platformMemberHelp
	}
}

func runPlatformMemberCLI(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string, h *http.Client) error {
	action := args[0]
	args = args[1:]
	if action == "member" {
		if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
			return writeOutput(stdout, []byte(platformMemberHelp))
		}
		action, args = args[0], args[1:]
		if action != "get" && action != "notifications" && action != "tool" && action != "status" {
			return ErrRequest
		}
	}
	f := flag.NewFlagSet("platforms "+action, flag.ContinueOnError)
	f.SetOutput(stderr)
	f.Usage = func() {
		_ = writeOutput(stdout, []byte(platformMemberActionHelp(action)))
		f.PrintDefaults()
	}
	req := platformMemberRequest{}
	in := platformMemberInput{}
	if action != "status" {
		f.StringVar(&in.PlatformID, "platform-id", "", "exact verified connection ID")
		f.Uint64Var(&in.PlatformVersion, "platform-version", 0, "observed connection version")
		req.Input = &in
	}
	if action == "status" || action == "notifications" || action == "tool" {
		f.StringVar(&req.OperationID, "operation-id", "", "stable configuration operation ID")
		f.Uint64Var(&req.SourceSeq, "source-seq", 0, "original human Room message sequence")
	}
	if action == "chats" || action == "projects" {
		f.StringVar(&req.Query, "query", "", "name search (optional)")
		f.StringVar(&req.PageToken, "page-token", "", "exact continuation token")
	}
	if action == "chats" {
		f.Func("type", "group (default) or p2p (verified personnel conversations)", func(s string) error {
			if s != "group" && s != "p2p" {
				return ErrRequest
			}
			req.Type = s
			return nil
		})
	}
	if action == "notifications" || action == "tool" {
		// A value is required so omitted and explicit false remain distinct.
		f.Func("enabled", "true or false", func(s string) error {
			if s != "true" && s != "false" {
				return ErrRequest
			}
			v, err := strconv.ParseBool(s)
			in.Enabled = &v
			return err
		})
	}
	if action == "notifications" {
		f.Func("todos-enabled", "GitLab personal todos: true or false; preserves other sources", func(s string) error {
			if s != "true" && s != "false" {
				return ErrRequest
			}
			v, err := strconv.ParseBool(s)
			in.TodosEnabled = &v
			return err
		})
		f.Func("events-enabled", "GitLab project activity: true or false", func(s string) error {
			if s != "true" && s != "false" {
				return ErrRequest
			}
			v, _ := strconv.ParseBool(s)
			in.EventsEnabled = &v
			return nil
		})
		f.Int64Var(&in.ProjectID, "project-id", 0, "exact native project ID")
		f.StringVar(&in.ProjectPath, "project-path", "", "exact path_with_namespace")
		f.Func("event-type", "activity type; repeat; empty clears filter", func(s string) error {
			if in.EventsFilter == nil {
				in.EventsFilter = &gitlab.EventsFilter{Types: []string{}}
			}
			if s != "" {
				in.EventsFilter.Types = append(in.EventsFilter.Types, s)
			}
			return nil
		})
		f.Func("todo-reason", "todo reason; repeat; empty selects none", func(s string) error {
			if in.TodosFilter == nil {
				in.TodosFilter = &gitlab.TodosFilter{}
			}
			if in.TodosFilter.Reasons == nil {
				in.TodosFilter.Reasons = []string{}
			}
			if s != "" {
				in.TodosFilter.Reasons = append(in.TodosFilter.Reasons, s)
			}
			return nil
		})
		f.Func("todo-type", "todo type; repeat; empty selects none", func(s string) error {
			if in.TodosFilter == nil {
				in.TodosFilter = &gitlab.TodosFilter{}
			}
			if in.TodosFilter.Types == nil {
				in.TodosFilter.Types = []string{}
			}
			if s != "" {
				in.TodosFilter.Types = append(in.TodosFilter.Types, s)
			}
			return nil
		})
		f.Uint64Var(&in.NotificationVersion, "notification-version", 0, "required observed exact subscription version (explicit 0 if absent)")
		f.Func("add-chat-id", "exact selected group ID; repeat to add multiple groups", func(id string) error { in.AddChatIDs = append(in.AddChatIDs, id); return nil })
		f.Func("remove-chat-id", "exact followed group ID; repeat to remove groups without a remote read", func(id string) error { in.RemoveChatIDs = append(in.RemoveChatIDs, id); return nil })
		f.StringVar(&in.GroupMessageMode, "group-message-mode", "", "all or mentions; omitted preserves existing mode")
		groupPatch := func() *platformMemberGroupPatch {
			if in.Groups == nil {
				in.Groups = &platformMemberGroupPatch{}
			}
			return in.Groups
		}
		privatePatch := func() *platformMemberPrivatePatch {
			if in.Private == nil {
				in.Private = &platformMemberPrivatePatch{}
			}
			return in.Private
		}
		boolFlag := func(name string, field func() **bool) {
			f.Func(name, "Feishu: true or false; omitted preserves current value", func(s string) error {
				if s != "true" && s != "false" {
					return ErrRequest
				}
				v := s == "true"
				*field() = &v
				return nil
			})
		}
		boolFlag("group-enabled", func() **bool { return &groupPatch().Enabled })
		boolFlag("group-at-me", func() **bool { return &groupPatch().AtMe })
		boolFlag("group-at-all", func() **bool { return &groupPatch().AtAll })
		boolFlag("private-enabled", func() **bool { return &privatePatch().Enabled })
		f.Func("group-scope", "Feishu: all or specified; changing all to specified also needs a complete list", func(s string) error {
			if s != "all" && s != "specified" {
				return ErrRequest
			}
			groupPatch().Scope = s
			return nil
		})
		f.Func("private-scope", "Feishu: all or specified; changing all to specified needs a complete list", func(s string) error {
			if s != "all" && s != "specified" {
				return ErrRequest
			}
			privatePatch().Scope = s
			return nil
		})
		f.Func("private-chat-id", "Feishu: complete personnel p2p chat_id list; repeat; a single empty value clears it", func(id string) error {
			p := privatePatch()
			if p.ChatIDs == nil {
				p.ChatIDs = new([]string)
				*p.ChatIDs = []string{}
			} else if id == "" || len(*p.ChatIDs) == 0 {
				return ErrRequest
			}
			if id != "" {
				*p.ChatIDs = append(*p.ChatIDs, id)
			}
			return nil
		})
		f.Func("group-chat-id", "Feishu: complete replacement list; repeat for multiple IDs; a single empty value clears it", func(id string) error {
			p := groupPatch()
			if p.ChatIDs == nil {
				p.ChatIDs = new([]string)
				*p.ChatIDs = []string{}
			} else if id == "" || len(*p.ChatIDs) == 0 {
				return ErrRequest // Do not mix an explicit clear with selected IDs.
			}
			if id != "" {
				*p.ChatIDs = append(*p.ChatIDs, id)
			}
			return nil
		})
	}
	if action == "tool" {
		f.Func("action", "Lark action to add; repeat; omitted enables current defaults", func(op string) error { in.Actions = append(in.Actions, op); return nil })
		f.Func("binding-revision", "optional observed exact binding revision; explicit 0 means never configured", func(s string) error {
			v, err := strconv.ParseUint(s, 10, 64)
			if err == nil {
				in.ExpectedBindingRevision = &v
			}
			return err
		})
	}
	_ = f.Bool("json", true, "JSON output")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return ErrRequest
	}
	present := map[string]bool{}
	f.Visit(func(f *flag.Flag) { present[f.Name] = true })
	if action == "notifications" {
		full := present["platform-version"] || present["notification-version"] || present["operation-id"]
		if full {
			for _, name := range []string{"platform-version", "notification-version", "operation-id"} {
				if !present[name] {
					return invalidField(name, "complete mode requires --platform-version, --notification-version (explicit 0 if absent), and --operation-id")
				}
			}
		} else {
			for name := range present {
				if name != "platform-id" && name != "source-seq" && name != "enabled" && name != "json" {
					return invalidField(name, "source/range/filter edits require complete observed versions and operation ID")
				}
			}
			if in.Enabled == nil {
				return invalidField("enabled", "short notification command requires explicit --enabled true|false")
			}
			req.ObserveCurrent = true
		}
	}
	if action == "get" {
		if present["platform-version"] && in.PlatformVersion == 0 {
			return invalidField("platform-version", "explicit platform version must be nonzero; omit to read current state")
		}
		req.ObserveCurrent = !present["platform-version"]
	}
	endpoint := strings.TrimRight(env("TEAMX_ENDPOINT"), "/")
	if endpoint == "" {
		return fmt.Errorf("platform member commands require bound Agent context")
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
	path := "/v1/platforms/member/" + action
	if action == "chats" || action == "projects" {
		path = "/v1/platforms/" + action
	}
	var result json.RawMessage // Preserve uint64 source sequences and revisions exactly.
	var wire any = req
	if req.ObserveCurrent {
		// Only the short form crosses HTTP: zero observed versions must be absent,
		// while complete requests keep explicit notification_version=0.
		wire = struct {
			platformMemberRequest
			Input struct {
				PlatformID string `json:"platform_id"`
				Enabled    *bool  `json:"enabled,omitempty"`
			} `json:"input"`
		}{platformMemberRequest: req, Input: struct {
			PlatformID string `json:"platform_id"`
			Enabled    *bool  `json:"enabled,omitempty"`
		}{in.PlatformID, in.Enabled}}
	}
	if err := client.request(ctx, path, wire, &result); err != nil {
		if action == "get" {
			return fmt.Errorf("platform_id=%s; inspect the exact platform connection: %w", in.PlatformID, err)
		}
		if req.ObserveCurrent && action == "notifications" {
			return fmt.Errorf("platform_id=%s source_seq=%d; inspect member status by the original source-seq; never automatically retry conflicts or unknown submissions: %w", in.PlatformID, req.SourceSeq, err)
		}
		return fmt.Errorf("operation_id=%s source_seq=%d; query member status and get before retry: %w", req.OperationID, req.SourceSeq, err)
	}
	return writeJSON(stdout, result)
}
