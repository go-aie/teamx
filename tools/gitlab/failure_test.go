package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestAPIFailuresAndCancellationNeverRetry(t *testing.T) {
	for _, kind := range []string{"permission", "transport", "cancelled", "bad-json", "graphql-query-error"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			a, out, errOut := testApp(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 && kind != "graphql-query-error" {
					return gqlResponse("namespace", map[string]any{"workItem": item([]any{})}), nil
				}
				switch kind {
				case "permission":
					res := response(map[string]any{"message": "secret-test-token"})
					res.StatusCode = 403
					return res, nil
				case "transport":
					return nil, errors.New("private transport details secret-test-token")
				case "cancelled":
					return nil, context.Canceled
				case "bad-json":
					res := response(nil)
					res.Body = http.NoBody
					return res, nil
				default:
					return response(map[string]any{"errors": []any{map[string]string{"message": "secret-test-token"}}}), nil
				}
			})
			code := a.run(context.Background(), []string{"work-items", "close", "--repo", "g/p", "--iid", "7"})
			want := 2
			if kind == "graphql-query-error" {
				want = 1
			}
			if code == 0 || calls != want || out.Len() != 0 || strings.Contains(errOut.String(), "secret-test-token") {
				t.Fatalf("code %d calls %d: %s %s", code, calls, out, errOut)
			}
		})
	}
}

func TestPaginationRejectsBrokenMetadata(t *testing.T) {
	for _, p := range []pageInfo{{}, {HasNextPage: boolPtr(true)}, {HasNextPage: boolPtr(true), EndCursor: "seen"}} {
		if _, err := nextCursor(p, map[string]bool{"seen": true}); err == nil {
			t.Fatal("accepted broken page", p)
		}
	}
	calls := 0
	a, out, _ := testApp(t, func(*http.Request) (*http.Response, error) {
		calls++
		res := response([]any{})
		res.Header.Set("X-Next-Page", "1")
		return res, nil
	})
	if a.run(context.Background(), []string{"issue", "discussions", "list", "--repo", "g/p", "--iid", "7"}) == 0 || calls != 1 || out.Len() != 0 {
		t.Fatal("invalid REST cursor accepted")
	}
}
func boolPtr(v bool) *bool { return &v }

func TestRESTPaginationWithoutHeadersReadsUntilEmpty(t *testing.T) {
	calls := 0
	a, out, errOut := testApp(t, func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return response([]any{map[string]string{"id": "thread"}}), nil
		}
		return response([]any{}), nil
	})
	if code := a.run(context.Background(), []string{"issue", "discussions", "list", "--repo", "g/p", "--iid", "7"}); code != 0 || calls != 2 || !strings.Contains(out.String(), "thread") {
		t.Fatalf("%d %d %s", code, calls, errOut)
	}
}

func TestWorkItemNoteAndReplyUseActualIDs(t *testing.T) {
	for _, reply := range []bool{false, true} {
		calls := 0
		a, out, errOut := testApp(t, func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return gqlResponse("namespace", map[string]any{"workItem": item([]any{})}), nil
			}
			var req struct{ Variables map[string]map[string]any }
			json.NewDecoder(r.Body).Decode(&req)
			input := req.Variables["input"]
			if input["noteableId"] != "gid://gitlab/WorkItem/9" || input["body"] != "stdin-body" {
				t.Fatal(input)
			}
			if reply && input["discussionId"] != "gid://gitlab/Discussion/thread" {
				t.Fatal(input)
			}
			return gqlResponse("createNote", map[string]any{"errors": []string{}, "note": map[string]string{"id": "gid://gitlab/Note/1", "body": "stdin-body"}}), nil
		})
		args := []string{"work-items", "note", "--repo", "g/p", "--iid", "7", "--file", "-"}
		if reply {
			args = append(args, "--discussion-id", "gid://gitlab/Discussion/thread")
		}
		if code := a.run(context.Background(), args); code != 0 || calls != 2 || !strings.Contains(out.String(), "Note/1") {
			t.Fatalf("%d %s %s", code, out, errOut)
		}
	}
}

func TestExtensionInstanceAndTokenRequired(t *testing.T) {
	for _, tc := range []struct{ host, token string }{{"", "token"}, {"http://host", "token"}, {"https://user:password@host", "token"}, {"https://host/path", "token"}, {"https://host?token=x", "token"}, {"host", ""}} {
		a, _, _ := testApp(t, func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid context requested network")
			return nil, nil
		})
		a.getenv = func(k string) string {
			if k == "GITLAB_HOST" {
				return tc.host
			}
			if k == "GITLAB_TOKEN" {
				return tc.token
			}
			return ""
		}
		if a.run(context.Background(), []string{"work-items", "view", "--repo", "g/p", "--iid", "7"}) == 0 {
			t.Fatal(tc)
		}
	}
}

