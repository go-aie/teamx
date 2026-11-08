package room

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAgentPolicyChangeValidation(t *testing.T) {
	base := agentPolicyChange{Type: "save", ID: "contacts", SourceSeq: 8, TurnNumber: 14, Basis: "明确长期指令", Body: &agentPolicyBody{Text: "先联系指定负责人"}}
	cases := []struct {
		field  string
		change func(*agentPolicyChange)
	}{
		{"id", func(q *agentPolicyChange) { q.ID = "" }},
		{"body.text", func(q *agentPolicyChange) { q.Body.Text = "" }},
		{"body.text", func(q *agentPolicyChange) { q.Body.Text = strings.Repeat("x", maxPolicyTextBytes+1) }},
		{"body", func(q *agentPolicyChange) { q.Body = nil }},
		{"body", func(q *agentPolicyChange) { q.ProposalSeq = 4 }},
		{"type", func(q *agentPolicyChange) { q.Type = "activate" }},
		{"type", func(q *agentPolicyChange) { q.Type, q.Version, q.ProposalSeq = "confirm", 1, 4 }},
		{"type", func(q *agentPolicyChange) { q.Type, q.Body, q.Version = "confirm", nil, 1 }},
		{"type", func(q *agentPolicyChange) { q.Type, q.Body = "revoke", nil }},
		{"type", func(q *agentPolicyChange) { q.Type, q.Body, q.Version, q.ProposalSeq = "revoke", nil, 1, 4 }},
		{"body.expires_at", func(q *agentPolicyChange) { q.Body.ExpiresAt = new(time.Time) }},
		{"source_seq", func(q *agentPolicyChange) { q.SourceSeq = 0 }},
		{"turn_number", func(q *agentPolicyChange) { q.TurnNumber = 0 }},
		{"basis", func(q *agentPolicyChange) { q.Basis = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			q := base
			body := *base.Body
			q.Body = &body
			tc.change(&q)
			err := q.validate()
			var diagnostic *requestDiagnostic
			if !errors.Is(err, ErrRequest) || !errors.As(err, &diagnostic) || diagnostic.Field != tc.field {
				t.Fatal(err)
			}
		})
	}
}

func TestAgentPolicyChangeValidBranches(t *testing.T) {
	for _, kind := range []string{"save", "suggest", "confirm", "revoke"} {
		t.Run(kind, func(t *testing.T) {
			q := agentPolicyChange{Type: kind, ID: "style", SourceSeq: 1, TurnNumber: 1, Basis: "explicit"}
			if kind == "save" || kind == "suggest" {
				q.Body = &agentPolicyBody{Text: "简短中文"}
			} else {
				q.Version = 1
			}
			if kind == "confirm" {
				q.ProposalSeq = 2
			}
			if err := q.validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAgentPolicyRejectsRemovedFields(t *testing.T) {
	base := `"type":"save","id":"contacts","source_seq":8,"turn_number":1,"basis":"explicit",`
	for _, field := range []string{"kind", "scope", "actions", "conditions", "exceptions", "scope_from"} {
		for _, value := range []string{`null`, `{}`, `"rule"`} {
			tail := `"body":{"text":"rule","` + field + `":` + value + `}`
			expected := "body." + field
			if field == "scope_from" {
				tail = `"body":{"text":"rule"},"scope_from":` + value
				expected = field
			}
			var q agentPolicyChange
			err := json.Unmarshal([]byte("{"+base+tail+"}"), &q)
			var d *requestDiagnostic
			if !errors.Is(err, ErrRequest) || !errors.As(err, &d) || d.Field != expected {
				t.Fatal(field, value, err)
			}
		}
	}
}

func TestAgentPolicyDiagnosticPreservesCause(t *testing.T) {
	for field, want := range map[string]string{
		"change.source_seq": "source_seq", "change.text": "body.text",
		"change.expires_at": "body.expires_at", "change": "record", "mode": "type",
	} {
		t.Run(field, func(t *testing.T) {
			original := &requestDiagnostic{cause: ErrIdentity, Field: field, Message: "invalid evidence", Hint: "use saved evidence"}
			err := agentPolicyDiagnostic(original)
			var diagnostic *requestDiagnostic
			if !errors.Is(err, ErrIdentity) || !errors.As(err, &diagnostic) || diagnostic.Field != want ||
				diagnostic.Message != original.Message || diagnostic.Hint != original.Hint || original.Field != field {
				t.Fatal(err, original)
			}
		})
	}
	if agentPolicyDiagnostic(ErrConflict) != ErrConflict {
		t.Fatal("unstructured error changed")
	}
}
