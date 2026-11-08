package room

import (
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Static assembly is the only place that selects native notification adapters.
// A provider need not implement admission checks, saved-envelope projection or
// message/action matching to participate in the common Inbox protocol.
func notificationCapabilities(provider string) notification.Capabilities {
	switch provider {
	case "lark":
		return lark.NotificationCapabilities()
	default:
		return notification.Capabilities{}
	}
}

func projectNotification(item notification.Item) notification.View {
	if project := notificationCapabilities(item.Source.Provider).Project; project != nil {
		return project(item)
	}
	return notification.Project(item)
}