func TestViewWithoutAssigneeWidgetDoesNotPreventReading(t *testing.T) {
	a, out, errOut := testApp(t, func(*http.Request) (*http.Response, error) {
		return gqlResponse("namespace", map[string]any{"workItem": item([]any{})}), nil
	})
	if a.run(context.Background(), []string{"work-items", "view", "--repo", "g/p", "--iid", "7"}) != 0 {
		t.Fatal(errOut.String())
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got["id"] != "gid://gitlab/WorkItem/9" || got["description"] != "" || got["assignees"] != nil {
		t.Fatal(out.String())
	}
}

func TestWorkItemViewsNeverOutputIncompletePages(t *testing.T) {
	for _, kind := range []string{"assignees", "discussions", "notes"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			a, out, errOut := testApp(t, func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 2 {
					return response(map[string]any{"errors": []any{map[string]string{"message": "denied"}}}), nil
				}
				widget := map[string]any{}
				if kind == "assignees" {
					widget["assignees"] = map[string]any{"nodes": []any{map[string]string{"id": "gid://gitlab/User/1"}}, "pageInfo": pageInfoValue(true, "more")}
				} else {
					outer, inner := pageInfoValue(kind == "discussions", "more"), pageInfoValue(kind == "notes", "more")
					widget["discussions"] = map[string]any{"nodes": []any{map[string]any{"id": "d1", "replyId": "reply-d1", "notes": map[string]any{"nodes": []any{map[string]string{"id": "n1", "body": "partial body"}}, "pageInfo": inner}}}, "pageInfo": outer}
				}
				return gqlResponse("namespace", map[string]any{"workItem": item([]any{widget})}), nil
			})
			args := []string{"work-items", "view"}
			if kind != "assignees" {
				args = []string{"work-items", "discussions", "list"}
			}
			args = append(args, "--repo", "g/p", "--iid", "7")
			if code := a.run(context.Background(), args); code == 0 || calls != 2 || out.Len() != 0 || errOut.Len() == 0 {
				t.Fatalf("code %d calls %d: out=%s err=%s", code, calls, out, errOut)
			}
		})
	}
}

func TestCloseChecksActualReturnedStateAndIdentity(t *testing.T) {
	for _, tc := range []struct {
		id, state string
		ok        bool
	}{
		{"gid://gitlab/WorkItem/9", "CLOSED", true},
		{"gid://gitlab/WorkItem/10", "CLOSED", false},
		{"gid://gitlab/WorkItem/9", "OPEN", false},
	} {
		calls := 0
		a, _, errOut := testApp(t, func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return gqlResponse("namespace", map[string]any{"workItem": item([]any{})}), nil
			}
			var req struct{ Variables map[string]map[string]any }
			json.NewDecoder(r.Body).Decode(&req)
			input := req.Variables["input"]
			if len(input) != 2 || input["stateEvent"] != "CLOSE" || input["id"] != "gid://gitlab/WorkItem/9" {
				t.Fatal(input)
			}
			return gqlResponse("workItemUpdate", map[string]any{"errors": []string{}, "workItem": map[string]string{"id": tc.id, "iid": "7", "state": tc.state}}), nil
		})
		code := a.run(context.Background(), []string{"work-items", "close", "--repo", "g/p", "--iid", "7"})
		if (code == 0) != tc.ok || calls != 2 {
			t.Fatalf("%+v code %d calls %d: %s", tc, code, calls, errOut)
		}
	}
}

func TestAssigneeMismatchDoesNotRetryMutation(t *testing.T) {
	writes := 0
	a, _, errOut := testApp(t, func(r *http.Request) (*http.Response, error) {
		var req struct{ Query string }
		json.NewDecoder(r.Body).Decode(&req)
		if strings.HasPrefix(req.Query, "mutation") {
			writes++
			return gqlResponse("workItemUpdate", map[string]any{"errors": []string{}, "workItem": item([]any{})}), nil
		}
		return gqlResponse("namespace", map[string]any{"workItem": item([]any{map[string]any{"assignees": map[string]any{"nodes": []any{}, "pageInfo": pageInfoValue(false, "")}}})}), nil
	})
	if code := a.run(context.Background(), []string{"work-items", "assignees", "--repo", "g/p", "--iid", "7", "--mode", "replace", "--users", "1"}); code == 0 || writes != 1 || !strings.Contains(errOut.String(), "differ from requested") {
		t.Fatalf("%d writes %d: %s", code, writes, errOut)
	}
}
