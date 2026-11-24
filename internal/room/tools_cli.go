package room

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const toolSkillHelp = "Usage: teamx tools skill ENTRY [--topic managed-send]\nGitLab managed-send: complete text/image MR-comment and exact-discussion send workflow, target reuse and unknown recovery. Choose it directly; no index or complete-Skill read is needed first. Omit --topic for the complete declared Skill, including development/review/merge. Unknown or unsupported topics are rejected. Current member identity and exact tool binding are required; reading needs no usable credentials or Workspace and grants no write authority."

// ExitError preserves the subprocess exit status through the production CLI.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("tool exited with status %d", e.Code) }

func strictToolJSON(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrRequest
	}
	return nil
}
func runToolsCLI(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, env func(string) string) error {
	return runToolsWithClient(ctx, args, stdin, stdout, stderr, env, &http.Client{})
}

func runToolsWithClient(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, env func(string) string, httpClient *http.Client) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(stdout, "Usage: teamx tools list | teamx tools run <tool> [args...] | teamx tools skill <tool> [--topic managed-send]\nReuse a known exact entry; list discovers bound tools and their availability/capabilities. For GitLab managed project issue/MR/work item comments/replies, read tools skill ENTRY --topic managed-send directly when guidance is needed. Omit --topic for the complete declared Skill, including development/review/merge. Local tools may have no platform account, Skill or Workspace requirement. Use tools run ENTRY --help when usage is missing. Never switch accounts after a failure or bypass the member binding.")
		return err
	}
	var skillTopic string
	if args[0] == "skill" {
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") || len(args) == 3 && (args[2] == "--help" || args[2] == "-h") {
			_, err := fmt.Fprintln(stdout, toolSkillHelp)
			return err
		}
		if len(args) >= 2 {
			f := flag.NewFlagSet("tools skill", flag.ContinueOnError)
			f.SetOutput(stderr)
			f.StringVar(&skillTopic, "topic", "", "GitLab managed-send; omit for the complete Skill")
			if err := f.Parse(args[2:]); err != nil {
				return err
			}
			if f.NArg() != 0 {
				return invalidField("arguments", "unexpected positional arguments; use tools skill ENTRY --help")
			}
			args = args[:2]
		}
	}
	if args[0] == "list" {
		f := flag.NewFlagSet("tools list", flag.ContinueOnError)
		f.SetOutput(stderr)
		_ = f.Bool("json", true, "JSON output")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return invalidField("arguments", "tools list accepts only --json")
		}
		args = []string{"list"}
	}
	if args[0] != "list" && args[0] != "run" && args[0] != "skill" || args[0] == "list" && len(args) != 1 || args[0] == "run" && len(args) < 2 || args[0] == "skill" && len(args) != 2 {
		return fmt.Errorf("usage: teamx tools list | teamx tools run <tool> [args...] | teamx tools skill <tool> [--topic managed-send]")
	}
	endpoint := strings.TrimRight(env("TEAMX_ENDPOINT"), "/")
	if endpoint == "" {
		return fmt.Errorf("tools require bound Agent context")
	}
	client := cliClient{endpoint: endpoint, http: httpClient}
	identity, err := client.identity(ctx, "", env)
	if err != nil {
		return err
	}
	if identity.Caller.Kind != "agent" {
		return fmt.Errorf("tools require bound Agent context")
	}
	req := toolsRequest{ServiceInfo: identity.ServiceInfo, Caller: identity.Caller}
	if args[0] == "list" {
		var result json.RawMessage
		if err = client.request(ctx, "/v1/tools/list", req, &result); err != nil {
			return err
		}
		return writeJSON(stdout, result)
	}
	req.Tool, req.Args = args[1], args[2:]
	req.Topic = skillTopic
	// Read request files in the Agent CLI process, never in the server's file
	// namespace. The bounded dispatcher receives exactly the same JSON contract.
	if args[0] == "run" && len(req.Args) >= 2 && req.Args[0] == "actions" && (req.Args[1] == "resolve" || req.Args[1] == "execute") {
		op := req.Args[1]
		if len(req.Args) != 3 || req.Args[2] != "--help" && req.Args[2] != "-h" {
			f := flag.NewFlagSet("actions "+op, flag.ContinueOnError)
			f.SetOutput(stderr)
			payload := f.String("request", "", "structured request")
			file := f.String("request-file", "", "JSON path; - reads stdin")
			_ = f.Bool("json", true, "JSON output")
			if err := f.Parse(req.Args[2:]); err != nil {
				return err
			}
			if f.NArg() != 0 {
				return invalidField("arguments", "unexpected positional arguments; use --help")
			}
			provided := map[string]bool{}
			f.Visit(func(f *flag.Flag) { provided[f.Name] = true })
			if provided["request"] && provided["request-file"] {
				return invalidField("--request-file", "mutually exclusive with --request")
			}
			if provided["request-file"] {
				*payload, err = readCLIJSONFile(*file, stdin)
				if err != nil {
					return fmt.Errorf("--request-file: %w", err)
				}
			}
			var target any = &agentActionRequest{}
			if op == "resolve" {
				target = &actionResolveRequest{}
			}
			if err := decodeRequestJSON(*payload, target); err != nil {
				flagName := "--request"
				if provided["request-file"] {
					flagName = "--request-file"
				}
				return fmt.Errorf("%s: %w", flagName, err)
			}
			req.Args = []string{"actions", op, "--request", *payload}
			stdin = strings.NewReader("")
		}
	}
	if args[0] == "skill" {
		data, _ := json.Marshal(req)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/tools/skill", bytes.NewReader(data))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return toolResponseError(response)
		}
		_, err = io.Copy(stdout, io.LimitReader(response.Body, 64<<10))
		return err
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// HTTP/1 duplex allows stdin and independent stdout/stderr to flow together.
	upload, send := io.Pipe()
	defer upload.Close()
	defer send.Close()
	go func() {
		_, err := io.Copy(send, io.MultiReader(bytes.NewReader(append(data, '\n')), stdin))
		_ = send.CloseWithError(err)
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/tools/run", upload)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := client.do(request)
	if err != nil {
		return toolTransportError(ctx, stderr, "tool transport interrupted; remote result unconfirmed; query before retry")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return toolResponseError(response)
	}
	decoder := json.NewDecoder(response.Body)
	for {
		var frame toolFrame
		if err := decoder.Decode(&frame); err != nil {
			return toolTransportError(ctx, stderr, "tool stream interrupted; remote result unconfirmed; query before retry")
		}
		switch frame.Stream {
		case "stdout":
			if err := writeOutput(stdout, frame.Data); err != nil {
				return err
			}
		case "stderr":
			if err := writeOutput(stderr, frame.Data); err != nil {
				return err
			}
		case "exit":
			if frame.Code < 0 || frame.Code > 255 {
				return ErrRequest
			}
			if frame.Code != 0 {
				return &ExitError{frame.Code}
			}
			return nil
		default:
			return ErrRequest
		}
	}
}

func toolResponseError(response *http.Response) error {
	var failure struct {
		apiError
		NextAction string          `json:"next_action"`
		Capability json.RawMessage `json:"capability,omitempty"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&failure) == nil && (failure.Error != "" || failure.Code != "") {
		if failure.Error == "" && failure.Message == "" {
			if failure.NextAction != "" {
				diagnostic := map[string]any{"code": failure.Code, "next_action": failure.NextAction, "hint": failure.Hint}
				if len(failure.Capability) != 0 {
					diagnostic["capability"] = failure.Capability
				}
				data, err := json.Marshal(diagnostic)
				if err == nil {
					return fmt.Errorf("platform tool: %s", data)
				}
			}
			return fmt.Errorf("platform tool: %s", failure.Code)
		}
		return failure.cliError()
	}
	return fmt.Errorf("tool launcher HTTP %s", response.Status)
}

func toolTransportError(ctx context.Context, stderr io.Writer, message string) error {
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, message)
		return &ExitError{130}
	}
	return fmt.Errorf("%s", message)
}
