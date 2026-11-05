package room

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s *service) memberMatterContext(req apiRequest, path string) (any, error) {
	if req.Caller.Kind != "agent" || req.AgentID != "" || req.MembershipID != "" {
		return nil, ErrIdentity
	}
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
	switch path {
	case "/v1/matters/context":
		return s.store.readMatterContext(id)
	case "/v1/matters/enrich":
		if req.MatterContext == nil {
			return nil, ErrRequest
		}
		return s.store.enrichMatterContext(id, *req.MatterContext)
	case "/v1/matters/relate":
		if req.MatterRelation == nil {
			return nil, ErrRequest
		}
		return s.store.relateMatters(id, *req.MatterRelation)
	}
	return nil, ErrRequest
}

const matterContextHelp = `Member discussion context and matter association (JSON output):
  teamx matters context
  teamx matters enrich --change JSON
  teamx matters relate --change JSON
Enrich: {id,revision,turn_number,resolutions:[{seq,report}]}.
report is the complete result from the configured tools run gitlab notifications resolve --queries '[{"source":EXACT_SOURCE,"url":EXACT_ITEM_URL}]'. Batch related inputs in ONE call. Use the source and item URL from Inbox; omit url only when the original URL is empty. Read context first: reuse verified identities and mappings, and wait at least one minute before rechecking a saved input. Reports must be fresh (10 minutes), match the original input exactly, and cannot change a known ID/IID. Partial/failed results remain visible; never treat them as proof of absence. No background polling is provided.
Relate: {id,revision,turn_number,kind,from:{seq,id},from_version,to:{seq,id},to_version,basis,evidence,relation_id?}.
kind append/merge redirects a source concern to the target concern of the SAME verified object, recipient and scope. Different decisions, unanswered questions and follow-up conditions stay independent; use kind relate for an explicit cross-object connection without redirecting work or authority. kind split undoes the exact edge in relation_id; to correct a merge, split it then create a new edge with a new operation ID. Create a new independent concern using matters update --version 0 before splitting a request. Use stable IDs and current context/matter versions. Same-ID exact retries return the original commit. Original receipts, actions, decisions and history are never moved/deleted; inspect all original evidence before continuing. Neither association nor a successful response completes any Todo. Re-read current remote state and authority before each external write; cached mappings confer no permission.
`

func runMatterContextCLI(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return writeOutput(stdout, []byte(matterContextHelp))
	}
	op := args[0]
	if op != "context" && op != "enrich" && op != "relate" {
		return ErrRequest
	}
	f := flag.NewFlagSet("matters "+op, flag.ContinueOnError)
	f.SetOutput(stderr)
	f.Usage = func() { _ = writeOutput(stdout, []byte(matterContextHelp)) }
	endpoint := f.String("endpoint", env("TEAMX_ENDPOINT"), "bound Room endpoint")
	change := f.String("change", "", "structured context/association change")
	_ = f.Bool("json", true, "JSON output")
	if err := f.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return ErrRequest
	}
	client := cliClient{endpoint: strings.TrimRight(*endpoint, "/"), http: &http.Client{Timeout: 10 * time.Second}}
	req, err := client.identity(ctx, env("TEAMX_ROOM_ID"), env)
	if err != nil {
		return err
	}
	if req.Caller.Kind != "agent" {
		return ErrIdentity
	}
	if op != "context" {
		var target any
		if op == "enrich" {
			req.MatterContext = &MatterContextChange{}
			target = req.MatterContext
		} else {
			req.MatterRelation = &MatterRelationChange{}
			target = req.MatterRelation
		}
		dec := json.NewDecoder(strings.NewReader(*change))
		dec.DisallowUnknownFields()
		if dec.Decode(target) != nil || dec.Decode(new(any)) != io.EOF {
			return ErrRequest
		}
	} else if *change != "" {
		return ErrRequest
	}
	var result MatterContext
	if err := client.request(ctx, "/v1/matters/"+op, &req, &result); err != nil {
		return err
	}
	return writeJSON(stdout, result)
}
