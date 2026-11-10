//go:build e2e

package room

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Explicit opt-in, GET-only GitLab acceptance. All Room/subscription writes use
// a temporary local Store. The historical baseline belongs only to this test;
// it permits verification with existing notifications without remote writes.
func TestInboxRoutingRealGitLab(t *testing.T) {
	origin := os.Getenv("TEAMX_AN_INBOX_REAL_URL")
	if origin == "" {
		t.Skip("set TEAMX_AN_INBOX_REAL_URL and TEAMX_REAL_OAUTH_SNAPSHOT for read-only real acceptance")
	}
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	m.platformHTTP = workspaceHTTPFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodGet {
			t.Errorf("real acceptance attempted non-GET request")
			return nil, ErrRequest
		}
		return http.DefaultTransport.RoundTrip(req)
	})
	room := managerCreate(t, m, "inbox-real-readonly")
	svc, err := startService(m.roomDir(room.RoomID), room.RoomID, serviceConfig{}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.runtimes[room.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	m.mu.Unlock()
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	conn := realOAuthPlatform(t, m, origin)
	clock := &eventTestClock{now: time.Now().UTC().Add(-30 * 24 * time.Hour)}
	m.notifications.Clock = clock.get
	s, err := m.configureTodosSubscription(context.Background(), notification.Key{Owner: room.RoomIdentity, Source: gitlab.TodosSourceID}, 0, gitlab.TodosConfig{Enabled: true, PlatformID: conn.ID, Target: inboxTarget(id)})
	if err != nil {
		t.Fatal("cannot configure isolated subscription", err)
	}
	clock.add(30 * 24 * time.Hour)
	m.mu.Lock()
	runner := m.notifications.NewRunnerLocked(s.Metadata().Key)
	m.mu.Unlock()
	for range 12 {
		_, stop := runner.Step(context.Background())
		if stop {
			t.Fatal("real source stopped before acceptance")
		}
		m.mu.Lock()
		s, _ = m.todosSubscriptionLocked(s.Metadata().Key)
		m.mu.Unlock()
		if s.Scan == nil && !s.LastFull.IsZero() {
			break
		}
	}
	if len(s.Inbox) == 0 {
		t.Fatal("no matching real GitLab notifications in the test window")
	}
	count := len(s.Inbox)
	for range count {
		if err := m.notifications.Deliver(context.Background(), s.Metadata().Key, s.SubscriptionVersion); err != nil {
			t.Fatal("real Inbox handoff failed", err)
		}
	}
	page := inboxRoutingPreview(t, svc, id)
	if page.SnapshotSeq != uint64(count) {
		t.Fatal("real source receipt count mismatch")
	}
	saved, err := svc.store.LoadRoom()
	if err != nil || saved.LatestSeq != 0 {
		t.Fatal("real raw notification reached Room")
	}
	t.Logf("real GitLab GET-only: %d notifications durably received; Room messages=0; preview non-consuming", count)
	if *webPreviewDuration > 0 {
		server := httptest.NewServer(m)
		defer server.Close()
		t.Log("isolated browser preview:", server.URL)
		<-time.After(*webPreviewDuration)
	}
}
