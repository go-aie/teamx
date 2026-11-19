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

func runCalendarCLI(ctx context.Context, args []string, stdin io.Reader, out, stderr io.Writer, env func(string) string) error {
	help := "Usage: teamx calendar list/get/update/claim/finish [--room ID] [--turn current_turn] [--request-file FILE|-]\nlist: --from/--to RFC3339 --state STATE --cursor CURSOR\nget: --task-id ID [--history --cursor CURSOR] or --run-id ID [--result]; claim: --run-id ID\nupdate/finish: --request-file FILE|- (or --request JSON). --turn is the explicit authenticated execution number, separate from business JSON.\n" + calendarGuide + "\n"
	if len(args) == 0 || args[0] == "--help" {
		return writeOutput(out, []byte(help))
	}
	op := args[0]
	if !strings.Contains(" list get update claim finish ", " "+op+" ") {
		return ErrRequest
	}
	f := flag.NewFlagSet("calendar "+op, flag.ContinueOnError)
	f.SetOutput(stderr)
	f.Usage = func() { _ = writeOutput(out, []byte(help)) }
	endpoint := f.String("endpoint", env("TEAMX_ENDPOINT"), "Room endpoint")
	room := f.String("room", env("TEAMX_ROOM_ID"), "Room identity")
	turn := f.Uint64("turn", 0, "authenticated current_turn")
	file := f.String("request-file", "", "JSON file or - for stdin")
	inline := f.String("request", "", "business JSON")
	task := f.String("task-id", "", "task ID")
	run := f.String("run-id", "", "run ID")
	from := f.String("from", "", "inclusive UTC timestamp")
	to := f.String("to", "", "exclusive UTC timestamp")
	state := f.String("state", "", "task state")
	cursor := f.String("cursor", "", "returned cursor")
	history := f.Bool("history", false, "explicit task history")
	readResult := f.Bool("result", false, "explicit run report")
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
	var data []byte
	if *file != "" && *inline != "" {
		return ErrRequest
	}
	if *file != "" {
		text, err := readCLIJSONFile(*file, stdin)
		if err != nil {
			return err
		}
		data = []byte(text)
	} else if *inline != "" {
		data = []byte(*inline)
	} else {
		q := map[string]any{}
		if *history {
			q["history"] = true
		}
		if *readResult {
			q["result"] = true
		}
		for key, value := range map[string]string{"task_id": *task, "run_id": *run, "from": *from, "to": *to, "state": *state, "cursor": *cursor} {
			if value != "" {
				q[key] = value
			}
		}
		data, _ = json.Marshal(q)
	}
	if !json.Valid(data) {
		return ErrRequest
	}
	c := cliClient{endpoint: *endpoint, http: &http.Client{Timeout: 30 * time.Second}}
	req, err := c.identity(ctx, *room, env)
	if err != nil {
		return err
	}
	if req.Caller.Kind == "agent" && op != "list" && op != "get" {
		var binding struct {
			Token string `json:"token"`
		}
		if json.Unmarshal([]byte(env("DSH_TEAMX_INPUT_BINDING")), &binding) != nil || binding.Token == "" {
			return ErrIdentity
		}
		req.CalendarExecution = binding.Token
	}
	req.TurnNumber = *turn
	req.Calendar = data
	req.CalendarBusinessView = true
	var result json.RawMessage
	if err := c.request(ctx, "/v1/calendar/"+op, req, &result); err != nil {
		return err
	}
	return writeOutput(out, append(result, '\n'))
}
