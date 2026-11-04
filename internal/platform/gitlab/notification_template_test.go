package gitlab

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/platform"
)

func TestNotificationTemplatesLeadWithContentAndLinkObjectContext(t *testing.T) {
	s := sourceTodoState(t)
	todo := Todo{ID: 9, Project: "group/project", ProjectID: 7, TargetType: "Issue", TargetIID: 2,
		Actor: &TodoActor{ID: 2, Username: "alice"}, Reason: "mentioned", Title: "登录失败",
		Body: "请核对重试逻辑。\n第二次请求仍然返回 500。", URL: s.Binding.URL + "/group/project/-/issues/2#note_15"}
	item := TodosMessage(s, TodosInboxEntry{Todo: todo})
	if !strings.HasPrefix(item.Text, "alice · 提及 ＠recipient\n\n> 请核对重试逻辑。\n> 第二次请求仍然返回 500。\n\n") ||
		!strings.HasSuffix(item.Text, "[group/project · 工作项 #2 · 登录失败]("+todo.URL+")") {
		t.Fatal(item.Text)
	}
	if strings.Contains(item.Text, "项目：") || strings.Contains(item.Text, "标题：") || strings.Contains(item.Text, "链接：") {
		t.Fatal("context expanded into separate metadata rows", item.Text)
	}
	// Content must survive the durable inbox and delivered-object snapshot.
	todo.RecipientID, todo.Kind, todo.Action, todo.TargetID, todo.State, todo.CreatedAt = 101, "work_item", "mentioned", 20, "pending", s.Baseline
	s.Accept([]Todo{todo}, s.Baseline)
	if s.Inbox[item.Key].Todo.Body != todo.Body || s.Acknowledge(item.Key).(TodosState).DeliveredObjects[item.Key].Body != todo.Body {
		t.Fatal("to-do body was dropped from durable delivery")
	}
	e := Event{ID: 1, Kind: "note", Author: EventAuthor{Username: "alice"}, NoteID: 15, NoteableType: "Issue", NoteableIID: 2, Title: todo.Title, NoteBody: todo.Body, URL: todo.URL}
	events := EventsState{Binding: platform.ProjectBinding{URL: s.Binding.URL, Project: todo.Project}, ProjectID: 7}
	message := EventsMessage(events, EventsInboxEntry{Event: e})
	if !strings.HasPrefix(message.Text, "alice · 发表了评论\n\n> 请核对重试逻辑。") || !strings.HasSuffix(message.Text, "[group/project · 工作项 #2 · 登录失败]("+todo.URL+")") {
		t.Fatal(message.Text)
	}
}

func TestNotificationTemplateAbsentContentAndUntrustedInput(t *testing.T) {
	s := sourceTodoState(t)
	todo := Todo{ID: 1, Project: "group/project", TargetType: "MergeRequest", TargetIID: 42, Reason: "review_requested", Title: "修复登录", Body: "修复登录", URL: s.Binding.URL + "/group/project/-/merge_requests/42"}
	text := TodosMessage(s, TodosInboxEntry{Todo: todo}).Text
	if strings.Contains(text, "> ") || strings.Count(text, "修复登录") != 1 {
		t.Fatal("object title was presented as new content", text)
	}
	todo.Body = ""
	if strings.Contains(TodosMessage(s, TodosInboxEntry{Todo: todo}).Text, "> ") {
		t.Fatal("missing body manufactured a quote")
	}
	todo.Body = "@developer\n# fake heading\n![image](https://evil.invalid)\n" + strings.Repeat("长", 10000)
	todo.Title = "@developer [unsafe](javascript:run)"
	todo.URL = "https://evil.invalid/object"
	text = TodosMessage(s, TodosInboxEntry{Todo: todo}).Text
	if !utf8.ValidString(text) || len(text) > 16<<10 || !strings.Contains(text, "…") || strings.Contains(text, "@developer") || strings.Contains(text, "![image]") || strings.Contains(text, "\n> # fake") || strings.Contains(text, "](https://evil.invalid/object)") {
		t.Fatal(text)
	}
}
