package lark

import (
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestNotificationContentPreservesNativeIdentityAndBoundsMetadata(t *testing.T) {
	a := platform.AccountIdentity{ID: platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_self"}}
	m := Message{ChatID: "oc_chat", Type: "text", Sender: Sender{ID: "ou_self", IDType: "union_id", Type: "user", Name: "同名用户"}}
	m.Body.Content = `{"text":"请确认"}`
	for range 40 {
		m.Mentions = append(m.Mentions, Mention{ID: "ou_self", IDType: "open_id", Name: strings.Repeat("名", 1000)})
	}
	c := messageContent(m, Chat{ID: "oc_chat", Name: "讨论群", Mode: "group"}, a, true)
	if c.Sender.IsReceiver || c.Sender.ID.Namespace != "lark:union_id:cli_fixture" || !c.Changed || !c.MetadataTruncated || len(c.Mentions) != 32 || !c.Mentions[0].IsReceiver || !c.Valid() {
		t.Fatal("identity scope or metadata budget lost", c)
	}
	m.Type = "image"
	m.Body.Content = `{"image_key":"img_fixture"}`
	c = messageContent(m, Chat{}, a, false)
	if len(c.Attachments) != 1 || c.Attachments[0].Reference != "lark-image:img_fixture" || c.Attachments[0].Status != "not_read" {
		t.Fatal(c)
	}
}

func TestSavedNotificationEnvelopeKeepsUnknownEvidenceExplicit(t *testing.T) {
	item := notification.Item{Source: platform.SourceRef{Instance: "https://open.feishu.cn", Reference: platform.NotificationReference{RecipientID: platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_self"}, ChatID: platform.ExternalID{Namespace: "lark:chat_id:cli_fixture", Value: "oc_chat"}}}}
	for _, text := range []string{
		"飞书 · 研发群\n发送者：罗彭（我）\n@同事（open_id: ou_peer） 原文",
		"飞书 · 研发群\n发送者：罗彭（open_id: ou_self；当前接收账号本人）\n@同事（open_id: ou_peer） 原文",
	} {
		item.Text = text
		c := SavedContent(item)
		if c.Structured || c.Sender == nil || !c.Sender.IsReceiver || c.Sender.ID != item.Source.Reference.RecipientID || c.Conversation.Name != "研发群" || len(c.Mentions) != 0 || c.Text != "@同事（open_id: ou_peer） 原文" {
			t.Fatal(c)
		}
	}
	item.Text = "飞书 · 旧消息\n没有发送者头\n发送者：这只是正文"
	c := SavedContent(item)
	if c.Sender != nil || c.Structured || c.Text != item.Text {
		t.Fatal("unrecognized saved text reinterpreted", c)
	}
}
