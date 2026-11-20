package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	inputstream "github.com/go-aie/teamx/internal/inputstreamtest"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/mark3labs/mcp-go/mcp"
)

// This backend exists ONLY in tests. It proves selection/DTO interactions and
// deliberately cannot stand in for the durable coordinator or platform Store.
type streamFixture struct {
	mu            sync.Mutex
	records       []inputstream.Record
	entries       map[inputstream.Location]json.RawMessage
	catalog       *inputstream.Catalog
	pages         map[string]*streamFixturePage
	continuations map[string]inputstream.ReadRequest
	remaining     int
	active        string
	reads         []string
	associations  map[inputstream.Location]bool
}
type streamFixturePage struct {
	request   inputstream.ReadRequest
	result    inputstream.Read
	locations []inputstream.Location
	confirmed bool
}

func newStreamFixture(t testing.TB, scenario string) *streamFixture {
	t.Helper()
	f := &streamFixture{entries: map[inputstream.Location]json.RawMessage{}, associations: map[inputstream.Location]bool{}, pages: map[string]*streamFixturePage{}, remaining: 25, continuations: map[string]inputstream.ReadRequest{}}
	member := inputstream.Member{RoomID: "fixture-room", RoomInstance: "fixture-instance", MembershipID: "fixture-member"}
	add := func(g inputstream.Group, partition string, seq uint64, text string, entry any, blocked bool) {
		r := inputstream.Record{Location: inputstream.Location{Partition: partition, Seq: seq}, Group: g, ReceivedAt: time.Unix(int64(seq), 0), Pending: true, Excerpt: text}
		if blocked {
			r.Blocked = "saved source unavailable; other streams remain readable"
		}
		f.records = append(f.records, r)
		b, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		f.entries[r.Location] = b
	}
	human := "只读核查项目 A 的发布阻塞；不要评论、合并或写入平台。核对保存证据，不要声称读完其他流。"
	if scenario == "direct" {
		human += "本次只看项目 A 的 MR !56。"
	}
	if scenario == "preview-return" {
		human += "MR !42 的第一条线索需要先用预览查看；先正式读取该线索指向的失败 MR，再返回 MR !42 正式读取另一条证据。不要先正式读取 MR !42，也不要把预览当作正式读取。"
	}
	room := inputstream.RoomGroup(member, "发布协作室")
	m := RoomMessage{Seq: 1, ClientMessageID: "request", Author: humanCaller.Author, Text: human, CreatedAt: time.Unix(1, 0)}
	message := projectRoomMessage(m)
	add(room, "room", 1, human, struct {
		Type string `json:"type"`
		agentRoomMessage
	}{"message", message}, false)
	count := 1000
	if scenario == "direct" {
		count = 1
	}
	for i := 1; i <= count; i++ {
		iid := int64(i + 100)
		project := "噪声项目"
		projectID := int64(8)
		label := fmt.Sprintf("历史更新 %d", i)
		text := "普通旧消息，无发布结论。"
		if i == count {
			iid = 56
			project = "项目 A"
			projectID = 7
			label = "项目 A／MR !56：发布检查"
			text = "RELEASE_CHECKSUM_MISMATCH：发布流水线产物校验值不一致，尚未解决。只读核查，不评论或合并。"
		}
		if scenario == "preview-return" && i == count-1 {
			iid = 42
			project = "项目 A"
			projectID = 7
			label = "项目 A／MR !42：发布修复"
			text = "本次只是文档排版调整；发布检查结论请先看 MR !56。"
		}
		item := notification.Item{Text: text, CreatedAt: time.Unix(int64(i), 0), Source: platform.SourceRef{Provider: "gitlab", Channel: "events", Instance: "https://gitlab.example.test", SubscriptionID: "events-project", Project: project, Object: label,
			Reference: platform.NotificationReference{Version: 1, RecipientID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "9"}, ProjectID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: fmt.Sprint(projectID)}, Object: platform.ObjectReference{Type: "MergeRequest", IID: platform.ExternalID{Namespace: "gitlab:rest:iid", Value: fmt.Sprint(iid)}}}}}
		group, err := frozenInputCapabilities("gitlab").Input.GroupStoredInput(item)
		if err != nil {
			t.Fatal(err)
		}
		entry := inboxEntryView{Type: "notification", Item: projectNotification(item), Source: followupSourceSummary{SourceRef: fmt.Sprintf("src_%d", i), Status: "resolved"}}
		add(group, "gitlab", uint64(i), text, projectStreamNotification(entry), scenario == "deep-blocked" && i == count-1)
		if scenario == "preview-return" && i == count-1 {
			text = "DOC_ONLY_VERIFIED：MR !42 的第二条证据确认仅修改文档，无发布修复；先前线索不能当作流水线已经恢复。"
			item.Text = text
			entry.Item = projectNotification(item)
			add(group, "gitlab", uint64(count+1), text, projectStreamNotification(entry), false)
		}
	}
	descriptors := []inputstream.Descriptor{inputstream.GitLabCapability().Descriptor, inputstream.LarkCapability().Descriptor}
	c, err := inputstream.NewCatalog(member, 131, descriptors, f.records, inputstream.Preferences{})
	if err != nil {
		t.Fatal(err)
	}
	f.catalog = c
	return f
}

