package dsh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRPCErrorSafeProviderHints(t *testing.T) {
	const secret = "private-provider-detail"
	for _, tc := range []struct {
		name    string
		message string
		data    json.RawMessage
		want    error
	}{
		{"authentication error code", "authentication_error " + secret, nil, ErrAuthentication},
		{"invalid API key", "Internal error", json.RawMessage(`{"code":"invalid_api_key","message":"private-provider-detail"}`), ErrAuthentication},
		{"unrelated 401", "request 401 failed " + secret, nil, nil},
		{"unrelated status digits", "1401 status code " + secret, nil, nil},
		{"model error in message", "404 model_not_found " + secret, nil, ErrModelNotFound},
		{"model error in string data", "Internal error", json.RawMessage(`"turn failed: 404 model_not_found private-provider-detail"`), ErrModelNotFound},
		{"model error in object data", "Internal error", json.RawMessage(`{"code":"model_not_found","message":"private-provider-detail"}`), ErrModelNotFound},
		{"unavailable model in message", "503 model_unavailable " + secret, nil, ErrModelUnavailable},
		{"unavailable model in string data", "Internal error", json.RawMessage(`"turn failed: 503: {\"code\":\"model_unavailable\",\"message\":\"private-provider-detail\",\"type\":\"server_error\"}"`), ErrModelUnavailable},
		{"unavailable model in object data", "Internal error", json.RawMessage(`{"code":"model_unavailable","message":"private-provider-detail"}`), ErrModelUnavailable},
		{"semantic hint before HTTP fallback", "HTTP 502 rate_limit_error " + secret, nil, ErrRateLimited},
		{"unlabeled 503", "503 server_error " + secret, nil, nil},
		{"rate limit", "rate_limit_error " + secret, nil, ErrRateLimited},
		{"token rate limit", "tpm_exceeded " + secret, nil, ErrRateLimited},
		{"unrelated 404", "404 resource not found " + secret, nil, nil},
		{"unknown error", secret, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := &rpcError{Code: -32603, Message: tc.message, Data: tc.data}
			want := "ACP request failed (code -32603)"
			if tc.want != nil {
				want += ": " + tc.want.Error()
				if !errors.Is(err, tc.want) {
					t.Fatal("provider classification missing", err)
				}
			} else if err.Unwrap() != nil {
				t.Fatal("unknown error classified", err)
			}
			if err.Error() != want || strings.Contains(err.Error(), secret) {
				t.Fatal("unsafe or missing public diagnostic", err)
			}
		})
	}
}

func TestRPCErrorHTTPStatusFallback(t *testing.T) {
	const secret = "private-provider-detail"
	for _, tc := range []struct {
		name    string
		message string
		data    json.RawMessage
		status  int
	}{
		{"SDK message", "turn failed: 401 status code (no body) " + secret, nil, 401},
		{"SDK string data", "Internal error", json.RawMessage(`"turn failed: 401 status code (no body) private-provider-detail"`), 401},
		{"SDK object data", "Internal error", json.RawMessage(`{"message":"401 status code (no body) private-provider-detail","code":"AUTH"}`), 401},
		{"HTTP status", "HTTP 502 " + secret, nil, 502},
		{"HTTP version", "HTTP/1.1 504 " + secret, nil, 504},
		{"lowercase HTTP", "http/2 403 " + secret, nil, 403},
		{"status label", "status: 400 " + secret, nil, 400},
		{"status code label", "status code = 408 " + secret, nil, 408},
		{"structured status", "Internal error", json.RawMessage(`{"status":500,"message":"private-provider-detail"}`), 500},
		{"structured camel case status", "Internal error", json.RawMessage(`{"statusCode":418,"message":"private-provider-detail"}`), 418},
		{"structured string status", "Internal error", json.RawMessage(`{"status_code":"599","message":"private-provider-detail"}`), 599},
		{"provider message", "503: " + secret, nil, 503},
		{"provider string data", "Internal error", json.RawMessage(`"turn failed: 502: private-provider-detail"`), 502},
		{"provider object data", "Internal error", json.RawMessage(`{"message":"503: private-provider-detail"}`), 503},
		{"escaped structured status", "Internal error", json.RawMessage(`"turn failed: {\"status\":429,\"message\":\"private-provider-detail\"}"`), 429},
		{"bare number", "request 502 failed " + secret, nil, 0},
		{"long number", "HTTP 5021 " + secret, nil, 0},
		{"nonerror status", "HTTP 200 " + secret, nil, 0},
		{"invalid status", "HTTP 600 " + secret, nil, 0},
		{"ambiguous code field", "Internal error", json.RawMessage(`{"code":401,"message":"private-provider-detail"}`), 0},
		{"ACP code only", secret, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := &rpcError{Code: -32603, Message: tc.message, Data: tc.data}
			want := "ACP request failed (code -32603)"
			var status *HTTPStatusError
			if tc.status != 0 {
				if !errors.As(err, &status) || status.StatusCode != tc.status {
					t.Fatal("missing HTTP status", err)
				}
				want += fmt.Sprintf(": model request failed (HTTP %d)", tc.status)
			} else if err.Unwrap() != nil {
				t.Fatal("unrelated number classified as HTTP status", err)
			}
			if err.Error() != want || strings.Contains(err.Error(), secret) {
				t.Fatal("unsafe or missing public diagnostic", err)
			}
		})
	}
}

