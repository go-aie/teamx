//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
)

// Reuses the existing bounded, private input and DSH harness. All stores are
// disposable; external operations are Lark reads and one bounded model turn.
func TestRealLarkSearchNotificationDSH(t *testing.T) {
	runRealLarkNotificationDSH(t, collectRealLarkSearch)
}

func collectRealLarkSearch(t *testing.T, m *roomManager, svc *service, id MemberIdentity, c managedPlatform, input larkRealInput, _ bool) lark.State {
	t.Helper()
	a := m.larkNotificationAdapter()
	// SearchSource is already the production adapter.
	m.notificationAdapters[lark.MessagesSourceID] = a
	m.initPlatformNotifications()
	client, err := lark.New(*c.CLIProfile)
	if err != nil {
		t.Fatal("real reader", lark.ErrorCode(err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// Select one native group hit from the fixed S1 window, then collect only
	// a two-minute window around it. This does not claim full-window coverage.
	var chat string
	for _, candidate := range input.Chats {
		metadata, e := client.Chat(ctx, c.Validation.Account, candidate)
		if e != nil {
			t.Fatal("sample metadata", lark.ErrorCode(e))
		}
		if metadata.Mode == "group" || metadata.Mode == "topic" {
			chat = candidate
			break
		}
	}
	if chat == "" {
		t.Fatal("fixed sample needs a group")
	}
	page, err := client.SearchMessages(ctx, c.Validation.Account, lark.MessageSearchRequest{ChatType: "group", ChatIDs: []string{chat}, Start: time.Unix(input.Start, 0).UTC(), End: time.Unix(input.End, 0).UTC(), PageSize: 1})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("bounded sample discovery", lark.ErrorCode(err))
	}
	at, err := time.Parse(time.RFC3339Nano, page.Items[0].MetaData.Created)
	if err != nil {
		t.Fatal("sample timestamp")
	}
	start, end := at.Add(-time.Minute).Truncate(time.Second), at.Add(time.Minute).Truncate(time.Second)
	clock := &eventTestClock{now: end.Add(time.Second)}
	m.notifications.Clock = clock.get
	cfg := m.defaultLarkConfig(c.ID)
	cfg.Target = inboxTarget(id)
	cfg.Private.Enabled = false
	cfg.Groups.Scope, cfg.Groups.ChatIDs, cfg.Groups.MessageMode = "specified", []string{chat}, "all"
	cfg.BackfillStart = start
	response := managerHTTP(m, "/v1/platform/notifications/configure", map[string]any{"service_epoch": m.epoch, "room_id": id.RoomID, "room_instance": id.RoomInstance, "caller": humanCaller, "provider": "lark", "channel": "messages", "subscription_id": "", "subscription_version": 0, "config": cfg})
	if response.Code != 200 {
		t.Fatal("real HTTP configuration", response.Code)
	}
	var view notificationView
	if json.Unmarshal(response.Body.Bytes(), &view) != nil || view.Lark.Search == nil {
		t.Fatal("HTTP omitted search diagnostics")
	}
	m.mu.Lock()
	s, err := m.currentLarkForTargetLocked(id.RoomIdentity, c.ID, cfg.Target)
	if err != nil || s == nil {
		m.mu.Unlock()
		t.Fatal("real search subscription unavailable")
	}
	runner := m.notifications.NewRunnerLocked(s.Metadata().Key)
	m.mu.Unlock()
	for step := 0; step < 100; step++ {
		_, stop := runner.Step(ctx)
		m.mu.Lock()
		s, err = m.currentLarkForTargetLocked(id.RoomIdentity, c.ID, cfg.Target)
		m.mu.Unlock()
		if err != nil || s == nil || stop || ctx.Err() != nil || s.ErrorCode != "" {
			t.Fatal("real search stopped")
		}
		b := s.Search.Branches[0]
		if b.ErrorCode != "" {
			t.Fatal("real search branch", b.ErrorCode)
		}
		if b.Completed >= end.Unix() {
			break
		}
		if step == 99 {
			t.Fatal("real search exceeded step bound")
		}
	}
	if len(s.Inbox) == 0 {
		t.Fatal("real sample not accepted")
	}
	result := *s
	for range len(s.Inbox) {
		if err := m.notifications.Deliver(ctx, s.Metadata().Key, s.SubscriptionVersion); err != nil {
			t.Fatal("real Inbox delivery")
		}
	}
	t.Logf("real search -> member Inbox: accepted=%d search_pages=%d detail_batches=%d verifications=%d remote_writes=0", len(result.Inbox), result.Search.Metrics.Pages, result.Search.Metrics.Batches, result.Search.Metrics.Verifications)
	return result
}
