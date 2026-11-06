package lark

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestSpecifiedGroupAllFiltering(t *testing.T) {
	for _, tt := range []struct {
		name, mode, chatID, senderType, messageType, scope string
		all, private, want                                 bool
	}{
		{name: "ordinary group message", mode: "group", chatID: "oc_chat", senderType: "user", all: true, want: true},
		{name: "topic group message", mode: "topic", chatID: "oc_chat", senderType: "user", all: true, want: true},
		{name: "group bot without private bots opt in", mode: "group", chatID: "oc_chat", senderType: "app", all: true, want: true},
		{name: "system excluded", mode: "group", chatID: "oc_chat", senderType: "user", messageType: "system", all: true},
		{name: "default still requires mention", mode: "group", chatID: "oc_chat", senderType: "user"},
		{name: "other group excluded", mode: "group", chatID: "oc_other", senderType: "user", all: true},
		{name: "discovery scope excluded", mode: "group", chatID: "oc_chat", senderType: "user", all: true, scope: "all"},
		{name: "private still requires opt in", mode: "p2p", chatID: "oc_chat", senderType: "user", all: true},
		{name: "private opt in preserved", mode: "p2p", chatID: "oc_chat", senderType: "user", all: true, private: true, want: true},
		{name: "private bots still require opt in", mode: "p2p", chatID: "oc_chat", senderType: "app", all: true, private: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := sampleMessage("om_one", tt.chatID, "ordinary message without mentions")
			m.Sender.Type = tt.senderType
			if tt.messageType != "" {
				m.Type = tt.messageType
			}
			cfg := Config{ScopeMode: tt.scope, Chats: []string{"oc_chat"}, GroupAll: tt.all, Private: tt.private}
			if got := Matches(m, Chat{ID: tt.chatID, Mode: tt.mode}, "ou_reader", cfg); got != tt.want {
				t.Fatalf("Matches = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSpecifiedGroupAllPersistsAndBuffersOrdinaryMessages(t *testing.T) {
	h, f, r := newStepHarness(t)
	cfg := h.s.Config
	cfg.GroupAll, cfg.GroupMentions = true, false
	var err error
	h.s, err = Configure(h.s.RoomIdentity, &h.s, h.s.Binding, cfg, h.now)
	if err != nil {
		t.Fatal(err)
	}
	f.chat.Mode = "group"
	f.pages = func(MessagesRequest) (MessagePage, error) {
		user := sampleMessage("om_user", "oc_chat", "ordinary group message")
		bot := sampleMessage("om_bot", "oc_chat", "bot message")
		bot.Sender.Type = "app"
		return MessagePage{Items: []Message{user, bot}}, nil
	}
	r.Step(context.Background())
	r.Step(context.Background())
	if len(h.s.Inbox) != 2 {
		t.Fatalf("expected user and bot messages in source Inbox, got %d", len(h.s.Inbox))
	}
	raw, err := json.Marshal(h.s)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := (&Source{}).Decode(raw, h.s.RoomIdentity)
	if err != nil || !decoded.(State).Config.GroupAll || len(decoded.(State).Inbox) != 2 {
		t.Fatal("group-all setting and buffered messages did not survive reload", err)
	}
	// Re-reading the same window must not create duplicate notifications.
	s := h.s
	p := s.Chats["oc_chat"]
	p.Scan = &Scan{Start: s.Baseline.Unix(), End: h.now.Unix()}
	s.Chats["oc_chat"] = p
	page, _ := f.pages(MessagesRequest{})
	next, err := acceptPage(s, "oc_chat", page, notification.DefaultLimits())
	if err != nil || len(next.Inbox) != 2 || next.Metrics.NewItems != 2 {
		t.Fatal("group-all messages were duplicated", err)
	}
}

func TestGroupAllRequiresSpecifiedScopeAndDefaultsOff(t *testing.T) {
	s := sampleState(t)
	raw, _ := json.Marshal(s)
	decoded, err := (&Source{}).Decode(raw, s.RoomIdentity)
	if err != nil || decoded.(State).Config.GroupAll {
		t.Fatal("legacy settings enabled group-all", err)
	}
	cfg := s.Config
	cfg.ScopeMode, cfg.Chats, cfg.GroupAll = "all", nil, true
	if _, err := NormalizeConfig(cfg); err == nil {
		t.Fatal("group-all accepted for all-chat discovery")
	}
}

func TestGroupMentionChoicesAreIndependent(t *testing.T) {
	for _, tt := range []struct {
		name     string
		me, all  bool
		mentions []Mention
		text     string
		want     bool
	}{
		{name: "me only accepts me", me: true, mentions: []Mention{{ID: "ou_reader", IDType: "open_id"}}, want: true},
		{name: "me only excludes all", me: true, mentions: []Mention{{ID: "all"}}},
		{name: "all only accepts all", all: true, mentions: []Mention{{ID: "all"}}, want: true},
		{name: "all only accepts native all", all: true, text: "@_all hello", want: true},
		{name: "all only excludes me", all: true, mentions: []Mention{{ID: "ou_reader", IDType: "open_id"}}},
		{name: "both accept me", me: true, all: true, mentions: []Mention{{ID: "ou_reader", IDType: "open_id"}}, want: true},
		{name: "both exclude ordinary", me: true, all: true},
		{name: "neither excludes me", mentions: []Mention{{ID: "ou_reader", IDType: "open_id"}}},
		{name: "neither excludes all", mentions: []Mention{{ID: "all"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := sampleMessage("om_group", "oc_chat", tt.text)
			m.Mentions = tt.mentions
			if got := Matches(m, Chat{ID: "oc_chat", Mode: "group"}, "ou_reader", Config{GroupMentions: tt.me, AtAll: tt.all}); got != tt.want {
				t.Fatalf("Matches = %v, want %v", got, tt.want)
			}
		})
	}
}
