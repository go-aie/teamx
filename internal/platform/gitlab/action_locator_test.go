package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

const locatorOrigin = "https://gitlab.example"
const locatorWeb = locatorOrigin + "/group/sub/project/-/merge_requests/42"
const locatorNote = `{"id":99,"noteable_id":9001,"noteable_iid":42,"noteable_type":"MergeRequest"}`
const locatorThread = `{"id":"680f5bcbb2f1d20b8beff17953e04503799e674f","individual_note":false,"notes":[` + locatorNote + `]}`

func locatorAccess() platform.AccessContext {
	a := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "gitlab", Instance: locatorOrigin}, ID: restID(101, "id")}
	return platform.AccessContext{RoomIdentity: platform.RoomIdentity{RoomID: "room", RoomInstance: "instance"}, AgentID: "agent", MembershipID: "member", Account: a, Receiver: a, PlatformID: "platform", Generation: "generation", PlatformVersion: 1, CredentialVersion: 1}
}

func locatorRead(t *testing.T, hook func(string, url.Values) (string, http.Header, error)) ReadAPI {
	t.Helper()
	return func(ctx context.Context, path string, q url.Values, h http.Header, limit int64) (APIResponse, error) {
		t.Helper()
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 30*time.Second || limit != 1<<20 {
			t.Fatal("unbounded read", limit)
		}
		if hook != nil {
			data, header, err := hook(path, q)
			if data != "" || err != nil {
				return APIResponse{Data: []byte(data), Header: header}, err
			}
		}
		data := ""
		switch path {
		case "/user":
			data = `{"id":101,"username":"user101"}`
		case "/projects/group%2Fsub%2Fproject":
			data = `{"id":123,"path_with_namespace":"group/sub/project"}`
		case "/projects/123/merge_requests/42":
			data = `{"id":9001,"iid":42,"project_id":123,"web_url":"` + locatorWeb + `"}`
		case "/projects/123/merge_requests/42/discussions":
			data = `[` + locatorThread + `]`
		case "/projects/123/merge_requests/42/discussions/680f5bcbb2f1d20b8beff17953e04503799e674f":
			data = locatorThread
		default:
			t.Fatal("unexpected path", path)
		}
		return APIResponse{Data: []byte(data)}, nil
	}
}

