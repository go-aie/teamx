package room

import (
	"testing"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

// A real Room actor/store with service lifecycle guards; no member/model is
// assembled here. Scheduler behavior is covered independently below.
func eventLiveService(t *testing.T, h *eventHarness) *service {
	t.Helper()
	svc, err := startService(h.m.roomDir(h.room.RoomID), h.room.RoomID, serviceConfig{updates: h.m.updates, unifiedInputs: h.m.config.unifiedInputs}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	h.m.runtimes[h.room.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	h.m.mu.Unlock()
	return svc
}
func seedEventInbox(t *testing.T, h *eventHarness, id int64) {
	t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	s, _ := h.m.eventsSubscriptionLocked(h.m.testEventsKeyLocked(h.room.RoomIdentity))
	s.ProjectID = 7
	if _, _, err := s.Accept([]gitlab.Event{h.event(id, 0)}, h.clock.get()); err != nil {
		t.Fatal(err)
	}
	if err := h.m.notifications.SaveLocked(s); err != nil {
		t.Fatal(err)
	}
}
