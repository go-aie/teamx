package room

import (
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Projection uses the Store's selected capabilities; it never substitutes the
// standard composition for an explicitly empty catalog.
func (s *Store) projectNotification(item notification.Item) notification.View {
	if project := s.notificationCapabilities(item.Source.Provider).Project; project != nil {
		return project(item)
	}
	return notification.Project(item)
}