func TestGitlabActionURLShapes(t *testing.T) {
	for _, raw := range []string{locatorWeb, strings.Replace(locatorWeb, "gitlab.example", "GITLAB.EXAMPLE", 1) + "/", locatorWeb + "#note_99", locatorWeb + "#discussion_full-native-id", locatorOrigin + "/group/%E9%A1%B9%E7%9B%AE/-/merge_requests/1", locatorWeb + "?", locatorWeb + "?view=parallel#note_99", locatorWeb + "/diffs#note_99", locatorWeb + "/commits#note_99", locatorWeb + "/notes/99", locatorWeb + "/notes/99#note_99", locatorWeb + "?utm_source=email#note_99"} {
		if _, err := parseActionURL(raw, locatorOrigin); err != nil {
			t.Errorf("rejected %q: %v", raw, err)
		}
	}
	for _, raw := range []string{
		strings.Replace(locatorWeb, "https:", "http:", 1), strings.Replace(locatorWeb, "gitlab.example", "user@gitlab.example", 1), locatorWeb + "//", locatorWeb + "#", locatorWeb + "#other", locatorWeb + "#note_0", locatorWeb + "#note_099", locatorWeb + "#note_%39%39", locatorWeb + "#discussion_", locatorWeb + "#discussion_..", locatorWeb + "#discussion_a%2fb", locatorWeb + "#discussion_a%25b", locatorWeb + "\n", strings.Replace(locatorWeb, "/42", "/042", 1), strings.Replace(locatorWeb, "/42", "/9223372036854775808", 1), strings.Replace(locatorWeb, "group/sub", "group/../sub", 1), strings.Replace(locatorWeb, "group/sub", "group/%2e/sub", 1), strings.Replace(locatorWeb, "group/sub", "group%2Fsub", 1), strings.Replace(locatorWeb, "group/sub", "group%252Fsub", 1), strings.Replace(locatorWeb, "group/sub", "group//sub", 1), strings.Replace(locatorWeb, "group/sub", "group\\sub", 1), strings.Replace(locatorWeb, "group/sub", "group/%00sub", 1), strings.Replace(locatorWeb, "group/sub", "group/%FFsub", 1), strings.Repeat("x", 4097),
	} {
		if _, err := parseActionURL(raw, locatorOrigin); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, suffix := range []string{"?note_id=99", "?redirect=https://other.example", "?view=%ZZ", "/notes/99#note_100", "/notes/99#discussion_thread", "/notes/0", "/diffs/notes/99", "/notes/99/extra"} {
		if _, err := parseActionURL(locatorWeb+suffix, locatorOrigin); err == nil {
			t.Errorf("accepted ambiguous locator %q", suffix)
		}
	}
}

func TestGitlabResolveActionURL(t *testing.T) {
	for _, anchor := range []string{"", "#note_99", "#discussion_680f5bcbb2f1d20b8beff17953e04503799e674f"} {
		t.Run(anchor, func(t *testing.T) {
			calls := []string{}
			read := locatorRead(t, func(path string, _ url.Values) (string, http.Header, error) {
				calls = append(calls, path)
				return "", nil, nil
			})
			r, err := ResolveActionURL(t.Context(), platform.TargetLocator{URL: locatorWeb + anchor}, locatorAccess(), read, "user101")
			if err != nil || r.Status != platform.ResolutionResolved || !r.Complete || r.Target == nil || r.Target.Object.ID != restID(9001, "id") || r.Target.Message.Valid() != (anchor == "#note_99") || r.Target.Discussion.Valid() != (anchor != "") || len(r.Aliases) != 1 || r.Aliases[0].Object.ID != restID(42, "iid") || len(calls) != 3+btoi(anchor != "") {
				t.Fatal(r, err, calls)
			}
		})
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestGitlabResolveActionURLRejectsEvidence(t *testing.T) {
	for _, mode := range []string{"instance", "anchor", "user", "username", "project", "mr-project", "mr-iid", "mr-url", "thread-id", "thread-owner", "system", "duplicate", "missing", "bad-page", "missing-boundary", "budget", "oversize", "bad-json", "403", "404", "429", "redirect", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			raw := locatorWeb + "#note_99"
			if mode == "instance" {
				raw = strings.Replace(raw, locatorOrigin, "https://other.example", 1)
			}
			if mode == "anchor" {
				raw = locatorWeb + "#unknown"
			}
			if mode == "thread-id" {
				raw = locatorWeb + "#discussion_680f5bcbb2f1d20b8beff17953e04503799e674f"
			}
			calls := 0
			read := locatorRead(t, func(path string, q url.Values) (string, http.Header, error) {
				calls++
				if path == "/user" {
					switch mode {
					case "user":
						return `{"id":102,"username":"user101"}`, nil, nil
					case "username":
						return `{"id":101,"username":"other"}`, nil, nil
					case "oversize":
						return strings.Repeat("x", (1<<20)+1), nil, nil
					case "bad-json":
						return `{}`, nil, nil
					case "403", "404", "429", "redirect":
						status := map[string]int{"403": 403, "404": 404, "429": 429, "redirect": 302}[mode]
						return "", nil, &queryError{Code: "query_failed", Status: status}
					case "deadline":
						return "", nil, context.DeadlineExceeded
					}
				}
				if mode == "project" && strings.HasPrefix(path, "/projects/group") {
					return `{"id":123,"path_with_namespace":"other/project"}`, nil, nil
				}
				if path == "/projects/123/merge_requests/42" {
					switch mode {
					case "mr-project":
						return `{"id":9001,"iid":42,"project_id":124,"web_url":"` + locatorWeb + `"}`, nil, nil
					case "mr-iid":
						return `{"id":9001,"iid":43,"project_id":123,"web_url":"` + locatorWeb + `"}`, nil, nil
					case "mr-url":
						return `{"id":9001,"iid":42,"project_id":123,"web_url":"` + strings.Replace(locatorWeb, "group/sub/project", "other/project", 1) + `"}`, nil, nil
					}
				}
				if strings.Contains(path, "/discussions") {
					switch mode {
					case "thread-id":
						return strings.Replace(locatorThread, "680f5bcbb2f1d20b8beff17953e04503799e674f", "different", 1), nil, nil
					case "thread-owner":
						return `[` + strings.Replace(locatorThread, `"noteable_id":9001`, `"noteable_id":9002`, 1) + `]`, nil, nil
					case "system":
						return `[` + strings.Replace(locatorThread, `"noteable_id":9001`, `"system":true,"noteable_id":9001`, 1) + `]`, nil, nil
					case "duplicate":
						return `[` + locatorThread + `,` + locatorThread + `]`, nil, nil
					case "missing":
						return `[]`, nil, nil
					case "bad-page":
						return `[]`, http.Header{"X-Next-Page": {"7"}}, nil
					case "missing-boundary", "budget":
						page := 1
						if q.Get("page") != "" {
							fmt.Sscan(q.Get("page"), &page)
						}
						rows := []map[string]any{}
						for i := 0; i < 50; i++ {
							rows = append(rows, map[string]any{"id": fmt.Sprintf("thread-%d-%d", page, i), "individual_note": false, "notes": []map[string]any{{"id": int64(page*1000 + i), "noteable_id": 9001, "noteable_type": "MergeRequest"}}})
						}
						data, _ := json.Marshal(rows)
						h := http.Header{}
						if mode == "budget" {
							h.Set("X-Next-Page", fmt.Sprint(page+1))
						}
						return string(data), h, nil
					}
				}
				return "", nil, nil
			})
			r, err := ResolveActionURL(t.Context(), platform.TargetLocator{URL: raw}, locatorAccess(), read, "user101")
			if err != nil || r.Status == platform.ResolutionResolved || r.Complete || r.ReasonCode == "" || calls > 13 {
				t.Fatal(r, err, calls)
			}
			if (mode == "instance" || mode == "anchor") && calls != 0 {
				t.Fatal("invalid URL caused read", calls)
			}
			if mode == "404" && r.ReasonCode != "not_found_or_invisible" || mode == "429" && r.ReasonCode != "rate_limited" || mode == "deadline" && r.ReasonCode != "timeout" {
				t.Fatal(r)
			}
		})
	}
}

func TestGitlabResolveActionURLEarlyMappingAndEncoding(t *testing.T) {
	read := locatorRead(t, func(path string, q url.Values) (string, http.Header, error) {
		if strings.HasSuffix(path, "/discussions") {
			if q.Get("page") != "1" {
				t.Fatal("continued after exact mapping")
			}
			return `[` + locatorThread + `]`, http.Header{"X-Next-Page": {"2"}}, nil
		}
		return "", nil, nil
	})
	r, err := ResolveActionURL(t.Context(), platform.TargetLocator{URL: locatorWeb + "#note_99"}, locatorAccess(), read, "user101")
	if err != nil || !r.Complete {
		t.Fatal(r, err)
	}
	parsed, err := parseActionURL(locatorOrigin+"/group/%E9%A1%B9%E7%9B%AE/-/merge_requests/42", locatorOrigin)
	if err != nil || url.PathEscape(parsed.project) != "group%2F%E9%A1%B9%E7%9B%AE" {
		t.Fatal(parsed, err)
	}
}
