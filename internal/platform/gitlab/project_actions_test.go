package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

// Exercise real HTTP routing from copied UI URLs through native identity,
// thread selection, execution and re-resolution. In particular /work_items/
// must never become a REST endpoint or a guessed global ID.
func TestProjectActionURLRoundTrip(t *testing.T) {
	for _, route := range []string{"merge_requests", "issues", "work_items"} {
		for _, mode := range []string{"comment", "thread", "reply", "note-path", "individual", "missing-focus", "stale-focus", "system", "foreign-note", "wrong-returned-author", "unknown-write"} {
			t.Run(route+"/"+mode, func(t *testing.T) {
				objectType, resource := "Issue", "issues"
				if route == "merge_requests" {
					objectType, resource = "MergeRequest", "merge_requests"
				}
				posts := 0
				removeFocus := false
				var origin string
				note := func(id int, body string) map[string]any {
					return map[string]any{"id": id, "body": body, "noteable_id": 9001, "noteable_type": objectType, "noteable_iid": 42, "system": false, "author": map[string]any{"id": 101, "username": "user101"}}
				}
				thread := func() map[string]any {
					original := note(98, "previous reply")
					if mode == "system" {
						original["system"] = true
					}
					if mode == "foreign-note" {
						original["noteable_id"] = 9002
					}
					notes := []any{original}
					if mode == "missing-focus" || removeFocus {
						notes = []any{note(97, "different comment")}
					}
					if posts > 0 {
						notes = append(notes, note(99, "exact reply"))
					}
					return map[string]any{"id": "thread", "individual_note": mode == "individual" && posts == 0, "notes": notes}
				}
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer fixture-secret" {
						t.Error("wrong account credential")
					}
					base := "/api/v4/projects/123/" + resource + "/42"
					var value any
					if r.Method == http.MethodPost {
						posts++
						want := base + "/discussions/thread/notes"
						if mode == "comment" {
							want = base + "/notes"
						}
						var input map[string]string
						if r.URL.Path != want || json.NewDecoder(r.Body).Decode(&input) != nil || len(input) != 1 || input["body"] != "exact reply" {
							t.Error("wrong target or body", r.URL.Path, input)
						}
						if mode == "unknown-write" {
							w.WriteHeader(http.StatusBadGateway)
							return
						}
						value = note(99, "exact reply")
						if mode == "wrong-returned-author" {
							value.(map[string]any)["author"] = map[string]any{"id": 102, "username": "other"}
						}
						w.WriteHeader(http.StatusCreated)
					} else {
						switch r.URL.Path {
						case "/api/v4/user":
							value = map[string]any{"id": 101, "username": "user101"}
						case "/api/v4/projects/group/sub/project", "/api/v4/projects/123":
							value = map[string]any{"id": 123, "path_with_namespace": "group/sub/project"}
						case base:
							value = map[string]any{"id": 9001, "iid": 42, "project_id": 123, "web_url": origin + "/group/sub/project/-/" + route + "/42"}
						case base + "/discussions":
							value = []any{thread()}
						case base + "/discussions/thread":
							value = thread()
						default:
							t.Error("unexpected read", r.URL)
							w.WriteHeader(404)
							return
						}
					}
					_ = json.NewEncoder(w).Encode(value)
				}))
				defer server.Close()
				origin = server.URL
				access := locatorAccess()
				access.Account.Instance, access.Receiver.Instance = origin, origin
				read := NewReadAPI(origin, platform.NewSecret("fixture-secret"), server.Client().Transport)
				web := origin + "/group/sub/project/-/" + route + "/42"
				raw := web + "#note_98"
				if mode == "comment" {
					raw = web
				} else if mode == "thread" {
					raw = web + "#discussion_thread"
				} else if mode == "note-path" {
					raw = web + "/notes/98?utm_source=email"
				}
				r, err := ResolveActionURL(t.Context(), platform.TargetLocator{URL: raw}, access, read, "user101")
				if mode == "missing-focus" || mode == "system" || mode == "foreign-note" {
					if err != nil || r.Status != platform.ResolutionPartial || !r.ObjectVerified || r.Complete || r.DiscussionVerified || r.MessageVerified || posts != 0 {
						t.Fatal("failed focus became executable or erased object evidence", r, err)
					}
					return
				}
				if err != nil || !r.Complete || r.Target == nil || r.Target.Object.Type != objectType || r.URL != web || r.MessageVerified != (mode != "comment" && mode != "thread") {
					t.Fatal(r, err)
				}
				if mode == "reply" || mode == "individual" {
					source := platform.SourceRef{Provider: "gitlab", Instance: origin, Channel: "events", SubscriptionID: "subscription", ItemKey: "note-98", URL: raw, Reference: platform.NotificationReference{Version: 1, RecipientID: restID(101, "id"), Scope: "project", ProjectID: restID(123, "id"), Object: objectReference(objectType, 9001, 42), NoteID: restID(98, "id")}}
					captured, err := ResolveTarget(t.Context(), platform.TargetLocator{Source: &source}, access, read)
					if err != nil || !captured.Complete || !reflect.DeepEqual(captured.Target, r.Target) || !reflect.DeepEqual(captured.Aliases, r.Aliases) {
						t.Fatal("URL and notification disagreed on native target", captured, r, err)
					}
				}
				q := platform.TargetActionRequest{Target: *r.Target, Aliases: r.Aliases, Operation: platform.OperationDiscussionReply, Input: json.RawMessage(`{"body":"exact reply"}`)}
				if mode == "comment" {
					q.Operation = platform.OperationObjectComment
				}
				removeFocus = mode == "stale-focus"
				write := NewNoteAPI(origin, platform.NewSecret("fixture-secret"), server.Client().Transport)
				cap := ReferenceActions(read, write, "user101")[q.Operation]
				result, err := cap.Execute(t.Context(), q, access)
				if mode == "stale-focus" {
					if err != nil || result.Status != "not_applied" || posts != 0 {
						t.Fatal("deleted comment focus reached write transport", result, err, posts)
					}
					return
				}
				if err != nil || posts != 1 {
					t.Fatal(result, err, posts)
				}
				if mode == "wrong-returned-author" || mode == "unknown-write" {
					if result.Status != "unknown" || result.RetryClass != "never" {
						t.Fatal("uncertain reply was accepted or made retryable", result)
					}
					return
				}
				if result.Status != "succeeded" || result.URL != web+"#note_99" || result.ResultID != restID(99, "id") || !reflect.DeepEqual(result.Target, *r.Target) {
					t.Fatal(result)
				}
				stored, err := ResolveStoredActionTarget(t.Context(), platform.TargetLocator{Candidate: r.Target, VerifiedAliases: r.Aliases}, access, read, "user101")
				if err != nil || !stored.Complete || stored.MessageVerified != r.MessageVerified || !reflect.DeepEqual(stored.Target, r.Target) || posts != 1 {
					t.Fatal("stored target lost comment focus", stored, err)
				}
			})
		}
	}
}

