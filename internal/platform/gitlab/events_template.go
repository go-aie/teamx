package gitlab

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func EventsMessage(s EventsState, entry EventsInboxEntry) notification.Item {
	e := entry.Event
	label, object, body, title := "项目活动", "项目活动", "", e.Title
	switch e.Kind {
	case "merge_request":
		label = map[string]string{"opened": "开启了合并请求", "merged": "合并了合并请求", "closed": "关闭了合并请求"}[e.Action]
		object = "MR（IID 未提供）"
		if e.TargetIID > 0 {
			object = fmt.Sprintf("MR !%d", e.TargetIID)
		} else if e.TargetID > 0 {
			object += fmt.Sprintf(" · ID %d", e.TargetID)
		}
	case "note":
		label, body = "发表了评论", e.NoteBody
		object = fmt.Sprintf("评论 #%d", e.NoteID)
		if e.NoteableIID > 0 {
			switch e.NoteableType {
			case "MergeRequest":
				object = fmt.Sprintf("MR !%d", e.NoteableIID)
			case "Issue", "WorkItem":
				object = fmt.Sprintf("工作项 #%d", e.NoteableIID)
			case "Snippet":
				object = fmt.Sprintf("Snippet %d", e.NoteableIID)
			}
		}
	case "push":
		label, body, title = "Push · "+notification.Fact(e.Action, 80), e.Title, ""
		object = "Push"
		if e.Branch != "" {
			object = "分支 " + e.Branch
		} else if e.Ref != "" {
			object = e.RefType + " " + e.Ref
		}
		if e.SHA != "" {
			object += " · " + e.SHA
		}
		if e.DetailsMissing {
			body = strings.TrimSpace(body + "\n批量 Push 未提供完整分支或提交信息")
		}
	}
	actor := e.Author.Username
	if actor == "" {
		actor = e.Author.Name
	}
	if actor == "" && e.Author.ID > 0 {
		actor = fmt.Sprintf("ID %d", e.Author.ID)
	}
	if actor == "" {
		actor = "未提供"
	}
	lines := []string{notification.Fact(actor, 256) + " · " + label}
	if excerpt := notification.QuoteExcerpt(body, 2048, false); excerpt != "" {
		lines = append(lines, excerpt)
	}
	// Only display links on the bound instance; never credentials or arbitrary
	// schemes. Missing/invalid object links fall back to the bound project.
	link := s.Binding.URL + "/" + s.Binding.Project
	base, _ := url.Parse(s.Binding.URL)
	target, err := url.Parse(e.URL)
	if err == nil && base != nil && target.Scheme == base.Scheme && target.Host == base.Host && target.User == nil && target.RawQuery == "" && len(target.String()) <= 2048 {
		link = target.String()
	}
	lines = append(lines, notificationReference(s.Binding.Project, object, title, link))
	return notification.Item{Key: EventsKey(s.Binding, s.ProjectID, e.ID), Route: entry.Route, DeliveryID: entry.Route.DeliveryID(entry.DeliveryID), CreatedAt: e.CreatedAt, Order: fmt.Sprintf("%020d", e.ID), Text: strings.Join(lines, "\n\n"), Source: platform.SourceRef{Provider: "gitlab", Channel: "events", ItemKey: EventsKey(s.Binding, s.ProjectID, e.ID), Instance: s.Binding.URL, Project: s.Binding.Project, ProjectID: s.ProjectID, SubscriptionID: s.SubscriptionID, URL: link, Reference: eventReference(s, e)}}
}
