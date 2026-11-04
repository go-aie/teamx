package dsh

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

func (s *Session) notification(method string, params json.RawMessage) error {
	if method != "session/update" {
		return nil
	}
	var event struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind       string          `json:"sessionUpdate"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"toolCallId"`
			Status     string          `json:"status"`
			Title      string          `json:"title"`
			RawInput   json.RawMessage `json:"rawInput"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &event) != nil || event.SessionID == "" || event.Update.Kind == "" {
		return fmt.Errorf("%w: invalid session update", ErrProtocol)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.id != "" && s.id != event.SessionID {
		return fmt.Errorf("%w: wrong update session", ErrProtocol)
	}
	t := s.active
	if t == nil || t.settling {
		return nil
	}
	switch event.Update.Kind {
	case "agent_message_chunk", "agent_thought_chunk":
		// ACP content is a union: message chunks carry a ContentBlock object,
		// while tool updates carry a ToolCallContent array. Decode only the
		// message variant here; bounded tool text is decoded separately below.
		var content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(event.Update.Content, &content) != nil || content.Type == "" {
			return fmt.Errorf("%w: invalid message content", ErrProtocol)
		}
		if content.Type != "text" {
			return nil
		}
		text, truncated := &t.result.Text, &t.result.TextTruncated
		if event.Update.Kind == "agent_thought_chunk" {
			text, truncated = &t.result.Thought, &t.result.ThoughtTruncated
		}
		appendDiagnostic(text, content.Text, s.opts.Limits.TextBytes, truncated)
		if content.Text != "" {
			kind := "text"
			if event.Update.Kind == "agent_thought_chunk" {
				kind = "thought"
			}
			t.appendActivity(ActivityEvent{Kind: kind, Text: content.Text}, s.opts.Limits.TextBytes)
		}
	case "tool_call", "tool_call_update":
		id, status := event.Update.ToolCallID, event.Update.Status
		if len(id) > 256 {
			id = id[:256]
		}
		if len(status) > 64 {
			status = status[:64]
		}
		tool := ToolUpdate{ID: id, Status: status}
		for i := len(t.result.Tools) - 1; i >= 0; i-- {
			if t.result.Tools[i].ID == id {
				tool = t.result.Tools[i]
				if status != "" {
					tool.Status = status
				}
				break
			}
		}
		if event.Update.Title != "" {
			tool.Title = ""
		}
		appendDiagnostic(&tool.Title, event.Update.Title, 256, &tool.DetailsTruncated)
		if len(event.Update.RawInput) != 0 && string(event.Update.RawInput) != "null" {
			tool.Input = ""
			appendDiagnostic(&tool.Input, string(event.Update.RawInput), 2048, &tool.DetailsTruncated)
		}
		var contents []struct {
			Type    string `json:"type"`
			Content struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if json.Unmarshal(event.Update.Content, &contents) == nil {
			tool.Output = ""
			for _, block := range contents {
				if block.Type == "content" && block.Content.Type == "text" {
					if tool.Output != "" {
						appendDiagnostic(&tool.Output, "\n", 4096, &tool.DetailsTruncated)
					}
					appendDiagnostic(&tool.Output, block.Content.Text, 4096, &tool.DetailsTruncated)
				}
			}
		}
		// Retain the latest events so terminal updates still arrive after the
		// limit. The encoded budget also bounds escaping in the saved file.
		t.result.Tools = append(t.result.Tools, tool)
		boundTools(&t.result)
		t.appendActivity(ActivityEvent{Kind: "tool", Tool: &tool}, s.opts.Limits.TextBytes)
	}
	return nil
}

const activityTextBytes = 16 << 10

func (t *turn) appendActivity(event ActivityEvent, textLimit int) {
	limit := min(textLimit, activityTextBytes)
	if n := len(t.result.Events); n > 0 {
		last := &t.result.Events[n-1]
		if last.Kind == event.Kind {
			if event.Tool == nil {
				appendDiagnostic(&last.Text, event.Text, limit, &last.TextTruncated)
				boundActivity(&t.result)
				return
			}
			if last.Tool != nil && last.Tool.ID == event.Tool.ID {
				last.Tool = event.Tool
				boundActivity(&t.result)
				return
			}
		}
	}
	t.eventSeq++
	event.Seq = t.eventSeq
	text := event.Text
	event.Text = ""
	appendDiagnostic(&event.Text, text, limit, &event.TextTruncated)
	t.result.Events = append(t.result.Events, event)
	boundActivity(&t.result)
}

func boundActivity(result *TurnResult) {
	for len(result.Events) > 0 {
		encoded, _ := json.Marshal(result.Events)
		if len(result.Events) <= 256 && len(encoded) <= 256<<10 {
			break
		}
		result.Events = result.Events[1:]
		result.EventsTruncated = true
	}
}

// The ordered flow and result summaries duplicate some text. Bound their
// combined encoded size as well, so escaping cannot overflow Room's 1 MiB
// diagnostic file limit. Keep the ordered flow and shorten summaries first.
func boundActivitySnapshot(result *TurnResult) {
	if result.Events == nil {
		return
	}
	for {
		encoded, _ := json.Marshal(result)
		if len(encoded) <= 768<<10 {
			return
		}
		text, truncated := &result.Thought, &result.ThoughtTruncated
		if len(result.Text) > len(result.Thought) {
			text, truncated = &result.Text, &result.TextTruncated
		}
		original := *text
		*text = ""
		appendDiagnostic(text, original, len(original)/2, truncated)
	}
}

func appendDiagnostic(dst *string, text string, limit int, truncated *bool) {
	remaining := max(0, limit-len(*dst))
	if len(text) > remaining {
		*truncated = true
		text = text[:remaining]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	*dst += text
}

func boundTools(result *TurnResult) {
	for len(result.Tools) > 0 {
		encoded, _ := json.Marshal(result.Tools)
		if len(result.Tools) <= 64 && len(encoded) <= 128<<10 {
			break
		}
		result.Tools = result.Tools[1:]
		result.ToolsTruncated = true
	}
}

// The reader only validates and admits bounded work. Neither policy evaluation
// nor reverse-response writes run on the reader/cancellation path.
func (s *Session) request(f frame) error {
	var permission PermissionRequest
	if f.Method == "session/request_permission" {
		if json.Unmarshal(f.Params, &permission) != nil || permission.SessionID == "" || len(permission.Options) == 0 || len(permission.Options) > 32 {
			return fmt.Errorf("%w: invalid permission request", ErrProtocol)
		}
		ids := map[string]bool{}
		for _, option := range permission.Options {
			if option.OptionID == "" || len(option.OptionID) > 256 || ids[option.OptionID] {
				return fmt.Errorf("%w: invalid permission options", ErrProtocol)
			}
			ids[option.OptionID] = true
		}
	}
	s.mu.Lock()
	if s.reverseIDs[string(f.ID)] {
		s.mu.Unlock()
		return fmt.Errorf("%w: duplicate reverse request ID", ErrProtocol)
	}
	if permission.SessionID != "" && s.id != "" && permission.SessionID != s.id {
		s.mu.Unlock()
		return fmt.Errorf("%w: wrong permission session", ErrProtocol)
	}
	select {
	case s.reverseSlots <- struct{}{}:
	default:
		s.mu.Unlock()
		return fmt.Errorf("%w: reverse request limit", ErrProtocol)
	}
	s.reverseIDs[string(f.ID)] = true
	var t *turn
	if s.active != nil && !s.active.settling && !s.closing {
		t = s.active
		t.permissions.Add(1)
	}
	s.workers.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.workers.Done()
		if t != nil {
			defer t.permissions.Done()
		}
		defer func() { s.mu.Lock(); delete(s.reverseIDs, string(f.ID)); s.mu.Unlock(); <-s.reverseSlots }()
		reply := frame{JSONRPC: "2.0", ID: f.ID}
		if f.Method != "session/request_permission" {
			reply.Error = &rpcError{Code: -32601, Message: "Method not found"}
		} else {
			outcome := map[string]string{"outcome": "cancelled"}
			if t != nil && s.opts.Permission != nil {
				ctx, cancel := context.WithTimeout(t.ctx, s.opts.Limits.PermissionTimeout)
				defer cancel()
				type decision struct {
					id  string
					err error
				}
				decided := make(chan decision, 1)
				handlerDone := make(chan struct{})
				go func() {
					defer close(handlerDone)
					request := permission
					request.Options = slices.Clone(permission.Options)
					id, err := s.opts.Permission(ctx, request)
					decided <- decision{id, err}
				}()
				// Even an uncooperative callback cannot block the response. It
				// does retain the worker/slot/lock until it actually returns.
				defer func() { <-handlerDone }()
				select {
				case d := <-decided:
					if d.err == nil && ctx.Err() == nil {
						for _, option := range permission.Options {
							if option.OptionID == d.id {
								outcome = map[string]string{"outcome": "selected", "optionId": d.id}
								break
							}
						}
					}
				case <-ctx.Done():
				}
			}
			if t != nil && t.ctx.Err() != nil {
				outcome = map[string]string{"outcome": "cancelled"}
			}
			reply.Result, _ = json.Marshal(map[string]any{"outcome": outcome})
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.opts.Limits.WriteTimeout)
		defer cancel()
		if err := s.conn.write(ctx, reply); err != nil {
			s.conn.fail(err)
		}
	}()
	return nil
}

// Diagnostics returns a private, bounded stderr snapshot with the configured
// credential redacted. It is optional and never gates terminal settlement.
func (s *Session) Diagnostics() (text string, truncated bool) {
	if s.process == nil {
		return "", false
	}
	s.process.mu.Lock()
	defer s.process.mu.Unlock()
	text = s.process.stderrText.String()
	return s.redactBounded(text, s.process.stderrTruncated), s.process.stderrTruncated
}

// Kept separate so diagnostics do not expose a key copied into backend output.
func (s *Session) redact(text string) string {
	return s.redactBounded(text, false)
}

func (s *Session) redactBounded(text string, truncated bool) string {
	if s.secret == "" {
		return text
	}
	text = strings.ReplaceAll(text, s.secret, "[REDACTED]")
	if !truncated {
		return text
	}
	// A retention boundary can cut a credential in half.
	for n := min(len(s.secret)-1, len(text)); n > 0; n-- {
		if strings.HasSuffix(text, s.secret[:n]) {
			return text[:len(text)-n] + "[REDACTED]"
		}
	}
	return text
}

func (s *Session) redactDiagnostic(text string, truncated bool) string {
	text = s.redactBounded(text, truncated)
	if s.secret == "" || truncated {
		return text
	}
	// Do not turn ordinary word endings into redaction markers just because
	// they share a single character with the credential.
	for n := min(len(s.secret)-1, len(text)); n >= 4; n-- {
		if strings.HasSuffix(text, s.secret[:n]) {
			return text[:len(text)-n] + "[REDACTED]"
		}
	}
	return text
}
