package lark

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContentResolvesMentionIdentity(t *testing.T) {
	m := sampleMessage("om_one", "oc_chat", "@_user_1 和 @_user_10 今天确认发版；@_user_1")
	// Same display name must not make another ID look like the receiving account.
	m.Mentions = []Mention{
		{Key: "@_user_1", ID: "ou_reader", IDType: "open_id", Name: "同名用户"},
		{Key: "@_user_10", ID: "ou_other", IDType: "open_id", Name: "同名用户"},
	}
	original, _ := json.Marshal(m)
	want := "@同名用户（open_id: ou_reader；当前接收账号） 和 @同名用户（open_id: ou_other） 今天确认发版；@同名用户（open_id: ou_reader；当前接收账号）"
	if got := m.ContentFor("ou_reader"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := m.Content(); strings.Contains(got, "当前接收账号") || !strings.Contains(got, "ou_other") {
		t.Fatal("unscoped rendering inferred self or lost identity", got)
	}
	after, _ := json.Marshal(m)
	if string(after) != string(original) {
		t.Fatal("rendering changed native message evidence")
	}
}

func TestContentMentionFallbacksAndBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name string
		text string
		ats  []Mention
		want string
	}{
		{"no_metadata", "@_user_1 发版", nil, "@_user_1（身份未解析） 发版"},
		{"missing_name", "@_user_1", []Mention{{Key: "@_user_1", ID: "ou_other", IDType: "open_id"}}, "@未知用户（open_id: ou_other）"},
		{"missing_id", "@_user_1", []Mention{{Key: "@_user_1", Name: "读者"}}, "@读者（身份未解析）"},
		{"wrong_id_type", "@_user_1", []Mention{{Key: "@_user_1", Name: "读者", ID: "ou_reader", IDType: "union_id"}}, "@读者（union_id: ou_reader）"},
		{"all", "@_all 发版", nil, "@所有人 发版"},
		{"all_metadata", "@_user_1", []Mention{{Key: "@_user_1", ID: "all"}}, "@所有人"},
		{"token_suffix", "@_user_1x @_user_10_extra @_all_extra", nil, "@_user_1x @_user_10_extra @_all_extra"},
		{"literal_display_name", "@_user_1", []Mention{{Key: "@_user_1", Name: "@_user_2", ID: "ou_other", IDType: "open_id"}}, "@@_user_2（open_id: ou_other）"},
		{"ordinary_text", "联系 @张三 或 someone@example.com", nil, "联系 @张三 或 someone@example.com"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := sampleMessage("om_one", "oc_chat", tt.text)
			m.Mentions = tt.ats
			if got := m.ContentFor("ou_reader"); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContentRichMentionsAndDeletedMessage(t *testing.T) {
	m := sampleMessage("om_one", "oc_chat", "")
	m.Type = "post"
	m.Body.Content = `{"zh_cn":{"title":"发版","content":[[{"tag":"at","user_id":"@_user_1"},{"tag":"text","text":"确认"},{"tag":"at","user_id":"ou_other","user_name":"旧名字"},{"tag":"at","user_id":"all"},{"tag":"at","user_id":"ou_unknown","user_name":"未映射用户"},{"tag":"img","image_key":"img_fixture"}]]}}`
	m.Mentions = []Mention{
		{Key: "@_user_1", ID: "ou_reader", IDType: "open_id", Name: "接收者"},
		{Key: "@_user_2", ID: "ou_other", IDType: "open_id", Name: "其他人"},
	}
	want := "发版\n@接收者（open_id: ou_reader；当前接收账号）\n确认\n@其他人（open_id: ou_other）\n@所有人\n@未映射用户（user_id: ou_unknown；身份未解析）\n![飞书图片](lark-image:img_fixture)\n[图片内容需通过附件读取]"
	if got := m.ContentFor("ou_reader"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	m.Deleted = true
	if got := m.ContentFor("ou_reader"); got != "消息已撤回" {
		t.Fatal("deleted message exposed mention evidence", got)
	}
}
