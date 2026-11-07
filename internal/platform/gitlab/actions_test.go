package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

const actionNoteJSON = `{"id":99,"body":"exact reply","system":false,"noteable_id":9001,"noteable_iid":42,"noteable_type":"MergeRequest","author":{"id":13,"username":"bot"}}`

func actionFixture(origin string, discussion bool) (platform.TargetActionRequest, platform.AccessContext) {
	_, a := referenceFixture(origin)
	target := platform.TargetIdentity{IdentityScope: a.Account.IdentityScope, Containers: []platform.NativeIdentity{{Type: "Project", ID: restID(123, "id")}}, Object: platform.NativeIdentity{Type: "MergeRequest", ID: restID(9001, "id")}}
	op := platform.OperationObjectComment
	if discussion {
		target.Discussion = platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: "thread"}
		op = platform.OperationDiscussionReply
	}
	alias := target
	alias.Object.ID = restID(42, "iid")
	return platform.TargetActionRequest{Target: target, Aliases: []platform.TargetIdentity{alias}, Operation: op, Input: json.RawMessage(`{"body":"exact reply"}`)}, a
}
func actionPayload(origin, path string) string {
	switch path {
	case "/api/v4/user":
		return `{"id":13,"username":"bot"}`
	case "/api/v4/projects/123":
		return `{"id":123,"path_with_namespace":"g/p"}`
	case "/api/v4/projects/123/merge_requests/42":
		return `{"id":9001,"iid":42,"project_id":123,"web_url":"` + origin + `/g/p/-/merge_requests/42"}`
	case "/api/v4/projects/123/merge_requests/42/discussions/thread":
		return `{"id":"thread","individual_note":false,"notes":[` + actionNoteJSON + `]}`
	default:
		return actionNoteJSON
	}
}
func TestReferenceActionNativeHTTP(t *testing.T) {
	for _, discussion := range []bool{false, true} {
		name := "object"
		if discussion {
			name = "discussion"
		}
		for _, mode := range []string{"ok", "wrong-account", "wrong-username", "wrong-mr", "wrong-iid", "wrong-project", "wrong-url", "wrong-project-label", "wrong-discussion", "individual", "wrong-note-object", "missing-note", "wrong-returned-author", "wrong-returned-body", "wrong-returned-id", "wrong-returned-iid", "system-note", "post-401", "post-403", "post-404", "post-408", "post-429", "post-502", "post-invalid", "post-oversize", "read-invalid"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				posts := 0
				var origin string
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer fixture-secret" {
						t.Error("credential lost")
					}
					data := actionPayload(origin, r.URL.Path)
					if r.Method == "POST" {
						posts++
						want := "/api/v4/projects/123/merge_requests/42/notes"
						if discussion {
							want = "/api/v4/projects/123/merge_requests/42/discussions/thread/notes"
						}
						if r.URL.Path != want {
							t.Error("wrong endpoint", r.URL)
						}
						var body map[string]string
						if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 1 || body["body"] != "exact reply" {
							t.Error("wrong body")
						}
						switch mode {
						case "post-401":
							w.WriteHeader(401)
							return
						case "post-403":
							w.WriteHeader(403)
							return
						case "post-404":
							w.WriteHeader(404)
							return
						case "post-408":
							w.WriteHeader(408)
							return
						case "post-429":
							w.WriteHeader(429)
							return
						case "post-502":
							w.WriteHeader(502)
							return
						case "post-invalid":
							data = `{}`
						case "post-oversize":
							data = strings.Repeat("x", (1<<20)+1)
						case "wrong-returned-author":
							data = strings.Replace(data, `"id":13`, `"id":14`, 1)
						case "wrong-returned-body":
							data = strings.Replace(data, "exact reply", "other", 1)
						case "wrong-returned-id":
							data = strings.Replace(data, `"id":99`, `"id":0`, 1)
						case "wrong-returned-iid":
							data = strings.Replace(data, `"noteable_iid":42`, `"noteable_iid":43`, 1)
						case "system-note":
							data = strings.Replace(data, `"system":false`, `"system":true`, 1)
						}
						w.WriteHeader(201)
					} else if r.Method == "GET" {
						switch {
						case mode == "wrong-account" && r.URL.Path == "/api/v4/user":
							data = `{"id":14,"username":"bot"}`
						case mode == "wrong-username" && r.URL.Path == "/api/v4/user":
							data = `{"id":13,"username":"other"}`
						case mode == "read-invalid":
							data = `{`
						case mode == "wrong-mr" && strings.HasSuffix(r.URL.Path, "/42"):
							data = strings.Replace(data, "9001", "9002", 1)
						case mode == "wrong-iid" && strings.HasSuffix(r.URL.Path, "/42"):
							data = strings.Replace(data, `"iid":42`, `"iid":43`, 1)
						case mode == "wrong-project" && strings.HasSuffix(r.URL.Path, "/42"):
							data = strings.Replace(data, "123", "124", 1)
						case mode == "wrong-url" && strings.HasSuffix(r.URL.Path, "/42"):
							data = strings.Replace(data, origin, "https://foreign.invalid", 1)
						case mode == "wrong-project-label" && r.URL.Path == "/api/v4/projects/123":
							data = `{"id":124,"path_with_namespace":"g/p"}`
						case mode == "wrong-discussion" && strings.HasSuffix(r.URL.Path, "/thread"):
							data = strings.Replace(data, `"thread"`, `"other"`, 1)
						case mode == "individual" && strings.HasSuffix(r.URL.Path, "/thread"):
							data = strings.Replace(data, `"individual_note":false`, `"individual_note":true`, 1)
						case mode == "wrong-note-object" && strings.HasSuffix(r.URL.Path, "/thread"):
							data = strings.Replace(data, "9001", "9002", 1)
						case mode == "missing-note" && posts > 0 && strings.HasSuffix(r.URL.Path, "/thread"):
							data = `{"id":"thread","notes":[]}`
						}
					} else {
						t.Error("unexpected method", r.Method)
					}
					_, _ = io.WriteString(w, data)
				}))
				defer server.Close()
				origin = server.URL
				q, a := actionFixture(origin, discussion)
				caps := ReferenceActions(NewReadAPI(origin, platform.NewSecret("fixture-secret"), server.Client().Transport), NewNoteAPI(origin, platform.NewSecret("fixture-secret"), server.Client().Transport), "bot")
				cap := caps[q.Operation]
				result, err := cap.Execute(context.Background(), q, a)
				before := mode == "wrong-account" || mode == "wrong-username" || mode == "wrong-mr" || mode == "wrong-iid" || mode == "wrong-project" || mode == "wrong-url" || mode == "wrong-project-label" || mode == "read-invalid" || (discussion && (mode == "wrong-discussion" || mode == "individual" || mode == "wrong-note-object"))
				want := "succeeded"
				count := 1
				retry := "never"
				if before {
					want = "not_applied"
					count = 0
				} else if strings.HasPrefix(mode, "post-") || strings.HasPrefix(mode, "wrong-returned") || mode == "system-note" || (discussion && mode == "missing-note") {
					want = "unknown"
				}
				if mode == "post-401" || mode == "post-403" || mode == "post-404" || mode == "post-429" {
					want = "not_applied"
				}
				if !discussion && (mode == "post-408" || mode == "post-429" || mode == "post-502") {
					retry = "transient"
				}
				if err != nil || result.Status != want || posts != count || result.RetryClass != retry {
					t.Fatal(result, err, posts, want, count, retry)
				}
				if want == "succeeded" && (result.ResultID != restID(99, "id") || result.URL != origin+"/g/p/-/merge_requests/42#note_99") {
					t.Fatal(result)
				}
				if mode == "ok" {
					i, err := cap.Inspect(context.Background(), q, a)
					if err != nil || i.AccountLabel != "bot" || i.ContainerLabel != "g/p" || posts != 1 {
						t.Fatal(i, err, posts)
					}
				}
			})
		}
	}
}
func TestReferenceActionInvalidEvidenceNeverQueries(t *testing.T) {
	for _, mode := range []string{"missing-iid", "conflicting-iid", "foreign-alias", "alias-id", "account", "instance", "project", "object", "issue", "message", "discussion", "dot-thread", "operation", "body", "extra", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			q, a := actionFixture("https://gitlab.invalid", true)
			switch mode {
			case "missing-iid":
				q.Aliases = nil
			case "conflicting-iid":
				alias := q.Aliases[0]
				alias.Object.ID = restID(43, "iid")
				q.Aliases = append(q.Aliases, alias)
			case "foreign-alias":
				q.Aliases[0].Instance = "https://other.invalid"
			case "alias-id":
				alias := q.Aliases[0]
				alias.Object.ID = restID(999, "id")
				q.Aliases = append(q.Aliases, alias)
			case "account":
				a.Receiver.ID = restID(99, "id")
			case "instance":
				q.Target.Instance = "https://other.invalid"
			case "project":
				q.Target.Containers[0].ID.Value = "123/notes"
			case "object":
				q.Target.Object.ID.Namespace = "gitlab:rest:iid"
			case "issue":
				q.Target.Object.Type = "Issue"
			case "message":
				q.Target.Message = restID(101, "id")
			case "discussion":
				q.Target.Discussion.Value = "thread/../notes"
			case "dot-thread":
				q.Target.Discussion.Value = ".."
			case "operation":
				q.Operation = platform.OperationObjectComment
			case "body":
				q.Input = []byte(`{"body":" "}`)
			case "extra":
				q.Input = []byte(`{"body":"exact reply","url":"anything"}`)
			case "trailing":
				q.Input = append(q.Input, []byte(` {}`)...)
			}
			read := func(context.Context, string, url.Values, http.Header, int64) (APIResponse, error) {
				t.Fatal("invalid input read")
				return APIResponse{}, nil
			}
			cap := ReferenceActions(read, nil, "bot")[q.Operation]
			if _, err := cap.Execute(context.Background(), q, a); !errors.Is(err, platform.ErrReference) {
				t.Fatal("accepted", err)
			}
		})
	}
}

