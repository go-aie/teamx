package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(b))}
}
func testApp(t *testing.T, tr transportFunc) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a := &app{in: strings.NewReader("stdin-body"), out: out, errOut: errOut, getenv: func(k string) string {
		switch k {
		case "GITLAB_HOST":
			return "gitlab.test"
		case "GITLAB_TOKEN":
			return "secret-test-token"
		case "TEAMX_GLAB_BIN":
			return "/missing/glab"
		}
		return ""
	}, transport: tr}
	return a, out, errOut
}
func fakeGlab(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "glab")
	s := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'glab 1.127.0 (2e352a17e)'; exit 0; fi\n" + body
	if err := os.WriteFile(p, []byte(s), 0700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestHelpRoutingWithoutCredentialsOrNetwork(t *testing.T) {
	bin := fakeGlab(t, `test -z "$GITLAB_TOKEN" || exit 91
test -z "$GITLAB_PAT" || exit 92
test -z "$CI_JOB_TOKEN" || exit 93
printf 'native-help: %s\n' "$*"
`)
	t.Setenv("GITLAB_PAT", "operator-secret")
	t.Setenv("CI_JOB_TOKEN", "operator-secret")
	for _, tc := range []struct {
		args              []string
		native, extension string
	}{
		{nil, "native-help: --help", "work-items close"},
		{[]string{"work-items", "--help"}, "native-help: work-items --help", "assignees"},
		{[]string{"issue", "--help"}, "native-help: issue --help", "discussions reply"},
		{[]string{"issue", "update", "--help"}, "native-help: issue update --help", ""},
		{[]string{"work-items", "close", "--help"}, "", "Usage: teamx tools run gitlab work-items close"},
		{[]string{"work-items", "view", "--help"}, "", "null = unsupported; [] = supported, nobody assigned"},
		{[]string{"work-items", "discussions", "list", "--help"}, "", "notes [{id, body, system, author {id, username, name}}]"},
		{[]string{"issue", "discussions", "--help"}, "", "reply"},
		{[]string{"work-items", "discussions"}, "", "list"},
	} {
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			a, out, errOut := testApp(t, func(*http.Request) (*http.Response, error) { t.Fatal("help requested network"); return nil, nil })
			a.getenv = func(k string) string {
				if k == "TEAMX_GLAB_BIN" {
					return bin
				}
				t.Fatalf("help resolved credential/context %s", k)
				return ""
			}
			if code := a.run(context.Background(), tc.args); code != 0 {
				t.Fatalf("code=%d %s", code, errOut)
			}
			if tc.native != "" && !strings.Contains(out.String(), tc.native) {
				t.Fatal(out.String())
			}
			if tc.native == "" && strings.Contains(out.String(), "native-help:") {
				t.Fatal("pure extension invoked glab")
			}
			if tc.extension != "" && !strings.Contains(out.String(), tc.extension) {
				t.Fatal(out.String())
			}
		})
	}
}

func TestNativeArgumentsStreamsAndFailureAreTransparent(t *testing.T) {
	bin := fakeGlab(t, "printf '%s\\n' \"$@\"\ncat\nprintf 'native failure\\n' >&2\nexit 42\n")
	a, out, errOut := testApp(t, func(*http.Request) (*http.Response, error) { t.Fatal("fallback API attempted"); return nil, nil })
	old := a.getenv
	a.getenv = func(k string) string {
		if k == "TEAMX_GLAB_BIN" {
			return bin
		}
		return old(k)
	}
	args := []string{"issue", "update", "7", "--assignee", "+one,!two", "--description", "body --help\nwith spaces", "--", "-literal"}
	if code := a.run(context.Background(), args); code != 42 {
		t.Fatalf("code %d %s", code, errOut)
	}
	if out.String() != strings.Join(args, "\n")+"\nstdin-body" || errOut.String() != "native failure\n" {
		t.Fatalf("streams changed: %q %q", out, errOut)
	}
}

