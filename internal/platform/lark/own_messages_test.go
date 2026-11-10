package lark

import (
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestOwnMessagesRespectCategoriesAndRanges(t *testing.T) {
	m := sampleMessage("om_own", "oc_chat", "已经回复并创建 issue")
	m.Sender.ID = "ou_reader"
	for _, mode := range []string{"p2p", "group", "topic"} {
		for _, scope := range []string{"specified", "all"} {
			t.Run(mode+"/"+scope, func(t *testing.T) {
				cfg := Config{Private: PrivateConfig{Enabled: true, Scope: scope, ChatIDs: []string{m.ChatID}}, Groups: GroupConfig{Enabled: true, ChatIDs: []string{m.ChatID}, MessageMode: "mentions"}}
				chat := peerChat(m.ChatID, mode)
				if !Matches(m, chat, "ou_reader", cfg) {
					t.Fatal("own context lost inside enabled category")
				}
				cfg.Private.Enabled = false
				cfg.Groups.Enabled = false
				if Matches(m, chat, "ou_reader", cfg) {
					t.Fatal("own message bypassed disabled category")
				}
				cfg.Private = PrivateConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_other"}}
				cfg.Groups = GroupConfig{Enabled: true, ChatIDs: []string{"oc_other"}}
				if Matches(m, chat, "ou_reader", cfg) {
					t.Fatal("own message escaped selected range")
				}
			})
		}
	}
}

func TestOwnMessageCollectionKeepsDedupAndRevisionEvidence(t *testing.T) {
	h, f, runner := newStepHarness(t)
	m := sampleMessage("om_own", "oc_chat", "已回复")
	m.Sender.ID = h.s.Binding.Account.ID.Value
	f.pages = func(MessagesRequest) (MessagePage, error) { return MessagePage{Items: []Message{m}}, nil }
	for range 4 {
		runner.Step(t.Context())
	}
	if len(h.s.Inbox) != 1 {
		t.Fatal("own message was not collected", len(h.s.Inbox))
	}
	for _, e := range h.s.Inbox {
		if !notification.Project(e.Item).ContextOnly {
			t.Fatal("own message lacks context-only marker")
		}
	}
	// Repeated native revision is observed exactly once, just like incoming messages.
	s := h.s.Clone()
	p := s.Chats[m.ChatID]
	p.Scan = &Scan{Start: 1000, End: 1010}
	s.Chats[m.ChatID] = p
	n, err := acceptPage(s, m.ChatID, MessagePage{Items: []Message{m}}, h.limits)
	if err != nil || len(n.Inbox) != 1 {
		t.Fatal("duplicate own-message delivery", len(n.Inbox), err)
	}
	m.Updated = "1002000"
	m.Body.Content = `{"text":"更正：已回复并创建 issue"}`
	n, err = acceptPage(s, m.ChatID, MessagePage{Items: []Message{m}}, h.limits)
	if err != nil || len(n.Inbox) != 2 {
		t.Fatal("own edit lost", len(n.Inbox), err)
	}
}

func TestOwnMessageReplyInspectionMarksContextOnly(t *testing.T) {
	f, a, q := replyFixture(t)
	m := f.messages[q.Target.Message.Value]
	m.Sender.ID = a.Account.ID.Value
	f.messages[m.ID] = m
	cap := ReplyActions(f, nil, "")[platform.OperationMessageReply]
	i, err := cap.Inspect(t.Context(), q, a)
	if err != nil || !i.ContextOnly {
		t.Fatal("native sender inspection missed own message", i, err)
	}
}
