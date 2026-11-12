package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type ingestionReader struct {
	*fakeReader
	chats      map[string]Chat
	search     func(MessageSearchRequest) (MessageSearchPage, error)
	batch      func([]string) (MessageBatch, error)
	membership func(string, string) (MembershipPage, error)
	requests   []MessageSearchRequest
	batches    [][]string
	members    []string
	chatReads  int
}

func (f *ingestionReader) Chat(_ context.Context, a platform.AccountIdentity, id string) (Chat, error) {
	f.chatReads++
	c, ok := f.chats[id]
	if !ok {
		return Chat{}, failure("conversation_unverified")
	}
	return c, nil
}
func (f *ingestionReader) SearchMessages(_ context.Context, _ platform.AccountIdentity, q MessageSearchRequest) (MessageSearchPage, error) {
	f.requests = append(f.requests, q)
	if f.search != nil {
		return f.search(q)
	}
	return MessageSearchPage{Items: []MessageSearchItem{}}, nil
}
func (f *ingestionReader) BatchMessages(_ context.Context, _ platform.AccountIdentity, ids []string) (MessageBatch, error) {
	f.batches = append(f.batches, slices.Clone(ids))
	if f.batch != nil {
		return f.batch(ids)
	}
	var result MessageBatch
	for _, id := range ids {
		if m, ok := f.messages[id]; ok {
			result.Items = append(result.Items, m)
		} else {
			result.MissingIDs = append(result.MissingIDs, id)
		}
	}
	return result, nil
}
func (f *ingestionReader) GroupMembership(_ context.Context, _ platform.AccountIdentity, id, token string) (MembershipPage, error) {
	f.members = append(f.members, id+":"+token)
	if f.membership != nil {
		return f.membership(id, token)
	}
	return MembershipPage{Joined: true}, nil
}

func discoveryItem(m Message, private bool) MessageSearchItem {
	var item MessageSearchItem
	item.ID, item.MetaData.MessageID, item.MetaData.ChatID, item.MetaData.FromID = m.ID, m.ID, m.ChatID, m.Sender.ID
	ms, _ := strconv.ParseInt(m.Created, 10, 64)
	item.MetaData.Created = time.UnixMilli(ms).UTC().Format(time.RFC3339Nano)
	item.MetaData.IsP2PChat = &private
	return item
}
func ingestionHarness(t *testing.T, cfg Config) (*stepHarness, *ingestionReader, *SearchSource) {
	t.Helper()
	base := sampleState(t)
	if cfg.PlatformID == "" {
		cfg.PlatformID, cfg.Target, cfg.Enabled = base.Config.PlatformID, base.Config.Target, true
	}
	s, err := ConfigureSearch(base.RoomIdentity, nil, base.Binding, cfg, time.Unix(1000, 0))
	if err != nil {
		t.Fatal(err)
	}
	h := &stepHarness{s: s, now: time.Unix(1010, 0), limits: notification.DefaultLimits()}
	f := &ingestionReader{fakeReader: &fakeReader{messages: map[string]Message{}}, chats: map[string]Chat{}}
	f.pages = func(MessagesRequest) (MessagePage, error) {
		t.Fatal("search used legacy history")
		return MessagePage{}, nil
	}
	source := &SearchSource{Source: Source{Reader: func(QueryScope) (MessageReader, error) { return f, nil }}}
	return h, f, source
}
func ingestionSteps(t *testing.T, h *stepHarness, source *SearchSource, count int) {
	t.Helper()
	// Recreate the runner and strictly decode the saved state at every boundary.
	// Nothing required for page recovery may live only in a runner variable.
	for range count {
		raw, err := json.Marshal(h.s)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := source.Decode(raw, h.s.RoomIdentity)
		if err != nil {
			t.Fatal("reload", err, string(raw))
		}
		h.s = decoded.(State)
		source.NewRunner(h).Step(t.Context())
	}
}

