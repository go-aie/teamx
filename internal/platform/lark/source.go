package lark

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type QueryScope struct {
	Binding         Binding             `json:"binding"`
	Profile         platform.CLIProfile `json:"profile"`
	PlatformVersion uint64              `json:"platform_version"`
}

// Source retains the schema-3 codec for explicit offline conversion.
// Live collection is implemented only by SearchSource.
type Source struct {
	Reader func(QueryScope) (MessageReader, error)
}

func (*Source) ID() notification.SourceID { return MessagesSourceID }
func (*Source) Schema() int               { return 3 }
func (*Source) Decode(b []byte, o platform.RoomIdentity) (notification.State, error) {
	var s State
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF || s.Validate(o) != nil || s.Search != nil || s.Config.Groups.Scope == "all" {
		return nil, notification.ErrInvalid
	}
	return s, nil
}
func hash(v any) string { b, _ := json.Marshal(v); return fmt.Sprintf("%x", sha256.Sum256(b)) }
func MessageKey(b Binding, m Message) string {
	return hash([]string{b.Account.Instance, b.Account.ID.Namespace, m.ChatID, m.ID})
}
func RevisionKey(b Binding, m Message) string {
	// CLI/API formatting and display-name changes are not native revisions.
	content := any(m.Body.Content)
	var decoded any
	decoder := json.NewDecoder(strings.NewReader(m.Body.Content))
	decoder.UseNumber()
	if decoder.Decode(&decoded) == nil && decoder.Decode(new(any)) == io.EOF {
		content = decoded
	}
	mentions := make([]any, 0, len(m.Mentions))
	for _, at := range m.Mentions {
		mentions = append(mentions, []string{at.Key, at.ID, at.IDType, at.TenantKey})
	}
	return hash([]any{MessageKey(b, m), m.Updated, m.Deleted, m.Type, content, mentions})
}

func nativeNamespace(a platform.AccountIdentity, kind string) string {
	return "lark:" + kind + ":" + strings.TrimPrefix(a.ID.Namespace, "lark:open_id:")
}
func sourceRef(s State, m Message, key string, chat Chat) platform.SourceRef {
	ns := nativeNamespace(s.Binding.Account, "message_id")
	r := platform.NotificationReference{Version: 2, RecipientID: s.Binding.Account.ID, AccountTenant: s.Binding.Account.Tenant, ChatID: platform.ExternalID{Namespace: nativeNamespace(s.Binding.Account, "chat_id"), Value: m.ChatID}, Target: platform.ObjectReference{Type: "message", ID: platform.ExternalID{Namespace: ns, Value: m.ID}}}
	if m.ParentID != "" {
		r.ParentID = platform.ExternalID{Namespace: ns, Value: m.ParentID}
	}
	if m.RootID != "" {
		r.RootID = platform.ExternalID{Namespace: ns, Value: m.RootID}
	}
	if m.ThreadID != "" {
		r.DiscussionID = platform.ExternalID{Namespace: nativeNamespace(s.Binding.Account, "thread_id"), Value: m.ThreadID}
	}
	ref := platform.SourceRef{Provider: "lark", Channel: "messages", Instance: s.Binding.Account.Instance, SubscriptionID: s.SubscriptionID, ItemKey: key, Object: "message", Reference: r}
	if chat.TenantKey != "" {
		ref.Tenant = platform.ExternalID{Namespace: "lark:tenant_key", Value: chat.TenantKey}
	}
	return ref
}
func (s *State) acceptMessage(m Message, p ChatProgress) {
	ms, _ := strconv.ParseInt(m.Created, 10, 64)
	if ms < s.Baseline.UnixMilli() {
		return
	}
	if notification.Expired(time.UnixMilli(ms), s.ExpiredBefore) {
		return
	}
	key := RevisionKey(s.Binding, m)
	if _, ok := s.Observed[key]; ok {
		return
	}
	route := notification.Route{Target: s.Config.Target, Version: s.SubscriptionVersion}
	oldRoute, wasAccepted := s.Accepted[MessageKey(s.Binding, m)]
	accepted := s.Config.Includes(p.Chat) && p.Chat.Relation.Kind != "self" && (wasAccepted || Matches(m, p.Chat, s.Binding.Account.ID.Value, s.Config))
	if wasAccepted {
		route = oldRoute
	}
	s.Observed[key] = Observation{Message: m, Accepted: accepted, Route: route}
	if !accepted {
		return
	}
	s.Accepted[MessageKey(s.Binding, m)] = route
	header := "飞书"
	if p.Chat.Name != "" {
		header += " · " + p.Chat.Name
	}
	text := header + "\n" + m.ContentWithSenderFor(s.Binding.Account.ID.Value)
	if wasAccepted {
		text = "飞书消息变更（关联原消息；不自动重做已完成动作）\n" + m.ContentWithSenderFor(s.Binding.Account.ID.Value)
	}
	text = strings.ReplaceAll(text, "\x00", "�")
	if len(text) > 15<<10 {
		text = text[:15<<10]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		text += "\n[正文已截断；原始结构保留在来源，可通过 lark read 重读；附件未读取]"
	}
	item := notification.Item{Key: key, DeliveryID: route.DeliveryID(notification.DeliveryID(s.Metadata().Key, key)), CreatedAt: time.UnixMilli(ms).UTC(), Order: m.ID + ":" + key, Text: text, Route: route, Source: sourceRef(*s, m, key, p.Chat)}
	item = notification.WithContent(item, messageContent(m, p.Chat, s.Binding.Account, wasAccepted))
	s.Inbox[key] = Entry{Message: m, Item: item}
	s.Metrics.NewItems++
}
func validChatError(code string) bool {
	return code == "" || code == "permission_denied" || code == "chat_unavailable" || code == "conversation_unverified" || code == "conversation_type_mismatch"
}
