package room

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func runPlatformMemberCLI(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string, h *http.Client) error {
	action := args[0]
	args = args[1:]
	if action == "member" {
		if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
			return writeOutput(stdout, []byte(platformConnectHelp))
		}
		action, args = args[0], args[1:]
		if action != "get" && action != "notifications" && action != "tool" && action != "status" {
			return ErrRequest
		}
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return writeOutput(stdout, []byte(platformConnectHelp))
	}
	f := flag.NewFlagSet("platforms "+action, flag.ContinueOnError)
	f.SetOutput(stderr)
	f.Usage = func() { _ = writeOutput(stdout, []byte(platformConnectHelp)) }
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
		f.Uint64Var(&in.NotificationVersion, "notification-version", 0, "observed subscription version (0 if absent)")
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
		f.Uint64Var(&in.ToolRevision, "tool-revision", 0, "observed Room tool revision")
	}
	_ = f.Bool("json", true, "JSON output")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return ErrRequest
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
	if err := client.request(ctx, path, req, &result); err != nil {
		return fmt.Errorf("operation_id=%s source_seq=%d; query member status and get before retry: %w", req.OperationID, req.SourceSeq, err)
	}
	return writeJSON(stdout, result)
}