func TestSearchIngestionConfigurationMatrix(t *testing.T) {
	for _, private := range []string{"off", "all", "specified"} {
		for _, groups := range []string{"off", "all", "specified"} {
			for _, mode := range []string{"all", "me", "everyone", "both", "own"} {
				t.Run(private+"/"+groups+"/"+mode, func(t *testing.T) {
					if private == "off" && groups == "off" {
						return
					}
					cfg := Config{Private: PrivateConfig{Enabled: private != "off", Scope: "all", ChatIDs: []string{"oc_peer"}}, Groups: GroupConfig{Enabled: groups != "off", Scope: "specified", ChatIDs: []string{"oc_group"}, MessageMode: "mentions", AtMe: mode == "me" || mode == "both", AtAll: mode == "everyone" || mode == "both"}}
					if private == "specified" {
						cfg.Private.Scope = private
					}
					if groups == "all" {
						cfg.Groups.Scope = groups
					}
					if mode == "all" {
						cfg.Groups.MessageMode = mode
					}
					h, f, source := ingestionHarness(t, cfg)
					f.chats["oc_peer"] = peerChat("oc_peer", "p2p")
					f.chats["oc_group"] = Chat{ID: "oc_group", Mode: "topic"}
					msgs := []Message{sampleMessage("om_peer", "oc_peer", "peer"), sampleMessage("om_me", "oc_group", "me"), sampleMessage("om_all", "oc_group", "@_all"), sampleMessage("om_both", "oc_group", "both"), sampleMessage("om_own", "oc_group", "own"), sampleMessage("om_ordinary", "oc_group", "ordinary"), sampleMessage("om_system", "oc_group", "system")}
					msgs[1].Mentions = []Mention{{ID: "ou_reader", IDType: "open_id"}}
					msgs[3].Mentions = []Mention{{ID: "ou_reader", IDType: "open_id"}, {ID: "all"}}
					msgs[4].Sender.ID = "ou_reader"
					msgs[4].ParentID, msgs[4].RootID, msgs[4].ThreadID = "om_root", "om_root", "omt_thread"
					msgs[6].Type = "system"
					for _, m := range msgs {
						f.messages[m.ID] = m
					}
					f.search = func(q MessageSearchRequest) (MessageSearchPage, error) {
						page := MessageSearchPage{Items: []MessageSearchItem{}}
						for _, m := range msgs {
							p2p := m.ChatID == "oc_peer"
							if p2p != (q.ChatType == "p2p") || q.FromID != "" && m.Sender.ID != q.FromID {
								continue
							}
							// Include false positives to exercise native local predicates.
							page.Items = append(page.Items, discoveryItem(m, p2p))
						}
						return page, nil
					}
					ingestionSteps(t, h, source, 90)
					var want []string
					if private != "off" {
						want = append(want, "om_peer")
					}
					if groups != "off" {
						want = append(want, "om_own")
						if mode != "own" {
							want = append(want, "om_both")
						}
						if mode == "me" || mode == "both" || mode == "all" {
							want = append(want, "om_me")
						}
						if mode == "everyone" || mode == "both" || mode == "all" {
							want = append(want, "om_all")
						}
						if mode == "all" {
							want = append(want, "om_ordinary")
						}
					}
					var got []string
					for _, e := range h.s.Inbox {
						got = append(got, e.Message.ID)
					}
					slices.Sort(want)
					slices.Sort(got)
					if !slices.Equal(got, want) {
						t.Fatalf("got %v want %v; branches %+v", got, want, h.s.Search.Branches)
					}
					if h.s.Directory != nil || h.s.LastFull.IsZero() {
						t.Fatal("search completion depends on directory")
					}
					for _, q := range f.requests {
						if q.ChatType == "p2p" && (len(q.ChatIDs) == 0) != (private == "all") {
							t.Fatal(q)
						}
						if q.ChatType == "group" && (len(q.ChatIDs) == 0) != (groups == "all") {
							t.Fatal(q)
						}
					}
					if e, ok := h.s.Inbox[RevisionKey(h.s.Binding, msgs[4])]; ok && e.Item.Source.Reference.DiscussionID.Value != "omt_thread" {
						t.Fatal("thread lost")
					}
				})
			}
		}
	}
}

