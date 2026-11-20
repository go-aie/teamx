package room

import (
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Thin default adapter for standalone stores and existing projection callers.
// Capability selection belongs to the same checked application catalog.
func notificationCapabilities(provider string) notification.Capabilities {
	return defaultPlatformCatalog().NotificationCapabilities(provider)
}

func projectNotification(item notification.Item) notification.View {
	if project := notificationCapabilities(item.Source.Provider).Project; project != nil {
		return project(item)
	}
	return notification.Project(item)
}
