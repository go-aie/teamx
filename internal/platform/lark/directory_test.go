package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type discoveryReader struct {
	*fakeReader
	list           func(ChatsRequest) (ChatPage, error)
	history        func(MessagesRequest) (MessagePage, error)
	chatError      map[string]string
	directoryCalls int
}

func (f *discoveryReader) Chats(_ context.Context, _ platform.AccountIdentity, q ChatsRequest) (ChatPage, error) {
	f.directoryCalls++
	return f.list(q)
}
func (f *discoveryReader) Chat(_ context.Context, _ platform.AccountIdentity, id string) (Chat, error) {
	f.calls++
	if code := f.chatError[id]; code != "" {
		return Chat{}, failure(code)
	}
	return Chat{ID: id, Mode: "p2p", TenantKey: "external_tenant", Relation: ConversationRelation{Account: sampleBinding().Account, ChatID: id, Kind: "peer", Peer: platform.ExternalID{Namespace: sampleBinding().Account.ID.Namespace, Value: "ou_sender"}}}, nil
}
func (f *discoveryReader) Messages(_ context.Context, _ platform.AccountIdentity, q MessagesRequest) (MessagePage, error) {
	f.calls++
	f.requests = append(f.requests, q)
	return f.history(q)
}
func allHarness(t *testing.T) (*stepHarness, *discoveryReader, *Source) {
	h, fr, _ := newStepHarness(t)
	cfg := h.s.Config
	cfg.Private.Scope = "all"
	cfg.Private.ChatIDs = nil
	var err error
	h.s, err = Configure(h.s.RoomIdentity, &h.s, h.s.Binding, cfg, h.now)
	if err != nil {
		t.Fatal(err)
	}
	f := &discoveryReader{fakeReader: fr, chatError: map[string]string{}, history: func(MessagesRequest) (MessagePage, error) { return MessagePage{Items: []Message{}}, nil }}
	source := &Source{Reader: func(QueryScope) (MessageReader, error) { return f, nil }}
	return h, f, source
}
func stepOK(t *testing.T, h *stepHarness, r notification.Runner) {
	t.Helper()
	delay, stop := r.Step(context.Background())
	if stop {
		t.Fatalf("unexpected stop: %s", h.s.ErrorCode)
	}
	if h.s.Status == "backoff" {
		h.now = h.now.Add(delay)
	}
}
func TestDirectoryRestartCursorExpiryAndNewChatsKeepBaseline(t *testing.T) {
	h, f, source := allHarness(t)
	chat := func(id string) Chat { return Chat{ID: id, Mode: "p2p"} }
	f.list = func(q ChatsRequest) (ChatPage, error) {
		if q.PageToken == "" {
			return ChatPage{Chats: []Chat{chat("oc_a")}, HasMore: true, PageToken: "next"}, nil
		}
		return ChatPage{Chats: []Chat{chat("oc_b")}}, nil
	}
	r := source.NewRunner(h)
	stepOK(t, h, r)
	if h.s.Directory.Scan == nil || h.s.Directory.Scan.Token != "next" || !h.s.LastFull.IsZero() {
		t.Fatal("directory prematurely completed")
	}
	// Production decode represents process restart, not just runner recreation.
	b, _ := json.Marshal(h.s)
	saved, err := source.Decode(b, h.s.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	h.s = saved.(State)
	r = source.NewRunner(h)
	stepOK(t, h, r) // metadata, interleaved with directory
	f.list = func(q ChatsRequest) (ChatPage, error) {
		if q.PageToken == "next" {
			return ChatPage{}, failure("cursor_expired")
		}
		return ChatPage{Chats: []Chat{chat("oc_a"), chat("oc_b")}}, nil
	}
	stepOK(t, h, r)
	if h.s.Directory.Scan.Token != "" || h.s.Chats["oc_a"].Unlisted {
		t.Fatal("expired cursor lost known membership")
	}
	for i := 0; i < 10 && h.s.Chats["oc_b"].Completed == 0; i++ {
		stepOK(t, h, r)
	}
	if h.s.Directory.LastFull.IsZero() || h.s.Chats["oc_b"].Completed == 0 {
		t.Fatal("directory resume incomplete")
	}
	baseline := h.s.Baseline
	h.now = h.now.Add(DirectoryInterval + time.Second)
	f.list = func(ChatsRequest) (ChatPage, error) {
		return ChatPage{Chats: []Chat{chat("oc_b"), chat("oc_new")}}, nil
	}
	r = source.NewRunner(h)
	for i := 0; i < 15 && h.s.Chats["oc_new"].Completed == 0; i++ {
		stepOK(t, h, r)
	}
	if !h.s.Baseline.Equal(baseline) || !h.s.Chats["oc_a"].Unlisted {
		t.Fatal("refresh reset baseline or failed to mark missing chat")
	}
	found := false
	for _, q := range f.requests {
		if q.ChatID == "oc_new" {
			found = true
			if q.Start != baseline.Unix() {
				t.Fatal("new chat reset to discovery time")
			}
		}
	}
	if !found {
		t.Fatal("new chat never scanned")
	}
}

func TestP2PDirectoryNameSurvivesVerificationAndRefresh(t *testing.T) {
	h, f, source := allHarness(t)
	f.list = func(ChatsRequest) (ChatPage, error) {
		return ChatPage{Chats: []Chat{{ID: "oc_chat", Mode: "p2p", Name: "同事的会话"}}}, nil
	}
	r := source.NewRunner(h)
	stepOK(t, h, r) // Directory supplies the name.
	stepOK(t, h, r) // Get supplies tenant/mode but an empty name.
	p := h.s.Chats["oc_chat"]
	if p.Chat.Name != "同事的会话" || p.Chat.TenantKey != "external_tenant" || p.NeedsVerification {
		t.Fatal("verification discarded directory display evidence", p)
	}
	h.s.Directory.Scan = &DirectoryScan{Seen: map[string]bool{}}
	n, err := acceptDirectory(h.s, ChatPage{Chats: []Chat{{ID: "oc_chat", Mode: "p2p", Name: "同事的新名称"}}}, h.now)
	if err != nil || n.Chats["oc_chat"].Chat.Name != "同事的新名称" || n.Chats["oc_chat"].Chat.TenantKey != p.Chat.TenantKey || n.Chats["oc_chat"].VerifiedAt != p.VerifiedAt {
		t.Fatal("directory refresh lost name or changed verified identity", n.Chats["oc_chat"], err)
	}
}
func TestPageFairnessWithHundredsOfChatsAndHotBacklog(t *testing.T) {
	h, f, source := allHarness(t)
	chats := []Chat{}
	for i := 0; i < 240; i++ {
		chats = append(chats, Chat{ID: fmt.Sprintf("oc_%03d", i), Mode: "p2p"})
	}
	f.list = func(q ChatsRequest) (ChatPage, error) {
		offset := 0
		if q.PageToken != "" {
			_, _ = fmt.Sscan(q.PageToken, &offset)
		}
		end := min(offset+50, len(chats))
		return ChatPage{Chats: chats[offset:end], HasMore: end < len(chats), PageToken: fmt.Sprint(end)}, nil
	}
	f.history = func(q MessagesRequest) (MessagePage, error) {
		if q.ChatID == "oc_000" {
			return MessagePage{Items: []Message{}, HasMore: true, PageToken: q.PageToken + "x"}, nil
		}
		return MessagePage{Items: []Message{}}, nil
	}
	r := source.NewRunner(h)
	// No fake wall-clock advancement while the workload remains due.
	for i := 0; i < 1000; i++ {
		stepOK(t, h, r)
		if !h.s.Directory.LastFull.IsZero() {
			all := true
			for id, p := range h.s.Chats {
				if id != "oc_000" && p.Completed == 0 {
					all = false
				}
			}
			if all {
				break
			}
		}
	}
	if h.s.Directory.LastFull.IsZero() || len(h.s.Chats) != 240 {
		t.Fatal("directory starved")
	}
	for id, p := range h.s.Chats {
		if id != "oc_000" && p.Completed == 0 {
			t.Fatal("cold chat starved", id)
		}
	}
	if h.s.Chats["oc_000"].Scan == nil || h.s.Chats["oc_000"].Completed != 0 {
		t.Fatal("hot backlog skipped")
	}
}
func TestSlowWideDirectoryReadsMessagesBeforeVerificationExpires(t *testing.T) {
	h, f, source := allHarness(t)
	h.s.Config.IntervalSeconds = 10
	const count = 309
	f.list = func(q ChatsRequest) (ChatPage, error) {
		offset := 0
		if q.PageToken != "" {
			_, _ = fmt.Sscan(q.PageToken, &offset)
		}
		end := min(offset+q.PageSize, count)
		chats := make([]Chat, 0, end-offset)
		for i := offset; i < end; i++ {
			chats = append(chats, Chat{ID: fmt.Sprintf("oc_%03d", i), Mode: "p2p"})
		}
		return ChatPage{Chats: chats, HasMore: end < count, PageToken: fmt.Sprint(end)}, nil
	}
	r := source.NewRunner(h)
	// One metadata call and one empty message page per chat, plus directory
	// pagination/refresh overhead, should be enough for the first full scan.
	for i := 0; i < 2*count+40; i++ {
		stepOK(t, h, r)
		// Real calls take time. A full directory rotation can exceed the
		// metadata verification lifetime even without errors or a backlog.
		h.now = h.now.Add(2 * time.Second)
		if !h.s.LastFull.IsZero() {
			return
		}
	}
	unchecked := 0
	for _, p := range h.s.Chats {
		if p.Completed == 0 {
			unchecked++
		}
	}
	t.Fatalf("full message scan never completed: %d of %d chats unchecked", unchecked, len(h.s.Chats))
}

func TestDirectoryAtomicSavePermissionIsolationAndAuthRecovery(t *testing.T) {
	h, f, source := allHarness(t)
	f.list = func(ChatsRequest) (ChatPage, error) {
		return ChatPage{Chats: []Chat{{ID: "oc_a", Mode: "p2p"}, {ID: "oc_b", Mode: "p2p"}}}, nil
	}
	r := source.NewRunner(h)
	stepOK(t, h, r)
	f.chatError["oc_a"] = "permission_denied"
	for i := 0; i < 6; i++ {
		stepOK(t, h, r)
	}
	if h.s.Chats["oc_a"].ErrorCode != "permission_denied" || h.s.Chats["oc_b"].Completed == 0 || h.s.QueryPaused() {
		t.Fatal("one denied chat blocked whole account")
	}
	// Expired user credentials pause the entire subscription without changing a
	// completed upper bound or discarding an unfinished scan.
	f.history = func(MessagesRequest) (MessagePage, error) { return MessagePage{}, failure("credential_rejected") }
	h.now = h.now.Add(time.Minute)
	before := h.s.Chats["oc_b"].Completed
	_, stop := r.Step(context.Background())
	if !stop || !h.s.QueryPaused() || h.s.Chats["oc_b"].Completed != before {
		t.Fatal("auth expiry was accepted as empty history")
	}
	calls := f.calls
	r.Step(context.Background())
	if f.calls != calls {
		t.Fatal("paused auth queried")
	}
	baseline := h.s.Baseline
	cfg := h.s.Config
	var err error
	h.s, err = Configure(h.s.RoomIdentity, &h.s, h.s.Binding, cfg, h.now)
	if err != nil {
		t.Fatal(err)
	}
	f.history = func(MessagesRequest) (MessagePage, error) { return MessagePage{Items: []Message{}}, nil }
	delete(f.chatError, "oc_a")
	for i := 0; i < 8; i++ {
		stepOK(t, h, r)
	}
	if h.s.QueryPaused() || !h.s.Baseline.Equal(baseline) || h.s.Chats["oc_a"].Completed == 0 {
		t.Fatal("reauthorization did not resume original progress")
	}
}
func TestDirectoryInvalidPageAndCapacityAreAtomic(t *testing.T) {
	h, _, _ := allHarness(t)
	s := h.s
	s.Directory.Scan = &DirectoryScan{Seen: map[string]bool{}}
	for _, p := range []ChatPage{{Chats: []Chat{}, HasMore: true}, {Chats: []Chat{{ID: "oc_a", Mode: "p2p"}, {ID: "oc_a", Mode: "p2p"}}}, {Chats: nil}} {
		if _, e := acceptDirectory(s, p, h.now); e == nil {
			t.Fatal("accepted incomplete page")
		}
	}
	for i := 0; i < MaxChats; i++ {
		id := fmt.Sprintf("oc_%d", i)
		s.Chats[id] = ChatProgress{Chat: Chat{ID: id, Mode: "p2p"}}
	}
	if _, e := acceptDirectory(s, ChatPage{Chats: []Chat{{ID: "oc_new", Mode: "p2p"}}}, h.now); !errors.Is(e, notification.ErrCapacity) {
		t.Fatal(e)
	}
	if len(s.Chats) != MaxChats || s.Directory.Scan.Pages != 0 {
		t.Fatal("candidate mutated saved state")
	}
}
func TestLongRunningOverlapRetentionPreservesPendingAndRoutes(t *testing.T) {
	h, f, r := newStepHarness(t)
	f.pages = func(q MessagesRequest) (MessagePage, error) {
		m := sampleMessage(fmt.Sprintf("om_%d", q.End), q.ChatID, "fixture")
		m.Created = fmt.Sprint(q.End * 1000)
		m.Updated = m.Created
		return MessagePage{Items: []Message{m}}, nil
	}
	r.Step(context.Background())
	r.Step(context.Background())
	var pending string
	var original notification.Item
	for key, e := range h.s.Inbox {
		pending = key
		original = e.Item
	}
	h.limits.Observed = 15
	for i := 0; i < 200; i++ {
		h.now = h.now.Add(time.Minute)
		for key := range h.s.Inbox {
			if key != pending {
				h.s = h.s.Acknowledge(key).(State)
			}
		}
		stepOK(t, h, r)
		stepOK(t, h, r)
	}
	if h.s.Chats["oc_chat"].Completed < h.now.Unix()-2 || len(h.s.Observed) > 10 || !reflect.DeepEqual(h.s.Inbox[pending].Item, original) {
		t.Fatal("sustained scan lost progress, bounded retention or frozen pending delivery")
	}
}

type directorySaveHost struct {
	*stepHarness
	reject bool
}

func (h *directorySaveHost) Commit(s notification.State) error {
	n := s.(State)
	if h.reject && len(n.Chats) > 0 {
		return errors.New("injected atomic save failure")
	}
	return h.stepHarness.Commit(s)
}
func TestDirectoryFailedSaveRetriesSamePageWithoutPublishing(t *testing.T) {
	h, f, source := allHarness(t)
	f.list = func(ChatsRequest) (ChatPage, error) {
		return ChatPage{Chats: []Chat{{ID: "oc_new", Mode: "p2p"}}, HasMore: true, PageToken: "next"}, nil
	}
	host := &directorySaveHost{stepHarness: h, reject: true}
	runner := source.NewRunner(host)
	runner.Step(context.Background())
	if len(h.s.Chats) != 0 || h.s.Directory.Scan.Token != "" || h.s.Directory.Scan.Pages != 0 {
		t.Fatal("failed save published directory candidate")
	}
	host.reject = false
	runner.Step(context.Background())
	if len(h.s.Chats) != 1 || h.s.Directory.Scan.Token != "next" || f.directoryCalls != 2 {
		t.Fatal("same directory page was not retried")
	}
}

func TestAllChatOptInAndLegacyCodec(t *testing.T) {
	s := sampleState(t)
	raw, _ := json.Marshal(s)
	source := &Source{}
	state, err := source.Decode(raw, s.RoomIdentity)
	if err != nil || state.(State).Config.DiscoverPrivate() || state.(State).Directory != nil {
		t.Fatal("legacy snapshot enabled discovery", err)
	}
	for _, mode := range []string{"unknown", "ALL"} {
		cfg := s.Config
		cfg.Private.Scope = mode
		if _, err := NormalizeConfig(cfg); err == nil {
			t.Fatal("unknown scope accepted")
		}
	}
	cfg := s.Config
	cfg.Private.Scope = "all"
	if _, err := NormalizeConfig(cfg); err != nil {
		t.Fatal("all scope must preserve specified IDs", err)
	}
	cfg.Private.ChatIDs = nil
	if _, err := NormalizeConfig(cfg); err != nil {
		t.Fatal(err)
	}
	raw = append(raw[:len(raw)-1], []byte(`,"unknown_progress":true}`)...)
	if _, err := source.Decode(raw, s.RoomIdentity); err == nil {
		t.Fatal("unknown progress accepted")
	}
}