func TestSearchPageRecoveryMissingDetailsAndCommitFailure(t *testing.T) {
	h, f, source := ingestionHarness(t, Config{Private: PrivateConfig{Enabled: true, Scope: "all"}})
	one, two := sampleMessage("om_one", "oc_peer", "one"), sampleMessage("om_two", "oc_peer", "two")
	f.messages[one.ID], f.messages[two.ID] = one, two
	f.chats[one.ChatID] = peerChat(one.ChatID, "p2p")
	f.search = func(q MessageSearchRequest) (MessageSearchPage, error) {
		return MessageSearchPage{Items: []MessageSearchItem{discoveryItem(two, true), discoveryItem(one, true)}}, nil
	}
	f.batch = func([]string) (MessageBatch, error) {
		return MessageBatch{Items: []Message{one}, MissingIDs: []string{two.ID}}, nil
	}
	ingestionSteps(t, h, source, 3)
	if h.s.Search.Branches[0].Scan.Page.Done != 0 || len(f.batches) != 1 {
		t.Fatal("batch did not persist")
	}
	ingestionSteps(t, h, source, 2) // single fallback, native relationship
	h.failResult = true
	ingestionSteps(t, h, source, 1)
	if len(h.s.Inbox) != 0 || h.s.Search.Branches[0].Scan.Page.Done != 0 {
		t.Fatal("failed commit advanced admission")
	}
	h.failResult = false
	ingestionSteps(t, h, source, 5)
	if len(h.s.Inbox) != 2 || h.s.Search.Branches[0].Completed != 1009 || len(f.requests) != 1 || len(f.batches) != 1 {
		t.Fatal("recovery repeated discovery/lost items", h.s.Search)
	}
	// Replay overlaps and a changed revision use the frozen original route.
	for key := range h.s.Inbox {
		h.s = h.s.Acknowledge(key).(State)
	}
	cfg := h.s.Config
	cfg.Target = notification.Target{Mode: "inbox", AgentID: "new_agent", MembershipID: "new_member"}
	var err error
	h.s, err = ConfigureSearch(h.s.RoomIdentity, &h.s, h.s.Binding, cfg, h.now)
	if err != nil {
		t.Fatal(err)
	}
	one.Updated = "1002000"
	f.messages[one.ID] = one
	f.batch = nil
	h.now = h.now.Add(time.Minute)
	ingestionSteps(t, h, source, 20)
	if len(h.s.Inbox) != 1 {
		t.Fatal("replay duplicated revisions", h.s.Inbox)
	}
	for _, e := range h.s.Inbox {
		if e.Item.Route.Target.MembershipID != "member" {
			t.Fatal("revision changed frozen route")
		}
	}
}

