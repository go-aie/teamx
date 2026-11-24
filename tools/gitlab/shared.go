package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

const sharedHelp = "Shared GitLab tools reuse native glab api ENDPOINT --method METHOD [-f key=value | -F key=value] [--input -] [--paginate], and the existing work-items / issue discussions extensions. Supply explicit project IDs/paths. Repository commands, repository placeholders, host overrides, headers and local-file inputs are unavailable. Use TeamX actions for managed comment/reply receipts. Interrupted writes must be checked before retry."

// The native API command is retained. Its hostname must be supplied by the
// selected connection so glab never infers one by inspecting a repository.
func sharedNativeArguments(args []string, origin string) ([]string, error) {
	if len(args) < 2 || args[0] != "api" {
		return nil, errors.New(sharedHelp)
	}
	endpoint := args[1]
	u, err := url.Parse(endpoint)
	if err != nil || u.IsAbs() || u.Host != "" || strings.HasPrefix(endpoint, "/") || u.Fragment != "" || strings.ContainsAny(endpoint, ":\\\r\n") || endpoint == "" {
		return nil, errors.New("use an explicit relative API endpoint without repository placeholders")
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return nil, errors.New("invalid API endpoint")
		}
	}
	for i := 2; i < len(args); i++ {
		flag, value, inline := strings.Cut(args[i], "=")
		switch flag {
		case "--paginate", "--silent", "--include":
			if inline {
				return nil, errors.New(sharedHelp)
			}
			continue
		case "--method", "-X", "--raw-field", "-f", "--field", "-F", "--input", "--output":
		default:
			return nil, errors.New(sharedHelp)
		}
		if !inline {
			i++
			if i == len(args) {
				return nil, errors.New(sharedHelp)
			}
			value = args[i]
		}
		switch flag {
		case "--input":
			if value != "-" {
				return nil, errors.New("use --input -; server file input is unavailable")
			}
		case "--field", "-F", "--raw-field", "-f":
			_, field, ok := strings.Cut(value, "=")
			if !ok || strings.HasPrefix(field, "@") || (flag == "--field" || flag == "-F") && strings.Contains(field, ":") {
				return nil, errors.New("file and repository placeholder fields are unavailable; use raw fields or JSON stdin")
			}
		case "--method", "-X":
			if !strings.Contains(" GET HEAD POST PUT PATCH DELETE ", " "+value+" ") {
				return nil, errors.New(sharedHelp)
			}
		case "--output":
			if value != "json" && value != "ndjson" {
				return nil, errors.New(sharedHelp)
			}
		}
	}
	host, err := url.Parse(origin)
	if err != nil || host.Scheme != "https" || host.Host == "" || host.User != nil || host.RawQuery != "" || host.Fragment != "" || host.Path != "" && host.Path != "/" {
		return nil, errors.New("selected GitLab HTTPS origin is required")
	}
	return append(append([]string{}, args...), "--hostname", host.Hostname()), nil
}

func sharedNativeConfig(origin string) ([]byte, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return nil, errors.New("invalid selected GitLab origin")
	}
	// glab's --hostname forbids a port. Its private host configuration keeps
	// the selected API authority (including port), without repository discovery.
	return json.Marshal(map[string]any{"hosts": map[string]any{u.Hostname(): map[string]string{"api_host": u.Host, "api_protocol": "https"}}})
}
