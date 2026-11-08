//go:build integration || e2e

package room

import (
	"context"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestLarkCustomBackfillHTTPReadsHistoryIntoMemberInbox(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.clock.add(14 * 24 * time.Hour)
	member := h.members[0]
	cfg := lark.Config{Enabled: true, ConnectionID: h.c.ID, Chats: []string{"oc_chat"}, Target: notification.Target{Mode: "inbox", AgentID: member.AgentID, MembershipID: member.MembershipID}, Private: true, BackfillStart: time.Unix(1, 0).UTC()}
	req := map[string]any{"service_epoch": h.m.epoch, "room_id": h.room.RoomID, "room_instance": h.room.RoomInstance, "caller": humanCaller, "provider": "lark", "channel": "messages", "subscription_id": "", "subscription_version": 0, "config": cfg}
	response := managerHTTP(h.m, "/v1/platform/notifications/configure", req)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	s := h.state()
	if !s.Baseline.Equal(cfg.BackfillStart) {
		t.Fatal("HTTP configuration lost absolute start")
	}
	h.m.mu.Lock()
	runner := h.m.notifications.NewRunnerLocked(s.Metadata().Key)
	h.m.mu.Unlock()
	runner.Step(context.Background())
	runner.Step(context.Background())
	if len(h.state().Inbox) != 1 {
		t.Fatal("historical message older than 24h was not collected")
	}
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	if inboxRoutingPreview(t, h.svc, id).SnapshotSeq != 1 {
		t.Fatal("historical message was not delivered to member Inbox")
	}
	cfg.BackfillStart = cfg.BackfillStart.Add(time.Second)
	req["config"], req["subscription_id"], req["subscription_version"] = cfg, s.SubscriptionID, s.SubscriptionVersion
	response = managerHTTP(h.m, "/v1/platform/notifications/configure", req)
	if response.Code != 409 || !h.state().Baseline.Equal(s.Baseline) {
		t.Fatal("existing baseline was changed", response.Code, response.Body.String())
	}
}