func TestSearchCursorFailureFairnessAndUnresolvedMembership(t *testing.T) {
	cfg := Config{Private: PrivateConfig{Enabled: true, Scope: "all"}, Groups: GroupConfig{Enabled: true, Scope: "all", MessageMode: "mentions", AtMe: true, AtAll: true}}
	h, f, source := ingestionHarness(t, cfg)
	own := sampleMessage("om_own", "oc_group", "own")
	own.Sender.ID = "ou_reader"
	f.messages[own.ID] = own
	f.chats[own.ChatID] = Chat{ID: own.ChatID, Mode: "group"}
	f.search = func(q MessageSearchRequest) (MessageSearchPage, error) {
		if q.ChatType == "p2p" {
			return MessageSearchPage{}, failure("rate_limited")
		}
		if q.FromID == "" {
			return MessageSearchPage{Items: []MessageSearchItem{}}, nil
		}
		return MessageSearchPage{Items: []MessageSearchItem{discoveryItem(own, false)}}, nil
	}
	f.membership = func(_, token string) (MembershipPage, error) {
		if token == "" {
			return MembershipPage{HasMore: true, PageToken: "members_next"}, nil
		}
		return MembershipPage{}, failure("permission_denied")
	}
	ingestionSteps(t, h, source, 40)
	if h.s.Search.Branches[0].ErrorCode != "rate_limited" || h.s.Search.Branches[1].Completed != 1009 || h.s.Search.Branches[2].Scan.Page.MemberToken != "members_next" || len(h.s.Inbox) != 0 || h.s.QueryPaused() {
		t.Fatal("blocked branch starved peers or admitted unverified group", h.s.Search)
	}
	f.membership = func(_, token string) (MembershipPage, error) { return MembershipPage{Joined: true}, nil }
	h.now = h.now.Add(2 * time.Second)
	ingestionSteps(t, h, source, 20)
	if len(h.s.Inbox) != 1 {
		t.Fatal("membership recovery failed")
	}

	// Empty first page with more results; expired cursor restarts the same window.
	h, f, source = ingestionHarness(t, Config{Private: PrivateConfig{Enabled: true, Scope: "all"}})
	expired := false
	f.search = func(q MessageSearchRequest) (MessageSearchPage, error) {
		if q.PageToken == "" {
			return MessageSearchPage{Items: []MessageSearchItem{}, HasMore: true, PageToken: "next"}, nil
		}
		if !expired {
			expired = true
			return MessageSearchPage{}, failure("cursor_expired")
		}
		return MessageSearchPage{Items: []MessageSearchItem{}}, nil
	}
	ingestionSteps(t, h, source, 5)
	b := h.s.Search.Branches[0]
	if b.Scan == nil || b.Scan.Start != 1000 || b.Scan.End != 1009 || b.Scan.Token != "" || b.Completed != 0 {
		t.Fatal("expired token lost frozen window", b)
	}
	h.now = h.now.Add(time.Second)
	ingestionSteps(t, h, source, 6)
	if h.s.Search.Branches[0].Completed != 1009 {
		t.Fatal("cursor recovery failed", h.s.Search)
	}
}

func TestSearchCapacityCancellationAndMalformedResponses(t *testing.T) {
	for _, mode := range []string{"capacity", "bad_detail", "loop", "missing_field", "search_notice", "network_error", "identity_mismatch", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			h, f, source := ingestionHarness(t, Config{Private: PrivateConfig{Enabled: true, Scope: "all"}})
			m := sampleMessage("om_one", "oc_peer", "one")
			f.messages[m.ID], f.chats[m.ChatID] = m, peerChat(m.ChatID, "p2p")
			f.search = func(q MessageSearchRequest) (MessageSearchPage, error) {
				switch mode {
				case "loop":
					return MessageSearchPage{Items: []MessageSearchItem{}, HasMore: true, PageToken: q.PageToken}, nil
				case "missing_field":
					return MessageSearchPage{}, nil
				case "network_error", "identity_mismatch", "cancelled":
					return MessageSearchPage{}, failure(mode)
				case "search_notice":
					return MessageSearchPage{}, &SearchNoticeError{Notice: "private evidence"}
				}
				return MessageSearchPage{Items: []MessageSearchItem{discoveryItem(m, true)}}, nil
			}
			if mode == "bad_detail" {
				changed := m
				changed.ChatID = "oc_wrong"
				f.messages[m.ID] = changed
			}
			if mode == "capacity" {
				h.limits.Inbox = 0
			}
			ingestionSteps(t, h, source, 12)
			b := h.s.Search.Branches[0]
			if b.Completed != 0 || b.Scan == nil || len(h.s.Inbox) != 0 || len(h.s.Observed) != 0 {
				t.Fatal("failure advanced progress", b)
			}
			if mode == "capacity" {
				if h.s.Status != "capacity" || b.Scan.Page.Done != 0 {
					t.Fatal(h.s.Status)
				}
				h.limits.Inbox = 10
				h.s.RuntimeState.ResumeCapacity()
				ingestionSteps(t, h, source, 4)
				if len(h.s.Inbox) != 1 {
					t.Fatal("capacity lost current page")
				}
			}
		})
	}
}