func TestMissingAndWrongDependency(t *testing.T) {
	a, out, errOut := testApp(t, nil)
	if code := a.run(context.Background(), []string{"--help"}); code != 127 || !strings.Contains(out.String(), "work-items close") || !strings.Contains(errOut.String(), "dependency unavailable") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	if !strings.Contains(errOut.String(), "TEAMX_GLAB_BIN") || !strings.Contains(errOut.String(), "configure this on the TeamX service") || strings.Contains(errOut.String(), "/missing/glab") {
		t.Fatal(errOut.String())
	}
	bin := filepath.Join(t.TempDir(), "glab")
	os.WriteFile(bin, []byte("#!/bin/sh\necho 'glab 1.128.0 (other)'\n"), 0700)
	a.getenv = func(key string) string {
		if key == "TEAMX_GLAB_BIN" {
			return bin
		}
		return ""
	}
	out.Reset()
	errOut.Reset()
	if code := a.run(context.Background(), []string{"mr", "close", "1"}); code != 127 || !strings.Contains(errOut.String(), "unsupported glab") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestRegistryControlsRouteAndHelp(t *testing.T) {
	original := extensions
	defer func() { extensions = original }()
	called := false
	extensions = append(append([]extension{}, extensions...), extension{"diagnostics ping", "Test extension", func(context.Context, *app, []string) error { called = true; return nil }})
	a, out, _ := testApp(t, nil)
	if a.run(context.Background(), []string{"diagnostics", "--help"}) != 0 || !strings.Contains(out.String(), "ping") {
		t.Fatal(out.String())
	}
	if a.run(context.Background(), []string{"diagnostics", "ping"}) != 0 || !called {
		t.Fatal("registry dispatch missing")
	}
}

func TestProcessInterruptionHasNoRetry(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	bin := fakeGlab(t, "touch '"+ready+"'\nsleep 30\n")
	a, _, errOut := testApp(t, nil)
	old := a.getenv
	a.getenv = func(k string) string {
		if k == "TEAMX_GLAB_BIN" {
			return bin
		}
		return old(k)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for i := 0; i < 500; i++ {
			if _, err := os.Stat(ready); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	if code := a.run(ctx, []string{"mr", "merge", "7"}); code != 130 || !strings.Contains(errOut.String(), "unconfirmed") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestParentHelpInterruptionStopsWithoutExtensionOutput(t *testing.T) {
	ready := filepath.Join(t.TempDir(), "ready")
	bin := fakeGlab(t, "touch '"+ready+"'\nsleep 30\n")
	a, out, errOut := testApp(t, nil)
	a.getenv = func(k string) string {
		if k == "TEAMX_GLAB_BIN" {
			return bin
		}
		t.Fatalf("help resolved account field %s", k)
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go cancelAfterFile(ctx, cancel, ready)
	if code := a.run(ctx, []string{"-h"}); code != 130 || !strings.Contains(errOut.String(), "help interrupted") || strings.Contains(errOut.String(), "remote result") || strings.Contains(out.String(), "Additional commands:") {
		t.Fatalf("code %d: %s %s", code, out, errOut)
	}
}

func cancelAfterFile(ctx context.Context, cancel context.CancelFunc, path string) {
	for {
		if _, err := os.Stat(path); err == nil {
			cancel()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestDependencyCheckCancellationSettlesChildren(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "glab")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a, _, errOut := testApp(t, nil)
	a.getenv = func(string) string { return bin }
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if code := a.run(ctx, []string{"-h"}); code != 130 || !strings.Contains(errOut.String(), "before glab execution") {
		t.Fatalf("code %d: %s", code, errOut)
	}
}

func gqlResponse(field string, v any) *http.Response {
	return response(map[string]any{"data": map[string]any{field: v}})
}
func item(widgets any) map[string]any {
	return map[string]any{"id": "gid://gitlab/WorkItem/9", "iid": "7", "title": "fixture", "state": "OPEN", "workItemType": map[string]string{"name": "Task"}, "widgets": widgets}
}
func pageInfoValue(next bool, cursor string) map[string]any {
	return map[string]any{"hasNextPage": next, "endCursor": cursor}
}

func TestGraphQLErrorsNeverBecomeSuccessOrRetry(t *testing.T) {
	for _, bad := range []any{
		map[string]any{"errors": []any{map[string]string{"message": "denied"}}},
		map[string]any{"data": map[string]any{"workItemUpdate": nil}},
		map[string]any{"data": map[string]any{"workItemUpdate": map[string]any{"errors": []string{"denied"}}}},
		map[string]any{"data": map[string]any{"workItemUpdate": map[string]any{"workItem": item([]any{})}}},
	} {
		t.Run(fmt.Sprint(bad), func(t *testing.T) {
			calls := 0
			a, out, errOut := testApp(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return gqlResponse("namespace", map[string]any{"workItem": item([]any{})}), nil
				}
				return response(bad), nil
			})
			code := a.run(context.Background(), []string{"work-items", "close", "--repo", "group/project", "--iid", "7"})
			if code == 0 || calls != 2 || out.Len() != 0 || errOut.Len() == 0 {
				t.Fatalf("code=%d calls=%d out=%s err=%s", code, calls, out, errOut)
			}
		})
	}
}

func TestIssueDiscussionsPaginationAndReplyStdin(t *testing.T) {
	calls := 0
	a, out, errOut := testApp(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer secret-test-token" || r.URL.Host != "gitlab.test" {
			t.Fatal("context missing")
		}
		if r.URL.EscapedPath() != "/api/v4/projects/group%2Fproject/issues/7/discussions" {
			t.Fatal(r.URL.String())
		}
		resp := response([]any{map[string]string{"id": fmt.Sprint(calls)}})
		if calls == 1 {
			resp.Header.Set("X-Next-Page", "2")
		} else {
			resp.Header.Set("X-Next-Page", "")
		}
		return resp, nil
	})
	if code := a.run(context.Background(), []string{"issue", "discussions", "list", "--repo", "group/project", "--iid", "7"}); code != 0 || calls != 2 {
		t.Fatalf("%d %s", code, errOut)
	}
	var all []any
	json.Unmarshal(out.Bytes(), &all)
	if len(all) != 2 {
		t.Fatal(out.String())
	}
	a, out, errOut = testApp(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || !strings.HasSuffix(r.URL.Path, "/discussions/thread/notes") {
			t.Fatal(r.URL.String())
		}
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		if b["body"] != "stdin-body" {
			t.Fatal(b)
		}
		return response(map[string]any{"id": 1, "body": "secret-test-token"}), nil
	})
	if code := a.run(context.Background(), []string{"issue", "discussions", "reply", "--repo", "group/project", "--iid", "7", "--discussion-id", "thread", "--file", "-"}); code != 0 || strings.Contains(out.String(), "secret-test-token") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
}

func TestAssigneeModesAndPagination(t *testing.T) {
	for _, tc := range []struct {
		mode, users string
		want        []string
	}{
		{"add", "3", []string{"1", "2", "3"}}, {"remove", "1", []string{"2"}}, {"replace", "3,3", []string{"3"}}, {"clear", "", []string{}},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			mutated := false
			queries := 0
			writes := 0
			var target []string
			a, out, errOut := testApp(t, func(r *http.Request) (*http.Response, error) {
				var b struct {
					Query     string
					Variables map[string]any
				}
				json.NewDecoder(r.Body).Decode(&b)
				if strings.HasPrefix(b.Query, "mutation") {
					writes++
					raw := b.Variables["input"].(map[string]any)["assigneesWidget"].(map[string]any)["assigneeIds"].([]any)
					target = []string{}
					for _, id := range raw {
						target = append(target, id.(string))
					}
					mutated = true
					return gqlResponse("workItemUpdate", map[string]any{"errors": []string{}, "workItem": item([]any{})}), nil
				}
				queries++
				ids := []string{"1"}
				p := pageInfoValue(true, "second")
				if b.Variables["after"] == "second" {
					ids = []string{"2"}
					p = pageInfoValue(false, "")
				}
				if mutated {
					ids = tc.want
					p = pageInfoValue(false, "")
				}
				nodes := []any{}
				for _, id := range ids {
					nodes = append(nodes, map[string]string{"id": "gid://gitlab/User/" + id, "username": "u" + id})
				}
				return gqlResponse("namespace", map[string]any{"workItem": item([]any{map[string]any{"assignees": map[string]any{"nodes": nodes, "pageInfo": p}}})}), nil
			})
			args := []string{"work-items", "assignees", "--repo", "g/p", "--iid", "7", "--mode", tc.mode}
			if tc.users != "" {
				args = append(args, "--users", tc.users)
			}
			if code := a.run(context.Background(), args); code != 0 || writes != 1 || queries != 3 {
				t.Fatalf("%d writes %d queries %d: %s %s", code, writes, queries, out, errOut)
			}
			want := []string{}
			for _, id := range tc.want {
				want = append(want, "gid://gitlab/User/"+id)
			}
			if !reflect.DeepEqual(target, want) {
				t.Fatalf("got %v want %v", target, want)
			}
		})
	}
}

