package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"strings"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func resolveDiscussionsCommand(ctx context.Context, a *app, args []string) error {
	f := flag.NewFlagSet("notifications resolve", flag.ContinueOnError)
	f.SetOutput(a.out)
	input := f.String("queries", "", "JSON array of 1-25 {source,url} queries; one batch coalesces matching objects (max 5 objects, 10 pages/object, 500 notes/object, 30s). GET only; partial/unavailable is never proof of absence")
	if err := f.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if f.NArg() != 0 || len(*input) > 128<<10 {
		return errors.New("invalid discussion queries")
	}
	var queries []gitlab.DiscussionQuery
	dec := json.NewDecoder(strings.NewReader(*input))
	dec.DisallowUnknownFields()
	if dec.Decode(&queries) != nil || dec.Decode(new(any)) != io.EOF {
		return errors.New("invalid discussion queries")
	}
	c, err := a.api()
	if err != nil {
		return err
	}
	c.responseLimit = 1 << 20
	reports, err := gitlab.ResolveDiscussions(ctx, c.origin, queries, func(ctx context.Context, path string, out any) (http.Header, error) {
		return c.request(ctx, "GET", path, nil, out)
	})
	if err != nil {
		return err
	}
	return a.output(reports)
}
