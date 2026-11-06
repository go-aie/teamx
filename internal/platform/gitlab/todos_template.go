package gitlab

import (
	"fmt"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TodosMessage(s TodosState, e TodosInboxEntry) notification.Item {
	t := e.Todo
	actor := "未提供"
	if t.Actor != nil {
		actor = t.Actor.Username
		if actor == "" {
			actor = t.Actor.Name
		}
		if actor == "" {
			actor = fmt.Sprintf("ID %d", t.Actor.ID)
		}
	}
	recipient := "＠" + notification.Fact(s.Binding.Username, 256)
	action := map[string]string{"mentioned": "提及 ", "assigned": "指派给 ", "review_requested": "请求评审：", "other": "待办接收账号："}[t.Reason] + recipient
	object := fmt.Sprintf("工作项 #%d", t.TargetIID)
	if t.Kind == "merge_request" {
		object = fmt.Sprintf("MR !%d", t.TargetIID)
	}
	lines := []string{notification.Fact(actor, 256) + " · " + action}
	// Some to-dos repeat the object title in body. Keep that title in the
	// reference, while note-linked content remains visible even if identical.
	if t.BodyTruncated || strings.TrimSpace(t.Body) != strings.TrimSpace(t.Title) || strings.Contains(t.URL, "#note_") {
		if body := notification.QuoteExcerpt(t.Body, 2048, t.BodyTruncated); body != "" {
			lines = append(lines, body)
		}
	}
	link := ""
	if trustedTodoURL(t.URL, s.Binding.URL, t.Project, t.TargetType, t.TargetIID) {
		link = t.URL
	}
	lines = append(lines, notificationReference(t.Project, object, t.Title, link))
	key := TodosKey(s.Binding, t.ID)
	content := &notification.Content{Structured: true, Action: action,
		Sender:  &notification.Participant{Name: actor, Type: "user"},
		Context: &notification.Context{Name: t.Project + " · " + object + " · " + t.Title, URL: link}}
	if t.Reason == "mentioned" {
		content.Mentions = []notification.Participant{{Name: s.Binding.Username, ID: restID(s.Binding.UserID, "id"), Type: "user", IsReceiver: true}}
	}
	if t.Actor != nil {
		content.Sender.ID = restID(t.Actor.ID, "id")
		content.Sender.IsReceiver = t.Actor.ID > 0 && t.Actor.ID == s.Binding.UserID
	}
	if len(lines) > 2 {
		content.Text = lines[1]
	}
	content.Truncated = t.BodyTruncated || content.Text != "" && content.Text != notification.Quote(t.Body, 2048)
	return notification.WithContent(notification.Item{Key: key, Route: e.Route, DeliveryID: e.Route.DeliveryID(e.DeliveryID), CreatedAt: t.CreatedAt, Order: fmt.Sprintf("%020d", t.ID), Text: strings.Join(lines, "\n\n"), Source: platform.SourceRef{Provider: "gitlab", Channel: "todos", Instance: s.Binding.URL, SubscriptionID: s.SubscriptionID, ItemKey: key, ProjectID: t.ProjectID, Project: t.Project, Object: object, URL: link, Reference: todoReference(s, t)}}, content)
}
