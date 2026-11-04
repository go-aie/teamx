package dsh

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSessionUpdateContentVariants(t *testing.T) {
	s := &Session{id: "test", opts: Options{Limits: Limits{TextBytes: 1024}}, active: &turn{}}
	// The fixed DSH emits an array for completed tool output, followed by
	// ordinary text. Both must be consumed without ending the prompt early.
	for _, update := range []string{
		`{"sessionUpdate":"tool_call","toolCallId":"shell","status":"in_progress"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"shell","status":"completed","content":[{"type":"content","content":{"type":"text","text":"private tool output"}}]}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"answer"}}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"image","data":"opaque","mimeType":"image/png"}}`,
	} {
		params := json.RawMessage(`{"sessionId":"test","update":` + update + `}`)
		if err := s.notification("session/update", params); err != nil {
			t.Fatal(err)
		}
	}
	result := s.active.result
	if result.Text != "answer" || len(result.Tools) != 2 || result.Tools[1] != (ToolUpdate{ID: "shell", Status: "completed", Output: "private tool output"}) {
		t.Fatal(result)
	}
}

func TestActivitySnapshotsBoundedRedactedAndIndependent(t *testing.T) {
	s := &Session{id: "test", secret: "secret-key", opts: Options{Limits: Limits{TextBytes: 64}}, active: &turn{}}
	emit := func(update string) {
		t.Helper()
		if err := s.notification("session/update", json.RawMessage(`{"sessionId":"test","update":`+update+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	emit(`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"checking secret-"}}`)
	emit(`{"sessionUpdate":"tool_call","toolCallId":"shell","title":"shell","status":"in_progress","rawInput":{"command":"echo secret-key"}}`)
	first, active := s.Activity()
	if !active || strings.Contains(first.Thought, "secret-") || !strings.Contains(first.Thought, "[REDACTED]") || strings.Contains(first.Tools[0].Input, "secret-key") {
		t.Fatal(first)
	}
	first.Tools[0].Status = "tampered"
	if strings.Contains(first.Events[0].Text, "secret-") || strings.Contains(first.Events[1].Tool.Input, "secret-key") {
		t.Fatal("activity event credentials were not redacted", first.Events)
	}
	first.Events[0].Text = "tampered"
	first.Events[1].Tool.Status = "tampered"
	emit(`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"key"}}`)
	emit(`{"sessionUpdate":"tool_call_update","toolCallId":"shell","status":"completed","content":[{"type":"content","content":{"type":"text","text":"secret-key output"}}]}`)
	second, _ := s.Activity()
	if second.Tools[0].Status != "in_progress" || second.Tools[1].Title != "shell" || second.Tools[1].Input == "" || second.Tools[1].Output != "[REDACTED] output" || second.Thought != "checking [REDACTED]" {
		t.Fatal(second)
	}
	if second.Events[0].Text == "tampered" || second.Events[1].Tool.Status != "in_progress" || second.Events[3].Tool.Output != "[REDACTED] output" {
		t.Fatal("activity event snapshots are not independent or redacted", second.Events)
	}
	// Text limits hold for UTF-8, and tool tails keep terminal updates beyond
	// the event cap instead of freezing at an old in_progress status.
	emit(`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":` + string(mustJSON(strings.Repeat("字", 50))) + `}}`)
	for range 80 {
		emit(`{"sessionUpdate":"tool_call","toolCallId":"shell","status":"in_progress"}`)
	}
	emit(`{"sessionUpdate":"tool_call_update","toolCallId":"shell","status":"completed"}`)
	bounded, _ := s.Activity()
	if !bounded.ThoughtTruncated || len(bounded.Thought) > 64 || !bounded.ToolsTruncated || len(bounded.Tools) > 64 || bounded.Tools[len(bounded.Tools)-1].Status != "completed" {
		t.Fatal(bounded)
	}
	s.active = nil
	if _, active := s.Activity(); active {
		t.Fatal("finished turn reported as active")
	}
}

func TestSessionUpdateRejectsInvalidMessageContent(t *testing.T) {
	for _, content := range []string{`[]`, `null`, `{}`, `"text"`, `{"type":"text","text":[]}`} {
		t.Run(content, func(t *testing.T) {
			s := &Session{id: "test", active: &turn{}}
			params := json.RawMessage(`{"sessionId":"test","update":{"sessionUpdate":"agent_message_chunk","content":` + content + `}}`)
			if err := s.notification("session/update", params); !errors.Is(err, ErrProtocol) {
				t.Fatalf("invalid message content accepted: %v", err)
			}
		})
	}
}

func TestToolDiagnosticsEncodedBudgetAndDetailTruncation(t *testing.T) {
	s := &Session{id: "test", opts: Options{Limits: Limits{TextBytes: 64 << 10}}, active: &turn{}}
	for i := range 80 {
		params := mustJSON(map[string]any{"sessionId": "test", "update": map[string]any{
			"sessionUpdate": "tool_call_update", "toolCallId": strings.Repeat("x", i+1), "title": "shell", "status": "completed",
			"rawInput": map[string]string{"command": strings.Repeat("字", 2048)},
			"content":  []any{map[string]any{"type": "content", "content": map[string]string{"type": "text", "text": strings.Repeat("\x00", 8192)}}},
		}})
		if err := s.notification("session/update", params); err != nil {
			t.Fatal(err)
		}
	}
	result, _ := s.Activity()
	encoded, err := json.Marshal(result.Tools)
	if err != nil || len(encoded) > 128<<10 || !result.ToolsTruncated || len(result.Tools) == 0 {
		t.Fatal(len(encoded), err, result.ToolsTruncated)
	}
	last := result.Tools[len(result.Tools)-1]
	if !last.DetailsTruncated || len(last.Input) > 2048 || len(last.Output) > 4096 || last.Status != "completed" {
		t.Fatal(last)
	}
	encoded, err = json.Marshal(result.Events)
	if err != nil || len(encoded) > 256<<10 || !result.EventsTruncated || len(result.Events) == 0 || result.Events[len(result.Events)-1].Tool.Status != "completed" {
		t.Fatal("activity event encoded budget or terminal retention", len(encoded), err, result.EventsTruncated)
	}
}

func TestActivityPreservesInterleavingAndCoalescesOnlyAdjacentSpans(t *testing.T) {
	s := &Session{id: "test", opts: Options{Limits: Limits{TextBytes: 1024}}, active: &turn{}}
	for _, update := range []string{
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"check "}}`,
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"files"}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"A","title":"read files","status":"in_progress"}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"A","status":"completed"}`,
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"text","text":"run tests"}}`,
		`{"sessionUpdate":"tool_call","toolCallId":"B","status":"in_progress"}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"waiting"}}`,
		`{"sessionUpdate":"tool_call_update","toolCallId":"B","status":"completed"}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"done"}}`,
	} {
		if err := s.notification("session/update", json.RawMessage(`{"sessionId":"test","update":`+update+`}`)); err != nil {
			t.Fatal(err)
		}
	}
	result, _ := s.Activity()
	want := []string{"thought", "tool", "thought", "tool", "text", "tool", "text"}
	if len(result.Events) != len(want) {
		t.Fatal(result.Events)
	}
	for i, kind := range want {
		if result.Events[i].Seq != uint64(i+1) || result.Events[i].Kind != kind {
			t.Fatal(i, result.Events[i])
		}
	}
	if result.Events[0].Text != "check files" || result.Events[1].Tool.Status != "completed" || result.Events[3].Tool.Status != "in_progress" || result.Events[5].Tool.Status != "completed" || result.Events[6].Text != "done" {
		t.Fatal(result.Events)
	}
}

func TestActivityRetainsBoundedTailWithStableSequences(t *testing.T) {
	s := &Session{id: "test", opts: Options{Limits: Limits{TextBytes: 64}}, active: &turn{}}
	for i := range 300 {
		kind := "agent_thought_chunk"
		if i%2 != 0 {
			kind = "agent_message_chunk"
		}
		params := mustJSON(map[string]any{"sessionId": "test", "update": map[string]any{
			"sessionUpdate": kind, "content": map[string]string{"type": "text", "text": strings.Repeat("字", 100)},
		}})
		if err := s.notification("session/update", params); err != nil {
			t.Fatal(err)
		}
	}
	result, _ := s.Activity()
	if !result.EventsTruncated || len(result.Events) != 256 || result.Events[0].Seq != 45 || result.Events[255].Seq != 300 {
		t.Fatal(result.EventsTruncated, len(result.Events))
	}
	for _, event := range result.Events {
		if !event.TextTruncated || len(event.Text) > 64 || !json.Valid(mustJSON(event)) {
			t.Fatal(event)
		}
	}
}

func TestActivityCombinedSnapshotFitsDiagnosticFile(t *testing.T) {
	s := &Session{id: "test", opts: Options{Limits: Limits{TextBytes: 64 << 10}}, active: &turn{}}
	for i := range 80 {
		kind := "agent_thought_chunk"
		if i%2 != 0 {
			kind = "agent_message_chunk"
		}
		params := mustJSON(map[string]any{"sessionId": "test", "update": map[string]any{
			"sessionUpdate": kind, "content": map[string]string{"type": "text", "text": strings.Repeat("\x00", 16<<10)},
		}})
		if err := s.notification("session/update", params); err != nil {
			t.Fatal(err)
		}
	}
	result, _ := s.Activity()
	encoded := mustJSON(result)
	if len(encoded) > 768<<10 || len(result.Events) == 0 || result.Events[len(result.Events)-1].Seq != 80 {
		t.Fatal("combined snapshot budget lost latest activity", len(encoded), len(result.Events))
	}
}
