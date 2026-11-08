package room

import (
	"context"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func newIndependentEventHarness(t *testing.T) (*eventHarness, platformConnection) {
	t.Helper()
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "remote-project")
	c := verifyTodoAccount(t, m, platformTestSave(t, m, platformTestRequest(m)), 101)
	clock := &eventTestClock{now: time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)}
	m.notifications.Clock = clock.get
	m.platformProject = func(_ context.Context, origin, project string, secret workspaceSecret) (int64, error) {
		if origin != c.URL || secret.value == "" {
			return 0, ErrRequest
		}
		switch project {
		case "remote/project":
			return 7, nil
		case "remote/other":
			return 8, nil
		}
		return 0, ErrRequest
	}
	pages := &eventPages{}
	m.notifications.Source(gitlab.EventsSourceID).(*gitlab.EventsSource).Prepare = func(_ context.Context, _ string, b gitlab.Binding) (gitlab.EventFetcher, int64, error) {
		return pages, b.ProjectID, nil
	}
	cfg := gitlab.EventsConfig{Enabled: true, ConnectionID: c.ID, Project: c.URL + "/remote/project.git"}
	s, err := m.configureEventsSubscription(context.Background(), notification.Key{Owner: room.RoomIdentity, Source: gitlab.EventsSourceID}, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	h := &eventHarness{t: t, m: m, room: room, clock: clock, pages: pages, baseline: s.Baseline}
	h.newRunner()
	return h, c
}
