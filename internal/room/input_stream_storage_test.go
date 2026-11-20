package room

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	frozen "github.com/go-aie/teamx/internal/inputstreamtest/frozen"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func storageAdapters() map[notification.SourceID]notificationAdapter {
	return defaultNotificationAdapters(nil)
}
func storedGitLab(id MemberIdentity, key, channel, project, iid, text string) notification.Item {
	return notification.Item{Key: key, DeliveryID: key, CreatedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Order: key, Text: text, Route: notification.Route{Version: 1, Target: notification.Target{Mode: "inbox", AgentID: id.AgentID, MembershipID: id.MembershipID}}, Source: platform.SourceRef{Provider: "gitlab", Channel: channel, Instance: "https://gitlab.example.com", SubscriptionID: inputstream.Digest([]byte(channel))[:32], ItemKey: key, Project: "研发项目", Reference: platform.NotificationReference{Version: 1, RecipientID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "9"}, ProjectID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: project}, Object: platform.ObjectReference{Type: "MergeRequest", IID: platform.ExternalID{Namespace: "gitlab:rest:iid", Value: iid}}}}}
}
func storedLark(id MemberIdentity, key, chat, name, thread, text string) notification.Item {
	item := storedGitLab(id, key, "events", "7", "42", text)
	item.Source = platform.SourceRef{Provider: "lark", Channel: "messages", Instance: "https://open.feishu.cn", SubscriptionID: inputstream.Digest([]byte("messages"))[:32], ItemKey: key, Reference: platform.NotificationReference{Version: 2, AccountTenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "tenant"}, RecipientID: platform.ExternalID{Namespace: "lark:open_id:cli_a", Value: "ou_reader"}, ChatID: platform.ExternalID{Namespace: "lark:chat_id:cli_a", Value: chat}, Target: platform.ObjectReference{Type: "message", ID: platform.ExternalID{Namespace: "lark:message_id:cli_a", Value: "om_" + key}}, DiscussionID: platform.ExternalID{Namespace: "lark:thread_id:cli_a", Value: thread}}}
	item.Content = &notification.Content{Text: text, Structured: true, Conversation: &notification.Context{ID: item.Source.Reference.ChatID, Name: name}}
	return item
}
func TestPlatformInputLocalDeliveryAndMemberIsolation(t *testing.T) {
	room := historyRoom(0, "")
	room.Members = append(room.Members, MemberState{AgentID: "B", MembershipID: "generation-B", Runner: room.Members[0].Runner})
	h := newRoomHarness(t, room, nil)
	aID := h.memberID("A", "generation-A")
	bID := h.memberID("B", "generation-B")
	a, err := h.store.openPlatformInputStorage(aID, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	// No input means no new partition/ledger directories.
	if _, err := os.Stat(filepath.Join(h.store.dir, "inboxes", aID.MembershipID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("empty partition created", err)
	}
	item := storedGitLab(aID, "delivery", "events", "7", "42", "流水线阻塞")
	receipt, err := a.Receive(item, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	again, err := a.Receive(item, time.Now())
	if err != nil || receipt != again {
		t.Fatal(again, err)
	}
	b, err := h.store.openPlatformInputStorage(bID, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Receive(item, time.Now()); !errors.Is(err, ErrIdentity) {
		t.Fatal("foreign target accepted", err)
	}
	item.Route.Target = notification.Target{Mode: "inbox", AgentID: bID.AgentID, MembershipID: bID.MembershipID}
	bReceipt, err := b.Receive(item, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if bReceipt.Seq != receipt.Seq || bReceipt.Partition == receipt.Partition {
		t.Fatal("member namespace lost")
	}
	snap, err := a.repository.Snapshot("gitlab")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.repository.Read(snap, inputstream.Location{Partition: snap.Partition, Seq: 1}); !errors.Is(err, inputstream.ErrIdentity) {
		t.Fatal(err)
	}
	// No legacy header, consumption, source association or formal delivery file.
	for _, name := range []string{"inbox.json", "consumption.json"} {
		if _, err := os.Stat(filepath.Join(h.store.dir, "inboxes", aID.MembershipID, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal(name, err)
		}
	}
	aCatalog, err := a.legacyCatalog(1, inputstream.Observation{})
	if err != nil {
		t.Fatal(err)
	}
	bCatalog, err := b.legacyCatalog(1, inputstream.Observation{})
	if err != nil {
		t.Fatal(err)
	}
	aList, _ := aCatalog.List(frozen.Query{})
	bList, _ := bCatalog.List(frozen.Query{})
	if aList.Streams[0].Ref == bList.Streams[0].Ref {
		t.Fatal("member stream refs merged")
	}
	// Reopen with the same original Store lock, lazy lifecycle and native owner.
	dir := h.store.dir
	if err := h.rt.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.rt = nil
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir, room.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	h.store = reopened
	newA, err := reopened.openPlatformInputStorage(aID, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	item.Route.Target = notification.Target{Mode: "inbox", AgentID: aID.AgentID, MembershipID: aID.MembershipID}
	got, err := newA.Receive(item, time.Now())
	if err != nil || got != receipt {
		t.Fatal("reopen lost receipt", got, err)
	}
}
func TestPlatformNativeQueryBoundariesAndSameRecord(t *testing.T) {
	h := newRoomHarness(t, historyRoom(0, ""), nil)
	id := h.memberID("A", "generation-A")
	p, err := h.store.openPlatformInputStorage(id, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	items := []notification.Item{
		storedGitLab(id, "g1", "events", "7", "42", "发布阻塞"), storedGitLab(id, "g2", "todos", "7", "42", "普通检查"), storedGitLab(id, "g3", "todos", "7", "56", "发布阻塞"), storedGitLab(id, "g4", "events", "8", "42", "另一个项目"),
		storedLark(id, "l1", "oc_one", "产品群", "omt_one", "发布阻塞"), storedLark(id, "l2", "oc_one", "产品群", "omt_two", "普通消息"), storedLark(id, "l3", "oc_two", "产品群", "omt_two", "第二个同名群"),
	}
	for _, item := range items {
		if _, err := p.Receive(item, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	c, err := p.legacyCatalog(1, inputstream.Observation{})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := c.Stats(frozen.StatsQuery{})
	if err != nil || stats.Entries != 7 || stats.Pending != 5 {
		t.Fatal(stats, err)
	}
	var gitlabScope, larkScope string
	for _, s := range stats.Sources {
		guide, err := c.DescribeInput(s.ScopeRef)
		if err != nil {
			t.Fatal(err)
		}
		if guide.Version == "gitlab/filter/v1" {
			gitlabScope = s.ScopeRef
		} else if guide.Version == "lark/filter/v1" {
			larkScope = s.ScopeRef
		}
	}
	if gitlabScope == "" || larkScope == "" {
		t.Fatal(stats)
	}
	filter := []byte(`{"project_id":7,"channel":"todos"}`)
	list, err := c.List(frozen.Query{Within: gitlabScope, Filter: filter, Text: "发布 阻塞", State: "all"})
	if err != nil || list.Matched != 1 || list.Streams[0].Pending != 1 {
		t.Fatal("different native/text rows co-matched", list, err)
	}
	matched, err := c.Stats(frozen.StatsQuery{Within: gitlabScope, Filter: filter})
	if err != nil || matched.Pending != 2 || matched.Entries != 3 {
		t.Fatal("whole stream counts", matched, err)
	}
	for _, tc := range []struct {
		within, filter string
		want           error
	}{
		{gitlabScope, `{"iid":"42"}`, frozen.ErrAmbiguous},
		{gitlabScope, `{"project_id":"7","project":"wrong"}`, inputstream.ErrConflict},
		{gitlabScope, `{"chat_id":"oc_one"}`, inputstream.ErrUnsupported},
		{larkScope, `{"project_id":"7"}`, inputstream.ErrUnsupported},
		{larkScope, `{"chat":"产品群"}`, frozen.ErrAmbiguous},
		{larkScope, `{"chat_id":"oc_one","chat":"other"}`, inputstream.ErrConflict},
	} {
		_, err := c.List(frozen.Query{Within: tc.within, Filter: []byte(tc.filter), State: "all"})
		if !errors.Is(err, tc.want) {
			t.Fatal(tc, err)
		}
		var ambiguous *frozen.ScopeError
		if errors.As(err, &ambiguous) {
			if len(ambiguous.Options) > 5 {
				t.Fatal(err)
			}
			for _, option := range ambiguous.Options {
				if option.Ref == "" {
					t.Fatal("unusable choice")
				}
			}
		}
	}
	thread, err := c.List(frozen.Query{Within: larkScope, Filter: []byte(`{"chat_id":"oc_one","thread_id":"omt_two"}`), State: "all"})
	if err != nil || thread.Matched != 1 || thread.Streams[0].Pending != 2 {
		t.Fatal("thread split chat", thread, err)
	}
	// One saved item lacks the parent IID; fallback cannot pretend an exact match.
	missing := storedGitLab(id, "g5", "events", "7", "", "missing iid")
	missing.Source.Reference.Object.IID = platform.ExternalID{}
	if _, err := p.Receive(missing, time.Now()); err != nil {
		t.Fatal(err)
	}
	newC, _ := p.legacyCatalog(2, inputstream.Observation{})
	fallback, err := newC.List(frozen.Query{Within: gitlabScope, Filter: []byte(`{"project_id":"7","iid":"999"}`), State: "all"})
	if err != nil || fallback.Matched != 0 || fallback.Notice == "" {
		t.Fatal(fallback, err)
	}
	crossApp := storedLark(id, "cross-app-thread", "oc_one", "产品群", "omt_cross", "saved mismatched thread")
	crossApp.Source.Reference.DiscussionID.Namespace = "lark:thread_id:cli_other"
	if _, err := p.Receive(crossApp, time.Now()); err != nil {
		t.Fatal(err)
	}
	finalCatalog, _ := p.legacyCatalog(3, inputstream.Observation{})
	if out, err := finalCatalog.List(frozen.Query{Within: larkScope, Filter: []byte(`{"thread_id":"omt_cross"}`), State: "all"}); err != nil || out.Matched != 0 || out.Notice == "" {
		t.Fatal("cross-app thread promoted", out, err)
	}
	// Supplemental display content can change without changing dedup semantics.
	item := items[0]
	item.Content = &notification.Content{Text: "new saved projection", Structured: true}
	receipt, err := p.Receive(item, time.Now())
	if err != nil || receipt.Seq != 1 {
		t.Fatal(receipt, err)
	}
}
func TestInputStorageUsesStaticSourceAssembly(t *testing.T) {
	codecs, err := inputStorageCodecs(storageAdapters(), notificationCapabilities)
	if err != nil || len(codecs) != 2 {
		t.Fatal(codecs, err)
	}
	before := []inputstream.Descriptor{}
	for _, c := range codecs {
		before = append(before, c.Descriptor())
	}
	// Removing a registered channel changes only the assembled descriptor.
	adapters := storageAdapters()
	delete(adapters, notification.SourceID{Provider: "gitlab", Channel: "todos"})
	after, err := inputStorageCodecs(adapters, notificationCapabilities)
	if err != nil || len(after) != 2 {
		t.Fatal(after, err)
	}
	if len(after[0].Descriptor().Channels) != 1 || !reflect.DeepEqual(after[1].Descriptor(), before[1]) {
		t.Fatal("independent source assembly lost")
	}
}

// A third adapter's native evidence models approval cases and nodes. The
// source assembly changes here; common DTOs, schema, storage and queries do not.
type approvalFixturePayload struct{ Receiver, Definition, Name, Instance, Node, Status string }
type approvalFixtureCodec struct{}

func (approvalFixtureCodec) Descriptor() inputstream.Descriptor {
	return inputstream.Descriptor{Provider: "approvals", Codec: "approval.case", Schema: 1, GroupingVersion: 1, Channels: []string{"changes", "reminders"}, Policy: inputstream.PlatformPolicy()}
}
func (approvalFixtureCodec) Decode(v int, b json.RawMessage) (inputstream.Projection, error) {
	var p approvalFixturePayload
	if v != 1 {
		return inputstream.Projection{}, inputstream.ErrVersion
	}
	if err := inputstream.StrictDecode(b, &p); err != nil {
		return inputstream.Projection{}, err
	}
	g := inputstream.Group{Version: 1, Instance: "https://approvals.test", Account: p.Receiver, Precision: "context", Context: []string{p.Instance}}
	return inputstream.Projection{Group: g, Label: p.Instance, SourceLabel: p.Receiver}, g.Validate()
}

type approvalQueryCodec struct{ approvalFixtureCodec }

func (c approvalQueryCodec) Decode(v int, b json.RawMessage) (frozen.Projection, error) {
	base, err := c.approvalFixtureCodec.Decode(v, b)
	if err != nil {
		return frozen.Projection{}, err
	}
	var payload approvalFixturePayload
	if err := inputstream.StrictDecode(b, &payload); err != nil {
		return frozen.Projection{}, err
	}
	return frozen.Projection{Projection: base, Scopes: []frozen.Scope{{Key: payload.Definition, Label: payload.Name}}, Text: payload.Status}, nil
}

func (approvalQueryCodec) NewIndex() frozen.NativeIndex {
	return &approvalFixtureIndex{rows: map[inputstream.Location]approvalFixturePayload{}}
}
func (approvalQueryCodec) DescribeInput() frozen.Guidance {
	return frozen.Guidance{Version: "approval/v1", Text: "Native filters: node and status. Local saved approval cases; no remote permission is granted."}
}

type approvalFixtureIndex struct {
	rows map[inputstream.Location]approvalFixturePayload
}

func (i *approvalFixtureIndex) Add(r inputstream.StoredRecord) error {
	var p approvalFixturePayload
	if err := inputstream.StrictDecode(r.Payload, &p); err != nil {
		return err
	}
	i.rows[r.Location] = p
	return nil
}
func (i *approvalFixtureIndex) Clone() frozen.NativeIndex {
	out := &approvalFixtureIndex{rows: map[inputstream.Location]approvalFixturePayload{}}
	for k, v := range i.rows {
		out.rows[k] = v
	}
	return out
}
func (i *approvalFixtureIndex) CompileInputFilter(c frozen.FilterContext, b json.RawMessage) (frozen.Selection, error) {
	var f struct {
		Node   string `json:"node"`
		Status string `json:"status"`
	}
	if err := frozen.DecodeFilter(b, &f); err != nil {
		return frozen.Selection{}, err
	}
	out := frozen.Selection{}
	for _, l := range c.Allowed {
		p := i.rows[l]
		if f.Node != "" && p.Node != f.Node || f.Status != "" && p.Status != f.Status {
			continue
		}
		out.Entries = append(out.Entries, l)
	}
	return out, nil
}
func TestThirdNativeSourceAndAdditionalChannelUseSameStorage(t *testing.T) {
	schema := frozen.DiscoverySchema
	h := newRoomHarness(t, historyRoom(0, ""), nil)
	id := h.memberID("A", "generation-A")
	adapters := storageAdapters()
	for _, channel := range []string{"changes", "reminders"} {
		key := notification.SourceID{Provider: "approvals", Channel: channel}
		adapters[key] = notificationAdapter{Source: inputFixtureSource{id: key}}
	}
	h.store.notificationAdapters = func(provider string) notification.Capabilities {
		if provider != "approvals" {
			return notificationCapabilities(provider)
		}
		return notification.Capabilities{Input: &notification.InputCapability{Codec: approvalFixtureCodec{}, EncodeStoredInput: func(item notification.Item) (inputstream.AppendRequest, error) {
			return inputstream.AppendRequest{DeliveryID: item.DeliveryID, Channel: item.Source.Channel, Payload: json.RawMessage(item.Text), CreatedAt: item.CreatedAt}, nil
		}}}
	}
	p, err := h.store.openPlatformInputStorage(id, adapters)
	if err != nil {
		t.Fatal(err)
	}
	for n, channel := range []string{"changes", "reminders"} {
		item := storedGitLab(id, "approval-"+channel, "events", "7", "42", "")
		native := approvalFixturePayload{"native-receiver", "travel-definition", "出差审批", "case-42", "manager", "waiting"}
		if n == 1 {
			native.Node = "finance"
		}
		body, _ := json.Marshal(native)
		item.Text = string(body)
		item.Source = platform.SourceRef{Provider: "approvals", Channel: channel, Instance: "https://approvals.test", SubscriptionID: inputstream.Digest([]byte(channel))[:32], ItemKey: item.Key}
		if _, err := p.Receive(item, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	c, err := p.legacyCatalog(1, inputstream.Observation{})
	if err != nil {
		t.Fatal(err)
	}
	stats, _ := c.Stats(frozen.StatsQuery{})
	if stats.Pending != 1 || stats.Entries != 2 {
		t.Fatal(stats)
	}
	source := stats.Sources[0].ScopeRef
	list, err := c.List(frozen.Query{Within: source, Filter: []byte(`{"node":"finance"}`), State: "all"})
	if err != nil || list.Matched != 1 || list.Streams[0].Pending != 2 {
		t.Fatal(list, err)
	}
	if _, err := c.List(frozen.Query{Within: source, Filter: []byte(`{"iid":42}`)}); !errors.Is(err, inputstream.ErrUnsupported) {
		t.Fatal(err)
	}
	if g, err := c.DescribeInput(source); err != nil || g.Version != "approval/v1" {
		t.Fatal(g, err)
	}
	if schema != frozen.DiscoverySchema {
		t.Fatal("new native model expanded the common schema")
	}
	entries, err := os.ReadDir(filepath.Join(h.store.dir, "inboxes", id.MembershipID))
	if err != nil || len(entries) != 1 || entries[0].Name() != "approvals" {
		t.Fatal("channel allocated another partition", entries, err)
	}
}

func TestPlatformInputUnknownCommitFreezesOwningStore(t *testing.T) {
	h := newRoomHarness(t, historyRoom(0, ""), nil)
	id := h.memberID("A", "generation-A")
	p, err := h.store.openPlatformInputStorage(id, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	rename := h.store.ops.rename
	h.store.ops.rename = func(old, next string) error {
		if filepath.Base(next) == "partition.json" {
			if err := rename(old, next); err != nil {
				return err
			}
			return errors.New("ambiguous rename after publication")
		}
		return rename(old, next)
	}
	item := storedGitLab(id, "one", "events", "7", "42", "retained after lost source receipt")
	if _, err := p.Receive(item, time.Now()); !errors.Is(err, ErrCommitUnknown) || !errors.Is(err, inputstream.ErrCommitUnknown) {
		t.Fatal(err)
	}
	if err := h.store.SaveRoom(historyRoom(0, "")); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal("unrelated Store write remained open", err)
	}
	dir := h.store.dir
	if err := h.rt.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.rt = nil
	if err := h.store.Close(); err != nil {
		t.Fatal(err)
	}
	h.store, err = OpenStore(dir, id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	p, err = h.store.openPlatformInputStorage(id, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := p.Receive(item, time.Now())
	if err != nil || receipt.Seq != 1 {
		t.Fatal(receipt, err)
	}
}

func TestMigratedSourceReferenceSurvivesConsumptionAndReopen(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	const oldSeq = 73
	item := storedGitLab(id, "legacy-delivery", "events", "7", "42", "saved legacy evidence")
	cap := h.store.notificationCapabilities("gitlab").Input
	request, err := cap.EncodeStoredInput(item)
	if err != nil {
		t.Fatal(err)
	}
	request.Seq = oldSeq
	request.ReceivedAt = item.CreatedAt
	oldRef := inputSourceRef(InboxEntry{MemberIdentity: id, Seq: oldSeq, Item: item})
	request.SourceRef = oldRef
	h.store.mu.Lock()
	receipt, err := p.repository.AppendOnce("gitlab", request)
	h.store.mu.Unlock()
	if err != nil || receipt.Seq != oldSeq || receipt.SourceRef != oldRef {
		t.Fatal(receipt, err)
	}
	loc := inputstream.Location{Partition: receipt.Partition, Seq: receipt.Seq}
	if oldRef == inputSourceRef(InboxEntry{MemberIdentity: id, Seq: oldSeq, Item: item, Location: &loc}) {
		t.Fatal("fixture did not exercise a reference change")
	}

	s := startPlatformReader(t, h, p, svc, 1)
	stream := streamFor(t, s, "saved legacy evidence")
	view := deliverPlatform(t, s, stream, "saved")
	if len(view.Entries) != 1 || view.Entries[0].Source.Ref != oldRef {
		t.Fatal(view)
	}

	if err := p.repository.Seal(s.execution); err != nil {
		t.Fatal(err)
	}

	codecs, err := inputStorageCodecs(storageAdapters(), h.store.notificationCapabilities)
	if err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	p.repository, err = inputstream.OpenRepository(h.store.dir, p.repositoryOwner(), codecs, inputPersistence{h.store})
	h.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	link, entry, err := h.store.readFollowupSourceContent(id, 0, oldRef)
	if err != nil || entry == nil || entry.Item.Text != item.Text || link.SourceRef != oldRef || link.Location == nil || *link.Location != loc {
		t.Fatal(link, entry, err)
	}
	inventory := inputSourceInventory{Revision: "source-acknowledged", Pending: map[notification.SourceID]map[string]bool{
		{Provider: "gitlab", Channel: "events"}: {},
	}}
	plan, err := p.maintain("gitlab", inventory, item.CreatedAt.Add(7*24*time.Hour))
	if err != nil || plan.Through != oldSeq {
		t.Fatal(plan, err)
	}
	link, entry, err = h.store.readFollowupSourceContent(id, 0, oldRef)
	if err != nil || entry != nil || link.SourceRef != oldRef || link.Location == nil || *link.Location != loc {
		t.Fatal("reclaim lost the reference", link, entry, err)
	}
}
