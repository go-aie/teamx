package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type options struct{ repo, iid, mode, users, body, bodyFile, discussion string }

func (a *app) options(path, action string, args []string) (options, bool, error) {
	o := options{}
	f := flag.NewFlagSet(path, flag.ContinueOnError)
	f.SetOutput(a.errOut)
	f.StringVar(&o.repo, "repo", "", "project or group full path (required)")
	f.StringVar(&o.iid, "iid", "", "work item / issue IID (required)")
	if action == "assignees" {
		f.StringVar(&o.mode, "mode", "", "add, remove, replace, clear")
		f.StringVar(&o.users, "users", "", "comma-separated numeric GitLab user IDs")
	}
	if action == "note" || action == "reply" {
		f.StringVar(&o.body, "message", "", "comment body")
		f.StringVar(&o.bodyFile, "file", "", "body file; - reads stdin")
		f.StringVar(&o.discussion, "discussion-id", "", "actual discussion ID; required for reply")
	}
	f.Usage = func() {
		f.SetOutput(a.out)
		fmt.Fprintf(a.out, "Usage: %s %s [flags]\n", usagePrefix, path)
		f.PrintDefaults()
		switch path {
		case "work-items view":
			fmt.Fprintln(a.out, "\nReturns JSON: id, iid, title, type, description, state, assignees (id/username/name).")
			fmt.Fprintln(a.out, "Assignees: null = unsupported; [] = supported, nobody assigned. Reads all assignee pages; failure returns no complete result.")
			fmt.Fprintln(a.out, "Example: "+usagePrefix+" work-items view --repo group/project --iid 7")
		case "work-items discussions list":
			fmt.Fprintln(a.out, "\nReturns a JSON array of discussions: id, replyId, notes [{id, body, system, author {id, username, name}}].")
			fmt.Fprintln(a.out, "Reads all discussion and note pages, preserving full bodies and null authors. Failure returns no complete result.")
			fmt.Fprintln(a.out, "Preserves native id and replyId; work-items note --discussion-id requires a verified discussion target, never a note ID.")
			fmt.Fprintln(a.out, "Example: "+usagePrefix+" work-items discussions list --repo group/project --iid 7")
		}
	}
	err := f.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return o, true, nil
	}
	if err != nil {
		return o, false, err
	}
	if f.NArg() != 0 {
		return o, false, errors.New("use --iid and --repo; unexpected positional arguments")
	}
	n, err := strconv.ParseInt(o.iid, 10, 64)
	if err != nil || n <= 0 || strings.Trim(o.repo, "/") != o.repo || o.repo == "" || strings.ContainsAny(o.repo, "?#:\\") {
		return o, false, errors.New("valid --repo full path and positive --iid required")
	}
	o.iid = strconv.FormatInt(n, 10)
	if action == "assignees" {
		if o.mode != "add" && o.mode != "remove" && o.mode != "replace" && o.mode != "clear" {
			return o, false, errors.New("--mode must be add, remove, replace or clear")
		}
		if (o.mode == "clear") != (o.users == "") {
			return o, false, errors.New("--users required except for clear; clear takes no users")
		}
		if _, err := userIDs(o.users); err != nil {
			return o, false, err
		}
	}
	if action == "note" || action == "reply" {
		if (o.body == "") == (o.bodyFile == "") {
			return o, false, errors.New("provide exactly one of --message or --file")
		}
		if action == "reply" && o.discussion == "" {
			return o, false, errors.New("--discussion-id required")
		}
		if o.bodyFile != "" {
			var r io.Reader = a.in
			var file *os.File
			if o.bodyFile != "-" {
				file, err = os.Open(o.bodyFile)
				if err != nil {
					return o, false, errors.New("cannot open body file")
				}
				defer file.Close()
				r = file
			}
			b, e := io.ReadAll(io.LimitReader(r, (1<<20)+1))
			if e != nil || len(b) > 1<<20 {
				return o, false, errors.New("cannot read body or body exceeds 1 MiB")
			}
			o.body = string(b)
		}
		if strings.TrimSpace(o.body) == "" {
			return o, false, errors.New("empty comment body")
		}
	}
	return o, false, nil
}

func userIDs(s string) ([]string, error) {
	ids := []string{}
	seen := map[string]bool{}
	if s == "" {
		return ids, nil
	}
	for _, v := range strings.Split(s, ",") {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return nil, errors.New("--users requires positive numeric user IDs")
		}
		id := fmt.Sprintf("gid://gitlab/User/%d", n)
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids, nil
}

func issueCommand(action string) func(context.Context, *app, []string) error {
	return func(ctx context.Context, a *app, args []string) error {
		o, help, err := a.options("issue discussions "+action, action, args)
		if help || err != nil {
			return err
		}
		c, err := a.api()
		if err != nil {
			return err
		}
		path := "/api/v4/projects/" + url.PathEscape(o.repo) + "/issues/" + o.iid + "/discussions"
		if action == "reply" {
			var out any
			_, err = c.request(ctx, "POST", path+"/"+url.PathEscape(o.discussion)+"/notes", map[string]string{"body": o.body}, &out)
			if err != nil {
				return err
			}
			return a.output(out)
		}
		all := []any{}
		page := 1
		for {
			if page > 10000 {
				return errors.New("REST pagination exceeded 10000 pages; no complete result")
			}
			var rows []any
			h, err := c.request(ctx, "GET", fmt.Sprintf("%s?per_page=100&page=%d", path, page), nil, &rows)
			if err != nil {
				return err
			}
			if rows == nil {
				return errors.New("missing discussion list")
			}
			all = append(all, rows...)
			next := h.Get("X-Next-Page")
			if next == "" {
				if _, ok := h["X-Next-Page"]; ok || len(rows) == 0 {
					break
				}
				// With stripped pagination headers, continue until an empty
				// page: instances may clamp per_page below the requested 100.
				page++
				continue
			}
			n, e := strconv.Atoi(next)
			if e != nil || n <= page || n > 10000 {
				return errors.New("invalid REST pagination")
			}
			page = n
		}
		return a.output(all)
	}
}
