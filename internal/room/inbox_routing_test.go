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
	next, err := h.m.configureEventsSubscription(context.Background(), s.Metadata().Key, s.SubscriptionVersion, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if next.SubscriptionID != s.SubscriptionID || !next.Baseline.Equal(s.Baseline) {
		t.Fatal("route switch reset source baseline")
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
