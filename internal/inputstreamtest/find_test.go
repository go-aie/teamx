package inputstreamtest

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestFindCombinedProjectionAndIndependentBudgets(t *testing.T) {
	c := newTestCatalog(t, testRecords(100, 30), Preferences{})
	q := FindRequest{Query: Query{Turn: 131, Provider: "objects", Limit: 10}, Preview: true, PreviewLimit: 3, IncludeSources: true}
	out, err := c.Find(q)
	if err != nil || out.Matched != 30 || out.Sources == nil || out.Sources.Entries != 100 || out.Scope == nil || out.Scope.Provider != "objects" {
		t.Fatal(out, err)
	}
	excerpts := 0
	for _, s := range out.Streams {
		excerpts += len(s.Preview)
	}
	if excerpts != 3 || jsonSize(out) > PageBytes || jsonSize(out.Sources) > OverviewBytes {
		t.Fatal(out)
	}
	previewBefore := c.previewBytes
	c.discoveryBytes = DiscoveryTurnBytes
	exhausted, err := c.Find(q)
	if err != nil || exhausted.Stop != "directory_budget_exhausted" || c.previewBytes != previewBefore {
		t.Fatal(exhausted, err, c.previewBytes, previewBefore)
	}
	c.discoveryBytes = 0
	c.previewBytes = PreviewTurnBytes
	exhausted, err = c.Find(q)
	if err != nil || exhausted.Streams[0].Notice != "preview_budget_exhausted" || exhausted.Matched != 30 {
		t.Fatal(exhausted, err)
	}
	// A replay is output too: its excerpts must count against the preview ledger.
	c.previewBytes = 0
	first, _ := c.Find(q)
	next, _ := c.Find(FindRequest{Query: Query{Turn: 131, Cursor: first.Next}})
	if len(next.Streams) == 0 {
		t.Fatal(next)
	}
	if _, err = c.List(Query{Turn: 131, Cursor: first.Next}); !errors.Is(err, ErrExpired) {
		t.Fatal("find cursor accepted by list", err)
	}
}
func TestFindFixedCandidateAndSourcePagination(t *testing.T) {
	records := testRecords(30, 30)
	for i := range records {
		records[i].Group.Account = string(rune('a' + i))
	}
	c := newTestCatalog(t, records, Preferences{})
	first, err := c.Find(FindRequest{Query: Query{Turn: 131, Limit: 3}, IncludeSources: true})
	if err != nil || first.Sources == nil || first.Next == "" || first.Sources.Next == "" {
		t.Fatal(first, err)
	}
	sourceCursor := first.Sources.Next
	for i := range records {
		records[i].Pending = false
	}
	if err := c.Rebuild(records, Preferences{}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for page := first; ; {
		for _, s := range page.Streams {
			if seen[s.Ref] {
				t.Fatal("duplicate", s.Ref)
			}
			seen[s.Ref] = true
		}
		if page.Next == "" {
			break
		}
		page, err = c.Find(FindRequest{Query: Query{Turn: 131, Cursor: page.Next}})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 30 {
		t.Fatal(len(seen))
	}
	src, err := c.Find(FindRequest{Query: Query{Turn: 131, Cursor: sourceCursor}})
	if err != nil || src.Sources == nil || len(src.Sources.Sources) != 10 || src.Sources.Pending != 30 || len(src.Streams) != 0 {
		t.Fatal(src, err)
	}
	replay, err := c.Find(FindRequest{Query: Query{Turn: 131, Cursor: sourceCursor}})
	if err != nil || !reflect.DeepEqual(src, replay) {
		t.Fatal(replay, err)
	}
	for range 5 {
		_, _ = c.Find(FindRequest{Query: Query{Turn: 131}})
	}
	if _, err = c.Find(FindRequest{Query: Query{Turn: 131, Cursor: sourceCursor}}); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
}
func TestFindReferencesValidationFeedbackAndDirectory(t *testing.T) {
	records := testRecords(10, 10)
	c := newTestCatalog(t, records, Preferences{})
	d, _, err := c.FindDirectory()
	if err != nil || len(d.Streams) > 5 {
		t.Fatal(d, err)
	}
	if d.Next != "" {
		if _, err = c.Find(FindRequest{Query: Query{Turn: 131, Cursor: d.Next}}); err != nil {
			t.Fatal(err)
		}
	}
	ref, _ := records[1].Group.StreamRef(testMember)
	p, err := c.Find(FindRequest{Query: Query{Turn: 131}, Ref: ref, Preview: true})
	if err != nil || len(p.Streams) != 1 || len(p.Streams[0].Preview) != 1 {
		t.Fatal(p, err)
	}
	for _, q := range []FindRequest{
		{Query: Query{Turn: 131, Project: "Project A"}, Ref: ref},
		{Query: Query{Turn: 131}, PreviewLimit: 2},
		{Query: Query{Turn: 131}, Preview: true, PreviewLimit: 4},
		{Query: Query{Turn: 132}},
		{Query: Query{Turn: 131, Cursor: d.Next}, Preview: true},
	} {
		if _, err = c.Find(q); err == nil {
			t.Fatal("invalid accepted", q)
		}
	}
	no, err := c.Find(FindRequest{Query: Query{Turn: 131, Project: "Project A", Text: "unknownword"}})
	if err != nil || !strings.Contains(no.Notice, "input_no_match") {
		t.Fatal(no, err)
	}
	missing, err := c.Find(FindRequest{Query: Query{Turn: 131, Project: "missing"}})
	if err != nil || !strings.Contains(missing.Notice, "input_scope_empty") {
		t.Fatal(missing, err)
	}
	records[1].Pending = false
	if err = c.Rebuild(records, Preferences{}); err != nil {
		t.Fatal(err)
	}
	empty, err := c.Find(FindRequest{Query: Query{Turn: 131}, Ref: ref, Preview: true})
	if err != nil || !strings.Contains(empty.Notice, "input_stream_empty") {
		t.Fatal(empty, err)
	}
	if _, err = c.Preview(PreviewRequest{Turn: 131, Ref: ref}); err == nil || !strings.Contains(err.Error(), "input_stream_empty") {
		t.Fatal(err)
	}
	for _, data := range []string{`{"turn_number":131,"preview":null}`, `{"turn_number":131,"cursor":"x","preview":false}`, `{"turn_number":131,"preview_limit":0}`, `{"turn_number":131,"mode":"list"}`} {
		var q FindRequest
		// preview_limit=0 must be rejected explicitly by the shared decoder too.
		if err = Decode([]byte(data), &q); err == nil {
			t.Fatal("invalid wire accepted", data)
		}
	}
	tools := FindTools()
	if len(tools) != 2 || tools[0].Name != "inbox_find" || tools[1].Name != "inbox_read" {
		t.Fatal(tools)
	}
	b, _ := json.Marshal(tools)
	for _, name := range []string{"inbox_list", "inbox_preview", "inbox_overview"} {
		if strings.Contains(string(b), name) {
			t.Fatal("control leaked", name)
		}
	}
}