func TestSearchConfigurationLifecycleAndPruning(t *testing.T) {
	h, f, source := ingestionHarness(t, Config{Private: PrivateConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_peer"}}})
	h.now = time.Unix(5000, 0)
	f.chats["oc_peer"] = peerChat("oc_peer", "p2p")
	for i, created := range []int64{1001, 1800, 2400, 4800} {
		m := sampleMessage(fmt.Sprintf("om_%d", i), "oc_peer", "message")
		m.Created, m.Updated = strconv.FormatInt(created*1000, 10), strconv.FormatInt(created*1000, 10)
		f.messages[m.ID] = m
	}
	f.search = func(q MessageSearchRequest) (MessageSearchPage, error) {
		p := MessageSearchPage{Items: []MessageSearchItem{}}
		for _, m := range f.messages {
			if validSearchCandidate(discoveryItem(m, true), q) {
				p.Items = append(p.Items, discoveryItem(m, true))
			}
		}
		return p, nil
	}
	ingestionSteps(t, h, source, 80)
	if len(h.s.Inbox) != 4 || h.s.Search.Branches[0].Completed != 4999 {
		t.Fatal("bounded catchup skipped backlog", h.s.Search, len(h.s.Inbox))
	}
	for _, q := range f.requests {
		if q.End.Sub(q.Start) > 15*time.Minute {
			t.Fatal("unbounded window")
		}
	}
	for key := range h.s.Inbox {
		h.s = h.s.Acknowledge(key).(State)
	}
	h.s.pruneObserved()
	if len(h.s.Observed) != 1 {
		t.Fatal("old evidence not pruned", len(h.s.Observed))
	}
	cfg := h.s.Config
	cfg.Private.Scope = "all"
	var err error
	h.s, err = ConfigureSearch(h.s.RoomIdentity, &h.s, h.s.Binding, cfg, h.now)
	if err != nil {
		t.Fatal(err)
	}
	newMessage := sampleMessage("om_new", "oc_new", "baseline backlog")
	f.messages[newMessage.ID] = newMessage
	f.chats["oc_new"] = peerChat("oc_new", "p2p")
	ingestionSteps(t, h, source, 100)
	if len(h.s.Inbox) != 1 || h.s.Baseline.Unix() != 1000 {
		t.Fatal("range expansion replayed pruned history or lost new chat backlog", h.s.Inbox)
	}
	if _, ok := h.s.Inbox[RevisionKey(h.s.Binding, newMessage)]; !ok {
		t.Fatal("new chat did not use baseline")
	}
	// Target-only save preserves current query page and frozen accepted routing.
	ingestionSteps(t, h, source, 1)
	before := h.s.Clone()
	cfg = h.s.Config
	cfg.Enabled = false
	h.s, err = ConfigureSearch(h.s.RoomIdentity, &h.s, h.s.Binding, cfg, h.now)
	if err != nil || h.s.Baseline != before.Baseline || !reflect.DeepEqual(h.s.Inbox, before.Inbox) {
		t.Fatal("disable changed evidence", err)
	}
	if _, stop := source.NewRunner(h).Step(t.Context()); !stop {
		t.Fatal("disabled source ran")
	}
	cfg.Enabled = true
	h.s, err = ConfigureSearch(h.s.RoomIdentity, &h.s, h.s.Binding, cfg, h.now)
	if err != nil {
		t.Fatal(err)
	}
	wrong := h.s.Binding
	wrong.Account.ID.Value = "ou_other"
	if _, err := ConfigureSearch(h.s.RoomIdentity, &h.s, wrong, cfg, h.now); !errors.Is(err, notification.ErrConflict) {
		t.Fatal("account boundary lost", err)
	}
}

func TestSearchCancellationAndSliceBudgetPreserveCurrentPage(t *testing.T) {
	h, f, source := ingestionHarness(t, Config{Private: PrivateConfig{Enabled: true, Scope: "all"}})
	runner := source.NewRunner(h)
	runner.Step(t.Context()) // freeze
	before := h.s.Clone()
	ctx, cancel := context.WithCancel(t.Context())
	f.search = func(MessageSearchRequest) (MessageSearchPage, error) {
		cancel()
		return MessageSearchPage{Items: []MessageSearchItem{}}, nil
	}
	if _, stop := runner.Step(ctx); !stop || !reflect.DeepEqual(h.s, before) {
		t.Fatal("cancelled search committed progress")
	}
	f.search = nil
	h.limits.SlicePages = 1
	runner = source.NewRunner(h)
	runner.Step(t.Context())
	count := len(f.requests)
	runner.Step(t.Context())
	if len(f.requests) != count || !h.s.RetryAt.After(h.now) || h.s.Search.Branches[0].Scan.Page == nil {
		t.Fatal("slice budget lost current page")
	}
	h.now = h.now.Add(time.Minute)
	runner.Step(t.Context())
	if h.s.Search.Branches[0].Completed != before.Search.Branches[0].Scan.End {
		t.Fatal("slice recovery skipped window")
	}
}

func TestSearchMembershipExcludesSelfAndRetainsUnverifiedPeer(t *testing.T) {
	for _, kind := range []string{"self", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			h, f, source := ingestionHarness(t, Config{Private: PrivateConfig{Enabled: true, Scope: "all"}})
			m := sampleMessage("om_one", "oc_chat", "message")
			m.Sender.ID = "ou_reader"
			f.messages[m.ID] = m
			chat := Chat{ID: m.ChatID, Mode: "p2p"}
			if kind == "self" {
				chat.Relation = ConversationRelation{Account: h.s.Binding.Account, ChatID: m.ChatID, Kind: "self"}
			}
			f.chats[m.ChatID] = chat
			f.search = func(MessageSearchRequest) (MessageSearchPage, error) {
				return MessageSearchPage{Items: []MessageSearchItem{discoveryItem(m, true)}}, nil
			}
			ingestionSteps(t, h, source, 12)
			if len(h.s.Inbox) != 0 {
				t.Fatal("unproven personnel conversation admitted")
			}
			if kind == "self" && h.s.Search.Branches[0].Completed == 0 {
				t.Fatal("proven self did not settle")
			}
			if kind == "unknown" && (h.s.Search.Branches[0].ErrorCode != "conversation_unverified" || h.s.Search.Branches[0].Scan.Page.Done != 0) {
				t.Fatal("unknown relationship discarded")
			}
		})
	}
}

