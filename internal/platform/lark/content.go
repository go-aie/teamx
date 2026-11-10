package lark

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var nativeAtAll = regexp.MustCompile(`(^|[^A-Za-z0-9_])@_all($|[^A-Za-z0-9_])`)
var nativeMention = regexp.MustCompile(`@_(?:user_[0-9]+|all)\b`)

// Content preserves resource identifiers without downloading or interpreting them.
func (m Message) Content() string { return m.ContentFor("") }

// ContentFor renders API mention keys using the message's own identity evidence.
// recipientID is the open_id in the same CLI app scope, never a display name or
// a Room member identity. No additional directory permission or lookup is needed.
func (m Message) ContentFor(recipientID string) string {
	if m.Deleted {
		return "消息已撤回"
	}
	var v map[string]any
	if json.Unmarshal([]byte(m.Body.Content), &v) != nil {
		return "[" + m.Type + " 消息；正文无法解析，附件未读取]"
	}
	if m.Type == "text" {
		if t, ok := v["text"].(string); ok {
			return m.mentionText(t, recipientID)
		}
	}
	if m.Type == "image" {
		if keys := m.ImageKeys(); len(keys) == 1 {
			return "![飞书图片](" + ImagePrefix + keys[0] + ")\n[图片内容需通过附件读取]"
		}
	}
	var parts []string
	resource := false
	imageResource := false
	var walk func(any, int)
	walk = func(x any, depth int) {
		if depth > 20 {
			return
		}
		switch t := x.(type) {
		case map[string]any:
			if t["tag"] == "img" {
				if key, ok := t["image_key"].(string); ok && imageKeyPattern.MatchString(key) {
					parts = append(parts, "![飞书图片]("+ImagePrefix+key+")")
					imageResource = true
					return
				}
			}
			if t["tag"] == "at" {
				id, _ := t["user_id"].(string)
				name, _ := t["user_name"].(string)
				parts = append(parts, m.mention(id, name, recipientID))
				return
			}
			if s, ok := t["text"].(string); ok {
				parts = append(parts, m.mentionText(s, recipientID))
			}
			if s, ok := t["title"].(string); ok {
				parts = append(parts, m.mentionText(s, recipientID))
			}
			if s, ok := t["href"].(string); ok {
				parts = append(parts, s)
			}
			for _, k := range []string{"file_name", "file_key", "image_key", "audio_key", "video_key", "calendar_id", "event_id"} {
				if s, ok := t[k].(string); ok {
					parts = append(parts, k+": "+s)
					resource = true
				}
			}
			for _, k := range []string{"content", "zh_cn", "en_us", "ja_jp", "elements", "body"} {
				if a, ok := t[k]; ok {
					walk(a, depth+1)
				}
			}
		case []any:
			for _, a := range t {
				walk(a, depth+1)
			}
		}
	}
	if m.Type == "post" {
		post := v
		for _, locale := range []string{"zh_cn", "en_us", "ja_jp"} {
			if localized, ok := v[locale].(map[string]any); ok {
				post = localized
				break
			}
		}
		if rows, ok := post["content"].([]any); ok {
			if title, ok := post["title"].(string); ok && title != "" {
				parts = append(parts, m.mentionText(title, recipientID))
			}
			for _, row := range rows {
				start := len(parts)
				walk(row, 0)
				// Rich-post elements within one row are inline, including
				// whitespace and mentions. Only native rows introduce newlines.
				parts = append(parts[:start], strings.Join(parts[start:], ""))
			}
		} else {
			walk(v, 0)
		}
	} else {
		walk(v, 0)
	}
	if m.Type == "post" && resource {
		parts = append(parts, "[附件未读取]")
	}
	if imageResource {
		parts = append(parts, "[图片内容需通过附件读取]")
	}
	if m.Type != "post" {
		parts = append(parts, "["+m.Type+" 消息；附件未读取／类型不支持理解]")
	}
	if len(parts) == 0 {
		return "[" + m.Type + " 消息；无可用文字，附件未读取]"
	}
	return strings.Join(parts, "\n")
}

func (m Message) mentionText(text, recipientID string) string {
	// Replace complete native tokens in one pass: @_user_1 must not consume
	// @_user_10, and a name containing another token must remain literal.
	return nativeMention.ReplaceAllStringFunc(text, func(key string) string {
		return m.mention(key, "", recipientID)
	})
}

