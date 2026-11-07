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

func gt04Evidence() (RoomSnapshot, gitlab.Todo) {
	todo := gitlab.Todo{ID: 42, RecipientID: 13, ProjectID: 7, TargetIID: 9, Kind: "work_item", TargetType: "Issue", Reason: "mentioned", State: "pending", Project: "group/project", URL: "https://gitlab.example.com/group/project/-/issues/9", Title: "GT04-test-offline", CreatedAt: time.Now().UTC().Add(-time.Minute)}
	source := IntegrationSource{Provider: "gitlab", Channel: "todos", Instance: "https://gitlab.example.com", ProjectID: 7, ItemKey: "https://gitlab.example.com|13|42", SubscriptionID: strings.Repeat("a", 32)}
	post := RoomMessage{Seq: 2, Author: Author{Kind: "integration", ID: "gitlab"}, ClientMessageID: "delivery", Source: &source, Text: "GitLab · 我的待办\n标题：GT04-test-offline"}
	reply := RoomMessage{Seq: 3, Author: Author{Kind: "agent", ID: "reviewer"}, ReplyTo: 2, Text: "GT04-test todo=42 seq=2 kind=work_item iid=9"}
	return RoomSnapshot{Messages: []RoomMessage{post, reply}, IntegrationReceipts: map[string]IntegrationReceipt{"delivery": {Seq: 2, Source: source}}}, todo
}
func TestGitLabGT04RejectsIncompletePublicEvidence(t *testing.T) {
	s, todo := gt04Evidence()
	if post, reply, err := gt04Messages(s, todo, "GT04-test"); err != nil || post.Seq != 2 || reply.Seq != 3 {
		t.Fatal(post, reply, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*RoomSnapshot)
	}{
		{"duplicate-todo", func(s *RoomSnapshot) { s.Messages = append(s.Messages, s.Messages[0]) }},
		{"duplicate-reply", func(s *RoomSnapshot) { s.Messages = append(s.Messages, s.Messages[1]) }},
		{"wrong-author", func(s *RoomSnapshot) { s.Messages[0].Author.Kind = "human" }},
		{"wrong-project", func(s *RoomSnapshot) { s.Messages[0].Source.ProjectID++ }},
		{"wrong-title", func(s *RoomSnapshot) { s.Messages[0].Text = "other title" }},
		{"no-receipt", func(s *RoomSnapshot) { s.IntegrationReceipts = nil }},
		{"receipt-seq", func(s *RoomSnapshot) {
			v := s.IntegrationReceipts["delivery"]
			v.Seq++
			s.IntegrationReceipts["delivery"] = v
		}},
		{"receipt-source", func(s *RoomSnapshot) {
			v := s.IntegrationReceipts["delivery"]
			v.Source.Channel = "events"
			s.IntegrationReceipts["delivery"] = v
		}},
		{"wrong-id", func(s *RoomSnapshot) {
			s.Messages[1].Text = strings.Replace(s.Messages[1].Text, "todo=42", "todo=41", 1)
		}},
		{"wrong-seq", func(s *RoomSnapshot) { s.Messages[1].Text = strings.Replace(s.Messages[1].Text, "seq=2", "seq=1", 1) }},
		{"wrong-kind", func(s *RoomSnapshot) {
			s.Messages[1].Text = strings.Replace(s.Messages[1].Text, "work_item", "merge_request", 1)
		}},
		{"wrong-iid", func(s *RoomSnapshot) { s.Messages[1].Text = strings.Replace(s.Messages[1].Text, "iid=9", "iid=8", 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, todo := gt04Evidence()
			tc.change(&s)
			if _, _, err := gt04Messages(s, todo, "GT04-test"); err == nil {
				t.Fatal("accepted invalid evidence")
			}
		})
	}
	for _, field := range []string{"channel", "recipient", "todo"} {
		t.Run(field, func(t *testing.T) {
			s, todo := gt04Evidence()
			switch field {
			case "channel":
				s.Messages[0].Source.Channel = "events"
			case "recipient":
				todo.RecipientID++
			case "todo":
				todo.ID++
			}
			post, reply, err := gt04Messages(s, todo, "GT04-test")
			if err != nil || post.Seq != 0 || reply.Seq != 0 {
				t.Fatal("crossed identity/channel scope", post, reply, err)
			}
		})
	}
}
func TestGitLabGT04TracePreservesRealTransportAndPrivateFields(t *testing.T) {
	s := &gt04Trace{path: filepath.Join(t.TempDir(), "queries.jsonl")}
	_, todo := gt04Evidence()
	todo.Title = "private-title"
	todo.Actor = &gitlab.TodoActor{ID: 1, Username: "private-actor"}
	batch := gitlab.TodoBatch{Version: 1, Todos: []gitlab.Todo{todo}, ResponseBytes: 100, NextPage: 2}
	q := gitlab.TodoQuery{State: "pending", Page: 1, PerPage: 100}
	f := s.wrap(todoFetchFunc(func(_ context.Context, got gitlab.TodoQuery) (gitlab.TodoBatch, error) {
		if got != q {
			t.Fatal(got)
		}
		return batch, nil
	}), "service-1")
	got, err := f.FetchTodos(context.Background(), q)
	if err != nil || !reflect.DeepEqual(got, batch) {
		t.Fatal(got, err)
	}
	if _, ok := s.target("private-title", "service-2"); ok {
		t.Fatal("crossed run scope")
	}
	if v, ok := s.target("private-title", "service-1"); !ok || v.ID != 42 {
		t.Fatal(v, ok)
	}
	if _, ok := s.first(42, "offline-probe"); ok {
		t.Fatal("probe masqueraded as production")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-title", "private-actor", todo.URL} {
		if strings.Contains(string(data), private) {
			t.Fatal("private remote fields in query evidence")
		}
	}
	var row gt04Query
	if err := json.Unmarshal(data, &row); err != nil {
		t.Fatal(err)
	}
	if row.Bytes != 100 || row.Complete || row.NextPage != 2 || len(row.Todos) != 1 || row.Todos[0].ID != 42 || row.Ended.Before(row.Started) {
		t.Fatal(row)
	}
	info, _ := os.Stat(s.path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	remoteErr := errors.New("private transport error")
	bad := s.wrap(todoFetchFunc(func(context.Context, gitlab.TodoQuery) (gitlab.TodoBatch, error) {
		return gitlab.TodoBatch{}, remoteErr
	}), "offline-probe")
	if _, err := bad.FetchTodos(context.Background(), q); err != remoteErr {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(s.path)
	if strings.Contains(string(data), remoteErr.Error()) || !s.queries[1].Failed {
		t.Fatal("failure not safely recorded")
	}
	broken := &gt04Trace{path: filepath.Join(t.TempDir(), "missing", "trace")}
	if _, err := broken.wrap(f, "service-2").FetchTodos(context.Background(), q); err != nil || broken.failure() == nil {
		t.Fatal("evidence failure changed query or went unreported")
	}
}
func TestGitLabGT04RequiresMeasuredPendingAndDone(t *testing.T) {
	start := time.Now().UTC()
	rows := []gt04Query{}
	for i, state := range []string{"pending", "done", "pending", "done"} {
		rows = append(rows, gt04Query{Run: "service-1", State: state, Complete: true, Started: start.Add(time.Duration(i+1) * time.Second), Ended: start.Add(time.Duration(i+2) * time.Second), Bytes: 100})
	}
	s := &gt04Trace{queries: rows}
	load, err := s.load(start, start.Add(75*time.Second), "service-1")
	if err != nil || load["calls"] != 4 || load["response_bytes"] != int64(400) {
		t.Fatal(load, err)
	}
	for _, mode := range []string{"short", "missing-done", "failed", "probes"} {
		t.Run(mode, func(t *testing.T) {
			qs := append([]gt04Query(nil), rows...)
			end := start.Add(75 * time.Second)
			switch mode {
			case "short":
				end = start.Add(time.Second)
			case "missing-done":
				for i := range qs {
					qs[i].State = "pending"
				}
			case "failed":
				qs[0].Failed = true
			case "probes":
				for i := range qs {
					qs[i].Run = "offline-probe"
				}
			}
			if _, err := (&gt04Trace{queries: qs}).load(start, end, "service-1"); err == nil {
				t.Fatal("accepted insufficient cost evidence")
			}
		})
	}
}
func TestGitLabGT04RequiresActualCompletedAgentRead(t *testing.T) {
	s, _ := gt04Evidence()
	post := s.Messages[0]
	good := dsh.TurnResult{StopReason: "end_turn", Tools: []dsh.ToolUpdate{{ID: "read-tool", Status: "completed", Input: "teamx read --json", Output: post.Source.ItemKey + " " + post.Text}}}
	if id, err := gt04ReadTool(good, post); err != nil || id != "read-tool" {
		t.Fatal(id, err)
	}
	for _, mode := range []string{"budget", "truncated", "tool-truncated", "failed", "missing-key", "no-read"} {
		t.Run(mode, func(t *testing.T) {
			r := good
			r.Tools = append([]dsh.ToolUpdate(nil), good.Tools...)
			switch mode {
			case "budget":
				r.StopReason = "max_tokens"
			case "truncated":
				r.ToolsTruncated = true
			case "tool-truncated":
				r.Tools[0].DetailsTruncated = true
			case "failed":
				r.Tools[0].Status = "failed"
			case "missing-key":
				r.Tools[0].Output = post.Text
			case "no-read":
				r.Tools[0].Input = "echo"
			}
			if _, err := gt04ReadTool(r, post); err == nil {
				t.Fatal("accepted incomplete tool evidence")
			}
		})
	}
}

func TestGitLabGT04SelectsReviewInsteadOfEarlierMention(t *testing.T) {
	trace := &gt04Trace{}
	_, todo := gt04Evidence()
	todo.Title = "same-MR"
	todo.Kind = "merge_request"
	mention := todo
	review := todo
	review.ID++
	review.Reason = "review_requested"
	trace.queries = []gt04Query{{Run: "service-1", raw: []gitlab.Todo{mention, review}}}
	got, ok := trace.target("same-MR", "service-1", "merge_request", "review_requested")
	if !ok || got.ID != review.ID {
		t.Fatal("earlier mention masked the review request", got, ok)
	}
	trace.queries[0].raw = []gitlab.Todo{mention}
	if _, ok := trace.target("same-MR", "service-1", "merge_request", "review_requested"); ok {
		t.Fatal("mention counted as review")
	}
}