func (f *streamFixture) Prepare(_ context.Context, q inputstream.ReadRequest) (inputstream.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if q.Validate() != nil || q.Turn != 131 {
		return inputstream.Delivery{}, inputstream.ErrInvalid
	}
	cacheKey := q.ReadID
	if q.Cursor != "" {
		cacheKey = q.Cursor
	}
	if previous := f.pages[cacheKey]; previous != nil {
		if !reflect.DeepEqual(q, previous.request) {
			return inputstream.Delivery{}, inputstream.ErrConflict
		}
		return f.delivery(cacheKey, previous), nil
	}
	selection := q
	if q.Cursor != "" {
		var ok bool
		selection, ok = f.continuations[q.Cursor]
		if !ok {
			return inputstream.Delivery{}, inputstream.ErrExpired
		}
	}
	if f.active != "" {
		return inputstream.Delivery{}, ErrBusy
	}
	limit := selection.Limit
	if limit == 0 {
		limit = 20
	}
	records := []inputstream.Record{}
	var group inputstream.Group
	for _, r := range f.records {
		ref, _ := r.Group.StreamRef(inputstream.Member{RoomID: "fixture-room", RoomInstance: "fixture-instance", MembershipID: "fixture-member"})
		if ref == selection.Ref && r.Pending {
			if r.Blocked != "" {
				return inputstream.Delivery{}, errors.New("input_stream_blocked")
			}
			group = r.Group
			records = append(records, r)
		}
	}
	if len(records) == 0 {
		for _, r := range f.records {
			ref, _ := r.Group.StreamRef(inputstream.Member{RoomID: "fixture-room", RoomInstance: "fixture-instance", MembershipID: "fixture-member"})
			if ref == selection.Ref {
				return inputstream.Delivery{}, errors.New("input_stream_empty: no pending entries; use saved history for delivered evidence")
			}
		}
		return inputstream.Delivery{}, inputstream.ErrExpired
	}
	if group.Kind != "room" {
		limit = min(limit, f.remaining)
	}
	limit = min(limit, len(records))
	page := &streamFixturePage{request: q}
	page.result = inputstream.Read{Stream: inputstream.Stream{Ref: selection.Ref, Kind: group.Kind, Label: group.Facts.Label}, Entries: []json.RawMessage{}, HasMore: len(records) > limit}
	for _, r := range records[:limit] {
		page.locations = append(page.locations, r.Location)
		if r.Group.Kind != "room" {
			f.associations[r.Location] = true
		}
		page.result.Entries = append(page.result.Entries, f.entries[r.Location])
	}
	if group.Kind != "room" {
		left := f.remaining - limit
		page.result.Remaining = &left
	}
	if page.result.HasMore && group.Kind != "room" && f.remaining == limit {
		page.result.Stop = "turn_budget_exhausted"
	}
	if page.result.HasMore && page.result.Stop == "" {
		// The fixture fixes the remaining set at construction. It cannot accept hot
		// records, so continuation only needs to preserve stream and explicit limit.
		next := cacheKey + "_next"
		f.continuations[next] = selection
		page.result.Next = &next
	}
	f.pages[cacheKey] = page
	f.active = cacheKey
	return f.delivery(cacheKey, page), nil
}
func (f *streamFixture) delivery(key string, page *streamFixturePage) inputstream.Delivery {
	return inputstream.Delivery{Result: page.result, Confirm: func(context.Context) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if page.confirmed {
			return nil
		}
		for i := range f.records {
			for _, location := range page.locations {
				if f.records[i].Location == location {
					f.records[i].Pending = false
					if f.records[i].Group.Kind != "room" {
						f.remaining--
					}
				}
			}
		}
		page.confirmed = true
		f.active = ""
		f.reads = append(f.reads, page.result.Stream.Label)

		return f.catalog.Rebuild(f.records, inputstream.Preferences{})
	}}
}

