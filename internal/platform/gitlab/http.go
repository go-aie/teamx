package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// ReadAPI is the built-in adapter's bounded GET boundary. It is independent of
// agent tools. Room wraps it to recheck the admitted credential before and after
// the complete response, including body consumption.
type ReadAPI func(context.Context, string, url.Values, http.Header, int64) (APIResponse, error)
type APIResponse struct {
	Data   []byte
	Header http.Header
	Status int
	Bytes  int64
}
type queryError struct {
	Code          string
	Status, Retry int
}

func (e *queryError) Error() string { return "GitLab query failed (" + e.Code + ")" }

func validOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && !strings.ContainsAny(origin, "# \t\r\n") && len(origin) <= 4096
}
func validToken(token string) bool {
	if token == "" || len(token) > 8192 {
		return false
	}
	for _, c := range token {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

// NewReadAPI never redirects or retries. Raw transport errors and remote error
// bodies never leave this boundary. Byte accounting is on the original body.
func NewReadAPI(origin string, secret platform.Secret, transport http.RoundTripper) ReadAPI {
	if transport == nil {
		transport = http.DefaultTransport
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return func(ctx context.Context, path string, query url.Values, headers http.Header, limit int64) (APIResponse, error) {
		var result APIResponse
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !validOrigin(origin) || !validToken(secret.Reveal()) || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#\r\n") || limit < 1 || limit > maxEventOutput {
			return result, &queryError{Code: "query_failed"}
		}
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		target := origin + "/api/v4" + path
		if len(query) > 0 {
			target += "?" + query.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return result, &queryError{Code: "query_failed"}
		}
		req.Header = headers.Clone()
		if req.Header == nil {
			req.Header = make(http.Header)
		}
		req.Header.Set("PRIVATE-TOKEN", secret.Reveal())
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			return result, &queryError{Code: "query_failed"}
		}
		defer resp.Body.Close()
		result.Header, result.Status = resp.Header.Clone(), resp.StatusCode
		for key, values := range result.Header {
			for i, value := range values {
				result.Header[key][i] = strings.ReplaceAll(value, secret.Reveal(), "[REDACTED]")
			}
		}

		if resp.StatusCode == http.StatusNotModified {
			return result, nil
		}
		if resp.StatusCode != http.StatusOK {
			return APIResponse{}, &queryError{Code: "query_failed", Status: resp.StatusCode, Retry: retrySeconds(resp.Header, time.Now())}
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if ctx.Err() != nil {
			return APIResponse{}, ctx.Err()
		}
		if err != nil || int64(len(raw)) > limit {
			return APIResponse{}, &queryError{Code: "query_failed"}
		}
		result.Bytes = int64(len(raw))
		// Redact JSON string values after parsing: this also catches escaped tokens
		// without damaging JSON syntax when a credential contains quotes/backslashes.
		var value any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&value) != nil || dec.Decode(new(any)) != io.EOF {
			return APIResponse{}, &queryError{Code: "invalid_response"}
		}
		redactJSON(value, secret.Reveal())
		result.Data, err = json.Marshal(value)
		if err != nil || len(result.Data) > maxEventOutput {
			return APIResponse{}, &queryError{Code: "invalid_response"}
		}
		return result, nil
	}
}
func redactJSON(v any, token string) {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if s, ok := v.(string); ok {
				x[k] = strings.ReplaceAll(s, token, "[REDACTED]")
			} else {
				redactJSON(v, token)
			}
		}
	case []any:
		for i, v := range x {
			if s, ok := v.(string); ok {
				x[i] = strings.ReplaceAll(s, token, "[REDACTED]")
			} else {
				redactJSON(v, token)
			}
		}
	}
}
func retrySeconds(h http.Header, now time.Time) int {
	n, err := strconv.Atoi(h.Get("Retry-After"))
	if err != nil {
		if date, e := http.ParseTime(h.Get("Retry-After")); e == nil {
			n = int(date.Sub(now).Seconds()) + 1
		} else {
			n = 0
		}
	}
	poll, _ := strconv.Atoi(h.Get("Poll-Interval"))
	return min(86400, max(0, n, poll))
}
func nextPage(h http.Header, page, perPage, count int) (int, error) {
	values, present := h["X-Next-Page"]
	next := ""
	if len(values) > 0 {
		next = values[0]
	}
	if !present && count == perPage {
		next = strconv.Itoa(page + 1)
	}
	if next == "" {
		return 0, nil
	}
	for _, c := range next {
		if c < '0' || c > '9' {
			return 0, errors.New("invalid GitLab pagination")
		}
	}
	n, err := strconv.Atoi(next)
	if err != nil || n <= page || n > 10000 {
		return 0, errors.New("invalid GitLab pagination")
	}
	return n, nil
}
func decodeJSON(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(out); err != nil {
		return errors.New("invalid GitLab response")
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("invalid GitLab response")
	}
	return nil
}