func pipeConnection(t *testing.T) (*connection, *os.File, *os.File) {
	t.Helper()
	peerRead, in, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out, peerWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	limits := Limits{}
	if err := limits.defaults(); err != nil {
		t.Fatal(err)
	}
	c := newConnection(in, out, limits)
	c.onNotification = func(string, json.RawMessage) error { return nil }
	c.onRequest = func(frame) error { return nil }
	go c.read()
	t.Cleanup(func() { c.fail(ErrUnavailable); _ = peerRead.Close(); _ = peerWrite.Close(); <-c.readDone })
	return c, peerRead, peerWrite
}

func TestProtocolEnvelopeAndPendingBounds(t *testing.T) {
	for _, input := range []string{
		`{"jsonrpc":"2.0","id":999,"result":{}}`,
		`{"jsonrpc":"2.0","id":null,"result":{}}`,
		`{"jsonrpc":"2.0","id":true,"result":{}}`,
		`{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1,"message":"x"}}`,
		`{"jsonrpc":"1.0","id":1,"result":{}}`,
		`[]`,
	} {
		t.Run(input, func(t *testing.T) {
			c, _, peer := pipeConnection(t)
			_, _ = peer.WriteString(input + "\n")
			select {
			case <-c.done:
			case <-time.After(time.Second):
				t.Fatal("invalid protocol accepted")
			}
			if !errors.Is(c.health(), ErrProtocol) {
				t.Fatal(c.health())
			}
		})
	}
	c, _, _ := pipeConnection(t)
	for range 4 {
		if _, err := c.begin(context.Background(), "pending", map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.begin(context.Background(), "overflow", nil); err == nil {
		t.Fatal("pending requests unbounded")
	}
}

func TestUnsentWriteCancellationLeavesConnectionUsable(t *testing.T) {
	c, _, _ := pipeConnection(t)
	<-c.writeSlot
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.begin(ctx, "not-sent", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	c.writeSlot <- struct{}{}
	if c.health() != nil || len(c.pending) != 0 {
		t.Fatal("unsent request damaged connection or leaked pending")
	}
	if _, err := c.begin(context.Background(), "sent", nil); err != nil {
		t.Fatal(err)
	}
}

func TestProcessIdentityParsingAndReparenting(t *testing.T) {
	table, err := parseProcessTable(" 10 1 10 Sat Oct 31 14:00:01 2026\n 11 10 11 Sat Oct 31 14:00:02 2026\n")
	if err != nil || table[11].parent != 10 || table[11].group != 11 || table[10].started != "Sat Oct 31 14:00:01 2026" {
		t.Fatalf("identity: %+v %v", table, err)
	}
	if _, err := parseProcessTable("10 1 missing"); err == nil {
		t.Fatal("malformed process identities accepted")
	}
	opts := fixtureOptions(t)
	s := openFixture(t, opts)
	pid := s.process.cmd.Process.Pid
	if _, err := s.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	live, err := s.process.snapshot()
	if err != nil || live[pid].pid != pid {
		t.Fatalf("root not observed: %+v %v", live, err)
	}
	// A remembered detached descendant stays owned after reparenting, while
	// a reused PID with a different start identity must never be signalled.
	p := &process{cmd: s.process.cmd, owned: map[int]processIdentity{12: {pid: 12, started: "old"}}, inspect: func() (map[int]processIdentity, error) {
		return map[int]processIdentity{12: {pid: 12, parent: 1, group: 12, started: "new"}}, nil
	}}
	if live, err := p.snapshot(); err != nil || len(live) != 0 {
		t.Fatalf("PID reuse: %+v %v", live, err)
	}
	p.owned[12] = processIdentity{pid: 12, started: "new"}
	if live, err := p.snapshot(); err != nil || len(live) != 1 {
		t.Fatalf("reparented descendant lost: %+v %v", live, err)
	}
}

func TestObserverFailureBeforeAndAfterStart(t *testing.T) {
	opts := fixtureOptions(t)
	opts.inspect = func() (map[int]processIdentity, error) { return nil, errors.New("observation denied") }
	if _, err := Open(context.Background(), opts); err == nil {
		t.Fatal("started without ownership observation")
	}
	if _, err := os.Stat(opts.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed preflight created state")
	}

	opts = fixtureOptions(t)
	observer := opts.inspect
	var denied atomic.Bool
	opts.inspect = func() (map[int]processIdentity, error) {
		if denied.Load() {
			return nil, errors.New("observation lost")
		}
		return observer()
	}
	s := openFixture(t, opts)
	if _, err := s.Run(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	denied.Store(true)
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.disabled != nil })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := s.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost observation reported cleanup: %v", err)
	}
	if _, err := lockDirectory(opts.StateDir); !errors.Is(err, ErrLocked) {
		t.Fatalf("lost observation unlocked state: %v", err)
	}
	denied.Store(false)
	if err := s.Close(context.Background()); !errors.Is(err, ErrRecoveryUnsupported) || errors.Is(err, ErrCleanupPending) {
		t.Fatal("lost observation failure after cleanup", err)
	}
}

func TestCredentialNeverMaterializedOrReturned(t *testing.T) {
	opts := fixtureOptions(t)
	s := openFixture(t, opts)
	result, err := s.Run(context.Background(), "secret-fixture-key")
	if err != nil || strings.Contains(result.Text, "secret-fixture-key") {
		t.Fatalf("redaction: %+v %v", result, err)
	}
	if err := filepath.WalkDir(opts.StateDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "secret-fixture-key") {
			t.Fatalf("credential materialized in %s", entry.Name())
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
