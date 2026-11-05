package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const commentInputJSON = `{"project_id":"123","mr_iid":"42","body":"exact reply"}`

func TestActionCommentInspection(t *testing.T) {
	calls := 0
	a, out, stderr := testApp(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" {
			t.Fatal("inspection wrote", r.Method)
		}
		switch r.URL.Path {
		case "/api/v4/user":
			return response(map[string]any{"id": 101, "username": "bot"}), nil
		case "/api/v4/projects/123/merge_requests/42":
			return response(map[string]any{"id": 9001, "iid": 42, "project_id": 123, "web_url": "https://gitlab.test/a/b/-/merge_requests/42"}), nil
		case "/api/v4/projects/123":
			return response(map[string]any{"id": 123, "path_with_namespace": "a/b"}), nil
		}
		t.Fatal(r.URL)
		return nil, nil
	})
	a.in = strings.NewReader(commentInputJSON)
	if code := a.run(context.Background(), []string{"__action-mr-comment", "inspect"}); code != 0 {
		t.Fatal(code, stderr.String())
	}
	var result commentInspection
	if json.Unmarshal(out.Bytes(), &result) != nil || result.Status != "verified" || result.MRID != 9001 || result.UserID != 101 || calls != 3 {
		t.Fatal(out.String(), calls)
	}
}
func TestActionCommentOneAttemptAndStructuredOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		payload   string
		transport bool
		want      string
		retry     bool
	}{
		{"success", 201, `{"id":99,"body":"exact reply","system":false,"noteable_id":9001,"noteable_iid":42,"noteable_type":"MergeRequest","author":{"id":101,"username":"bot"}}`, false, "succeeded", false},
		{"permission", 403, `{}`, false, "not_applied", false},
		{"input", 422, `{}`, false, "not_applied", false},
		{"rate-limit", 429, `{}`, false, "not_applied", true},
		{"server", 502, `{}`, false, "unknown", true},
		{"timeout", 408, `{}`, false, "unknown", true},
		{"transport", 0, "", true, "unknown", true},
		{"business-invalid", 200, `{"message":"not a note"}`, false, "unknown", false},
		{"partial", 201, `{"id":`, false, "unknown", false},
		{"wrong-body", 201, `{"id":99,"body":"other"}`, false, "unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a, out, stderr := testApp(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/api/v4/projects/123/merge_requests/42/notes" {
					t.Fatal(r.Method, r.URL)
				}
				var body map[string]string
				if json.NewDecoder(r.Body).Decode(&body) != nil || body["body"] != "exact reply" || len(body) != 1 {
					t.Fatal(body)
				}
				if tc.transport {
					return nil, errors.New("connection reset")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(tc.payload))}, nil
			})
			a.in = strings.NewReader(commentInputJSON)
			if code := a.run(context.Background(), []string{"__action-mr-comment", "send"}); code != 0 {
				t.Fatal(code, stderr.String())
			}
			var got commentResult
			if json.Unmarshal(out.Bytes(), &got) != nil || got.Status != tc.want || got.Retryable != tc.retry || calls != 1 || strings.Contains(out.String(), "secret-test-token") {
				t.Fatal(out.String(), calls)
			}
		})
	}
}
func TestActionCommentRejectsArbitraryTargetsAndInput(t *testing.T) {
	for _, q := range []string{
		`{"project_id":"a/b","mr_iid":"42","body":"x"}`,
		`{"project_id":"123/other","mr_iid":"42","body":"x"}`,
		`{"project_id":"123","mr_iid":"42/notes","body":"x"}`,
		`{"project_id":"123","mr_iid":"42","body":" "}`,
		`{"project_id":"123","mr_iid":"42","body":"x","argv":["delete"]}`,
		commentInputJSON + ` {}`,
	} {
		a, _, _ := testApp(t, func(*http.Request) (*http.Response, error) { t.Fatal("bad request reached network"); return nil, nil })
		a.in = strings.NewReader(q)
		if code := a.run(context.Background(), []string{"__action-mr-comment", "send"}); code == 0 {
			t.Fatal("accepted", q)
		}
	}
}