func TestSearchNativeMentionPredicates(t *testing.T) {
	chat := Chat{ID: "oc_group", Mode: "group"}
	cfg := Config{Groups: GroupConfig{Enabled: true, Scope: "all", MessageMode: "mentions", AtMe: true}}
	m := sampleMessage("om_one", chat.ID, "@所有人 @reader")
	if Matches(m, chat, "ou_reader", cfg) {
		t.Fatal("display text treated as identity")
	}
	m.Type = "post"
	m.Body.Content = `{"content":[[{"tag":"at","user_id":"ou_reader"},{"tag":"at","user_id":"all"}]]}`
	if !Matches(m, chat, "ou_reader", cfg) {
		t.Fatal("native rich-post self mention lost")
	}
	cfg.Groups.AtMe, cfg.Groups.AtAll = false, true
	if !Matches(m, chat, "ou_reader", cfg) {
		t.Fatal("native rich-post all mention lost")
	}
	m.Body.Content = `{"content":[[{"tag":"at","user_id":"all"}]]}`
	cfg.Groups.AtMe, cfg.Groups.AtAll = true, false
	if Matches(m, chat, "ou_reader", cfg) {
		t.Fatal("all-only hit matched at-me")
	}
}

func TestSearchLargePartialBatchRetriesOnlyUnresolvedIDs(t *testing.T) {
	h, f, source := ingestionHarness(t, Config{Private: PrivateConfig{Enabled: true, Scope: "all"}})
	page := MessageSearchPage{Items: []MessageSearchItem{}}
	for i := range 5 {
		m := sampleMessage(fmt.Sprintf("om_%d", i), "oc_peer", "message")
		f.messages[m.ID] = m
		page.Items = append(page.Items, discoveryItem(m, true))
	}
	f.chats["oc_peer"] = peerChat("oc_peer", "p2p")
	f.search = func(MessageSearchRequest) (MessageSearchPage, error) {
		if len(f.requests) > 1 {
			return MessageSearchPage{Items: []MessageSearchItem{}}, nil
		}
		return page, nil
	}
	f.batch = func(ids []string) (MessageBatch, error) {
		if len(f.batches) == 1 {
			return MessageBatch{Items: []Message{f.messages["om_4"]}, MissingIDs: []string{"om_0", "om_1", "om_2", "om_3"}}, nil
		}
		batch := MessageBatch{}
		for _, id := range ids {
			batch.Items = append(batch.Items, f.messages[id])
		}
		return batch, nil
	}
	ingestionSteps(t, h, source, 5)
	if h.s.Search.Branches[0].Scan.Page.BatchRead || len(h.s.Search.Branches[0].Scan.Page.Details) != 1 {
		t.Fatal("partial batch lost cached detail or did not retain unresolved IDs")
	}
	h.now = h.now.Add(time.Second)
	ingestionSteps(t, h, source, 18)
	if len(h.s.Inbox) != 5 || len(f.batches) != 2 || len(f.batches[1]) != 4 || f.calls != 0 {
		t.Fatal("large missing set expanded into single reads or refetched successful details", f.batches)
	}
}

