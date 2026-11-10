package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func inboxRoutingMember(t *testing.T, svc *service, name, membership string) MemberIdentity {
	t.Helper()
	id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: name, MembershipID: membership}
	addr, err := svc.rt.Spawn(notificationSink{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.room.Call[MemberInfo](context.Background(), AddMember{MemberIdentity: id, Runner: RunnerConfig{Kind: "dsh", Profile: "runtime"}, Target: addr}); err != nil {
		t.Fatal(err)
	}
	svc.setTarget(membership, addr)
	return id
}
func inboxRoutingPreview(t *testing.T, svc *service, id MemberIdentity) inboxPreviewPage {
	t.Helper()
	w := httptest.NewRecorder()
	body, _ := json.Marshal(apiRequest{ServiceInfo: svc.info, Caller: humanCaller, AgentID: id.AgentID, MembershipID: id.MembershipID, Limit: 1})
	svc.ServeHTTP(w, httptest.NewRequest("POST", "/v1/member/inbox", strings.NewReader(string(body))))
	var page inboxPreviewPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	return page
}
func inboxRoutingConfigure(t *testing.T, h *eventHarness, target notification.Target) gitlab.EventsState {
	t.Helper()
	s := h.state()
	cfg := s.Config
	cfg.Target = target
	next, err := gitlab.ConfigureEvents(s.RoomIdentity, &s, &s.Binding, cfg, h.m.notifications.Now())
	if err != nil {
		t.Fatal(err)
	}
	loadHistoricalNotificationFixture(t, h.m, next)
	h.newRunner()
	if next.SubscriptionID != s.SubscriptionID || !next.Baseline.Equal(s.Baseline) {
		t.Fatal("historical route fixture reset source baseline")
	}
	return next
}
func inboxRoutingDeliver(t *testing.T, h *eventHarness) {
	t.Helper()
	s := h.state()
	if err := h.m.notifications.Deliver(context.Background(), s.Metadata().Key, s.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
}
func inboxTarget(id MemberIdentity) notification.Target {
	return notification.Target{Mode: "inbox", AgentID: id.AgentID, MembershipID: id.MembershipID}
}

func TestInboxRoutingPausedMemberStaysPaused(t *testing.T) {
	svc, err := newFixtureService(t.TempDir(), "inbox-paused", serviceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := svc.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	member, err := svc.add(apiRequest{AgentID: "assistant", Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}})
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	post := integrationPost(svc.info.RoomIdentity, 1)
	item := notification.Item{Key: post.Source.ItemKey, DeliveryID: post.DeliveryID, Text: post.Text, Source: post.Source, CreatedAt: time.Now().UTC(), Route: notification.Route{Target: inboxTarget(id), Version: 1}}
	if err := svc.receiveInbox(context.Background(), inboxTarget(id), item); err != nil {
		t.Fatal(err)
	}
	status, err := svc.target(id.MembershipID).Call[MemberStatus](context.Background(), InspectMember{id})
	if err != nil || !status.Paused || status.Active || status.Agent.TurnNumber != 0 {
		t.Fatal("Inbox enabled or scheduled paused member", status, err)
	}
	if inboxRoutingPreview(t, svc, id).SnapshotSeq != 1 {
		t.Fatal("paused member did not receive")
	}
}

func TestInboxRoutingStoppedInboxReleasesStore(t *testing.T) {
	dir := t.TempDir()
	svc, err := startService(dir, "inbox-stopped", serviceConfig{}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer svc.close(context.Background())
	id := inboxRoutingMember(t, svc, "assistant", strings.Repeat("1", 32))
	inboxRoutingPreview(t, svc, id)
	failure := errors.New("injected Inbox stop failure")
	svc.inboxMu.Lock()
	addr := svc.inboxes[id.MembershipID]
	svc.inboxMu.Unlock()
	if err := addr.Stop(failure); err != nil {
		t.Fatal(err)
	}
	if _, err := addr.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.close(context.Background()); !errors.Is(err, failure) || !svc.closed {
		t.Fatal("settled actor failure stranded Store ownership", err)
	}
	reopened, err := OpenStore(dir, "inbox-stopped")
	if err != nil {
		t.Fatal("settled failure prevented recovery", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

// Constructs upgraded historical progress whose current target differs from a
// frozen pending route. Production configure must reject such a target change.
// These fixtures have no workers; reloading uses the normal source codecs.
func loadHistoricalNotificationFixture(t *testing.T, m *roomManager, state notification.State) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, prior := range m.notifications.StatesLocked(state.Metadata().Key.Owner) {
		if m.notifications.DoneLocked(prior.Metadata().Key) != nil {
			t.Fatal("historical fixture has live worker")
		}
	}
	path, err := m.notifications.StatePathLocked(state.Metadata().Key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	envelope := notificationEnvelope{SourceID: state.Metadata().Key.Source, Schema: m.notifications.Source(state.Metadata().Key.Source).Schema(), State: raw}
	if err := m.store.save(path, envelope, m.notifications.Limits.Bytes); err != nil {
		t.Fatal(err)
	}
	clock, limits := m.notifications.Clock, m.notifications.Limits
	m.initPlatformNotifications()
	m.notifications.Clock, m.notifications.Limits = clock, limits
	if err := m.loadPlatformNotificationsLocked(); err != nil {
		t.Fatal(err)
	}
}
