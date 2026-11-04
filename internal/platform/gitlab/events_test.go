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

func eventTestBinding() Binding {
	return Binding{Version: 1, RoomID: "room", RoomInstance: "generation", URL: "https://gitlab.example.com", Project: "group/project", TokenEnv: "ROOM_PAT", ProjectID: 7}
}

const rawEventFixture = `{"id":9,"project_id":7,"action_name":"opened","target_type":"MergeRequest","target_id":70,"target_iid":42,"target_title":"修复登录失败","created_at":"2026-11-01T12:00:00Z","author":{"id":2,"username":"alice","name":"Alice"}}`

func eventFetcher(t *testing.T, body string, h http.Header) (*GoEventFetcher, *int) {
	t.Helper()
	read, calls := fixtureRead(t, body, h)
	return &GoEventFetcher{binding: eventTestBinding(), read: read}, calls
}
func TestEventsGoSinglePageAndWindow(t *testing.T) {
	f, calls := eventFetcher(t, "["+rawEventFixture+"]", http.Header{"X-Next-Page": {"2"}})
	b, err := f.FetchEvents(context.Background(), EventQuery{})
	if err != nil || *calls != 1 || b.Complete || b.NextPage != 2 || b.Events[0].Title != "修复登录失败" || b.Events[0].URL != "https://gitlab.example.com/group/project/-/merge_requests/42" || b.ResponseBytes != int64(len("["+rawEventFixture+"]")) {
		t.Fatal(b, err)
	}
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	body := "[" + strings.Replace(rawEventFixture, "2026-11-01T12:00:00Z", "2026-10-31T23:59:59Z", 1) + "," + strings.Replace(rawEventFixture, `"id":9`, `"id":10`, 1) + "," + strings.Replace(rawEventFixture, "2026-11-01T12:00:00Z", "2026-11-02T00:00:00Z", 1) + "]"
	f.read = NewReadAPI(f.binding.URL, platform.NewSecret("secret"), roundTripFunc(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		if r.URL.Path != "/api/v4/projects/7/events" || q.Get("sort") != "desc" || q.Get("page") != "2" || q.Get("after") != "2026-10-31" || q.Get("before") != "2026-11-03" || r.Header.Get("If-None-Match") != "" {
			t.Fatal(r.URL)
		}
		return fixtureResponse(body, 200, http.Header{"X-Next-Page": {"3"}, "Etag": {`"window"`}}), nil
	}))
	b, err = f.FetchEvents(context.Background(), EventQuery{Mode: "window", Page: 2, Start: start, End: end})
	if err != nil || len(b.Events) != 1 || b.Events[0].ID != 10 || b.NextPage != 3 || b.ETag != "" {
		t.Fatal(b, err)
	}
}
func TestEventsGoNormalization(t *testing.T) {
	var base map[string]any
	json.Unmarshal([]byte(rawEventFixture), &base)
	cases := []struct {
		patch map[string]any
		check func(Event) bool
	}{
		{map[string]any{"target_type": "Note", "action_name": "commented on", "note": map[string]any{"id": 1312, "body": "comment", "noteable_type": "MergeRequest", "noteable_id": 70, "noteable_iid": 42}}, func(e Event) bool {
			return e.Kind == "note" && e.NoteID == 1312 && strings.HasSuffix(e.URL, "/merge_requests/42#note_1312")
		}},
		{map[string]any{"target_type": nil, "push_data": map[string]any{"ref": "main", "ref_type": "branch", "commit_to": strings.Repeat("a", 40)}}, func(e Event) bool { return e.Kind == "push" && e.Branch == "main" && len(e.SHA) == 40 }},
		{map[string]any{"target_type": nil, "push_data": map[string]any{"ref_count": 3, "ref": nil, "commit_to": nil}}, func(e Event) bool {
			return e.Kind == "push" && e.DetailsMissing && e.RefCount == 3 && e.SHA == "" && e.Branch == ""
		}},
		{map[string]any{"target_type": "DiscussionNote"}, func(e Event) bool { return e.Kind == "unknown" && e.SkipReason == "unsupported_event" }},
		{map[string]any{"target_type": "Issue", "author": nil, "author_username": "fallback"}, func(e Event) bool { return e.Kind == "unknown" && e.Author.Username == "fallback" }},
	}
	for _, tc := range cases {
		raw := map[string]any{}
		for k, v := range base {
			raw[k] = v
		}
		for k, v := range tc.patch {
			raw[k] = v
		}
		data, _ := json.Marshal([]any{raw})
		f, _ := eventFetcher(t, string(data), nil)
		b, err := f.FetchEvents(context.Background(), EventQuery{})
		if err != nil || !tc.check(b.Events[0]) {
			t.Fatal(b, err)
		}
	}
}
func TestEventsGoValidationETagAndSafeErrors(t *testing.T) {
	f, calls := eventFetcher(t, "[]", nil)
	for _, q := range []EventQuery{{Page: 2}, {PerPage: 101}, {ETag: "bad\nheader"}, {Mode: "bad"}, {Mode: "window"}, {Mode: "window", Start: time.Now(), End: time.Now().Add(time.Hour), ETag: "old"}} {
		if _, err := f.FetchEvents(context.Background(), q); err == nil {
			t.Fatal(q)
		}
	}
	if *calls != 0 {
		t.Fatal("invalid queries reached HTTP")
	}
	for _, body := range []string{"{}", "null", "broken", strings.Replace("["+rawEventFixture+"]", `"id":9`, `"id":true`, 1), strings.Replace("["+rawEventFixture+"]", `"project_id":7`, `"project_id":8`, 1), strings.Replace("["+rawEventFixture+"]", `"id":9`, `"id":0`, 1), strings.Replace("["+rawEventFixture+"]", `2026-11-01T12:00:00Z`, `2026-11-01T12:00:00`, 1)} {
		f, _ := eventFetcher(t, body, nil)
		if _, err := f.FetchEvents(context.Background(), EventQuery{}); err == nil {
			t.Fatal(body)
		}
	}
	for _, status := range []int{200, 304, 401, 403, 429} {
		f.read = NewReadAPI(f.binding.URL, platform.NewSecret("secret"), roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("If-None-Match") != `"old"` {
				t.Fatal("missing validator")
			}
			return fixtureResponse("[]", status, http.Header{"Etag": {`"new"`}, "Poll-Interval": {"9"}}), nil
		}))
		b, err := f.FetchEvents(context.Background(), EventQuery{ETag: `"old"`})
		if status == 200 {
			if err != nil || b.NotModified || !b.Complete {
				t.Fatal(b, err)
			}
		} else if status == 304 {
			if err != nil || !b.NotModified || b.Complete || b.ETag != `"new"` || b.RetryAfterSeconds != 9 {
				t.Fatal(b, err)
			}
		} else {
			var e *EventQueryError
			if !errors.As(err, &e) || e.HTTPStatus != status || e.RetryAfterSeconds != 9 {
				t.Fatal(err)
			}
		}
	}
}
func TestEventsGoProjectVerificationNoResources(t *testing.T) {
	for _, body := range []string{`{"id":7,"path_with_namespace":"group/project","http_url_to_repo":"https://gitlab.example.com/group/project.git"}`, `{"id":8,"path_with_namespace":"other/project","http_url_to_repo":"https://gitlab.example.com/other/project.git"}`, `{"id":0}`, `bad`} {
		read, calls := fixtureRead(t, body, nil)
		f, err := NewEventFetcher(context.Background(), eventTestBinding(), read)
		if strings.Contains(body, `"id":7`) {
			if err != nil || f.ProjectID() != 7 || *calls != 1 {
				t.Fatal(err)
			}
		} else if err == nil {
			t.Fatal("unverified project")
		}
	}
	b := eventTestBinding()
	b.URL = "https://user:password@gitlab.example.com"
	if _, err := NewEventFetcher(context.Background(), b, nil); err == nil {
		t.Fatal("URL credential accepted")
	}
}
