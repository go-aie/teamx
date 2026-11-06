package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

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
		_, err := fmt.Fprintln(stdout, "Usage: teamx tools list | teamx tools run <tool> [args...] | teamx tools skill <tool>\nRun <tool> --help to discover its commands. Read a skill only when its workflow is needed.")
		return err
	}
	if args[0] != "list" && args[0] != "run" && args[0] != "skill" || args[0] == "list" && len(args) != 1 || args[0] == "run" && len(args) < 2 || args[0] == "skill" && len(args) != 2 {
		return fmt.Errorf("usage: teamx tools list | teamx tools run <tool> [args...] | teamx tools skill <tool>")
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
	if args[0] == "skill" {
		data, _ := json.Marshal(req)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v1/tools/skill", bytes.NewReader(data))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.http.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("tool skill HTTP %s", response.Status)
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
	response, err := client.http.Do(request)
	if err != nil {
		return toolTransportError(ctx, stderr, "tool transport interrupted; remote result unconfirmed; query before retry")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure apiError
		if json.NewDecoder(io.LimitReader(response.Body, 32<<10)).Decode(&failure) == nil && failure.Error != "" {
			return failure.cliError()
		}
		return fmt.Errorf("tool launcher HTTP %s", response.Status)
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

func toolTransportError(ctx context.Context, stderr io.Writer, message string) error {
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, message)
		return &ExitError{130}
	}
	return fmt.Errorf("%s", message)
}
