package room

import (
	"testing"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func unifiedGitLabConfig(t *testing.T, h *eventHarness) gitlab.NotificationConfig {
	t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	v, err := h.m.gitlabNotificationViewLocked(h.room.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	return v.Config
}
