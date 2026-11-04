package gitlab

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtureResponse(body string, status int, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body))}
}
func fixtureRead(t *testing.T, body string, headers http.Header) (ReadAPI, *int) {
	t.Helper()
	calls := 0
	read := NewReadAPI("https://gitlab.example.com", platform.NewSecret("offline-secret"), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.Header.Get("PRIVATE-TOKEN") != "offline-secret" {
			t.Fatal("incorrect read scope")
		}
		if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > 30*time.Second {
			t.Error("missing deadline")
		}
		return fixtureResponse(body, 200, headers), nil
	}))
	return read, &calls
}
func TestHTTPBoundsRedirectsErrorsAndCancellation(t *testing.T) {
	for _, status := range []int{301, 302, 307, 401, 403, 429, 500} {
		calls := 0
		closed := false
		read := NewReadAPI("https://gitlab.example.com", platform.NewSecret("secret"), roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://evil.invalid"}, "Retry-After": {"17"}}, Body: closeBody{Reader: strings.NewReader("sensitive-body"), closed: &closed}}, nil
		}))
		_, err := read(context.Background(), "/todos", nil, nil, 100)
		var qe *queryError
		if !errors.As(err, &qe) || qe.Status != status || qe.Retry != 17 || calls != 1 || !closed || strings.Contains(err.Error(), "sensitive") {
			t.Fatal(status, err, calls, closed)
		}
	}
	for _, body := range []string{"bad", `[] {}`, strings.Repeat("x", 101)} {
		read, _ := fixtureRead(t, body, nil)
		if _, err := read(context.Background(), "/todos", nil, nil, 100); err == nil {
			t.Fatal("accepted invalid body")
		}
	}
	calls := 0
	read := NewReadAPI("https://gitlab.example.com", platform.NewSecret("secret"), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, errors.New("secret-transport-error")
	}))
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, err := read(ctx, "/todos", nil, nil, 100); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatal(err, calls)
	}
	if _, err := read(ctx, "/todos", nil, nil, 100); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatal("canceled request ran", err)
	}
	read = NewReadAPI("https://gitlab.example.com", platform.NewSecret("secret"), roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, errors.New("secret-url") }))
	if _, err := read(context.Background(), "/todos", nil, nil, 100); err == nil || strings.Contains(err.Error(), "secret-url") {
		t.Fatal(err)
	}
}

type closeBody struct {
	io.Reader
	closed *bool
}

func (b closeBody) Close() error { *b.closed = true; return nil }
func TestRedactionAndPaginationHints(t *testing.T) {
	read, _ := fixtureRead(t, `{"title":"offline-secret","body":"escaped offline-\u0073ecret"}`, nil)
	response, err := read(context.Background(), "/todos", nil, nil, 1024)
	if err != nil || strings.Contains(string(response.Data), "offline-secret") || !strings.Contains(string(response.Data), "[REDACTED]") || response.Bytes != int64(len(`{"title":"offline-secret","body":"escaped offline-\u0073ecret"}`)) {
		t.Fatal(response, err)
	}
	for _, value := range []string{"1", "10001", "https://evil.invalid", "-2", "２"} {
		if _, err := nextPage(http.Header{"X-Next-Page": {value}}, 1, 20, 1); err == nil {
			t.Fatal(value)
		}
	}
	if n, err := nextPage(nil, 1, 20, 20); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	if _, err := nextPage(nil, 10000, 1, 1); err == nil {
		t.Fatal("overflow page")
	}
	now := time.Now().Truncate(time.Second)
	for _, tc := range []struct {
		h    http.Header
		want int
	}{{http.Header{"Retry-After": {"9999999"}}, 86400}, {http.Header{"Retry-After": {"bad"}}, 0}, {http.Header{"Poll-Interval": {"19"}}, 19}, {http.Header{"Retry-After": {now.Add(10 * time.Second).UTC().Format(http.TimeFormat)}}, 11}} {
		if got := retrySeconds(tc.h, now); got != tc.want {
			t.Fatal(got, tc.want)
		}
	}
	// Invalid origins and credentials cannot trigger a transport.
	for _, origin := range []string{"http://gitlab.example.com", "https://user@gitlab.example.com", "https://gitlab.example.com/", "https://gitlab.example.com?"} {
		read := NewReadAPI(origin, platform.NewSecret("secret"), roundTripFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid origin reached transport")
			return nil, nil
		}))
		if _, err := read(context.Background(), "/todos", url.Values{}, nil, 100); err == nil {
			t.Fatal(origin)
		}
	}
}
