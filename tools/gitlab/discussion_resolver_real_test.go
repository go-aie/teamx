package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

// GET-only, existing operator-selected MR; no fixture creation, model or writes.
func TestDiscussionResolverRealGitLab(t *testing.T) {
	if os.Getenv("TEAMX_AN_CONTEXT_REAL") != "1" {
		t.Skip("set TEAMX_AN_CONTEXT_REAL with GITLAB_HOST/TOKEN, TEAMX_AN_CONTEXT_MR and a new absolute TEAMX_AN_CONTEXT_EVIDENCE file")
	}
	target := os.Getenv("TEAMX_AN_CONTEXT_MR")
	file := os.Getenv("TEAMX_AN_CONTEXT_EVIDENCE")
	u, e := url.Parse(target)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || !filepath.IsAbs(file) {
		t.Fatal("exact MR URL and new absolute evidence file required")
	}
	project, iid, ok := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/-/merge_requests/")
	n, e := strconv.Atoi(iid)
	if !ok || e != nil || n <= 0 {
		t.Fatal("invalid MR")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	a := &app{out: &bytes.Buffer{}, errOut: &bytes.Buffer{}, getenv: os.Getenv}
	c, e := a.api()
	if e != nil {
		t.Fatal(e)
	}
	if c.origin != u.Scheme+"://"+u.Host {
		t.Fatal("configured instance mismatch")
	}
	var user struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	if _, e = c.request(ctx, "GET", "/api/v4/user", nil, &user); e != nil {
		t.Fatal(e)
	}
	var obj struct {
		ID        int64  `json:"id"`
		IID       int64  `json:"iid"`
		ProjectID int64  `json:"project_id"`
		WebURL    string `json:"web_url"`
	}
	if _, e = c.request(ctx, "GET", "/api/v4/projects/"+url.PathEscape(project)+"/merge_requests/"+iid, nil, &obj); e != nil {
		t.Fatal(e)
	}
	if obj.WebURL != target || obj.IID != int64(n) || obj.ID <= 0 || obj.ProjectID <= 0 {
		t.Fatal("remote object mismatch")
	}
	type thread struct {
		ID         string `json:"id"`
		Individual bool   `json:"individual_note"`
		Notes      []struct {
			ID int64 `json:"id"`
		} `json:"notes"`
	}
	var all []thread
	for page := 1; page <= 10; page++ {
		var rows []thread
		h, e := c.request(ctx, "GET", fmt.Sprintf("/api/v4/projects/%d/merge_requests/%d/discussions?per_page=50&page=%d", obj.ProjectID, obj.IID, page), nil, &rows)
		if e != nil {
			t.Fatal(e)
		}
		all = append(all, rows...)
		if h.Get("X-Next-Page") == "" {
			break
		}
		if h.Get("X-Next-Page") != strconv.Itoa(page+1) || page == 10 {
			t.Fatal("fixture pagination incomplete")
		}
	}
	ext := func(n int64, ns string) platform.ExternalID {
		return platform.ExternalID{Namespace: "gitlab:rest:" + ns, Value: strconv.FormatInt(n, 10)}
	}
	var queries []gitlab.DiscussionQuery
	expected := map[string]string{}
	ordinary, threaded := false, false
	for _, d := range all {
		if len(d.Notes) == 0 || d.Individual && ordinary || !d.Individual && threaded {
			continue
		}
		note := d.Notes[0].ID
		q := gitlab.DiscussionQuery{Source: platform.SourceRef{Provider: "gitlab", Channel: "events", Instance: c.origin, SubscriptionID: strings.Repeat("a", 32), ItemKey: fmt.Sprintf("acceptance-note-%d", note), URL: target + "#note_" + strconv.FormatInt(note, 10), Reference: platform.NotificationReference{Version: 1, RecipientID: ext(user.ID, "id"), Scope: "project", ProjectID: ext(obj.ProjectID, "id"), Object: platform.ObjectReference{Type: "MergeRequest", ID: ext(obj.ID, "id"), IID: ext(obj.IID, "iid")}, NoteID: ext(note, "id")}}}
		q.URL = q.Source.URL
		// First exercise an IID-only event. Then the same comment as a URL-only
		// Todo candidate using an ID-only object; both must hit the verified alias.
		q.Source.Reference.Object.ID = platform.ExternalID{}
		queries = append(queries, q)
		expected[q.Source.ItemKey] = d.ID
		todo := q
		todo.Source.Channel = "todos"
		todo.Source.Reference.Scope = "account"
		todo.Source.ItemKey += "-todo"
		todo.Source.Reference.Object = platform.ObjectReference{Type: "MergeRequest", ID: ext(obj.ID, "id")}
		todo.Source.Reference.NoteID = platform.ExternalID{}
		queries = append(queries, todo)
		expected[todo.Source.ItemKey] = d.ID
		if d.Individual {
			ordinary = true
		} else {
			threaded = true
		}
	}
	if len(queries) == 0 {
		t.Fatal("no existing comment fixture")
	}
	b, _ := json.Marshal(queries)
	out, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	a.out, a.errOut = out, stderr
	if code := a.run(ctx, []string{"notifications", "resolve", "--queries", string(b)}); code != 0 {
		t.Fatal("real resolver failed", code)
	}
	var reports []gitlab.DiscussionResolution
	if json.Unmarshal(out.Bytes(), &reports) != nil || len(reports) != len(queries) {
		t.Fatal("invalid resolver output")
	}
	for _, r := range reports {
		if r.Status != "resolved" || !r.ObjectVerified || !r.Complete || r.DiscussionID.Value != expected[r.Query.Source.ItemKey] || r.Object.ID != ext(obj.ID, "id") || r.Object.IID != ext(obj.IID, "iid") {
			t.Fatal("real mapping or alias mismatch", r.Status, r.Reason)
		}
	}
	evidence := struct {
		Status   string                        `json:"status"`
		Target   string                        `json:"target"`
		Ordinary bool                          `json:"ordinary_comment"`
		Threaded bool                          `json:"discussion_comment"`
		Reports  []gitlab.DiscussionResolution `json:"reports"`
	}{"passed", target, ordinary, threaded, reports}
	f, e := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e = json.NewEncoder(f).Encode(evidence); e != nil {
		t.Fatal(e)
	}
	t.Logf("GET-only real mapping: %d queries; ordinary=%t threaded=%t; source inputs are explicit resolver fixtures, not subscription coverage evidence", len(reports), ordinary, threaded)
}
