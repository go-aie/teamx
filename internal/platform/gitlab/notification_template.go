package gitlab

import (
	"strings"

	"github.com/go-aie/teamx/internal/platform/notification"
)

// The caller validates the destination against its source binding. Keep the
// object context together in one link, below the action and actual content.
func notificationReference(project, object, title, destination string) string {
	parts := []string{notification.Fact(project, 512), notification.Fact(object, 256)}
	if title = notification.Fact(title, 2048); title != "" {
		parts = append(parts, title)
	}
	label := strings.Join(parts, " · ")
	if destination == "" {
		return label
	}
	destination = strings.NewReplacer("(", "%28", ")", "%29", "@", "%40").Replace(destination)
	return "[" + label + "](" + destination + ")"
}