func TestNestedDiscussionsPagination(t *testing.T) {
	calls := 0
	want := []any{}
	for i, did := range []string{"d1", "d2"} {
		notes := []any{}
		first, last := 1, 3
		if i == 1 {
			first, last = 4, 4
		}
		for j := first; j <= last; j++ {
			var author any
			if j != 2 {
				author = map[string]any{"id": "gid://gitlab/User/1", "username": "alice", "name": "张三"}
			}
			notes = append(notes, map[string]any{"id": fmt.Sprintf("n%d", j), "body": fmt.Sprintf("完整正文 %d\n> 引用\n`code`", j), "system": j == 2, "author": author})
		}
		want = append(want, map[string]any{"id": did, "replyId": "reply-" + did, "notes": notes})
	}
	a, out, errOut := testApp(t, func(r *http.Request) (*http.Response, error) {
		calls++
		var b struct{ Variables map[string]any }
		json.NewDecoder(r.Body).Decode(&b)
		did, nid := "d1", "n1"
		outer, inner := pageInfoValue(true, "outer2"), pageInfoValue(true, "notes2")
		if b.Variables["notesAfter"] == "notes2" {
			nid = "n2"
			inner = pageInfoValue(true, "notes3")
		}
		if b.Variables["notesAfter"] == "notes3" {
			nid = "n3"
			inner = pageInfoValue(false, "")
		}
		if b.Variables["after"] == "outer2" {
			did, nid = "d2", "n4"
			outer, inner = pageInfoValue(false, ""), pageInfoValue(false, "")
		}
		index := 0
		if did == "d2" {
			index = 1
		}
		discussion := want[index].(map[string]any)
		var selected any
		for _, n := range discussion["notes"].([]any) {
			if n.(map[string]any)["id"] == nid {
				selected = n
			}
		}
		d := map[string]any{"id": did, "replyId": discussion["replyId"], "notes": map[string]any{"nodes": []any{selected}, "pageInfo": inner}}
		return gqlResponse("namespace", map[string]any{"workItem": item([]any{map[string]any{"discussions": map[string]any{"nodes": []any{d}, "pageInfo": outer}}})}), nil
	})
	if code := a.run(context.Background(), []string{"work-items", "discussions", "list", "--repo", "g/p", "--iid", "7"}); code != 0 || calls != 4 {
		t.Fatalf("%d calls=%d %s", code, calls, errOut)
	}
	var all []any
	if err := json.Unmarshal(out.Bytes(), &all); err != nil || !reflect.DeepEqual(all, want) {
		t.Fatalf("discussion output lost content or retained connections: %s (decode: %v)", out, err)
	}
}