func TestReferenceActionWriteBoundary(t *testing.T) {
	for _, mode := range []string{"redirect", "transport", "cancelled", "invalid-origin", "invalid-project", "invalid-thread", "redaction", "oversize", "invalid-json"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			secret := "fixture-secret"
			var origin string
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer "+secret {
					t.Error("wrong credential")
				}
				if mode == "redirect" {
					w.Header().Set("Location", origin+"/leak")
					w.WriteHeader(307)
					return
				}
				data := actionNoteJSON
				switch mode {
				case "redaction":
					data = strings.Replace(data, "exact reply", `fixture-\u0073ecret`, 1)
				case "oversize":
					data = strings.Repeat("x", (1<<20)+1)
				case "invalid-json":
					data = `{`
				}
				w.WriteHeader(201)
				_, _ = io.WriteString(w, data)
			}))
			defer server.Close()
			origin = server.URL
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			project, thread := "123", ""
			var transport http.RoundTripper = server.Client().Transport
			switch mode {
			case "cancelled":
				cancel()
			case "invalid-origin":
				origin = "https://user:pass@example.com"
			case "invalid-project":
				project = "123/../456"
			case "invalid-thread":
				thread = ".."
			case "transport":
				transport = roundTripFunc(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New(secret + " RAW ERROR") })
			}
			got, err := NewNoteAPI(origin, platform.NewSecret(secret), transport)(ctx, project, "42", thread, "exact reply")
			if mode == "redaction" {
				if err != nil || strings.Contains(string(got.Data), secret) || !strings.Contains(string(got.Data), "[REDACTED]") {
					t.Fatal(got, err)
				}
				return
			}
			var failure *noteError
			if !errors.As(err, &failure) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "RAW ERROR") {
				t.Fatal("unsafe or missing failure", err)
			}
			before := mode == "cancelled" || strings.HasPrefix(mode, "invalid-") && mode != "invalid-json"
			expected := 1
			if before {
				expected = 0
			}
			if calls != expected || failure.notApplied != before {
				t.Fatal(calls, expected, failure)
			}
		})
	}
}
