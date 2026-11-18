package room

import (
	"context"
	"encoding/json"
	"flag"
	"io"

	"github.com/go-aie/teamx/internal/inputstream"
)

func writeInputJSON(out io.Writer, flush func() error, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	n, err := out.Write(b)
	if err != nil {
		return err
	}
	if n != len(b) {
		return io.ErrShortWrite
	}
	if flush == nil {
		return ErrRequest
	}
	return flush()
}
func runInputCLI(ctx context.Context, args []string, call func(context.Context, string, []byte) error) error {
	if len(args) < 2 || args[0] != "inbox" {
		return ErrRequest
	}
	name := args[1]
	if name != "list" && name != "read" {
		return ErrRequest
	}
	flags := flag.NewFlagSet("inbox "+name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	limit := flags.Int("limit", 0, "maximum entries or streams")
	var stream, cursor *string
	if name == "read" {
		stream = flags.String("stream", "", "returned stream reference")
	} else {
		cursor = flags.String("cursor", "", "directory continuation")
	}
	_ = flags.Bool("json", false, "JSON output")
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return ErrRequest
	}
	request := map[string]any{}
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "limit":
			request["limit"] = *limit
		case "stream":
			request["stream_ref"] = *stream
		case "cursor":
			request["cursor"] = *cursor
		}
	})
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if name == "read" {
		_, err = inputstream.DecodeStreamRead(data)
	} else {
		_, err = inputstream.DecodeList(data)
	}
	if err != nil {
		return err
	}
	return call(ctx, name, data)
}
