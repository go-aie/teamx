package room

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const platformSelectionHelp = `Usage:
  teamx platforms selection start --kind account|groups --operation-id ID --source-seq SEQ [--platform-id ID --platform-version V --query NAME] [--name NAME] [--notify inbox|none]
  teamx platforms selection status --operation-id ID | --source-seq SEQ
  teamx platforms selection update --operation-id ID --version V [--selected-platform-id ID --selected-platform-version V --name NAME] [--chat-id CHAT_ID] [--group-message-mode all|mentions]
  teamx platforms selection wait --operation-id ID [--timeout 20s]
  teamx platforms selection cancel --operation-id ID --version V
  teamx platforms selection notify --operation-id ID --mode inbox|none
  teamx platforms selection acknowledge --operation-id ID --result-version V

Selections publish fixed account/name or group/message-mode forms in conversation
and Settings. start input and initial draft are immutable; update replaces only
the draft with compare-and-swap version checking. Human and Agent edits share the
same version. Only the original human can submit the form. A submitted receipt
cannot change and does not connect an account or configure notifications. Use the
existing connect/member commands after reading the selection, latest intent and
step receipts. Unknown submission: query the same ID; never create a replacement.
Group forms require an exact verified account/version. Candidates are read from
that account by the service with search/pagination, then checked again on submit.
Only choose explicit stable IDs. Account forms may select a verified account or
name a new connection. Fields expire after 24h; cancellation applies no settings.
notify/acknowledge and bounded wait share the connection continuation semantics.
Source-seq status returns all selection steps for that original human request.
`

func runPlatformSelectionCLI(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string, h *http.Client) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return writeOutput(stdout, []byte(platformSelectionHelp))
	}
	action := args[0]
	args = args[1:]
	f := flag.NewFlagSet("platforms selection "+action, flag.ContinueOnError)
	f.SetOutput(stderr)
	f.Usage = func() { _ = writeOutput(stdout, []byte(platformSelectionHelp)) }
	req := platformSelectionRequest{}
	in := platformSelectionInput{}
	draft := platformSelectionDraft{}
	mode := ""
	timeout := 20 * time.Second
	f.StringVar(&req.OperationID, "operation-id", "", "stable selection ID")
	switch action {
	case "start":
		f.Uint64Var(&req.SourceSeq, "source-seq", 0, "real human request seq")
		f.StringVar(&in.Kind, "kind", "", "account or groups")
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
