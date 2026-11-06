package lark

import (
	"encoding/json"
	"regexp"
	"strings"
)

var nativeAtAll = regexp.MustCompile(`(^|[^A-Za-z0-9_])@_all($|[^A-Za-z0-9_])`)

// Content preserves resource identifiers without downloading or interpreting them.
func (m Message) Content() string {
	if m.Deleted {
		return "消息已撤回"
	}
	var v map[string]any
	if json.Unmarshal([]byte(m.Body.Content), &v) != nil {
		return "[" + m.Type + " 消息；正文无法解析，附件未读取]"
	}
	if m.Type == "text" {
		if t, ok := v["text"].(string); ok {
			return t
		}
	}
	var parts []string
	resource := false
	var walk func(any, int)
	walk = func(x any, depth int) {
		if depth > 20 {
			return
		}
		switch t := x.(type) {
		case map[string]any:
			if s, ok := t["text"].(string); ok {
				parts = append(parts, s)
			}
			if s, ok := t["title"].(string); ok {
				parts = append(parts, s)
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
	walk(v, 0)
	if m.Type == "post" && resource {
		parts = append(parts, "[附件未读取]")
	}
	if m.Type != "post" {
		parts = append(parts, "["+m.Type+" 消息；附件未读取／类型不支持理解]")
	}
	if len(parts) == 0 {
		return "[" + m.Type + " 消息；无可用文字，附件未读取]"
	}
	return strings.Join(parts, "\n")
}

func Matches(m Message, chat Chat, accountID string, c Config) bool {
	if m.Type == "system" || m.Sender.Type == "" || m.Sender.ID == "" {
		return false
	}
	if m.Sender.Type == "user" && m.Sender.IDType == "open_id" && m.Sender.ID == accountID {
		return false
	}
	if chat.Mode == "p2p" {
		return c.Private && (m.Sender.Type == "user" && m.Sender.IDType == "open_id" || c.Bots && m.Sender.Type == "app")
	}
	if !c.GroupMentions {
		return false
	}
	for _, at := range m.Mentions {
		if at.IDType == "open_id" && at.ID == accountID || c.AtAll && at.ID == "all" {
			return true
		}
	}
	// Rich posts can encode @all in an at element without an entry in mentions.
	if c.AtAll && (m.Type == "post" || m.Type == "text") {
		if m.Type == "text" {
			var body struct {
				Text string `json:"text"`
			}
			return json.Unmarshal([]byte(m.Body.Content), &body) == nil && (nativeAtAll.MatchString(body.Text) || strings.Contains(body.Text, `<at user_id="all">`))
		}
		var v any
		if json.Unmarshal([]byte(m.Body.Content), &v) == nil {
			return hasAll(v, 0)
		}
	}
	return false
}
func hasAll(v any, depth int) bool {
	if depth > 20 {
		return false
	}
	switch x := v.(type) {
	case map[string]any:
		if x["tag"] == "at" && x["user_id"] == "all" {
			return true
		}
		for _, v := range x {
			if hasAll(v, depth+1) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if hasAll(v, depth+1) {
				return true
			}
		}
	}
	return false
}
