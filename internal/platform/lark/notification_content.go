package lark

import (
	"regexp"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

var savedSender = regexp.MustCompile(`^发送者：(.*?)（((?:[A-Za-z_]+|ID类型未知): [^（）\r\n]*|身份未解析[^（）\r\n]*)）$`)

// SavedContent reads only the service-generated envelope of older notices. It
// does not infer mention/reply identities from user-controlled message bodies,
// and remains structured=false because the original native snapshot is absent.
func SavedContent(item notification.Item) *notification.Content {
	c := &notification.Content{Text: item.Text}
	lines := strings.SplitN(item.Text, "\n", 3)
	if len(lines) < 3 || !(lines[0] == "飞书" || strings.HasPrefix(lines[0], "飞书 · ") || lines[0] == "飞书消息变更（关联原消息；不自动重做已完成动作）") {
		return c
	}
	a := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: item.Source.Instance}, ID: item.Source.Reference.RecipientID}
	var p notification.Participant
	if match := savedSender.FindStringSubmatch(lines[1]); match != nil {
		p.Name = match[1]
		identity := strings.SplitN(match[2], "；", 2)[0]
		kind, id, ok := strings.Cut(identity, ": ")
		if ok && kind != "ID类型未知" && strings.HasPrefix(a.ID.Namespace, "lark:open_id:") {
			p.ID = platform.ExternalID{Namespace: nativeNamespace(a, kind), Value: id}
			p.IsReceiver = kind == "open_id" && p.ID == a.ID
		}
	} else if name, ok := strings.CutPrefix(lines[1], "发送者："); ok && (name == "我" || strings.HasSuffix(name, "（我）")) {
		p.Name = strings.TrimSuffix(name, "（我）")
		p.ID = a.ID
		p.IsReceiver = a.ID.Valid()
	} else {
		return c
	}
	if p.Name == "姓名未获取" || p.Name == "飞书用户" || p.Name == "我" && p.IsReceiver {
		p.Name = ""
	}
	c.Sender, c.Text = &p, lines[2]
	c.ContextOnly = p.IsReceiver && a.ID.Valid() && p.ID == a.ID
	c.Changed = strings.HasPrefix(lines[0], "飞书消息变更")
	name := strings.TrimPrefix(lines[0], "飞书 · ")
	if name == lines[0] || strings.HasPrefix(name, "会话 oc_") {
		name = ""
	}
	c.Conversation = &notification.Context{Name: name, ID: item.Source.Reference.ChatID}
	return c
}

func messageContent(m Message, chat Chat, account platform.AccountIdentity, changed bool) *notification.Content {
	body, truncated := notification.BoundedBody(m.ContentFor(account.ID.Value), 15<<10)
	c := &notification.Content{Text: body, Structured: true, Changed: changed, Deleted: m.Deleted, Truncated: truncated,
		Conversation: &notification.Context{Name: chat.Name, ID: platform.ExternalID{Namespace: nativeNamespace(account, "chat_id"), Value: m.ChatID}, Type: chat.Mode}}
	participant := func(name, id, kind, typ, tenant string) notification.Participant {
		p := notification.Participant{Name: name, Type: typ}
		if !validSenderName(name) {
			p.Name = ""
			c.MetadataTruncated = c.MetadataTruncated || name != ""
		} else if len(name) > 256 {
			p.Name, _ = notification.BoundedBody(name, 256)
			c.MetadataTruncated = true
		}
		if len(typ) > 32 || strings.ContainsAny(typ, "\x00\r\n") {
			p.Type, c.MetadataTruncated = "", true
		}
		if len(id) > 256 || len(kind) > 32 || strings.ContainsAny(id+kind, "\x00\r\n") {
			c.MetadataTruncated = true
		} else if id != "" && kind != "" {
			p.ID = platform.ExternalID{Namespace: nativeNamespace(account, kind), Value: id}
		}
		if len(tenant) > 256 || strings.ContainsAny(tenant, "\x00\r\n") {
			c.MetadataTruncated = true
		} else if tenant != "" {
			p.Tenant = platform.ExternalID{Namespace: "lark:tenant_key", Value: tenant}
		}
		p.IsReceiver = typ == "user" && kind == "open_id" && p.ID == account.ID
		return p
	}
	sender := participant(m.Sender.Name, m.Sender.ID, m.Sender.IDType, m.Sender.Type, m.Sender.TenantKey)
	c.Sender = &sender
	c.ContextOnly = sender.IsReceiver
	for i, at := range m.Mentions {
		if i == 32 {
			c.MetadataTruncated = true
			break
		}
		p := participant(at.Name, at.ID, at.IDType, "user", at.TenantKey)
		if at.ID == "all" {
			p = notification.Participant{Name: "所有人", All: true}
		}
		c.Mentions = append(c.Mentions, p)
	}
	for i, key := range m.ImageKeys() {
		if i == 16 {
			c.MetadataTruncated = true
			break
		}
		c.Attachments = append(c.Attachments, notification.Attachment{Type: "image", Reference: ImagePrefix + key, Status: "not_read"})
	}
	return c
}