func TestInputStreamsProjectionAndIsolatedSelection(t *testing.T) {
	f := newStreamFixture(t, "preview-return")
	service := inputstream.Service{Catalog: f.catalog, Reader: f}
	ctx := context.Background()
	// Every content block is examined; structuredContent must not duplicate internals.
	call := func(name string, args any) *mcp.CallToolResult {
		t.Helper()
		b, _ := json.Marshal(args)
		result, confirm, err := service.Call(ctx, name, b)
		if err != nil {
			t.Fatal(err)
		}
		if result.StructuredContent != nil || len(result.Content) != 1 {
			t.Fatal("extra internal output", result)
		}
		for _, block := range result.Content {
			text, ok := block.(mcp.TextContent)
			if !ok {
				t.Fatal("unexpected content block", block)
			}
			data := []byte(text.Text)
			for _, forbidden := range []string{"partition_ref", "entry_seqs", "read_through", "membership_id", "delivery_id"} {
				if stringContainsJSONKey(data, forbidden) {
					t.Fatal("internal field", forbidden, string(data))
				}
			}
		}
		if confirm != nil {
			if err := confirm(ctx); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	before := f.remaining
	list, err := f.catalog.List(inputstream.Query{Turn: 131, Project: "项目 A", Text: "发布"})
	if err != nil || list.Matched != 2 {
		t.Fatal(list, err)
	}
	ref := list.Streams[0].Ref
	call("inbox_preview", inputstream.PreviewRequest{Turn: 131, Ref: ref})
	if f.remaining != before || len(f.reads) != 0 {
		t.Fatal("preview consumed or associated")
	}
	r := inputstream.ReadRequest{Turn: 131, Ref: ref, ReadID: "read-a", Limit: 1}
	call("inbox_read", r)
	call("inbox_read", r)
	if f.remaining != before-1 {
		t.Fatal("retry reconsumed")
	}
	for _, s := range list.Streams {
		if s.Ref != ref {
			call("inbox_read", inputstream.ReadRequest{Turn: 131, Ref: s.Ref, ReadID: "read-b", Limit: 1})
		}
	}
	call("inbox_read", inputstream.ReadRequest{Turn: 131, Ref: ref, ReadID: "return-a", Limit: 1})
	if f.remaining != 22 {
		t.Fatal("A/B/A progress", f.remaining)
	}
	call("inbox_overview", inputstream.Query{Turn: 131, Project: "项目 A"})
	fresh, _ := f.catalog.Overview(inputstream.Query{Turn: 131, Project: "项目 A"})
	if fresh.Pending != 0 {
		t.Fatal("consumed totals stale", fresh)
	}
	// Original catalog response remains fixed while new queries reflect consumption.
	if list.Streams[0].Pending != 2 {
		t.Fatal("old snapshot changed", list)
	}
}

func stringContainsJSONKey(b []byte, key string) bool {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return true
	}
	var visit func(any) bool
	visit = func(x any) bool {
		switch x := x.(type) {
		case map[string]any:
			for k, v := range x {
				if k == key || visit(v) {
					return true
				}
			}
		case []any:
			for _, v := range x {
				if visit(v) {
					return true
				}
			}
		}
		return false
	}
	return visit(v)
}

func TestInputStreamBusinessProjectionPreservesEvidence(t *testing.T) {
	item := notification.Item{Text: "保留限制与撤销；unknown 动作需要核验", Source: platform.SourceRef{Provider: "lark", Channel: "messages", Instance: "https://open.feishu.cn", Reference: platform.NotificationReference{RecipientID: platform.ExternalID{Namespace: "lark:open_id:cli_a", Value: "ou_me"}, ParentID: platform.ExternalID{Namespace: "lark:message_id:cli_a", Value: "om_parent"}}}, Content: &notification.Content{Text: "原文限制", ContextOnly: true, Sender: &notification.Participant{Name: "原作者", ID: platform.ExternalID{Namespace: "lark:open_id:cli_a", Value: "ou_other"}}, Truncated: true, Attachments: []notification.Attachment{{Type: "image", Reference: "image_key", Status: "not_supplied"}}}}
	view := inboxEntryView{Type: InboxPlatformNotification, Seq: 77, Item: projectNotification(item), Source: followupSourceSummary{SourceRef: "src_saved", ObjectRef: "obj_saved", TargetRef: "target_saved", Seq: 77, Status: "partial", ReasonCode: "missing_native_fact"}}
	projected := projectStreamNotification(view)
	if !reflect.DeepEqual(projected.Item, view.Item) || projected.Source.Ref != "src_saved" || projected.Source.ObjectRef != "obj_saved" || projected.Source.Status != "partial" || projected.Source.ReasonCode != "missing_native_fact" {
		t.Fatal("business evidence dropped", projected)
	}
	b, err := json.Marshal(projected)
	if err != nil || stringContainsJSONKey(b, "seq") || stringContainsJSONKey(b, "delivery_id") {
		t.Fatal("platform storage location leaked", string(b), err)
	}
}

func TestInputFixtureFixedReadCursorAndSharedBudget(t *testing.T) {
	f := newStreamFixture(t, "direct")
	original := f.records[1]
	for i := uint64(2); i <= 26; i++ {
		entry := original
		entry.Location.Seq = i
		f.records = append(f.records, entry)
		f.entries[entry.Location] = f.entries[original.Location]
	}
	member := inputstream.Member{RoomID: "fixture-room", RoomInstance: "fixture-instance", MembershipID: "fixture-member"}
	var err error
	f.catalog, err = inputstream.NewCatalog(member, 131, []inputstream.Descriptor{inputstream.GitLabCapability().Descriptor}, f.records, inputstream.Preferences{})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := original.Group.StreamRef(member)
	request := inputstream.ReadRequest{Turn: 131, Ref: ref, ReadID: "fixed", Limit: 20}
	first, err := f.Prepare(context.Background(), request)
	if err != nil || len(first.Result.Entries) != 20 || first.Result.Next == nil {
		t.Fatal(first.Result, err)
	}
	changed := request
	changed.Limit = 1
	if _, err := f.Prepare(context.Background(), changed); err != inputstream.ErrConflict {
		t.Fatal("changed read parameters accepted", err)
	}
	if _, err := f.Prepare(context.Background(), inputstream.ReadRequest{Turn: 131, Ref: ref, ReadID: "another"}); err != ErrBusy {
		t.Fatal("second unconfirmed page allowed", err)
	}
	if err := first.Confirm(context.Background()); err != nil {
		t.Fatal(err)
	}
	next, err := f.Prepare(context.Background(), inputstream.ReadRequest{Turn: 131, Cursor: *first.Result.Next})
	if err != nil || len(next.Result.Entries) != 5 || next.Result.Stop != "turn_budget_exhausted" || !next.Result.HasMore {
		t.Fatal(next.Result, err)
	}
	if err := next.Confirm(context.Background()); err != nil {
		t.Fatal(err)
	}
	retry, err := f.Prepare(context.Background(), request)
	if err != nil || len(retry.Result.Entries) != 20 || *retry.Result.Remaining != 5 {
		t.Fatal("first-page replay changed", retry.Result, err)
	}
	if err := retry.Confirm(context.Background()); err != nil || f.remaining != 0 {
		t.Fatal("retry reset/recharged budget", err, f.remaining)
	}
	b, _ := json.Marshal(next.Result)
	var result map[string]any
	_ = json.Unmarshal(b, &result)
	if value, exists := result["next_cursor"]; !exists || value != nil {
		t.Fatal("exhausted cursor must be null", string(b))
	}
}