type failingSearchHost struct {
	*stepHarness
	fail func(State) bool
}

func (h *failingSearchHost) Commit(state notification.State) error {
	if h.fail != nil && h.fail(state.(State)) {
		return errors.New("injected snapshot failure")
	}
	return h.stepHarness.Commit(state)
}

func TestSearchSnapshotFailureAtEveryPageBoundary(t *testing.T) {
	for _, boundary := range []string{"window", "page", "details", "relationship", "admission", "completion"} {
		t.Run(boundary, func(t *testing.T) {
			h, f, source := ingestionHarness(t, Config{Private: PrivateConfig{Enabled: true, Scope: "all"}})
			m := sampleMessage("om_one", "oc_peer", "message")
			f.messages[m.ID], f.chats[m.ChatID] = m, peerChat(m.ChatID, "p2p")
			f.search = func(MessageSearchRequest) (MessageSearchPage, error) {
				return MessageSearchPage{Items: []MessageSearchItem{discoveryItem(m, true)}}, nil
			}
			failed := false
			host := &failingSearchHost{stepHarness: h}
			host.fail = func(next State) bool {
				b := next.Search.Branches[0]
				matches := boundary == "completion" && b.Completed > 0
				if b.Scan != nil {
					p := b.Scan.Page
					matches = matches || boundary == "window" && p == nil || boundary == "page" && p != nil && !p.BatchRead || boundary == "details" && p != nil && p.BatchRead || boundary == "relationship" && len(next.Chats) > 0 || boundary == "admission" && len(next.Inbox) > 0
				}
				if matches && !failed {
					failed = true
					return true
				}
				return false
			}
			for range 15 {
				source.NewRunner(host).Step(t.Context())
			}
			if !failed || len(h.s.Inbox) != 1 || h.s.Metrics.NewItems != 1 || h.s.Search.Branches[0].Completed != 1009 {
				t.Fatal("snapshot boundary lost/duplicated admission", failed, h.s.Search)
			}
		})
	}
}

func TestSearchStateAndDeliveriesAreScopedToRoomAndMember(t *testing.T) {
	var deliveryIDs []string
	for i := range 3 {
		h, f, source := ingestionHarness(t, Config{Private: PrivateConfig{Enabled: true, Scope: "all"}})
		owner := h.s.RoomIdentity
		cfg := h.s.Config
		if i == 1 {
			cfg.Target.MembershipID, cfg.Target.AgentID = "member_two", "agent_two"
		}
		if i == 2 {
			owner.RoomInstance = "another_instance"
		}
		var err error
		h.s, err = ConfigureSearch(owner, nil, h.s.Binding, cfg, time.Unix(1000, 0))
		if err != nil {
			t.Fatal(err)
		}
		m := sampleMessage("om_same", "oc_peer", "message")
		f.messages[m.ID], f.chats[m.ChatID] = m, peerChat(m.ChatID, "p2p")
		f.search = func(MessageSearchRequest) (MessageSearchPage, error) {
			return MessageSearchPage{Items: []MessageSearchItem{discoveryItem(m, true)}}, nil
		}
		ingestionSteps(t, h, source, 10)
		for _, e := range h.s.Inbox {
			deliveryIDs = append(deliveryIDs, e.Item.DeliveryID)
		}
		raw, _ := json.Marshal(h.s)
		other := owner
		other.RoomInstance += "_wrong"
		if _, err := source.Decode(raw, other); err == nil {
			t.Fatal("snapshot crossed room instance")
		}
	}
	slices.Sort(deliveryIDs)
	if len(slices.Compact(deliveryIDs)) != 3 {
		t.Fatal("receivers shared delivery identity")
	}
}

