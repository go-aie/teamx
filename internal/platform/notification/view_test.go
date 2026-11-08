package notification

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func TestBusinessViewKeepsScopesAndExcludesDeliveryFields(t *testing.T) {
	item := Item{Key: "dedup", DeliveryID: "delivery", Order: "sort", Text: "saved original", Source: platform.SourceRef{
		Provider: "lark", Channel: "messages", Instance: "https://open.feishu.cn", SubscriptionID: "subscription", ItemKey: "dedup",
		Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "external"}, Reference: platform.NotificationReference{
			RecipientID: platform.ExternalID{Namespace: "lark:open_id:cli_a", Value: "ou_reader"}, AccountTenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "internal"},
			Target:   platform.ObjectReference{Type: "message", ID: platform.ExternalID{Namespace: "lark:message_id:cli_a", Value: "om_message"}},
			ParentID: platform.ExternalID{Namespace: "lark:message_id:cli_a", Value: "om_parent"}}}}
	item = WithContent(item, &Content{Structured: true, Text: "body", Mentions: []Participant{{Name: "reader", IsReceiver: true}}})
	v := Project(item)
	if v.Tenant.Value != "external" || v.Receiver.Tenant.Value != "internal" || v.MessageID.Value != "om_message" || v.ReplyTo.Value != "om_parent" {
		t.Fatal("identity scopes collapsed", v)
	}
	v.Mentions[0].Name = "changed"
	if item.Content.Mentions[0].Name != "reader" {
		t.Fatal("projection mutated durable snapshot")
	}
	b, _ := json.Marshal(v)
	for _, key := range []string{`"subscription_id":`, `"item_key":`, `"key":`, `"delivery_id":`, `"route":`, `"order":`, `"version":`} {
		if strings.Contains(string(b), key) {
			t.Fatal("internal field exposed", key)
		}
	}
}

func TestContentBudgetAccountsForJSONEscapingWithoutChangingRawEvidence(t *testing.T) {
	raw := strings.Repeat("\x01", 15<<10)
	item := WithContent(Item{Text: raw}, &Content{Structured: true, Text: raw})
	if item.Text != raw || item.Content == nil || !item.Content.Truncated || !item.Content.Valid() {
		t.Fatal("budget changed raw evidence or hid truncation")
	}
	b, _ := json.Marshal(item)
	if len(b) > 96<<10 {
		t.Fatal("combined JSON budget exceeded", len(b))
	}
}

func TestOwnContextRequiresExactReceiverIdentity(t *testing.T) {
	id := platform.ExternalID{Namespace: "lark:open_id:cli_a", Value: "ou_reader"}
	item := Item{Source: platform.SourceRef{Provider: "lark", Reference: platform.NotificationReference{RecipientID: id}},
		Content: &Content{Sender: &Participant{ID: id, IsReceiver: true}}}
	if !Project(item).ContextOnly {
		t.Fatal("own message is not context only")
	}
	item.Content.Sender.ID.Namespace = "lark:open_id:cli_b"
	if Project(item).ContextOnly {
		t.Fatal("same raw ID from another app was treated as receiver")
	}
	item.Content.Sender.ID, item.Content.Sender.IsReceiver = id, false
	if Project(item).ContextOnly {
		t.Fatal("unverified sender was treated as receiver")
	}
	item.Content.Sender.IsReceiver, item.Source.Provider = true, "gitlab"
	if Project(item).ContextOnly {
		t.Fatal("Lark exception changed another provider")
	}
}
