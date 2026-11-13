package lark

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type categoryReader struct {
	*discoveryReader
	chats map[string]Chat
}

func (f *categoryReader) NotificationChat(_ context.Context, a platform.AccountIdentity, id string) (Chat, error) {
	c, ok := f.chats[id]
	if !ok {
		return Chat{}, failure("conversation_unverified")
	}
	if c.Mode == "p2p" && c.Relation.Kind == "peer" {
		c.Relation.Account = a
		c.Relation.ChatID = id
		c.Relation.Peer = platform.ExternalID{Namespace: a.ID.Namespace, Value: "ou_sender"}
	}
	return c, nil
}
func TestPrivateDiscoveryCombinesSelectedGroupsWithoutReadingOtherGroups(t *testing.T) {
	h, fr, source := allHarness(t)
	cfg := h.s.Config
	cfg.Groups = GroupConfig{Enabled: true, ChatIDs: []string{"oc_group", "oc_topic"}, MessageMode: "all"}
	var err error
	h.s, err = Configure(h.s.RoomIdentity, &h.s, h.s.Binding, cfg, h.now)
	if err != nil {
		t.Fatal(err)
	}
	fr.list = func(ChatsRequest) (ChatPage, error) {
		return ChatPage{Chats: []Chat{{ID: "oc_person", Mode: "p2p"}, {ID: "oc_self", Mode: "p2p"}, {ID: "oc_bot", Mode: "p2p"}, {ID: "oc_ignored", Mode: "group"}}}, nil
	}
	f := &categoryReader{discoveryReader: fr, chats: map[string]Chat{
		"oc_person": {ID: "oc_person", Mode: "p2p", Relation: ConversationRelation{Kind: "peer"}},
		"oc_self":   {ID: "oc_self", Mode: "p2p", Relation: ConversationRelation{Account: h.s.Binding.Account, ChatID: "oc_self", Kind: "self"}},
		"oc_bot":    {ID: "oc_bot", Mode: "p2p"},
		"oc_group":  {ID: "oc_group", Mode: "group"}, "oc_topic": {ID: "oc_topic", Mode: "topic"},
	}}
	fr.history = func(q MessagesRequest) (MessagePage, error) {
		return MessagePage{Items: []Message{sampleMessage("om_"+q.ChatID, q.ChatID, "hello")}}, nil
	}
	source.Reader = func(QueryScope) (MessageReader, error) { return f, nil }
	runner := source.NewRunner(h)
	for range 30 {
		stepOK(t, h, runner)
	}
	read := []string{}
	for _, q := range fr.requests {
		read = append(read, q.ChatID)
	}
	for _, id := range []string{"oc_person", "oc_group", "oc_topic"} {
		if !slices.Contains(read, id) {
			t.Fatal("missing selected category", id, read)
		}
	}
	for _, id := range []string{"oc_self", "oc_bot", "oc_ignored"} {
		if slices.Contains(read, id) {
			t.Fatal("excluded body read", id)
		}
	}
	if _, ok := h.s.Chats["oc_ignored"]; ok {
		t.Fatal("unselected group admitted by directory")
	}
	if h.s.Chats["oc_bot"].ErrorCode != "conversation_unverified" || len(h.s.Inbox) != 3 {
		t.Fatal("unverified peer reporting", len(h.s.Inbox), h.s.Chats)
	}
	baseline := h.s.Baseline
	fr.list = func(ChatsRequest) (ChatPage, error) {
		return ChatPage{Chats: []Chat{{ID: "oc_person", Mode: "p2p"}, {ID: "oc_new", Mode: "p2p"}}}, nil
	}
	f.chats["oc_new"] = Chat{ID: "oc_new", Mode: "p2p", Relation: ConversationRelation{Kind: "peer"}}
	// New messages are in the original baseline window even for later discovery.
	h.now = h.now.Add(DirectoryInterval + time.Second)
	for range 25 {
		stepOK(t, h, runner)
	}
	if h.s.Chats["oc_new"].Completed == 0 || !h.s.Baseline.Equal(baseline) {
		t.Fatal("new private conversation not discovered or baseline changed")
	}
}
func TestCategoryPreferencesSurviveMasterSwitchAndRestart(t *testing.T) {
	s := sampleState(t)
	cfg := s.Config
	cfg.Groups = GroupConfig{Enabled: false, ChatIDs: []string{"oc_group"}, MessageMode: "all", AtMe: true, AtAll: true}
	cfg.Private.Scope = "all" // Keep the earlier selection for a later switch back.
	n, err := Configure(s.RoomIdentity, &s, s.Binding, cfg, time.Unix(2000, 0))
	if err != nil {
		t.Fatal(err)
	}
	cfg = n.Config
	cfg.Enabled = false
	n, err = Configure(n.RoomIdentity, &n, n.Binding, cfg, time.Unix(3000, 0))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(n)
	state, err := (&Source{}).Decode(raw, n.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	n = state.(State)
	cfg = n.Config
	cfg.Enabled = true
	cfg.Private.Scope = "specified"
	n, err = Configure(n.RoomIdentity, &n, n.Binding, cfg, time.Unix(4000, 0))
	if err != nil || !n.Baseline.Equal(s.Baseline) || !reflect.DeepEqual(n.Config.Private.ChatIDs, s.Config.Private.ChatIDs) || !reflect.DeepEqual(n.Config.Groups, cfg.Groups) {
		t.Fatal("preferences reset", err)
	}
	cfg.Private.Enabled = false
	if _, err := NormalizeConfig(cfg); err == nil {
		t.Fatal("enabled master with no categories accepted")
	}
	cfg.Enabled = false
	if _, err := NormalizeConfig(cfg); err != nil {
		t.Fatal("disabled master lost preferences", err)
	}
}
func TestWrongSelectedCategoryNeverReadsBodies(t *testing.T) {
	h, f, source := allHarness(t)
	cfg := h.s.Config
	cfg.Private = PrivateConfig{Scope: "all"}
	cfg.Groups = GroupConfig{Enabled: true, ChatIDs: []string{"oc_chat"}, MessageMode: "all"}
	var err error
	h.s, err = Configure(h.s.RoomIdentity, &h.s, h.s.Binding, cfg, h.now)
	if err != nil {
		t.Fatal(err)
	}
	f.history = func(MessagesRequest) (MessagePage, error) {
		t.Fatal("wrong-category body read")
		return MessagePage{}, nil
	}
	runner := source.NewRunner(h)
	for range 4 {
		stepOK(t, h, runner)
	}
	if h.s.Chats["oc_chat"].ErrorCode != "conversation_type_mismatch" {
		t.Fatal("missing category diagnostic")
	}
}
func TestClosedCategoryRejectsOwnMessagesAndRevisions(t *testing.T) {
	s := sampleState(t)
	s.Config.Groups = GroupConfig{Enabled: true, ChatIDs: []string{"oc_group"}, MessageMode: "all"}
	chat := Chat{ID: "oc_group", Mode: "group"}
	p := ChatProgress{Chat: chat, Scan: &Scan{Start: 1000, End: 1010}}
	s.Chats[chat.ID] = p
	own := sampleMessage("om_own", chat.ID, "own")
	own.Sender.ID = s.Binding.Account.ID.Value
	n, err := acceptPage(s, chat.ID, MessagePage{Items: []Message{own}}, notification.DefaultLimits())
	if err != nil || len(n.Inbox) != 1 {
		t.Fatal(err)
	}
	original := n.Inbox[RevisionKey(n.Binding, own)]
	n.Config.Groups.Enabled = false
	own.Updated = "1002000"
	p = n.Chats[chat.ID]
	p.Scan = &Scan{Start: 1000, End: 1010}
	n.Chats[chat.ID] = p
	next, err := acceptPage(n, chat.ID, MessagePage{Items: []Message{own}}, notification.DefaultLimits())
	if err != nil || len(next.Inbox) != 1 || !reflect.DeepEqual(next.Inbox[original.Item.Key], original) {
		t.Fatal("closed category accepted own edit or changed frozen delivery", err)
	}
}
