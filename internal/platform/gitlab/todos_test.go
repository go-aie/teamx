package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

const rawTodoFixture = `{"id":9,"project":{"id":7,"path_with_namespace":"group/nested/project"},"author":{"id":2,"username":"alice","name":"Alice"},"action_name":"review_requested","target_type":"MergeRequest","target":{"id":70,"iid":42,"project_id":7,"title":"修复登录","author":{"id":3,"username":"bob","name":"Bob"}},"body":"请核对重试逻辑。\n第二次请求返回 500。","target_url":"https://gitlab.example.com/group/nested/project/-/merge_requests/42","state":"pending","created_at":"2026-11-01T12:00:00Z"}`

func todoFetcher(t *testing.T, body string, h http.Header) (*GoTodoFetcher, *int) {
	t.Helper()
	read, calls := fixtureRead(t, body, h)
	f, err := NewTodoFetcher("https://gitlab.example.com", 101, read)
	if err != nil {
		t.Fatal(err)
	}
	return f, calls
}
func TestTodosGoOnePersonalPageAndLimits(t *testing.T) {
	f, calls := todoFetcher(t, "["+rawTodoFixture+"]", http.Header{"X-Next-Page": {"2"}, "Retry-After": {"9"}})
	if *calls != 0 {
		t.Fatal("prepare queried remote")
	}
	b, err := f.FetchTodos(context.Background(), TodoQuery{State: "pending"})
	if err != nil || *calls != 1 || b.Complete || b.NextPage != 2 || b.RetryAfterSeconds != 9 {
		t.Fatal(b, err)
	}
	item := b.Todos[0]
	if item.RecipientID != 101 || item.Actor.ID != 2 || item.TargetAuthor.ID != 3 || item.TargetIID != 42 || item.Body != "请核对重试逻辑。\n第二次请求返回 500。" {
		t.Fatal(item)
	}
	before := *calls
	for _, q := range []TodoQuery{{}, {State: "all"}, {State: "pending", Page: 10001}, {State: "pending", PerPage: 101}, {State: "pending", MaxBytes: maxTodoOutput + 1}, {State: "pending", Timeout: 31 * time.Second}, {State: "pending", Timeout: -1}} {
		if _, err := f.FetchTodos(context.Background(), q); err == nil {
			t.Fatal(q)
		}
	}
	if *calls != before {
		t.Fatal("invalid query reached HTTP")
	}
	for _, q := range []TodoQuery{{State: "done"}, {State: "pending", MaxBytes: 10}} {
		if _, err := f.FetchTodos(context.Background(), q); err == nil {
			t.Fatal(q)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before = *calls
	if _, err := f.FetchTodos(ctx, TodoQuery{State: "pending"}); !errors.Is(err, context.Canceled) || *calls != before {
		t.Fatal(err)
	}
}
func TestTodosGoNormalizationAndObservedSkips(t *testing.T) {
	for _, action := range []string{"mentioned", "directly_addressed", "assigned", "review_requested", "marked", "future_action"} {
		for _, kind := range []string{"Issue", "WorkItem"} {
			raw := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(rawTodoFixture, "review_requested", action), "MergeRequest", kind), "pending", "done")
			raw = strings.ReplaceAll(raw, "merge_requests/42", "work_items/42#note_12")
			f, _ := todoFetcher(t, "["+raw+"]", nil)
			b, err := f.FetchTodos(context.Background(), TodoQuery{State: "done"})
			if err != nil || b.Todos[0].Kind != "work_item" || b.Todos[0].Reason != todoReason(action) {
				t.Fatal(b, err)
			}
		}
	}
	for _, tc := range []struct {
		key   string
		value any
		skip  string
	}{{"project", nil, "no_project"}, {"target_type", "Epic", "unsupported_target"}, {"target", nil, "target_unavailable"}, {"target_url", "https://evil.invalid/a", "untrusted_url"}, {"target_url", "https://gitlab.example.com/group/nested/project/-/merge_requests/43", "untrusted_url"}, {"target_url", "https://gitlab.example.com/group/nested/project/-/merge_requests/42?secret=1", "untrusted_url"}, {"target_url", "https://gitlab.example.com/group/nested/project/-/merge_requests/42#bad", "untrusted_url"}} {
		var raw map[string]any
		json.Unmarshal([]byte(rawTodoFixture), &raw)
		raw[tc.key] = tc.value
		data, _ := json.Marshal([]any{raw})
		f, _ := todoFetcher(t, string(data), nil)
		b, err := f.FetchTodos(context.Background(), TodoQuery{State: "pending"})
		if err != nil || b.Todos[0].SkipReason != tc.skip || b.Todos[0].URL != "" {
			t.Fatal(b, err)
		}
	}
	var raw map[string]any
	json.Unmarshal([]byte(rawTodoFixture), &raw)
	raw["body"] = strings.Repeat("长", 5000)
	data, _ := json.Marshal([]any{raw})
	f, _ := todoFetcher(t, string(data), nil)
	b, err := f.FetchTodos(context.Background(), TodoQuery{State: "pending"})
	if err != nil || len([]rune(b.Todos[0].Body)) != 4096 {
		t.Fatal(err)
	}
}
func TestTodosGoMalformedPagesAndPagination(t *testing.T) {
	for _, body := range []string{"", "bad", "{}", "null", "[" + rawTodoFixture + "," + rawTodoFixture + "]", strings.Replace("["+rawTodoFixture+"]", `"id":9`, `"id":true`, 1), strings.Replace("["+rawTodoFixture+"]", `"id":9`, `"id":0`, 1), strings.Replace("["+rawTodoFixture+"]", "2026-11-01T12:00:00Z", "2026-11-01T12:00:00", 1), strings.Replace("["+rawTodoFixture+"]", `"project_id":7`, `"project_id":8`, 1), strings.Replace("["+rawTodoFixture+"]", "group/nested/project", "group/../project", 1)} {
		f, _ := todoFetcher(t, body, nil)
		if _, err := f.FetchTodos(context.Background(), TodoQuery{State: "pending"}); err == nil {
			t.Fatal(body)
		}
	}
	f, _ := todoFetcher(t, "["+rawTodoFixture+"]", nil)
	b, err := f.FetchTodos(context.Background(), TodoQuery{State: "pending", PerPage: 1, Page: 3})
	if err != nil || b.NextPage != 4 {
		t.Fatal(b, err)
	}
	for _, next := range []string{"1", "10001", "-2", "https://evil.invalid"} {
		f, _ := todoFetcher(t, "[]", http.Header{"X-Next-Page": {next}})
		if _, err := f.FetchTodos(context.Background(), TodoQuery{State: "pending"}); err == nil {
			t.Fatal(next)
		}
	}
}
func TestTodosGoSafeErrorsQueryScopeAndCancellation(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500} {
		calls := 0
		read := NewReadAPI("https://gitlab.example.com", platform.NewSecret("secret"), roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			q := r.URL.Query()
			if r.URL.Path != "/api/v4/todos" || q.Get("state") != "pending" || q.Has("project_id") || q.Get("page") != "1" || q.Get("per_page") != "100" || q.Get("sort") != "" {
				t.Fatal(r.URL)
			}
			return fixtureResponse("sensitive-body", status, http.Header{"Retry-After": {"17"}}), nil
		}))
		f, _ := NewTodoFetcher("https://gitlab.example.com", 101, read)
		_, err := f.FetchTodos(context.Background(), TodoQuery{State: "pending"})
		var e *TodoQueryError
		if !errors.As(err, &e) || e.HTTPStatus != status || e.RetryAfterSeconds != 17 || calls != 1 {
			t.Fatal(err, calls)
		}
	}
	settled := false
	read := NewReadAPI("https://gitlab.example.com", platform.NewSecret("secret"), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		settled = true
		return fixtureResponse("[]", 200, nil), nil
	}))
	f, _ := NewTodoFetcher("https://gitlab.example.com", 101, read)
	if _, err := f.FetchTodos(context.Background(), TodoQuery{State: "pending", Timeout: time.Millisecond}); !errors.Is(err, context.DeadlineExceeded) || !settled {
		t.Fatal(err)
	}
}
