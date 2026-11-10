package lark

import (
	"encoding/json"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"testing"
	"time"
)

func TestInboxExpiryLarkUsesOriginalTimeBeforePageCapacity(t *testing.T) {
	h, _, _ := newStepHarness(t)
	chat := Chat{ID: "oc_chat", Mode: "p2p", TenantKey: h.s.Binding.Account.Tenant.Value}
	chat.Relation = ConversationRelation{Account: h.s.Binding.Account, ChatID: chat.ID, Kind: "peer", Peer: platform.ExternalID{Namespace: h.s.Binding.Account.ID.Namespace, Value: "ou_sender"}}
	h.s.Chats[chat.ID] = ChatProgress{Chat: chat, Scan: &Scan{Start: 1000, End: 1020}}
	old := sampleMessage("om_old", chat.ID, "old body")
	fresh := sampleMessage("om_fresh", chat.ID, "fresh body")
	fresh.Created = "1011000"
	fresh.Updated = fresh.Created
	limits := notification.DefaultLimits()
	limits.Inbox = 1
	limits.ExpireBefore = time.Unix(1001, 0)
	next, err := acceptPage(h.s, chat.ID, MessagePage{Items: []Message{old, fresh}}, limits)
	if err != nil || len(next.Inbox) != 1 || len(next.Observed) != 1 {
		t.Fatal("expired history filled capacity", err, len(next.Inbox))
	}
	for _, entry := range next.Inbox {
		if entry.Item.CreatedAt != time.Unix(1011, 0).UTC() {
			t.Fatal("used receive/edit time")
		}
	}
	next.Status = "capacity"
	next = next.Expire(time.Unix(1011, 0)).(State)
	if len(next.Inbox) != 0 || next.Status != "running" {
		t.Fatal("capacity not released")
	}
	data, _ := json.Marshal(next)
	decoded, err := (&Source{}).Decode(data, next.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	restored := decoded.(State)
	p := restored.Chats[chat.ID]
	p.Scan = &Scan{Start: 1000, End: 1020}
	restored.Chats[chat.ID] = p
	old.Updated = "1019000" // An edit never resets the original message lifetime.
	limits.ExpireBefore = time.Time{}
	restored, err = acceptPage(restored, chat.ID, MessagePage{Items: []Message{old, fresh}}, limits)
	if err != nil || len(restored.Inbox) != 0 {
		t.Fatal("requery after mode switch resurrected old body", err)
	}
}
