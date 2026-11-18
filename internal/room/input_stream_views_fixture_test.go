package room

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	inputstream "github.com/go-aie/teamx/internal/inputstreamtest"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func viewsComparisonFixture(t testing.TB, scenario string) *streamFixture {
	base := scenario
	if strings.HasPrefix(scenario, "ext-") {
		base = "direct"
	}
	f := comparisonFixture(t, base)
	// Use collector-shaped saved project paths in both arms, without translating
	// names for the model. Native grouping keys and object labels stay unchanged.
	for i := range f.records {
		r := &f.records[i]
		if r.Group.Facts.Project == "项目 A" {
			r.Group.Facts.Project = "acme/release-service"
		}
		if r.Group.Kind == "room" {
			r.Excerpt = strings.ReplaceAll(r.Excerpt, "项目 A", "acme/release-service")
		}
		f.entries[r.Location] = json.RawMessage(strings.ReplaceAll(string(f.entries[r.Location]), "项目 A", "acme/release-service"))
	}
	setHuman := func(text string) {
		r := &f.records[0]
		r.Excerpt = text
		var entry map[string]any
		_ = json.Unmarshal(f.entries[r.Location], &entry)
		entry["text"] = text
		f.entries[r.Location], _ = json.Marshal(entry)
	}
	clone := func(index int, seq uint64, channel, blocked, text string) {
		r := f.records[index]
		r.Location.Seq = seq
		r.Group.Channel = channel
		r.Blocked = blocked
		r.Excerpt = text
		r.Group.Facts.Summary = text
		r.ReceivedAt = time.Unix(int64(seq), 0)
		f.records = append(f.records, r)
		f.entries[r.Location], _ = json.Marshal(map[string]any{"type": "notification", "item": map[string]any{"text": text, "provider": r.Group.Provider, "channel": channel}})
	}
	switch scenario {
	case "ext-stats":
		setHuman("仅统计 acme/release-service 中含 Todo 的待交付流：总流数、这些流全部待交付条数、受阻数和接收账号分布。消息条件只选流，不是数 Todo 条数。不要预览或正式读取平台流。不评论、不合并、不写入平台。")
		clone(1, 2, "todos", "", "SAVED_TODO")
		clone(1, 3, "events", "", "SAVED_EVENT")
		clone(1, 4, "todos", "saved source unavailable", "BLOCKED_TODO")
		f.records[4].Group.Context = []string{"project", "merge_request", "77"}
		f.records[4].Group.Facts.IID = "77"
		f.records[4].Group.Facts.Label = "MR !77 blocked"
		clone(4, 5, "events", "saved source unavailable", "BLOCKED_EVENT")
	case "ext-scope":
		setHuman("先查询 acme/release-service 的本地项目概况以确认范围，随后复用返回的原生范围并交集限定 merge_request、IID=56，正式读取该对象并报告证据。不要读其他项目或对象。不评论、不合并、不写入平台。")
		clone(1, 2, "events", "", "UNRELATED_PROJECT")
		f.records[2].Group.Context = []string{"other-project", "merge_request", "56"}
		f.records[2].Group.Facts.Project = "acme/other-service"
		f.records[2].Group.Facts.ProjectID = "8"
		f.records[2].Group.Facts.Label = "Other project !56"
	case "ext-explore":
		setHuman("只读核查飞书群「发布值班群」：先列出本地受阻流并说明原因，再按最早等待排序查该群中含 thread_id=omt_release 且文字 NEEDLE 的流。命中在后文也必须从群里最早未交付消息顺序正式读取，报告 FIRST_CONTEXT 和 NEEDLE_EVIDENCE。不要读其他会话。不评论、不合并、不写入平台。")
		// Replace the platform fixture with serialized native Lark notification shapes.
		f.records = f.records[:1]
		for i := 1; i <= 4; i++ {
			chat, name, thread := "oc_release", "发布值班群", ""
			text := "FIRST_CONTEXT: 发布前置限制，先核对证据。"
			if i == 2 {
				thread = "omt_release"
				text = "NEEDLE_EVIDENCE: 保存的发布校验失败。"
			}
			if i == 3 {
				chat = "oc_blocked"
				name = "受阻排查群"
				text = "BLOCKED_CHAT"
			}
			if i == 4 {
				chat = "oc_unnamed"
				name = ""
				text = "UNKNOWN_NAME"
			}
			ref := platform.NotificationReference{Version: 2, AccountTenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "tenant_fixture"}, RecipientID: platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_receiver"}, ChatID: platform.ExternalID{Namespace: "lark:chat_id:cli_fixture", Value: chat}}
			if thread != "" {
				ref.DiscussionID = platform.ExternalID{Namespace: "lark:thread_id:cli_fixture", Value: thread}
			}
			item := notification.Item{Text: text, CreatedAt: time.Unix(int64(i), 0), Source: platform.SourceRef{Provider: "lark", Channel: "messages", Instance: "https://open.feishu.cn", SubscriptionID: "messages", Reference: ref}, Content: &notification.Content{Text: text, Structured: true, Conversation: &notification.Context{Name: name, ID: ref.ChatID}}}
			g, err := inputstream.GroupLark(item)
			if err != nil {
				t.Fatal(err)
			}
			r := inputstream.Record{Location: inputstream.Location{Partition: "lark", Seq: uint64(i)}, Group: g, ReceivedAt: item.CreatedAt, Pending: true, Excerpt: text}
			if i == 3 {
				r.Blocked = "saved source unavailable"
			}
			f.records = append(f.records, r)
			f.entries[r.Location], _ = json.Marshal(projectStreamNotification(inboxEntryView{Type: "notification", Item: projectNotification(item)}))
		}
	}
	var err error
	f.catalog, err = inputstream.NewCatalog(comparisonMember, 131, []inputstream.Descriptor{inputstream.GitLabCapability().Descriptor, inputstream.LarkCapability().Descriptor}, f.records, inputstream.Preferences{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestInputViewsExtensionFixtures(t *testing.T) {
	for _, s := range []string{"ext-stats", "ext-scope", "ext-explore"} {
		t.Run(s, func(t *testing.T) {
			f := viewsComparisonFixture(t, s)
			if _, _, err := f.catalog.Directory(); err != nil {
				t.Fatal(fmt.Sprint(s, err))
			}
		})
	}
}
