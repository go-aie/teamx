package inputstreamtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

var testMember = Member{"room", "instance", "membership"}
var testDescriptors = []Descriptor{{Provider: "objects", Family: "object", Channels: []string{"events", "todos"}}, {Provider: "chats", Family: "conversation", Channels: []string{"messages"}}}

func testRecords(n, streams int) []Record {
	out := make([]Record, 0, n)
	for i := range n {
		id := fmt.Sprint(i % streams)
		g := Group{Version: 1, Kind: "notification", Provider: "objects", Channel: "events", Instance: "https://objects.test", Account: "account", Precision: "context", Context: []string{"project", "merge_request", id}, Facts: Facts{Project: "Project A", ProjectID: "7", ObjectType: "merge_request", IID: id, Label: "Release " + id, Summary: "saved local summary"}}
		out = append(out, Record{Location: Location{"objects", uint64(i + 1)}, Group: g, Pending: true, ReceivedAt: time.Unix(int64(i), 0), Excerpt: "saved text"})
	}
	return out
}
func newTestCatalog(t testing.TB, records []Record, prefs Preferences) *Catalog {
	t.Helper()
	c, err := NewCatalog(testMember, 131, testDescriptors, records, prefs)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestCatalogScaleFixedPagesAndDirectTarget(t *testing.T) {
	records := testRecords(10000, 1000)
	c := newTestCatalog(t, records, Preferences{})
	directory, _, err := c.Directory()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(directory)
	if len(data) > DirectoryBytes || len(directory.Streams) > 5 || directory.Pending != 1000 || directory.Next == "" {
		t.Fatal("unbounded directory", len(data), directory)
	}
	// Explicit one-source query cancels per-source quotas.
	q := Query{Turn: 131, Provider: "objects", Limit: 17}
	first, err := c.List(q)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	all := first
	for {
		for _, s := range all.Streams {
			if seen[s.Ref] {
				t.Fatal("duplicate fixed page", s.Ref)
			}
			seen[s.Ref] = true
		}
		if all.Next == "" {
			break
		}
		c.discoveryBytes = 0 // isolate pagination from the separately tested per-turn byte cap
		// Hot append and changed attention do not alter the old snapshot.
		if len(seen) == 17 {
			extra := testRecords(1, 1)[0]
			extra.Location.Seq = 10001
			extra.Group.Context = []string{"new"}
			extra.Group.Facts.Label = "new hot target"
			records = append(records, extra)
			if err := c.Rebuild(records[1:], Preferences{Version: 2}); err != nil {
				t.Fatal(err)
			}
		}
		all, err = c.List(Query{Turn: 131, Cursor: all.Next})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 1000 {
		t.Fatal("lost flows", len(seen))
	}
	replay, err := c.List(Query{Turn: 131, Cursor: first.Next})
	if err != nil || len(replay.Streams) != 17 {
		t.Fatal("fixed replay failed", err)
	}
	deep, err := c.List(Query{Turn: 131, Provider: "objects", ProjectID: "7", IID: "999"})
	if err != nil || deep.Matched != 1 {
		t.Fatal("could not jump to deep target", deep, err)
	}
	old := deep.Streams[0].Pending
	preview, err := c.Preview(PreviewRequest{Turn: 131, Ref: deep.Streams[0].Ref})
	if err != nil || len(preview.Entries) != 1 {
		t.Fatal(preview, err)
	}
	after, _ := c.List(Query{Turn: 131, Provider: "objects", ProjectID: "7", IID: "999"})
	if after.Streams[0].Pending != old {
		t.Fatal("preview consumed")
	}
	if _, err = c.List(Query{Turn: 131, Cursor: first.Next, Limit: 2}); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err = c.Overview(Query{Turn: 131, Cursor: first.Next}); !errors.Is(err, ErrExpired) {
		t.Fatal("cursor purpose", err)
	}
	other, _ := NewCatalog(Member{"room", "instance", "other"}, 131, testDescriptors, records[1:], Preferences{})
	if _, err = other.List(Query{Turn: 131, Cursor: first.Next}); !errors.Is(err, ErrExpired) {
		t.Fatal("cross-member cursor", err)
	}
	for range 5 {
		_, _ = c.List(Query{Turn: 131, Text: "release"})
	}
	if _, err = c.List(Query{Turn: 131, Cursor: first.Next}); !errors.Is(err, ErrExpired) {
		t.Fatal("evicted cursor silently reused", err)
	}
}
func TestCatalogFilteringMatrix(t *testing.T) {
	r := testRecords(3, 2)
	r[0].Group.Channel = "todos"
	r[0].Group.Facts.Summary = "Release PIPELINE"
	r[2].Group.Facts.Summary = "other summary"
	chat := r[0]
	chat.Location = Location{"chats", 1}
	chat.Group.Provider = "chats"
	chat.Group.Channel = "messages"
	chat.Group.Context = []string{"chat"}
	chat.Group.Facts = Facts{Chat: "产品群", ChatID: "oc_one", ThreadID: "thread_one", Label: "产品群"}
	r = append(r, chat)
	room := r[0]
	room.Location = Location{"room", 1}
	room.Group = RoomGroup(testMember, "Room")
	room.Group.Facts.EntryKind = "event"
	room.Group.Facts.EventType = "user_selection"
	room.Group.Facts.OperationID = "selection"
	r = append(r, room)
	c := newTestCatalog(t, r, Preferences{})
	cases := []struct {
		name    string
		q       Query
		want    int
		failure error
	}{
		{"project", Query{Project: "Project A"}, 2, nil},
		{"project exact", Query{Project: "project a"}, 0, nil},
		{"numeric iid", Query{ProjectID: "7", IID: "1"}, 1, nil},
		{"AND text case whitespace", Query{Text: "  release   pipeline "}, 1, nil},
		{"channel context", Query{Channel: "todos"}, 1, nil},
		{"chat", Query{Chat: "产品群", ThreadID: "thread_one"}, 1, nil},
		{"event", Query{EntryKind: "event", EventType: "user_selection", OperationID: "selection"}, 1, nil},
		{"conflicting families", Query{Project: "Project A", Chat: "产品群"}, 0, ErrConflict},
		{"room account", Query{Kind: "room", AccountRef: "account"}, 0, ErrConflict},
		{"provider field", Query{Provider: "chats", Project: "Project A"}, 0, ErrConflict},
		{"unsupported channel", Query{Provider: "objects", Channel: "messages"}, 0, ErrUnsupported},
		{"unsupported source", Query{Provider: "unknown"}, 0, ErrUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := tc.q
			q.Turn = 131
			v, err := c.List(q)
			if !errors.Is(err, tc.failure) || err == nil && v.Matched != tc.want {
				t.Fatal(v, err)
			}
		})
	}
	// Exact IID needs a unique saved account/project, even when only one matches.
	duplicate := r[0]
	duplicate.Location = Location{"objects", 4}
	duplicate.Group.Account = "other"
	r = append(r, duplicate)
	c = newTestCatalog(t, r, Preferences{})
	if _, err := c.List(Query{Turn: 131, IID: "1"}); !errors.Is(err, ErrAmbiguous) {
		t.Fatal(err)
	}
	account := r[0].Group.AccountRef(testMember)
	if v, err := c.List(Query{Turn: 131, AccountRef: account, ProjectID: "7", IID: "1"}); err != nil || v.Matched != 1 {
		t.Fatal(v, err)
	}
	missing := r[0]
	missing.Location = Location{"objects", 5}
	missing.Group.Context = []string{"fallback"}
	missing.Group.Precision = "source"
	missing.Group.Subscription = "s"
	missing.Group.Missing = "no native object"
	missing.Group.Facts.ProjectID = ""
	r = append(r, missing)
	c = newTestCatalog(t, r, Preferences{})
	v, err := c.List(Query{Turn: 131, ProjectID: "7"})
	if err != nil || v.Notice == "" {
		t.Fatal("missing evidence silently excluded", v, err)
	}
	if _, err = c.Overview(Query{Turn: 131, Text: "release"}); !errors.Is(err, ErrUnsupported) {
		t.Fatal(err)
	}
	var query Query
	if err = Decode([]byte(`{"turn_number":131,"iid":56,"project_id":7}`), &query); err != nil || query.IID != "56" {
		t.Fatal("numeric native ID", query, err)
	}
	for _, bad := range []string{`{"turn_number":131,"author":"x"}`, `{"turn_number":131,"limit":null}`, `{"turn_number":131,"limit":0}`, `{"turn_number":131,"cursor":"saved","query":""}`} {
		if Decode([]byte(bad), &query) == nil {
			t.Fatal("ignored unknown/null", bad)
		}
	}
	if _, err := c.List(Query{Turn: 131, AccountRef: "another-members-account"}); !errors.Is(err, ErrIdentity) {
		t.Fatal("foreign account accepted", err)
	}
}
func TestCatalogAttentionExposureAndBounds(t *testing.T) {
	r := testRecords(30, 15)
	for i := range r {
		if i%2 == 0 {
			r[i].Group.Account = "second"
		}
	}
	first := r[0].Group.SourceKey(testMember)
	second := r[1].Group.SourceKey(testMember)
	prefs := Preferences{Version: 1, Attention: map[string]string{first: "high", second: "low"}}
	c := newTestCatalog(t, r, prefs)
	d, ex, err := c.Directory()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, s := range d.Streams {
		counts[s.Attention]++
	}
	if counts["high"] != 3 || counts["low"] != 1 || len(d.Streams) != 4 {
		t.Fatal("quota/fairness", d)
	}
	again, ex2, _ := c.Directory()
	if !reflect.DeepEqual(d, again) || !reflect.DeepEqual(ex, ex2) {
		t.Fatal("duplicate exposure")
	}
	prefs.Attention[first] = "low"
	if fresh, _, _ := c.Directory(); !reflect.DeepEqual(d, fresh) {
		t.Fatal("settings reordered prompt")
	}
	// Equal attention alternates the sources without a platform priority branch.
	c = newTestCatalog(t, r, Preferences{LastSource: first})
	d, _, _ = c.Directory()
	if len(d.Streams) != 4 {
		t.Fatal(d)
	}
	if c.byRef[d.Streams[0].Ref].source == first || c.byRef[d.Streams[1].Ref].source != first {
		t.Fatal("ring order", d)
	}
	all, err := c.List(Query{Turn: 131, Provider: "objects", Limit: 15})
	if err != nil || len(all.Streams) != 15 {
		t.Fatal("explicit source must cancel quota", all, err)
	}
	r[0].Blocked = "saved source unavailable"
	r[1].Group.Facts.Label = strings.Repeat("超长标签", 5000)
	c = newTestCatalog(t, r, Preferences{})
	d, _, _ = c.Directory()
	if d.Blocked != 1 {
		t.Fatal("blocked flow missing", d)
	}
	data, _ := json.Marshal(d)
	if len(data) > DirectoryBytes {
		t.Fatal(len(data))
	}
	for _, s := range d.Streams {
		if s.Blocked != "" {
			t.Fatal("blocked in readable directory")
		}
	}
	blocked, err := c.List(Query{Turn: 131, State: "blocked"})
	if err != nil || blocked.Matched != 1 || blocked.Streams[0].Blocked == "" {
		t.Fatal(blocked, err)
	}
}
func TestStreamIdentityIgnoresDisplayAndIsolatesMembers(t *testing.T) {
	r := testRecords(1, 1)[0]
	ref, err := r.Group.StreamRef(testMember)
	if err != nil {
		t.Fatal(err)
	}
	r.Group.Facts.Label = "renamed"
	r.Group.Channel = "todos"
	changed, _ := r.Group.StreamRef(testMember)
	if changed != ref {
		t.Fatal("display or channel regrouped native object")
	}
	for _, member := range []Member{{"room", "other-instance", "membership"}, {"room", "instance", "new-membership"}} {
		changed, _ = r.Group.StreamRef(member)
		if changed == ref {
			t.Fatal("member/room collision")
		}
	}
	r.Group.Version = 2
	if _, err = r.Group.StreamRef(testMember); err == nil {
		t.Fatal("unknown grouping version accepted")
	}
}

func TestDiscoveryAndPreviewBudgetsAreIndependentAndFixed(t *testing.T) {
	r := testRecords(100, 100)
	r[0].Excerpt = strings.Repeat("large saved text ", 500)
	c := newTestCatalog(t, r, Preferences{})
	d, _, err := c.Directory()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		list, err := c.List(Query{Turn: 131, Provider: "objects", Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		if list.Stop == "directory_budget_exhausted" {
			if len(list.Streams) != 0 || c.discoveryBytes > DiscoveryTurnBytes {
				t.Fatal("budget overflow")
			}
			break
		}
		if i == 99 {
			t.Fatal("directory never stopped")
		}
	}
	ref, _ := r[0].Group.StreamRef(testMember)
	preview, err := c.Preview(PreviewRequest{Turn: 131, Ref: ref})
	if err != nil || len(preview.Entries) != 1 || !preview.Entries[0].Truncated || preview.Notice == "" {
		t.Fatal("lost truncation evidence", preview, err)
	}
	for i := 0; i < 100; i++ {
		preview, err = c.Preview(PreviewRequest{Turn: 131, Ref: ref})
		if err != nil {
			t.Fatal(err)
		}
		if preview.Stop == "preview_budget_exhausted" {
			if c.previewBytes > PreviewTurnBytes || preview.Ref != ref {
				t.Fatal("lost preview location")
			}
			break
		}
		if i == 99 {
			t.Fatal("preview never stopped")
		}
	}
	// Hot later records/new partitions cannot expand this accepted turn.
	appendix := r[0]
	appendix.Location.Seq = 101
	appendix.Group.Context = []string{"new-hot"}
	appendix.Group.Facts.Label = "hot"
	r = append(r, appendix)
	appendix.Location = Location{"new-partition", 1}
	r = append(r, appendix)
	if err := c.Rebuild(r, Preferences{Version: 9}); err != nil {
		t.Fatal(err)
	}
	c.discoveryBytes = 0
	all, err := c.List(Query{Turn: 131, Text: "hot"})
	if err != nil || all.Matched != 0 {
		t.Fatal("expanded turn scope", all, err)
	}
	// Same-turn prompt retry never advances exposure or charges again.
	used := c.discoveryBytes
	retry, _, err := c.Directory()
	if err != nil || !reflect.DeepEqual(d, retry) || c.discoveryBytes != used {
		t.Fatal("prompt retry changed snapshot or budget")
	}
}

func TestCatalogEntryPredicatesDoNotMixAcrossMessages(t *testing.T) {
	r := testRecords(2, 1)
	r[0].Group.Channel = "todos"
	r[0].Group.Facts.Summary = "plain"
	r[1].Group.Facts.Summary = "urgent"
	c := newTestCatalog(t, r, Preferences{})
	list, err := c.List(Query{Turn: 131, Channel: "todos", Text: "urgent"})
	if err != nil || list.Matched != 0 {
		t.Fatal("mixed predicates across entries", list, err)
	}
	list, err = c.List(Query{Turn: 131, Channel: "todos"})
	if err != nil || list.Matched != 1 || list.Streams[0].Pending != 2 {
		t.Fatal("predicate filtered messages instead of streams", list, err)
	}
}

func TestExactObjectScopeSurvivesConsumptionAndIgnoresKeywords(t *testing.T) {
	r := testRecords(2, 2)
	c := newTestCatalog(t, r, Preferences{})
	for i := range r {
		r[i].Pending = false
	}
	if err := c.Rebuild(r, Preferences{}); err != nil {
		t.Fatal(err)
	}
	list, err := c.List(Query{Turn: 131, ProjectID: "7", IID: "1"})
	if err != nil || list.Matched != 0 {
		t.Fatal("consumed object became ambiguous", list, err)
	}
	r = testRecords(2, 2)
	r[1].Group.Facts.ProjectID = "8"
	r[1].Group.Facts.Project = "other project"
	r[1].Group.Facts.Summary = "unique keyword"
	c = newTestCatalog(t, r, Preferences{})
	if _, err := c.List(Query{Turn: 131, IID: "1", Text: "unique keyword"}); !errors.Is(err, ErrAmbiguous) {
		t.Fatal("keyword silently chose an IID scope", err)
	}
}

func TestDirectoryByteTrimmingRetainsUnshownCandidates(t *testing.T) {
	r := testRecords(6, 6)
	for i := range r {
		r[i].Group.Account = fmt.Sprint(i)
		r[i].Group.Facts.SourceLabel = strings.Repeat("\x01", 300)
		r[i].Group.Facts.Label = strings.Repeat("\x01", 250)
	}
	c := newTestCatalog(t, r, Preferences{})
	d, _, err := c.Directory()
	if err != nil || !d.Truncated || d.Next == "" {
		t.Fatal(d, err)
	}
	b, _ := json.Marshal(d)
	if len(b) > DirectoryBytes {
		t.Fatal("oversized directory", len(b))
	}
	seen := map[string]bool{}
	for _, s := range d.Streams {
		seen[s.Ref] = true
	}
	cursor := d.Next
	for cursor != "" {
		page, err := c.List(Query{Turn: 131, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range page.Streams {
			if seen[s.Ref] {
				t.Fatal("duplicate after trimming")
			}
			seen[s.Ref] = true
		}
		cursor = page.Next
	}
	if len(seen) != 6 {
		t.Fatal("byte trimming skipped choices", len(seen))
	}
}
func BenchmarkCatalogRebuild(b *testing.B) {
	records := testRecords(10000, 1000)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := NewCatalog(testMember, 131, testDescriptors, records, Preferences{}); err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkCatalogHotQuery(b *testing.B) {
	c := newTestCatalog(b, testRecords(10000, 1000), Preferences{})
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := c.List(Query{Turn: 131, ProjectID: "7", IID: "999"}); err != nil {
			b.Fatal(err)
		}
	}
}