func TestSearchSnapshotByteCapacityKeepsIDPage(t *testing.T) {
	h, f, source := ingestionHarness(t, Config{Private: PrivateConfig{Enabled: true, Scope: "all"}})
	m := sampleMessage("om_one", "oc_peer", string(make([]byte, 10000)))
	f.messages[m.ID], f.chats[m.ChatID] = m, peerChat(m.ChatID, "p2p")
	f.search = func(MessageSearchRequest) (MessageSearchPage, error) {
		return MessageSearchPage{Items: []MessageSearchItem{discoveryItem(m, true)}}, nil
	}
	ingestionSteps(t, h, source, 2)
	raw, _ := json.Marshal(h.s)
	h.limits.Bytes = int64(len(raw)) + 500
	ingestionSteps(t, h, source, 3)
	b := h.s.Search.Branches[0]
	if h.s.Status != "capacity" || b.Completed != 0 || b.Scan.Page == nil || b.Scan.Page.Done != 0 || len(b.Scan.Page.Details) != 0 {
		t.Fatal("byte capacity crossed ID-page boundary")
	}
	h.limits.Bytes = notification.DefaultLimits().Bytes
	h.s.RuntimeState.ResumeCapacity()
	ingestionSteps(t, h, source, 8)
	if len(h.s.Inbox) != 1 || h.s.Search.Branches[0].Completed == 0 {
		t.Fatal("byte capacity did not recover")
	}
}

func TestSearchCredentialRotationKeepsPendingScanWithoutReplayingHistory(t *testing.T) {
	h, _, source := ingestionHarness(t, Config{Private: PrivateConfig{Enabled: true, Scope: "all"}})
	ingestionSteps(t, h, source, 2)
	before := h.s.Clone()
	if before.Search.Branches[0].Scan == nil {
		t.Fatal("fixture has no pending page")
	}
	binding := before.Binding
	binding.CredentialVersion++
	next, err := ConfigureSearch(before.RoomIdentity, &before, binding, before.Config, h.now)
	if err != nil || !reflect.DeepEqual(next.Search.Branches[0].Scan, before.Search.Branches[0].Scan) || next.Search.Branches[0].Completed != before.Search.Branches[0].Completed || next.Baseline != before.Baseline || next.Search.Branches[0].Fingerprint == before.Search.Branches[0].Fingerprint {
		t.Fatal("same-account credentials rewound the pending scan", err)
	}
	raw, _ := json.Marshal(next)
	if _, err := source.Decode(raw, next.RoomIdentity); err != nil {
		t.Fatal("rebound cursor does not survive strict reload", err)
	}
	changed := before.Config
	changed.Private.Scope, changed.Private.ChatIDs = "specified", []string{"oc_other"}
	other, err := ConfigureSearch(before.RoomIdentity, &before, binding, changed, h.now)
	if err != nil || other.Search.Branches[0].Scan != nil || other.Search.Branches[0].Completed != 0 {
		t.Fatal("different query inherited the old token", err)
	}
	binding.Account.ID.Value = "ou_other"
	if _, err := ConfigureSearch(before.RoomIdentity, &before, binding, before.Config, h.now); !errors.Is(err, notification.ErrConflict) {
		t.Fatal("credential repair crossed native account boundary", err)
	}
}