func TestWorkItemViewBusinessFields(t *testing.T) {
	for _, kind := range []string{"unsupported", "unassigned", "assigned"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			body := "完整正文\n\n> 原始引用\n```go\nfmt.Println(\"你好\")\n```\n"
			users := []any{
				map[string]any{"id": "gid://gitlab/User/1", "username": "alice", "name": "张三"},
				map[string]any{"id": "gid://gitlab/User/2", "username": "bob", "name": "李四"},
			}
			var assignees any
			if kind == "unassigned" {
				assignees = []any{}
			} else if kind == "assigned" {
				assignees = users
			}
			a, out, errOut := testApp(t, func(r *http.Request) (*http.Response, error) {
				calls++
				var req struct {
					Query     string
					Variables map[string]any
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(req.Query, "description") || req.Variables["path"] != "g/p" || req.Variables["iid"] != "7" {
					t.Fatalf("unexpected query: %+v", req)
				}
				widgets := []any{map[string]any{"description": body}, map[string]any{}}
				if kind != "unsupported" {
					nodes, page := []any{}, pageInfoValue(false, "")
					if kind == "assigned" {
						if calls == 1 {
							if req.Variables["after"] != nil {
								t.Fatal("unexpected first cursor")
							}
							nodes, page = users[:1], pageInfoValue(true, "next")
						} else {
							if req.Variables["after"] != "next" {
								t.Fatal("missing continuation cursor")
							}
							nodes = users[1:]
						}
					}
					widgets = append(widgets, map[string]any{"assignees": map[string]any{"nodes": nodes, "pageInfo": page}})
				}
				return gqlResponse("namespace", map[string]any{"workItem": item(widgets)}), nil
			})
			if code := a.run(context.Background(), []string{"work-items", "view", "--repo", "g/p", "--iid", "7"}); code != 0 {
				t.Fatalf("code %d: %s", code, errOut)
			}
			wantCalls := 1
			if kind == "assigned" {
				wantCalls = 2
			}
			want := map[string]any{"id": "gid://gitlab/WorkItem/9", "iid": "7", "title": "fixture", "type": "Task", "description": body, "state": "OPEN", "assignees": assignees}
			var got map[string]any
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) || calls != wantCalls {
				t.Fatalf("calls %d, output %s (decode: %v)", calls, out, err)
			}
		})
	}
}

