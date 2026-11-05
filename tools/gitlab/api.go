package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type roundTripper = http.RoundTripper
type apiFailure struct {
	message               string
	retryable, notApplied bool
}

func (e *apiFailure) Error() string { return e.message }

type api struct {
	origin, token string
	client        *http.Client
	responseLimit int64
}

func (a *app) api() (*api, error) {
	host := a.getenv("GITLAB_HOST")
	if host == "" {
		return nil, errors.New("GITLAB_HOST is required; no implicit gitlab.com instance")
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}
	u, err := url.Parse(host)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("GITLAB_HOST must be an HTTPS instance origin")
	}
	token := a.getenv("GITLAB_TOKEN")
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, errors.New("GITLAB_TOKEN is required")
	}
	return &api{origin: strings.TrimSuffix(host, "/"), token: token, client: &http.Client{Transport: a.transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *api) request(ctx context.Context, method, path string, body, out any) (http.Header, error) {
	var b []byte
	var err error
	if body != nil {
		b, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, bytes.NewReader(b))
	if err != nil {
		return nil, errors.New("invalid API request")
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		if method != "GET" {
			var unknownCA x509.UnknownAuthorityError
			var invalidCert x509.CertificateInvalidError
			var hostname x509.HostnameError
			retry := ctx.Err() == nil && !errors.As(err, &unknownCA) && !errors.As(err, &invalidCert) && !errors.As(err, &hostname)
			return nil, &apiFailure{"request interrupted or transport failed; remote result unconfirmed; query before any retry", retry, false}
		}
		return nil, errors.New("API transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &apiFailure{fmt.Sprintf("GitLab API HTTP %d (no automatic retry)", resp.StatusCode), resp.StatusCode == 408 || resp.StatusCode == 429 || resp.StatusCode >= 500, resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 408}
	}
	b, err = io.ReadAll(io.LimitReader(resp.Body, c.byteLimit()+1))
	if err != nil {
		return nil, &apiFailure{"API response interrupted; remote result unconfirmed if writing", ctx.Err() == nil, false}
	}
	if err != nil || int64(len(b)) > c.byteLimit() || !json.Valid(b) {
		return nil, errors.New("incomplete or invalid API response; remote result unconfirmed if writing")
	}
	b = bytes.ReplaceAll(b, []byte(c.token), []byte("[REDACTED]"))
	if err = json.Unmarshal(b, out); err != nil {
		return nil, errors.New("unexpected API response")
	}
	return resp.Header, nil
}

// GraphQL HTTP 200 is not sufficient: both error layers and null payloads fail.
func (c *api) graphql(ctx context.Context, query string, vars map[string]any, field string, mutation bool, out any) error {
	var envelope struct {
		Errors []json.RawMessage          `json:"errors"`
		Data   map[string]json.RawMessage `json:"data"`
	}
	if _, err := c.request(ctx, "POST", "/api/graphql", map[string]any{"query": query, "variables": vars}, &envelope); err != nil {
		return err
	}
	if len(envelope.Errors) > 0 {
		return errors.New("GitLab GraphQL errors; operation did not report success")
	}
	p := envelope.Data[field]
	if len(p) == 0 || string(p) == "null" {
		return errors.New("GitLab GraphQL returned null or missing payload")
	}
	if mutation {
		var status struct {
			Errors *[]string `json:"errors"`
		}
		if json.Unmarshal(p, &status) != nil || status.Errors == nil {
			return errors.New("missing GraphQL mutation errors field; result unconfirmed")
		}
		if len(*status.Errors) > 0 {
			return errors.New("GitLab GraphQL mutation errors; operation did not report success")
		}
	}
	if json.Unmarshal(p, out) != nil {
		return errors.New("unexpected GraphQL payload")
	}
	return nil
}

func (a *app) output(v any) error {
	e := json.NewEncoder(a.out)
	e.SetIndent("", "  ")
	return e.Encode(v)
}

type pageInfo struct {
	HasNextPage *bool  `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

func nextCursor(p pageInfo, seen map[string]bool) (string, error) {
	if len(seen) >= 10000 {
		return "", errors.New("pagination exceeded 10000 pages; no complete result")
	}
	if p.HasNextPage == nil {
		return "", errors.New("missing pagination metadata")
	}
	if !*p.HasNextPage {
		return "", nil
	}
	if p.EndCursor == "" || seen[p.EndCursor] {
		return "", errors.New("invalid or repeated pagination cursor")
	}
	seen[p.EndCursor] = true
	return p.EndCursor, nil
}

func (c *api) byteLimit() int64 {
	if c.responseLimit > 0 {
		return c.responseLimit
	}
	return 16 << 20
}
