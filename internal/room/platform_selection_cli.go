package room

import (
	"context"
	"flag"
	"fmt"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"io"
	"net/http"
	"strings"
	"time"
)

func runPlatformSelectionCLI(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string, h *http.Client) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return writeOutput(stdout, []byte(platformSelectionHelp))
	}
	action := args[0]
	args = args[1:]
	f := flag.NewFlagSet("platforms selection "+action, flag.ContinueOnError)
	f.SetOutput(stderr)
	f.Usage = func() {
		help, _ := platformSelectionHelpText(action)
		_ = writeOutput(stdout, []byte(help))
	}
	req := platformSelectionRequest{}
	in := platformSelectionInput{}
	draft := platformSelectionDraft{}
	mode := ""
	timeout := 20 * time.Second
	f.StringVar(&req.OperationID, "operation-id", "", "stable selection ID")
	switch action {
	case "start":
		f.Uint64Var(&req.SourceSeq, "source-seq", 0, "real human request seq")
		f.StringVar(&in.Provider, "provider", "", "lark (default), gitlab or scion (existing accounts only)")
		f.StringVar(&in.Origin, "origin", "", "canonical GitLab instance (required) or Scion instance (optional) for account choice")
		f.StringVar(&in.Kind, "kind", "", "account, groups or projects")
		f.StringVar(&in.PlatformID, "platform-id", "", "verified account for group search")
		f.Uint64Var(&in.PlatformVersion, "platform-version", 0, "account version")
		f.StringVar(&in.Query, "query", "", "initial group search")
		f.StringVar(&draft.Name, "name", "", "initial connection name")
		f.StringVar(&mode, "notify", "", "inbox or none")
		req.Input = &in
		req.Draft = &draft
	case "status":
		f.Uint64Var(&req.SourceSeq, "source-seq", 0, "original human request seq")
	case "update":
		f.Uint64Var(&req.Version, "version", 0, "observed draft version")
		f.StringVar(&draft.PlatformID, "selected-platform-id", "", "selected connection ID")
		f.Uint64Var(&draft.PlatformVersion, "selected-platform-version", 0, "selected connection version")
		f.StringVar(&draft.Name, "name", "", "new connection name")
		f.Func("chat-id", "selected group ID; repeat for multiple", func(v string) error { draft.ChatIDs = append(draft.ChatIDs, v); return nil })
		f.Int64Var(&draft.ProjectID, "project-id", 0, "native project ID")
		f.StringVar(&draft.ProjectPath, "project-path", "", "exact project path")
		f.Func("event-type", "activity type; repeat; empty clears", func(s string) error {
			if draft.EventsFilter == nil {
				draft.EventsFilter = &gitlab.EventsFilter{Types: []string{}}
			}
			if s != "" {
				draft.EventsFilter.Types = append(draft.EventsFilter.Types, s)
			}
			return nil
		})
		f.StringVar(&draft.GroupMessageMode, "group-message-mode", "", "all or mentions")
		req.Draft = &draft
	case "wait":
		f.DurationVar(&timeout, "timeout", timeout, "bounded wait up to 2m")
	case "cancel":
		f.Uint64Var(&req.Version, "version", 0, "observed version")
	case "notify":
		f.StringVar(&mode, "mode", "", "inbox or none")
	case "acknowledge":
		f.Uint64Var(&req.ResultVersion, "result-version", 0, "exact result version")
	default:
		return ErrRequest
	}
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || timeout < 0 || timeout > 2*time.Minute {
		return ErrRequest
	}
	f.Visit(func(v *flag.Flag) {
		if v.Name == "notify" || v.Name == "mode" {
			req.Notify = &mode
		}
	})
	endpoint := strings.TrimRight(env("TEAMX_ENDPOINT"), "/")
	if endpoint == "" {
		return fmt.Errorf("selection commands require bound Agent context")
	}
	client := cliClient{endpoint: endpoint, http: h}
	identity, err := client.identity(ctx, "", env)
	if err != nil {
		return err
	}
	if identity.Caller.Kind != "agent" {
		return ErrIdentity
	}
	req.ServiceInfo = identity.ServiceInfo
	req.Caller = identity.Caller
	if action != "wait" {
		var result any
		if err := client.request(ctx, "/v1/platforms/selection/"+action, req, &result); err != nil {
			return fmt.Errorf("operation_id=%s source_seq=%d; query selection status before retry: %w", req.OperationID, req.SourceSeq, err)
		}
		return writeJSON(stdout, result)
	}
	if req.OperationID == "" {
		return ErrRequest
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last *platformSelectionView
	for {
		readCtx, stop := context.WithTimeout(waitCtx, 5*time.Second)
		var v platformSelectionView
		err := client.request(readCtx, "/v1/platforms/selection/status", req, &v)
		stop()
		if err != nil && (waitCtx.Err() == nil || ctx.Err() != nil) {
			return err
		}
		if err == nil {
			last = &v
			if v.Status != "waiting" {
				return writeJSON(stdout, struct {
					Pending   bool                   `json:"pending"`
					Selection *platformSelectionView `json:"selection"`
				}{false, last})
			}
		}
		if waitCtx.Err() != nil {
			return writeJSON(stdout, struct {
				Pending     bool                   `json:"pending"`
				OperationID string                 `json:"operation_id"`
				Selection   *platformSelectionView `json:"selection,omitempty"`
			}{true, req.OperationID, last})
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
