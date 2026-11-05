package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func referenceFixture(origin string) (platform.SourceRef, platform.AccessContext) {
	source := platform.SourceRef{Provider: "gitlab", Instance: origin, Channel: "events", SubscriptionID: "subscription", ItemKey: "note-101", URL: origin + "/g/p/-/merge_requests/42#note_101", Reference: platform.NotificationReference{Version: 1, RecipientID: restID(13, "id"), Scope: "project", ProjectID: restID(123, "id"), Object: objectReference("MergeRequest", 9001, 42), NoteID: restID(101, "id")}}
	account := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "gitlab", Instance: origin}, ID: restID(13, "id")}
	access := platform.AccessContext{RoomIdentity: platform.RoomIdentity{RoomID: "room", RoomInstance: "generation"}, AgentID: "agent", MembershipID: "member", Account: account, Receiver: account, ConnectionID: "connection", Generation: "generation", ConnectionVersion: 1, CredentialVersion: 1}
	return source, access
}
func referencePayload(origin, path string) any {
	switch {
	case path == "/api/v4/user":
		return map[string]any{"id": 13, "username": "bot"}
	case strings.HasSuffix(path, "/discussions"):
		return []any{map[string]any{"id": "thread", "individual_note": false, "notes": []any{map[string]any{"id": 101}}}}
	default:
		resource := "merge_requests"
		if strings.Contains(path, "/issues/") {
			resource = "issues"
		}
		return referenceObject{9001, 42, 123, origin + "/g/p/-/" + resource + "/42"}
	}
}
func referenceServer(t *testing.T, handler func(http.ResponseWriter, *http.Request, string)) *httptest.Server {
	t.Helper()
	var origin string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("PRIVATE-TOKEN") != "fixture-secret" {
			t.Error("unexpected method or credential")
			w.WriteHeader(403)
			return
		}
		handler(w, r, origin)
	}))
	origin = server.URL
	t.Cleanup(server.Close)
	return server
}
func TestReferenceNativeHTTPIdentityLayers(t *testing.T) {
	for _, mode := range []string{"thread", "individual", "issue", "issue-work-item", "work-item-wrong-id", "work-item-wrong-iid", "work-item-wrong-project", "work-item-foreign-origin", "mr-work-item", "iid-only", "id-only", "local", "conflict", "wrong-project", "wrong-url", "account-mismatch", "source-account", "graphql", "foreign-url"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := referenceServer(t, func(w http.ResponseWriter, r *http.Request, origin string) {
				calls++
				payload := referencePayload(origin, r.URL.Path)
				switch {
				case strings.Contains(mode, "work-item") && strings.HasSuffix(r.URL.Path, "/42"):
					obj := payload.(referenceObject)
					obj.WebURL = origin + "/g/p/-/work_items/42"
					switch mode {
					case "work-item-wrong-id":
						obj.ID = 999
					case "work-item-wrong-iid":
						obj.IID = 99
					case "work-item-wrong-project":
						obj.ProjectID = 999
					case "work-item-foreign-origin":
						obj.WebURL = "https://other.invalid/g/p/-/work_items/42"
					}
					payload = obj
				case mode == "individual" && strings.HasSuffix(r.URL.Path, "/discussions"):
					payload = []any{map[string]any{"id": "ordinary", "individual_note": true, "notes": []any{map[string]any{"id": 101}}}}
				case mode == "account-mismatch" && strings.HasSuffix(r.URL.Path, "/user"):
					payload = map[string]any{"id": 99, "username": "bot"}
				case strings.HasSuffix(r.URL.Path, "/merge_requests"):
					if r.URL.Query().Get("scope") != "all" || r.URL.Query().Get("per_page") != "50" {
						t.Error("object list query lost")
					}
					payload = []referenceObject{{9001, 42, 123, origin + "/g/p/-/merge_requests/42"}}
				case mode == "conflict" && strings.HasSuffix(r.URL.Path, "/42"):
					obj := payload.(referenceObject)
					obj.ID = 999
					payload = obj
				case mode == "wrong-project" && strings.HasSuffix(r.URL.Path, "/42"):
					obj := payload.(referenceObject)
					obj.ProjectID = 999
					payload = obj
				case mode == "wrong-url" && strings.HasSuffix(r.URL.Path, "/42"):
					obj := payload.(referenceObject)
					obj.WebURL = "https://other.invalid/g/p/-/merge_requests/42"
					payload = obj
				}
				_ = json.NewEncoder(w).Encode(payload)
			})
			source, access := referenceFixture(server.URL)
			switch mode {
			case "issue", "issue-work-item", "work-item-wrong-id", "work-item-wrong-iid", "work-item-wrong-project", "work-item-foreign-origin":
				source.Reference.Object.Type = "Issue"
				source.URL = server.URL + "/g/p/-/issues/42#note_101"
			case "iid-only":
				source.Reference.Object.ID = platform.ExternalID{}
			case "id-only":
				source.Reference.Object.IID = platform.ExternalID{}
			case "local":
				source.Reference.NoteID = platform.ExternalID{}
				source.URL = ""
			case "source-account":
				source.Reference.RecipientID = restID(99, "id")
			case "graphql":
				source.Reference.Object.ID = platform.ExternalID{Namespace: "gitlab:graphql:gid", Value: "gid://gitlab/MergeRequest/9001"}
			case "foreign-url":
				source.Reference.NoteID = platform.ExternalID{}
				source.URL = "https://other.invalid/g/p/-/merge_requests/42#note_101"
			}
			result, err := ResolveTarget(context.Background(), platform.TargetLocator{Source: &source}, access, NewReadAPI(server.URL, platform.NewSecret("fixture-secret"), server.Client().Transport))
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "conflict", "wrong-project", "wrong-url", "account-mismatch", "source-account", "work-item-wrong-id", "work-item-wrong-iid", "work-item-wrong-project", "work-item-foreign-origin", "mr-work-item":
				if result.Status != platform.ResolutionMismatch || result.ObjectVerified || result.Target != nil || calls > 2 {
					t.Fatal(result, calls)
				}
			case "graphql":
				if result.Status != platform.ResolutionUnsupported || calls != 0 {
					t.Fatal(result, calls)
				}
			case "foreign-url":
				if result.Status != platform.ResolutionPartial || result.DiscussionVerified || !result.ObjectVerified {
					t.Fatal(result)
				}
			default:
				if result.Status != platform.ResolutionResolved || !result.ObjectVerified || !result.Complete || result.Target.Object.ID != restID(9001, "id") || len(result.Aliases) != 1 || result.Aliases[0].Object.ID != restID(42, "iid") {
					t.Fatal(result)
				}
				if mode == "local" {
					if calls != 0 {
						t.Fatal("native source made remote request")
					}
				} else if !result.MessageVerified || result.DiscussionVerified == (mode == "individual") {
					t.Fatal(result)
				}
			}
		})
	}
}
func TestReferenceHTTPFailuresKeepStepAndObject(t *testing.T) {
	for _, step := range []string{"user", "object", "discussions"} {
		for _, status := range []int{401, 403, 404, 429, 500, 502, 200} {
			for _, native := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%d/native=%t", step, status, native), func(t *testing.T) {
					calls := 0
					server := referenceServer(t, func(w http.ResponseWriter, r *http.Request, origin string) {
						calls++
						hit := (step == "user" && strings.HasSuffix(r.URL.Path, "/user")) || (step == "object" && strings.HasSuffix(r.URL.Path, "/42")) || (step == "discussions" && strings.HasSuffix(r.URL.Path, "/discussions"))
						if hit {
							w.WriteHeader(status)
							_, _ = w.Write([]byte("secret-remote-body-invalid-json"))
							return
						}
						_ = json.NewEncoder(w).Encode(referencePayload(origin, r.URL.Path))
					})
					source, access := referenceFixture(server.URL)
					if !native {
						source.Reference.Object.ID = platform.ExternalID{}
					}
					result, err := ResolveTarget(context.Background(), platform.TargetLocator{Source: &source}, access, NewReadAPI(server.URL, platform.NewSecret("fixture-secret"), server.Client().Transport))
					want := map[string]string{"user": "账号读取", "object": "对象读取", "discussions": "讨论读取"}[step]
					if err != nil || !strings.Contains(result.Reason, want) || strings.Contains(result.Reason, "note") || strings.Contains(result.Reason, "secret") || result.DiscussionVerified || result.Complete {
						t.Fatal(result, err)
					}
					verified := native || step == "discussions"
					if result.ObjectVerified != verified || (result.Status == platform.ResolutionPartial) != verified || (result.Target != nil) != verified {
						t.Fatal(result)
					}
					code := "query_failed"
					if status == 401 || status == 403 {
						code = "permission_denied"
					}
					if status == 200 {
						code = "invalid_response"
					}
					if result.ReasonCode != code || (status != 200 && !strings.Contains(result.Reason, strconv.Itoa(status))) {
						t.Fatal(result)
					}
					if calls != map[string]int{"user": 1, "object": 2, "discussions": 3}[step] {
						t.Fatal(calls)
					}
				})
			}
		}
	}
}
func TestReferenceBoundedPaginationAndMapping(t *testing.T) {
	for _, mode := range []string{"missing-note", "source-thread-conflict", "full-no-header", "bad-next", "duplicate-note", "duplicate-thread", "oversize-notes", "ten-pages", "five-object-pages", "invalid-json", "oversize-body", "url-note", "opaque-note"} {
		t.Run(mode, func(t *testing.T) {
			pages := 0
			server := referenceServer(t, func(w http.ResponseWriter, r *http.Request, origin string) {
				if strings.HasSuffix(r.URL.Path, "/merge_requests") {
					pages++
					w.Header().Set("X-Next-Page", strconv.Itoa(pages+1))
					_ = json.NewEncoder(w).Encode([]referenceObject{})
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/discussions") {
					_ = json.NewEncoder(w).Encode(referencePayload(origin, r.URL.Path))
					return
				}
				pages++
				rows := []any{map[string]any{"id": "thread", "notes": []any{map[string]any{"id": 101}}}}
				switch mode {
				case "missing-note":
					rows = []any{}
				case "full-no-header", "ten-pages":
					rows = nil
					for n := range 50 {
						rows = append(rows, map[string]any{"id": fmt.Sprintf("thread-%d-%d", pages, n), "notes": []any{map[string]any{"id": (pages-1)*50 + n + 101}}})
					}
					if mode == "ten-pages" {
						w.Header().Set("X-Next-Page", strconv.Itoa(pages+1))
					}
				case "bad-next":
					w.Header().Set("X-Next-Page", "1")
				case "duplicate-note":
					rows = append(rows, map[string]any{"id": "another", "notes": []any{map[string]any{"id": 101}}})
				case "duplicate-thread":
					rows = append(rows, rows[0])
				case "oversize-notes":
					notes := []any{}
					for n := range 501 {
						notes = append(notes, map[string]any{"id": n + 101})
					}
					rows = []any{map[string]any{"id": "thread", "notes": notes}}
				case "invalid-json":
					_, _ = w.Write([]byte(`[{"id":7}]`))
					return
				case "oversize-body":
					_, _ = w.Write([]byte(strings.Repeat("x", (1<<20)+1)))
					return
				}
				_ = json.NewEncoder(w).Encode(rows)
			})
			source, access := referenceFixture(server.URL)
			switch mode {
			case "source-thread-conflict":
				source.Reference.DiscussionID = platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: "other"}
			case "url-note":
				source.Reference.NoteID = platform.ExternalID{}
			case "opaque-note":
				source.Reference.NoteID = platform.ExternalID{Namespace: "gitlab:graphql:gid", Value: "gid://gitlab/Note/101"}
			case "five-object-pages":
				source.Reference.Object.IID = platform.ExternalID{}
			}
			result, err := ResolveTarget(context.Background(), platform.TargetLocator{Source: &source}, access, NewReadAPI(server.URL, platform.NewSecret("fixture-secret"), server.Client().Transport))
			if err != nil || !result.ObjectVerified {
				t.Fatal(result, err)
			}
			if mode == "url-note" {
				if !result.DiscussionVerified || result.Status != platform.ResolutionResolved {
					t.Fatal(result)
				}
				return
			}
			if result.Status != platform.ResolutionPartial || result.DiscussionVerified || result.MessageVerified || result.Complete || result.ReasonCode == "" {
				t.Fatal(result)
			}
			if mode == "ten-pages" && pages != 10 || mode == "five-object-pages" && pages != 5 {
				t.Fatal("unbounded pages", pages)
			}
		})
	}
}
func TestReferenceBatchBudgetAliasesAndIsolation(t *testing.T) {
	calls := 0
	server := referenceServer(t, func(w http.ResponseWriter, r *http.Request, origin string) {
		calls++
		_ = json.NewEncoder(w).Encode(referencePayload(origin, r.URL.Path))
	})
	source, access := referenceFixture(server.URL)
	first := source
	first.Reference.Object.ID = platform.ExternalID{}
	second := source
	second.Reference.Object.IID = platform.ExternalID{}
	conflicting := source
	conflicting.Reference.Object.ID = restID(999, "id")
	queries := []platform.TargetLocator{{Source: &first}, {Source: &second}, {Source: &source}, {Source: &conflicting}}
	read := NewReadAPI(server.URL, platform.NewSecret("fixture-secret"), server.Client().Transport)
	results, err := ResolveTargets(context.Background(), queries, access, read)
	if err != nil || calls != 3 || !results[0].DiscussionVerified || !results[1].DiscussionVerified || results[3].ObjectVerified || results[3].ReasonCode != "identity_conflict" {
		t.Fatal(results, calls, err)
	}
	queries = nil
	for n := range 6 {
		q := source
		q.Reference.ProjectID = restID(int64(n+1), "id")
		queries = append(queries, platform.TargetLocator{Source: &q})
	}
	calls = 0
	results, err = ResolveTargets(context.Background(), queries, access, read)
	if err != nil || calls != 6 || results[5].ReasonCode != "incomplete" {
		t.Fatal(results, calls, err)
	}
	if _, err := ResolveTargets(context.Background(), make([]platform.TargetLocator, 26), access, read); err == nil {
		t.Fatal("input bound missing")
	}
	other := access
	other.RoomID = "another"
	other.Account.ID = restID(99, "id")
	calls = 0
	result, err := ResolveTarget(context.Background(), platform.TargetLocator{Source: &source}, other, read)
	if err != nil || result.Status != platform.ResolutionMismatch || calls != 0 {
		t.Fatal(result, calls, err)
	}
}