func (m Message) mention(key, name, recipientID string) string {
	if key == "all" || key == "@_all" {
		return "@所有人"
	}
	for _, at := range m.Mentions {
		if key == "" || at.Key != key && at.ID != key {
			continue
		}
		if at.ID == "all" {
			return "@所有人"
		}
		if at.Name != "" {
			name = at.Name
		}
		if name == "" {
			name = "未知用户"
		}
		if at.ID == "" {
			return "@" + name + "（身份未解析）"
		}
		kind := at.IDType
		if kind == "" {
			kind = "ID"
		}
		identity := kind + ": " + at.ID
		if at.IDType == "open_id" && recipientID != "" && at.ID == recipientID {
			identity += "；当前接收账号"
		}
		return fmt.Sprintf("@%s（%s）", name, identity)
	}
	if name != "" {
		return fmt.Sprintf("@%s（user_id: %s；身份未解析）", name, key)
	}
	if key != "" {
		return "@" + strings.TrimPrefix(key, "@") + "（身份未解析）"
	}
	return "@未知用户（身份未解析）"
}

func Matches(m Message, chat Chat, accountID string, c Config) bool {
	if !c.Includes(chat) || chat.Relation.Kind == "self" || chat.Mode == "p2p" && (chat.Relation.Kind != "peer" || !chat.Relation.ValidFor(chat.Relation.Account, chat.ID) || chat.Relation.Account.ID.Value != accountID) {
		return false
	}
	if m.Type == "system" || m.Sender.Type == "" || m.Sender.ID == "" {
		return false
	}
	if m.Sender.Type == "user" && m.Sender.IDType == "open_id" && m.Sender.ID == accountID {
		// Own messages are progress context only inside an enabled category.
		return true
	}
	if chat.Mode == "p2p" {
		return m.Sender.Type == "user" && m.Sender.IDType == "open_id"
	}
	if c.Groups.MessageMode == "all" {
		return true
	}
	if !c.Groups.AtMe && !c.Groups.AtAll {
		return false
	}

	for _, at := range m.Mentions {
		if c.Groups.AtMe && at.IDType == "open_id" && at.ID == accountID || c.Groups.AtAll && at.ID == "all" {
			return true
		}
	}
	// Rich posts can carry native identities without a mentions entry.
	if m.Type == "post" && c.Groups.AtMe {
		var v any
		if json.Unmarshal([]byte(m.Body.Content), &v) == nil && hasNativeAt(v, accountID, 0) {
			return true
		}
	}
	if c.Groups.AtAll && (m.Type == "post" || m.Type == "text") {
		if m.Type == "text" {
			var body struct {
				Text string `json:"text"`
			}
			return json.Unmarshal([]byte(m.Body.Content), &body) == nil && (nativeAtAll.MatchString(body.Text) || strings.Contains(body.Text, `<at user_id="all">`))
		}
		var v any
		if json.Unmarshal([]byte(m.Body.Content), &v) == nil {
			return hasNativeAt(v, "all", 0)
		}
	}
	return false
}
func hasNativeAt(v any, id string, depth int) bool {
	if depth > 20 {
		return false
	}
	switch x := v.(type) {
	case map[string]any:
		if x["tag"] == "at" && x["user_id"] == id {
			return true
		}
		for _, v := range x {
			if hasNativeAt(v, id, depth+1) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if hasNativeAt(v, id, depth+1) {
				return true
			}
		}
	}
	return false
}

func validSenderName(name string) bool {
	return strings.TrimSpace(name) != "" && len(name) <= 1024 && !strings.ContainsAny(name, "\x00\r\n")
}

// ContentWithSenderFor is shared by Inbox delivery and native target/context
// reads. ContentFor itself remains a rendering of just the message body.
func (m Message) ContentWithSenderFor(recipientID string) string {
	s := m.Sender
	name := s.Name
	if !validSenderName(name) {
		name = "飞书用户"
		if s.Type == "app" {
			name = "机器人"
		}
	}
	if s.Type == "user" && s.IDType == "open_id" && recipientID != "" && s.ID == recipientID {
		if validSenderName(s.Name) {
			name += "（我）"
		} else {
			name = "我"
		}
		return "发送者：" + name + "\n" + m.ContentFor(recipientID)
	}
	kind := s.IDType
	if kind == "" {
		kind = "ID类型未知"
	}
	identity := kind + ": " + s.ID
	if s.ID == "" {
		identity = "身份未解析"
	}
	// Peer ID distinguishes same-name speakers. Chat/account IDs already travel
	// in SourceRef; sender type/tenant remain in the stored native Message.
	return "发送者：" + name + "（" + identity + "）\n" + m.ContentFor(recipientID)
}