func TestWorkItemDiscussionsEmptyLists(t *testing.T) {
	for _, emptyDiscussion := range []bool{false, true} {
		t.Run(fmt.Sprint(emptyDiscussion), func(t *testing.T) {
			rows := []any{}
			want := []any{}
			if emptyDiscussion {
				rows = append(rows, map[string]any{"id": "empty", "replyId": "", "notes": map[string]any{"nodes": []any{}, "pageInfo": pageInfoValue(false, "")}})
				want = append(want, map[string]any{"id": "empty", "replyId": "", "notes": []any{}})
			}
			a, out, errOut := testApp(t, func(*http.Request) (*http.Response, error) {
				return gqlResponse("namespace", map[string]any{"workItem": item([]any{map[string]any{"discussions": map[string]any{"nodes": rows, "pageInfo": pageInfoValue(false, "")}}})}), nil
			})
			if code := a.run(context.Background(), []string{"work-items", "discussions", "list", "--repo", "g/p", "--iid", "7"}); code != 0 {
				t.Fatalf("code %d: %s", code, errOut)
			}
			var got []any
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("output %s (decode: %v)", out, err)
			}
		})
	}
}

func TestInvalidArgumentsDoNotResolveCredentials(t *testing.T) {
	for _, args := range [][]string{
		{"work-items", "close"},
		{"work-items", "assignees", "--repo", "g/p", "--iid", "7", "--mode", "clear", "--users", "1"},
		{"work-items", "note", "--repo", "g/p", "--iid", "7", "--message", "x", "--file", "-"},
		{"issue", "discussions", "reply", "--repo", "g/p", "--iid", "7", "--message", "x"},
	} {
		a, _, _ := testApp(t, nil)
		a.getenv = func(string) string { t.Fatal("invalid arguments resolved credentials"); return "" }
		if a.run(context.Background(), args) == 0 {
			t.Fatal(args)
		}
	}
}
