package dsh

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Match explicit HTTP/status labels, SDK status-code messages, and provider
// errors of the form "503: ...". Bare numbers are not HTTP diagnostics.
var httpStatusPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\b(?:http(?:/\d(?:\.\d)?)?|status(?:[ _]?code)?)"?\s*[:=]?\s*"?([45]\d{2})\b`),
	regexp.MustCompile(`\b([45]\d{2})\s+status code\b`),
	regexp.MustCompile(`(?m)(?:^|[:"]\s*)([45]\d{2}):(?:\s|$)`),
}

// Remote messages/data can contain credentials or tool content. Public errors
// retain numeric codes and fixed provider hints only; private model output
// is returned separately.
func (e *rpcError) Error() string {
	message := fmt.Sprintf("ACP request failed (code %d)", e.Code)
	if hint := e.Unwrap(); hint != nil {
		message += ": " + hint.Error()
	}
	return message
}

func (e *rpcError) Unwrap() error {
	// Return a fixed diagnostic, never the remote message or response data.
	var data string
	if json.Unmarshal(e.Data, &data) != nil {
		data = string(e.Data)
	}
	detail := strings.ToLower(e.Message + "\n" + data)
	if strings.Contains(detail, "authentication_error") || strings.Contains(detail, "invalid_api_key") {
		return ErrAuthentication
	}
	if strings.Contains(detail, "tpm_exceeded") || strings.Contains(detail, "rate_limit_error") {
		return ErrRateLimited
	}
	if strings.Contains(detail, "model_not_found") {
		return ErrModelNotFound
	}
	if strings.Contains(detail, "model_unavailable") {
		return ErrModelUnavailable
	}
	for _, pattern := range httpStatusPatterns {
		if match := pattern.FindStringSubmatch(detail); match != nil {
			status, _ := strconv.Atoi(match[1]) // Pattern guarantees three digits.
			return &HTTPStatusError{StatusCode: status}
		}
	}
	return nil
}

type response struct {
	result json.RawMessage
	err    error
}
type frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type connection struct {
	in, out        *os.File
	limits         Limits
	mu             sync.Mutex
	sequence       uint64
	pending        map[string]chan response
	failed         error
	done           chan struct{}
	readDone       chan struct{}
	writeSlot      chan struct{}
	onNotification func(string, json.RawMessage) error
	onRequest      func(frame) error
	onFailure      func(error)
}

func newConnection(in, out *os.File, limits Limits) *connection {
	c := &connection{in: in, out: out, limits: limits, pending: map[string]chan response{}, done: make(chan struct{}), readDone: make(chan struct{}), writeSlot: make(chan struct{}, 1)}
	c.writeSlot <- struct{}{}
	return c
}

func (c *connection) fail(err error) {
	c.mu.Lock()
	if c.failed != nil {
		c.mu.Unlock()
		return
	}
	c.failed = err
	close(c.done)
	for id, pending := range c.pending {
		pending <- response{err: err}
		delete(c.pending, id)
	}
	c.mu.Unlock()
	_ = c.in.Close()
	_ = c.out.Close()
	if c.onFailure != nil {
		c.onFailure(err)
	}
}

func (c *connection) health() error { c.mu.Lock(); defer c.mu.Unlock(); return c.failed }

func (c *connection) write(ctx context.Context, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > c.limits.FrameBytes {
		return fmt.Errorf("%w: outgoing frame exceeds limit", ErrProtocol)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.health()
	case <-c.writeSlot:
	}
	defer func() { c.writeSlot <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.health(); err != nil {
		return err
	}
	deadline := time.Now().Add(c.limits.WriteTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.in.SetWriteDeadline(deadline); err != nil {
		c.fail(err)
		return err
	}
	data = append(data, '\n')
	// Once Write starts, any error (including a partial write) is uncertain.
	n, err := c.in.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		c.fail(errors.New("ACP transport write failed"))
		return c.health()
	}
	return nil
}

func (c *connection) begin(ctx context.Context, method string, params any) (<-chan response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.failed != nil {
		err := c.failed
		c.mu.Unlock()
		return nil, err
	}
	if len(c.pending) >= 4 {
		c.mu.Unlock()
		return nil, errors.New("ACP pending request limit")
	}
	c.sequence++
	id := strconv.FormatUint(c.sequence, 10)
	pending := make(chan response, 1)
	c.pending[id] = pending
	c.mu.Unlock()
	if err := c.write(ctx, frame{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: encoded}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	return pending, nil
}

func (c *connection) call(ctx context.Context, method string, params any, result any) error {
	pending, err := c.begin(ctx, method, params)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case reply := <-pending:
		if reply.err != nil {
			return reply.err
		}
		if err := json.Unmarshal(reply.result, result); err != nil {
			return fmt.Errorf("%w: malformed %s result", ErrProtocol, method)
		}
		return nil
	}
}

func (c *connection) notify(ctx context.Context, method string, params any) error {
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(ctx, frame{JSONRPC: "2.0", Method: method, Params: encoded})
}

func validID(id json.RawMessage) bool {
	if len(id) == 0 {
		return false
	}
	if id[0] == '"' {
		var s string
		return json.Unmarshal(id, &s) == nil
	}
	var n json.Number
	return json.Unmarshal(id, &n) == nil && string(n) != ""
}

func (c *connection) read() {
	defer close(c.readDone)
	scanner := bufio.NewScanner(c.out)
	scanner.Buffer(make([]byte, 4096), c.limits.FrameBytes+2)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) > c.limits.FrameBytes {
			c.fail(fmt.Errorf("%w: incoming frame exceeds limit", ErrProtocol))
			return
		}
		var f frame
		if json.Unmarshal(line, &f) != nil || f.JSONRPC != "2.0" || (len(f.ID) != 0 && !validID(f.ID)) {
			c.fail(fmt.Errorf("%w: malformed envelope", ErrProtocol))
			return
		}
		var err error
		if f.Method != "" {
			if f.Result != nil || f.Error != nil {
				err = ErrProtocol
			} else if f.ID != nil {
				err = c.onRequest(f)
			} else {
				err = c.onNotification(f.Method, f.Params)
			}
		} else {
			if f.ID == nil || (f.Result == nil) == (f.Error == nil) || bytes.Equal(f.Result, []byte("null")) {
				err = ErrProtocol
			} else {
				c.mu.Lock()
				pending, ok := c.pending[string(f.ID)]
				if ok {
					delete(c.pending, string(f.ID))
					reply := response{result: f.Result}
					if f.Error != nil {
						reply.err = f.Error
					}
					pending <- reply
				}
				c.mu.Unlock()
				if !ok {
					err = fmt.Errorf("%w: unknown response ID", ErrProtocol)
				}
			}
		}
		if err != nil {
			c.fail(err)
			return
		}
	}
	if scanner.Err() != nil {
		c.fail(fmt.Errorf("%w: unreadable or oversized frame", ErrProtocol))
	} else {
		c.fail(errors.New("ACP transport EOF"))
	}
}
