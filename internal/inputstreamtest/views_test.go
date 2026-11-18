package inputstreamtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/jsonschema-go/jsonschema"
	"reflect"
	"strings"
	"testing"
)

func callViews(t testing.TB, c *Catalog, name, args string, out any) error {
	t.Helper()
	r, confirm, err := (ViewsService{Service{Catalog: c}}).Call(context.Background(), name, []byte(args))
	if err != nil {
		return err
	}
	if confirm != nil || r.StructuredContent != nil {
		t.Fatal("discovery leaked confirmation/duplicate DTO")
	}
	b, _ := json.Marshal(r.Content[0])
	var content struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(b, &content)
	reflect.ValueOf(out).Elem().SetZero()
	return json.Unmarshal([]byte(content.Text), out)
}
func TestViewsStatsIntersectionAndWholeStreamCounts(t *testing.T) {
	r := testRecords(6, 3)
	r[0].Group.Channel = "todos"
	r[1].Blocked = "unavailable"
	c := newTestCatalog(t, r, Preferences{})
	var stats Overview
	if err := callViews(t, c, "inbox_stats", `{"turn_number":131,"filter":{"project":"Project A","channel":"todos"}}`, &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 1 || stats.Entries != 2 || stats.ScopeRef == "" {
		t.Fatal(stats)
	}
	var all List
	if err := callViews(t, c, "inbox_list", fmt.Sprintf(`{"turn_number":131,"within":%q,"filter":{"channel":"todos"},"state":"all"}`, stats.ScopeRef), &all); err != nil || all.Matched != 1 || all.Streams[0].Pending != 2 {
		t.Fatal(all, err)
	}
	if err := callViews(t, c, "inbox_stats", `{"turn_number":131,"filter":{"project":"Project A"}}`, &stats); err != nil || stats.Pending != 3 || stats.Entries != 6 || stats.Blocked != 1 {
		t.Fatal(stats, err)
	}
	if err := callViews(t, c, "inbox_list", `{"turn_number":131,"filter":{"project":"Project A"},"state":"all"}`, &all); err != nil || stats.Pending != all.Matched {
		t.Fatal(all, err)
	}
	var scoped List
	if err := callViews(t, c, "inbox_list", fmt.Sprintf(`{"turn_number":131,"within":%q,"filter":{"iid":1}}`, all.Streams[0].Ref), &scoped); err != nil || scoped.Matched != 0 {
		t.Fatal(scoped, err)
	}
	// Conditions must coexist on one record, not on different entries in a flow.
	r[3].Group.Facts.OperationID = "operation"
	c = newTestCatalog(t, r, Preferences{})
	if err := callViews(t, c, "inbox_stats", `{"turn_number":131,"filter":{"channel":"todos","iid":2}}`, &stats); err != nil || stats.Pending != 0 {
		t.Fatal(stats, err)
	}
}
func TestViewsStrictSchemaAndTransport(t *testing.T) {
	c := newTestCatalog(t, testRecords(4, 2), Preferences{})
	for _, arg := range []string{
		`{"turn_number":131,"project":"Project A"}`, `{"turn_number":131,"filter":null}`, `{"turn_number":131,"filter":{"iid":"1"}}`,
		`{"turn_number":131,"filter":{"query":"x"}}`, `{"turn_number":131,"filter":{"project":""}}`, `{"turn_number":131,"filter":{"chat":null}}`,
		`{"turn_number":131,"filter":{"chat":"chat","iid":1}}`, `{"turn_number":131,"filter":{"project":"Project A","project_id":8}}`,
		`{"turn_number":131,"within":""}`, `{"turn_number":131,"query":" "}`, `{"turn_number":131,"cursor":""}`, `{"turn_number":131,"cursor":"x","filter":{}}`,
		`{"turn_number":131,"filter":{"kind":"room","channel":"events"}}`, `{"turn_number":131,"filter":{"provider":"objects","chat":"c"}}`,
		`{"turn_number":131,"within":"foreign_member_ref"}`, `{"turn_number":132}`, `{"turn_number":131,"limit":0}`, `{"turn_number":131,"filter":{"iid":1.5}}`,
	} {
		var out List
		if err := callViews(t, c, "inbox_list", arg, &out); err == nil {
			t.Fatalf("accepted %s", arg)
		}
	}
	for _, arg := range []string{`{"turn_number":131,"query":"x"}`, `{"turn_number":131,"state":"all"}`, `{"turn_number":131,"sort":"oldest"}`} {
		var out Overview
		if err := callViews(t, c, "inbox_stats", arg, &out); err == nil {
			t.Fatal(arg)
		}
	}
	tools := ViewsTools()
	b, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 4 || !strings.Contains(string(b), `"additionalProperties":false`) || strings.Contains(string(b), "inbox_find") {
		t.Fatal(string(b))
	}
	for i, tool := range tools {
		if tool.Annotations.ReadOnlyHint == nil || *tool.Annotations.ReadOnlyHint != (i < 3) {
			t.Fatal(tool)
		}
	}
}
func TestViewsScopesStableEmptyAndAmbiguity(t *testing.T) {
	r := testRecords(2, 2)
	other := r[0]
	other.Location = Location{"other", 1}
	other.Group.Account = "other"
	other.Group.Facts.AccountLabel = "other"
	r = append(r, other)
	c := newTestCatalog(t, r, Preferences{})
	var out List
	err := callViews(t, c, "inbox_list", `{"turn_number":131,"filter":{"project":"Project A","iid":1}}`, &out)
	if !errors.Is(err, ErrAmbiguous) || !strings.Contains(err.Error(), "scope_ref") {
		t.Fatal(err)
	}
	ref := r[0].Group.ProjectScopeRef(testMember)
	err = callViews(t, c, "inbox_list", fmt.Sprintf(`{"turn_number":131,"within":%q,"filter":{"iid":1}}`, ref), &out)
	if err != nil || out.Matched != 1 || out.ScopeRef != ref {
		t.Fatal(out, err)
	}
	renamed := r[0].Group
	renamed.Facts.Project = "Renamed"
	renamed.Facts.Label = "Renamed"
	if renamed.ProjectScopeRef(testMember) != ref {
		t.Fatal("rename changed scope")
	}
	if renamed.ProjectScopeRef(Member{"room", "new-instance", "membership"}) == ref {
		t.Fatal("instance leak")
	}
	for i := range r {
		r[i].Pending = false
	}
	if err := c.Rebuild(r, Preferences{}); err != nil {
		t.Fatal(err)
	}
	var stats Overview
	if err := callViews(t, c, "inbox_stats", fmt.Sprintf(`{"turn_number":131,"within":%q}`, ref), &stats); err != nil || stats.Pending != 0 || stats.ScopeRef != ref {
		t.Fatal(stats, err)
	}
}
func TestViewsFixedPaginationBudgetsAndLiteralCoverage(t *testing.T) {
	r := testRecords(200, 100)
	r[50].Group.Facts.Project = "group/project-a"
	r[50].Group.Facts.Summary = "later NEEDLE"
	c := newTestCatalog(t, r, Preferences{})
	var out List
	if err := callViews(t, c, "inbox_list", `{"turn_number":131,"query":"group/project-a needle"}`, &out); err != nil || out.Matched != 1 || out.Streams[0].Pending != 2 {
		t.Fatal(out, err)
	}
	if err := callViews(t, c, "inbox_list", `{"turn_number":131,"limit":2,"sort":"oldest"}`, &out); err != nil || out.Next == "" {
		t.Fatal(out, err)
	}
	cursor := out.Next
	for i := range r {
		r[i].Pending = false
	}
	if err := c.Rebuild(r, Preferences{}); err != nil {
		t.Fatal(err)
	}
	var old, replay List
	arg := fmt.Sprintf(`{"turn_number":131,"cursor":%q}`, cursor)
	if err := callViews(t, c, "inbox_list", arg, &old); err != nil || len(old.Streams) != 2 {
		t.Fatal(old, err)
	}
	_ = callViews(t, c, "inbox_list", arg, &replay)
	if !reflect.DeepEqual(old.Streams, replay.Streams) {
		t.Fatal("snapshot changed")
	}
	var stats Overview
	if err := callViews(t, c, "inbox_stats", arg, &stats); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	c.discoveryBytes = DiscoveryTurnBytes
	if err := callViews(t, c, "inbox_list", arg, &out); err != nil || out.Stop != "directory_budget_exhausted" || out.Next != "" {
		t.Fatal(out, err)
	}
	if err := c.Rebuild(r, Preferences{}); err != nil {
		t.Fatal(err)
	}
	if c.discoveryBytes != DiscoveryTurnBytes {
		t.Fatal("rebuild renewed budget")
	}
}
func BenchmarkViewsRebuild10000(b *testing.B) {
	r := testRecords(10000, 1000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, err := NewCatalog(testMember, 131, testDescriptors, r, Preferences{})
		if err != nil {
			b.Fatal(err)
		}
	}
}
func BenchmarkViewsHotQuery10000(b *testing.B) {
	for _, text := range []bool{false, true} {
		b.Run(fmt.Sprint(text), func(b *testing.B) {
			c := newTestCatalog(b, testRecords(10000, 1000), Preferences{})
			q := ViewsRequest{Turn: 131, Filter: ExactFilter{ProjectID: "7", IID: "999"}}
			if text {
				q.Filter.IID = ""
				q.Text = "Release 999"
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				c.discoveryBytes = 0
				_, err := c.List(q.query())
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestViewsExactFactsAcrossRegisteredFamilies(t *testing.T) {
	records := testRecords(3, 2)
	records[0].Group.Channel = "todos"
	records[0].Group.Facts.ObjectRef = "object_saved"
	chat := records[0]
	chat.Location = Location{"chats", 1}
	chat.Group.Provider = "chats"
	chat.Group.Channel = "messages"
	chat.Group.Context = []string{"chat"}
	chat.Group.Facts = Facts{Chat: "发布值班群", ChatID: "oc_release", ThreadID: "omt_release", Label: "发布值班群"}
	records = append(records, chat)
	room := records[0]
	room.Location = Location{"room", 1}
	room.Group = RoomGroup(testMember, "Room")
	room.Group.Facts.EntryKind = "event"
	room.Group.Facts.EventType = "selection"
	room.Group.Facts.OperationID = "operation_saved"
	records = append(records, room)
	third := records[0]
	third.Location = Location{"third", 1}
	third.Group.Provider = "third"
	third.Group.Channel = "new_channel"
	third.Group.Context = []string{"third"}
	records = append(records, third)
	ds := append(append([]Descriptor{}, testDescriptors...), Descriptor{Provider: "third", Family: "object", Channels: []string{"new_channel"}})
	c, err := NewCatalog(testMember, 131, ds, records, Preferences{})
	if err != nil {
		t.Fatal(err)
	}
	for _, filter := range []string{
		`{"kind":"notification"}`, `{"provider":"third","channel":"new_channel"}`, `{"project":"Project A"}`, `{"project_id":7}`, `{"provider":"objects","object_type":"merge_request","iid":1}`, `{"object_ref":"object_saved"}`, `{"channel":"todos"}`, `{"attention":"normal"}`,
		`{"chat":"发布值班群"}`, `{"chat_id":"oc_release"}`, `{"thread_id":"omt_release"}`, `{"entry_kind":"event"}`, `{"event_type":"selection"}`, `{"operation_id":"operation_saved"}`,
	} {
		var stats Overview
		var list List
		args := `{"turn_number":131,"filter":` + filter + `}`
		if err := callViews(t, c, "inbox_stats", args, &stats); err != nil || stats.Pending == 0 {
			t.Fatal(args, stats, err)
		}
		args = `{"turn_number":131,"state":"all","filter":` + filter + `}`
		if err := callViews(t, c, "inbox_list", args, &list); err != nil || stats.Pending != list.Matched {
			t.Fatal(args, list, err)
		}
		n := 0
		for _, stream := range list.Streams {
			n += stream.Pending
		}
		if n != stats.Entries {
			t.Fatal(args, stats, list)
		}
	}
}
func TestViewsStatsPagesRemainFixed(t *testing.T) {
	r := testRecords(30, 30)
	for i := range r {
		r[i].Group.Account = fmt.Sprint(i)
	}
	c := newTestCatalog(t, r, Preferences{})
	var first, second, replay Overview
	if err := callViews(t, c, "inbox_stats", `{"turn_number":131,"limit":2}`, &first); err != nil || len(first.Sources) != 2 || first.Next == "" {
		t.Fatal(first, err)
	}
	for i := range r {
		r[i].Pending = false
	}
	_ = c.Rebuild(r, Preferences{})
	args := fmt.Sprintf(`{"turn_number":131,"cursor":%q}`, first.Next)
	if err := callViews(t, c, "inbox_stats", args, &second); err != nil || len(second.Sources) != 2 || second.Pending != 30 || !second.ObservedAt.Equal(first.ObservedAt) {
		t.Fatal(second, err)
	}
	_ = callViews(t, c, "inbox_stats", args, &replay)
	if !reflect.DeepEqual(second.Sources, replay.Sources) {
		t.Fatal("stats replay changed")
	}
	var list List
	if err := callViews(t, c, "inbox_list", args, &list); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
}

func TestViewsPublishedSchemaMatchesRequestBranches(t *testing.T) {
	for _, tool := range ViewsTools() {
		var schema jsonschema.Schema
		if err := json.Unmarshal(tool.RawInputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		cases := map[string]bool{}
		if tool.Name == "inbox_stats" || tool.Name == "inbox_list" {
			cases = map[string]bool{`{"turn_number":131}`: true, `{"turn_number":131,"filter":{}}`: true, `{"turn_number":131,"filter":{"project":"a","iid":1}}`: true, `{"turn_number":131,"cursor":"cursor"}`: true, `{"turn_number":131,"cursor":"cursor","filter":{}}`: false, `{"turn_number":131,"filter":{"chat":"c","iid":1}}`: false, `{"turn_number":131,"filter":{"kind":"room","project":"p"}}`: false,
				`{"turn_number":131,"filter":{"kind":"room","provider":"objects"}}`: false, `{"turn_number":131,"filter":{"unknown":"x"}}`: false, `{"turn_number":131,"filter":null}`: false}
		} else if tool.Name == "inbox_preview" {
			cases = map[string]bool{`{"turn_number":131,"stream_ref":"stream"}`: true, `{"turn_number":131,"stream_ref":"stream","limit":4}`: false}
		} else {
			cases = map[string]bool{`{"turn_number":131,"stream_ref":"stream","read_id":"id"}`: true, `{"turn_number":131,"cursor":"cursor"}`: true, `{"turn_number":131,"stream_ref":"stream"}`: false, `{"turn_number":131,"cursor":"cursor","limit":1}`: false}
		}
		for arg, want := range cases {
			var value any
			_ = json.Unmarshal([]byte(arg), &value)
			if err := resolved.Validate(value); (err == nil) != want {
				t.Fatalf("%s %s: %v", tool.Name, arg, err)
			}
		}
	}
}

func TestViewsFirstScreenContinuation(t *testing.T) {
	c := newTestCatalog(t, testRecords(50, 50), Preferences{})
	d, _, err := c.ViewsDirectory()
	if err != nil || len(d.Streams) > 5 || d.Next == "" {
		t.Fatal(d, err)
	}
	var out List
	if err := callViews(t, c, "inbox_list", fmt.Sprintf(`{"turn_number":131,"cursor":%q}`, d.Next), &out); err != nil || len(out.Streams) == 0 {
		t.Fatal(out, err)
	}
	seen := map[string]bool{}
	for _, s := range d.Streams {
		seen[s.Ref] = true
	}
	for _, s := range out.Streams {
		if seen[s.Ref] {
			t.Fatal("duplicate first-screen candidate")
		}
	}
}
