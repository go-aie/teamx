//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func ge04PublicEvidence() (RoomSnapshot, ge04Event) {
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	source := IntegrationSource{Provider: "gitlab", Channel: "events", Instance: "https://gitlab.example.com", ProjectID: 7, ItemKey: "https://gitlab.example.com|7|42", SubscriptionID: strings.Repeat("a", 32)}
	post := RoomMessage{Seq: 2, Author: Author{Kind: "integration", ID: "gitlab"}, ClientMessageID: "delivery", Source: &source, CreatedAt: at.Add(time.Second)}
	reply := RoomMessage{Seq: 3, Author: Author{Kind: "agent", ID: "reviewer"}, ReplyTo: 2, Text: "GE04-test event=42 seq=2 iid=9 head=" + strings.Repeat("b", 40) + " state=opened"}
	return RoomSnapshot{Messages: []RoomMessage{post, reply}, IntegrationReceipts: map[string]IntegrationReceipt{"delivery": {Seq: 2, Source: source}}}, ge04Event{ID: 42, IID: 9, CreatedAt: at}
}

func TestGitLabGE04RejectsIncompletePublicEvidence(t *testing.T) {
	s, e := ge04PublicEvidence()
	if post, reply, err := ge04Messages(s, e, "GE04-test"); err != nil || post.Seq != 2 || reply.Seq != 3 {
		t.Fatal(post, reply, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*RoomSnapshot)
	}{
		{"duplicate-event", func(s *RoomSnapshot) { s.Messages = append(s.Messages, s.Messages[0]) }},
		{"duplicate-reply", func(s *RoomSnapshot) { s.Messages = append(s.Messages, s.Messages[1]) }},
		{"human-spoof", func(s *RoomSnapshot) { s.Messages[0].Author.Kind = "human" }},
		{"missing-receipt", func(s *RoomSnapshot) { s.IntegrationReceipts = nil }},
		{"wrong-receipt-seq", func(s *RoomSnapshot) {
			r := s.IntegrationReceipts["delivery"]
			r.Seq++
			s.IntegrationReceipts["delivery"] = r
		}},
		{"wrong-receipt-source", func(s *RoomSnapshot) {
			r := s.IntegrationReceipts["delivery"]
			r.Source.ProjectID++
			s.IntegrationReceipts["delivery"] = r
		}},
		{"wrong-id", func(s *RoomSnapshot) {
			s.Messages[1].Text = strings.Replace(s.Messages[1].Text, "event=42", "event=41", 1)
		}},
		{"wrong-source-seq", func(s *RoomSnapshot) { s.Messages[1].Text = strings.Replace(s.Messages[1].Text, "seq=2", "seq=1", 1) }},
		{"wrong-iid", func(s *RoomSnapshot) { s.Messages[1].Text = strings.Replace(s.Messages[1].Text, "iid=9", "iid=8", 1) }},
		{"missing-sha", func(s *RoomSnapshot) {
			s.Messages[1].Text = strings.Replace(s.Messages[1].Text, strings.Repeat("b", 40), "short", 1)
		}},
		{"clock-skew", func(s *RoomSnapshot) { s.Messages[0].CreatedAt = e.CreatedAt.Add(-time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e := ge04PublicEvidence()
			tc.change(&s)
			if _, _, err := ge04Messages(s, e, "GE04-test"); err == nil {
				t.Fatal("accepted invalid evidence")
			}
		})
	}
	for _, index := range []int{0, 1} {
		s, e := ge04PublicEvidence()
		s.Messages = s.Messages[:index]
		post, reply, err := ge04Messages(s, e, "GE04-test")
		if err != nil || reply.Seq != 0 || index == 0 && post.Seq != 0 {
			t.Fatal("incomplete evidence should remain pending", post, reply, err)
		}
	}
}

func TestGitLabGE04TracePreservesTransportAndScopes(t *testing.T) {
	s := &ge04Trace{path: filepath.Join(t.TempDir(), "queries.jsonl")}
	batch := gitlab.EventBatch{Version: 1, Events: []gitlab.Event{{ID: 42, TargetIID: 9, Kind: "merge_request", Action: "opened", Title: "private-title", NoteBody: "private-body"}, {ID: 43, Kind: "push"}}, ResponseBytes: 123, ETag: "private-etag", NextPage: 2}
	q := gitlab.EventQuery{Mode: "quick", Page: 1, PerPage: 20}
	f := s.wrap(eventFetchFunc(func(_ context.Context, got gitlab.EventQuery) (gitlab.EventBatch, error) {
		if got != q {
			t.Fatal(got)
		}
		return batch, nil
	}), "service-1")
	got, err := f.FetchEvents(context.Background(), q)
	if err != nil || !reflect.DeepEqual(got, batch) {
		t.Fatal(got, err)
	}
	if _, ok := s.target("private-title", "service-2"); ok {
		t.Fatal("crossed restart scope")
	}
	e, ok := s.target("private-title", "service-1")
	if !ok || e.ID != 42 {
		t.Fatal(e, ok)
	}
	row, ok := s.first(42, "service-1", "quick")
	if !ok || row.Bytes != 123 || !row.HasETag || row.Conditional || row.Ended.Before(row.Started) || !reflect.DeepEqual(row.EventIDs, []int64{42, 43}) {
		t.Fatal(row)
	}
	if _, ok := s.first(42, "service-1", "window"); ok {
		t.Fatal("quick check masqueraded as backfill")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-title", "private-body", "private-etag"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("external data leaked in query trace")
		}
	}
	var saved ge04Query
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(s.path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	transportErr := errors.New("private-transport-error")
	bad := s.wrap(eventFetchFunc(func(context.Context, gitlab.EventQuery) (gitlab.EventBatch, error) {
		return gitlab.EventBatch{}, transportErr
	}), "offline-probe")
	if _, err := bad.FetchEvents(context.Background(), q); err != transportErr {
		t.Fatal(err)
	}
	if !s.queries[1].Failed {
		t.Fatal("failed request not recorded")
	}
	data, _ = os.ReadFile(s.path)
	if strings.Contains(string(data), transportErr.Error()) {
		t.Fatal("raw error leaked")
	}
	broken := &ge04Trace{path: filepath.Join(t.TempDir(), "missing", "trace")}
	if _, err := broken.wrap(f, "service-2").FetchEvents(context.Background(), q); err != nil || broken.failure() == nil {
		t.Fatal("trace failure changed query or went unreported", err)
	}
}

func TestGitLabGE04RejectsUnmeasuredOrChangingIdleLoad(t *testing.T) {
	start := time.Now().UTC()
	row := ge04Query{Run: "service-1", Mode: "quick", Started: start.Add(time.Second), Ended: start.Add(2 * time.Second), Bytes: 100, EventIDs: []int64{2, 1}}
	second := row
	second.Started = start.Add(6 * time.Second)
	second.Ended = start.Add(7 * time.Second)
	s := &ge04Trace{queries: []ge04Query{row, second}}
	load, err := s.idle(start, start.Add(time.Minute))
	if err != nil || load["quick_calls"] != 2 || load["response_bytes"] != int64(200) {
		t.Fatal(load, err)
	}
	for _, mode := range []string{"failed", "changed", "probe-only", "one-call"} {
		t.Run(mode, func(t *testing.T) {
			x := &ge04Trace{queries: []ge04Query{row, second}}
			switch mode {
			case "failed":
				x.queries[1].Failed = true
			case "changed":
				x.queries[1].EventIDs = []int64{3, 2}
			case "probe-only":
				x.queries[1].Run = "etag-probe"
			case "one-call":
				x.queries = x.queries[:1]
			}
			if _, err := x.idle(start, start.Add(time.Minute)); err == nil {
				t.Fatal("accepted invalid idle sample")
			}
		})
	}
}

func TestGitLabGE04ETagCapabilityIsSeparateAndNeverEnabled(t *testing.T) {
	for _, mode := range []string{"no-etag", "conditional-200", "supported", "broken-304", "transport-failed"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			f := eventFetchFunc(func(_ context.Context, q gitlab.EventQuery) (gitlab.EventBatch, error) {
				calls++
				if q.Mode != "quick" || q.Page != 1 || q.PerPage != 20 {
					t.Fatal(q)
				}
				b := gitlab.EventBatch{Version: 1, Events: []gitlab.Event{}}
				if mode == "transport-failed" {
					return b, errors.New("offline")
				}
				if mode == "no-etag" {
					return b, nil
				}
				b.ETag = `"a"`
				if calls == 1 && q.ETag != "" || calls == 2 && q.ETag != `"a"` {
					t.Fatal(q)
				}
				if calls == 2 {
					b.NotModified = mode != "conditional-200"
				}
				if calls == 3 {
					if q.ETag == `"a"` || q.ETag == "" {
						t.Fatal(q)
					}
					b.NotModified = mode == "broken-304"
				}
				return b, nil
			})
			result, err := ge04ProbeETag(context.Background(), f)
			if mode == "broken-304" || mode == "transport-failed" {
				if err == nil {
					t.Fatal("accepted broken probe")
				}
				return
			}
			if err != nil || result["enabled"] != false {
				t.Fatal(result, err)
			}
			if mode == "supported" && (calls != 3 || result["status"] != "conditional_304_and_unmatched_200") {
				t.Fatal(result, calls)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ge04Wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestGitLabGE04RejectsMissingModelDetailQueryEvidence(t *testing.T) {
	sha := strings.Repeat("a", 40)
	good := func() dsh.TurnResult {
		return dsh.TurnResult{StopReason: "end_turn", Tools: []dsh.ToolUpdate{{ID: "real-tool", Status: "completed", Input: `"$TEAMX_CLI" tools run gitlab mr view 9 --repo group/project -F json`, Output: `{"iid":9,"sha":"` + sha + `"}`}}}
	}
	if id, err := ge04DetailTool(good(), 9, sha); err != nil || id != "real-tool" {
		t.Fatal(id, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*dsh.TurnResult)
	}{
		{"budget-stop", func(r *dsh.TurnResult) { r.StopReason = "max_tokens" }},
		{"truncated-text", func(r *dsh.TurnResult) { r.TextTruncated = true }},
		{"truncated-tools", func(r *dsh.TurnResult) { r.ToolsTruncated = true }},
		{"truncated-detail", func(r *dsh.TurnResult) { r.Tools[0].DetailsTruncated = true }},
		{"running-tool", func(r *dsh.TurnResult) { r.Tools[0].Status = "in_progress" }},
		{"missing-tool", func(r *dsh.TurnResult) { r.Tools = nil }},
		{"wrong-sha", func(r *dsh.TurnResult) { r.Tools[0].Output = strings.Repeat("b", 40) }},
		{"wrong-mr-with-same-head", func(r *dsh.TurnResult) {
			r.Tools[0].Output = strings.Replace(r.Tools[0].Output, `"iid":9`, `"iid":90`, 1)
		}},
		{"no-query", func(r *dsh.TurnResult) { r.Tools[0].Input = "git rev-parse HEAD" }},
		{"retired-script", func(r *dsh.TurnResult) { r.Tools[0].Input = `python3 "$TEAMX_GITLAB_TOOLS/gitlab.py" mr --iid 9` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := good()
			tc.change(&r)
			if _, err := ge04DetailTool(r, 9, sha); err == nil {
				t.Fatal("accepted incomplete model proof")
			}
		})
	}
}
