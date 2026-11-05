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

func runPolicyCLI(ctx context.Context, args []string, stdout, stderr io.Writer, env func(string) string) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return writeOutput(stdout, []byte(`Member policy and one-off decisions (JSON output):
  teamx policy read
  teamx policy suggest --change '{"id":"stable-id","revision":0,"version":0,"kind":"preference","status":"suggested","text":"简短中文回复","scope":{"description":"本成员回复方式"},"source_seq":1,"turn_number":1}'
  teamx policy suggest-decision --change JSON
Agent suggestions require a real human Room source and the current turn. Only human member settings can activate, amend or revoke effective policy/decisions. Runtime supplies effective policy and current_turn; policy read restores the full effective view only when its body is missing. Inbox read and matters list include effective decisions for each exact matter. Preferences confer no authority. Adapted GitLab MR comments use actions execute, which checks policy_token and records the outcome; see actions --help.
`))
	}
	op := args[0]
	paths := map[string]string{"read": "/v1/policy/read", "suggest": "/v1/policy/update", "suggest-decision": "/v1/decisions/update"}
	path, ok := paths[op]
	if !ok {
		return ErrRequest
	}
	f := flag.NewFlagSet("policy "+op, flag.ContinueOnError)
	f.SetOutput(stderr)
	endpoint := f.String("endpoint", env("TEAMX_ENDPOINT"), "bound Room endpoint")
	change := f.String("change", "", "structured proposed change; never active authorization")
	_ = f.Bool("json", true, "JSON output")
	if err := f.Parse(args[1:]); err != nil {
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
	if op == "suggest" || op == "suggest-decision" {
		dec := json.NewDecoder(strings.NewReader(*change))
		dec.DisallowUnknownFields()
		var c PolicyChange
		if dec.Decode(&c) != nil || dec.Decode(new(any)) != io.EOF || c.Status != "suggested" {
			return ErrRequest
		}
		req.Policy = &c
	}
	var result any
	if err := client.request(ctx, path, &req, &result); err != nil {
		return err
	}
	return writeJSON(stdout, result)
}