func TestProjectURLAliasesAndUnsupportedScope(t *testing.T) {
	// A /work_items/ URL can be returned as /issues/ by older GitLab versions,
	// and both spellings must keep one canonical native identity.
	var first *platform.TargetIdentity
	for _, route := range []string{"issues", "work_items"} {
		web := strings.Replace(locatorWeb, "merge_requests", route, 1)
		read := locatorRead(t, func(path string, _ url.Values) (string, http.Header, error) {
			if path == "/projects/123/issues/42" {
				return `{"id":9001,"iid":42,"project_id":123,"web_url":"` + strings.Replace(locatorWeb, "merge_requests", "work_items", 1) + `"}`, nil, nil
			}
			return "", nil, nil
		})
		r, err := ResolveActionURL(t.Context(), platform.TargetLocator{URL: web}, locatorAccess(), read, "user101")
		if err != nil || !r.Complete || r.Target.Object.Type != "Issue" || first != nil && !reflect.DeepEqual(first, r.Target) {
			t.Fatal(r, err)
		}
		first = r.Target
	}
	read := func(_ context.Context, _ string, _ url.Values, _ http.Header, _ int64) (APIResponse, error) {
		t.Fatal("unsupported scope should not query credentials or remote state")
		return APIResponse{}, nil
	}
	r, err := ResolveActionURL(t.Context(), platform.TargetLocator{URL: locatorOrigin + "/groups/group/sub/-/work_items/42"}, locatorAccess(), read, "user101")
	if err != nil || r.Status != platform.ResolutionUnsupported || r.ReasonCode != "unsupported_scope" || r.Target != nil {
		t.Fatal(r, err)
	}
}
