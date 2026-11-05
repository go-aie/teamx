package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func discussionQuery() DiscussionQuery {
	return DiscussionQuery{Source: platform.SourceRef{Provider: "gitlab", Instance: "https://gitlab.example.com", Channel: "events", ItemKey: "note-101", Reference: platform.NotificationReference{Version: 1, RecipientID: restID(13, "id"), Scope: "project", ProjectID: restID(123, "id"), Object: objectReference("MergeRequest", 9001, 42), NoteID: restID(101, "id")}}}
}
func discussionFixture(out any, value any) error {
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}
func TestDiscussionResolverBatchAliasesAndMappings(t *testing.T) {
	q := discussionQuery()
	q.Source.Reference.Object.ID = platform.ExternalID{}
	q2 := q
	q2.Source.Reference.Object = objectReference("MergeRequest", 9001, 0)
	q2.Source.Reference.NoteID = restID(102, "id")
	q2.Source.Channel = "todos"
	calls := []string{}
	get := func(_ context.Context, path string, out any) (http.Header, error) {
		calls = append(calls, path)
		switch {
		case path == "/api/v4/user":
			return nil, discussionFixture(out, map[string]any{"id": 13, "username": "bot"})
		case path == "/api/v4/projects/123/merge_requests/42":
			return nil, discussionFixture(out, resolvedObject{9001, 42, 123, "https://gitlab.example.com/renamed/project/-/merge_requests/42"})
		case strings.Contains(path, "discussions"):
			return nil, discussionFixture(out, []any{map[string]any{"id": "ordinary", "individual_note": true, "notes": []any{map[string]any{"id": 101}}}, map[string]any{"id": "thread", "individual_note": false, "notes": []any{map[string]any{"id": 102}}}})
		}
		return nil, fmt.Errorf("unexpected GET %s", path)
	}
	rs, e := ResolveDiscussions(context.Background(), q.Source.Instance, []DiscussionQuery{q, q2, q}, get)
	if e != nil || len(calls) != 3 || len(rs) != 3 {
		t.Fatal(rs, calls, e)
	}
	if rs[0].Object.ID != restID(9001, "id") || rs[0].DiscussionID.Value != "ordinary" || rs[1].DiscussionID.Value != "thread" || rs[1].Query.Source.Channel != "todos" || !rs[1].Complete {
		t.Fatal(rs)
	}
}
func TestDiscussionResolverDegradationAndNamespaceIsolation(t *testing.T) {
	for _, mode := range []string{"forbidden", "deleted", "limited", "full-without-boundary", "repeated-page", "missing-note", "opaque-note", "different-instance", "different-account", "conflicting-alias", "unsupported-type", "bad-project", "bad-object-id"} {
		t.Run(mode, func(t *testing.T) {
			q := discussionQuery()
			queries := []DiscussionQuery{q}
			switch mode {
			case "opaque-note":
				queries[0].Source.Reference.NoteID = platform.ExternalID{Namespace: "gitlab:graphql:gid", Value: "gid://gitlab/Note/101"}
			case "different-instance":
				queries[0].Source.Instance = "https://other.example.com"
			case "different-account":
				queries[0].Source.Reference.RecipientID = restID(99, "id")
			case "conflicting-alias":
				q.Source.Reference.Object.ID = restID(999, "id")
				queries = append(queries, q)
			case "unsupported-type":
				queries[0].Source.Reference.Object.Type = "WorkItem"
			case "bad-project":
				queries[0].Source.Reference.ProjectID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: "../123"}
			case "bad-object-id":
				queries[0].Source.Reference.Object.ID = platform.ExternalID{Namespace: "gitlab:rest:iid", Value: "9001"}
			}
			get := func(_ context.Context, path string, out any) (http.Header, error) {
				if path == "/api/v4/user" {
					return nil, discussionFixture(out, map[string]any{"id": 13, "username": "bot"})
				}
				if !strings.Contains(path, "discussions") {
					return nil, discussionFixture(out, resolvedObject{9001, 42, 123, "https://gitlab.example.com/g/p/-/merge_requests/42"})
				}
				if mode == "forbidden" || mode == "deleted" || mode == "limited" {
					return nil, errors.New(mode)
				}
				rows := []any{map[string]any{"id": "thread", "notes": []any{map[string]any{"id": 101}}}}
				h := http.Header{}
				switch mode {
				case "full-without-boundary":
					rows = nil
					for n := range 50 {
						rows = append(rows, map[string]any{"id": fmt.Sprint(n), "notes": []any{map[string]any{"id": n + 1}}})
					}
				case "repeated-page":
					h.Set("X-Next-Page", "1")
				case "missing-note":
					rows = []any{}
				}
				return h, discussionFixture(out, rows)
			}
			rs, e := ResolveDiscussions(context.Background(), q.Source.Instance, queries, get)
			if e != nil || rs[len(rs)-1].Status == "resolved" {
				t.Fatal(rs, e)
			}
			if mode == "different-account" || mode == "different-instance" || mode == "conflicting-alias" {
				if rs[len(rs)-1].ObjectVerified {
					t.Fatal("conflict reused identity", rs)
				}
			}
		})
	}
}
func TestDiscussionResolverURLCandidatesAndIDOnlyLookup(t *testing.T) {
	for _, mode := range []string{"valid", "other-project", "other-object", "other-instance", "bad-fragment", "no-fragment", "id-only", "id-not-found"} {
		t.Run(mode, func(t *testing.T) {
			q := discussionQuery()
			q.Source.Reference.NoteID = platform.ExternalID{}
			q.URL = "https://gitlab.example.com/g/p/-/merge_requests/42#note_101"
			switch mode {
			case "other-project":
				q.URL = "https://gitlab.example.com/other/p/-/merge_requests/42#note_101"
			case "other-object":
				q.URL = strings.Replace(q.URL, "42#", "9001#", 1)
			case "other-instance":
				q.URL = strings.Replace(q.URL, "gitlab.example.com", "evil.example.com", 1)
			case "bad-fragment":
				q.URL += "junk"
			case "no-fragment":
				q.URL = strings.Split(q.URL, "#")[0]
			case "id-only", "id-not-found":
				q.Source.Reference.Object.IID = platform.ExternalID{}
			}
			get := func(_ context.Context, path string, out any) (http.Header, error) {
				if path == "/api/v4/user" {
					return nil, discussionFixture(out, map[string]any{"id": 13, "username": "bot"})
				}
				if strings.Contains(path, "discussions") {
					return nil, discussionFixture(out, []any{map[string]any{"id": "thread", "notes": []any{map[string]any{"id": 101}}}})
				}
				obj := resolvedObject{9001, 42, 123, "https://gitlab.example.com/g/p/-/merge_requests/42"}
				if strings.Contains(path, "?") {
					if mode == "id-not-found" {
						obj.ID = 9002
					}
					return nil, discussionFixture(out, []resolvedObject{obj})
				}
				if strings.HasSuffix(path, "/9001") {
					t.Fatal("global ID used in IID path")
				}
				return nil, discussionFixture(out, obj)
			}
			rs, e := ResolveDiscussions(context.Background(), q.Source.Instance, []DiscussionQuery{q}, get)
			if e != nil {
				t.Fatal(e)
			}
			want := mode == "valid" || mode == "id-only"
			if (rs[0].DiscussionID.Value == "thread") != want {
				t.Fatal(rs)
			}
			if mode == "id-not-found" && rs[0].ObjectVerified {
				t.Fatal(rs)
			}
		})
	}
}
