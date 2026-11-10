//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Compose the search adapter only in this disposable manager, before any state
// exists. Keep its production Admit/Reader and the shared delivery lifecycle.
func composeLarkSearch(h *larkHarness) {
	a := h.m.notificationAdapters[lark.MessagesSourceID]
	// SearchSource is already the production adapter.
	h.m.notificationAdapters[lark.MessagesSourceID] = a
	h.m.initPlatformNotifications()
	h.m.notifications.Clock = h.clock.get
}

func newLarkSearchHarness(t *testing.T) *larkHarness {
	t.Helper()
	h := newLarkHarness(t)
	composeLarkSearch(h)

	return h
}

func searchConfigHTTP(t *testing.T, h *larkHarness, current *lark.State, cfg lark.Config) lark.State {
	t.Helper()
	req := map[string]any{"service_epoch": h.m.epoch, "room_id": h.room.RoomID, "room_instance": h.room.RoomInstance, "caller": humanCaller, "provider": "lark", "channel": "messages", "subscription_id": "", "subscription_version": 0, "config": cfg}
	if current != nil {
		req["subscription_id"], req["subscription_version"] = current.SubscriptionID, current.SubscriptionVersion
	}
	r := managerHTTP(h.m, "/v1/platform/notifications/configure", req)
	if r.Code != 200 {
		t.Fatal("configure", r.Code, r.Body.String())
	}
	return h.state()
}

func finishLarkSearch(t *testing.T, h *larkHarness, end int64) lark.State {
	t.Helper()
	s := h.state()
	h.m.mu.Lock()
	r := h.m.notifications.NewRunnerLocked(s.Metadata().Key)
	h.m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for range 180 {
		_, stop := r.Step(ctx)
		s = h.state()
		complete := true
		for _, b := range s.Search.Branches {
			if b.ErrorCode != "" {
				t.Fatal("branch", b.Kind, b.ErrorCode)
			}
			complete = complete && b.Completed >= end
		}
		if complete {
			return s
		}
		if stop || ctx.Err() != nil || s.ErrorCode != "" {
			t.Fatal("search stopped", s.ErrorCode, ctx.Err())
		}
	}
	t.Fatal("bounded search did not finish")
	return s
}

func TestLarkSearchHTTPDeliveryAndAgentRead(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkSearchHarness(t)
	member := h.members[0]
	id := notificationMemberIdentity(h.room.RoomIdentity, member)
	// Ordinary text, explicit @me, @all and own messages in an unselected group.
	var messages []lark.Message
	raw, _ := os.ReadFile(filepath.Join(h.dir, "messages.json"))
	_ = json.Unmarshal(raw, &messages)
	base := messages[0]
	for i, kind := range []string{"plain", "me", "all", "own"} {
		m := base
		m.ID, m.ChatID = "om_"+kind, "oc_group"
		if i == 1 || i == 2 {
			at := "ou_reader"
			if i == 2 {
				at = "all"
			}
			m.Mentions = []lark.Mention{{Key: "@_user_1", ID: at, IDType: "open_id"}}
			m.Body.Content = `{"text":"@_user_1 请确认这条脱敏测试消息"}`
		}
		if kind == "own" {
			m.Sender.ID = "ou_reader"
		}
		messages = append(messages, m)
	}
	raw, _ = json.Marshal(messages)
	if err := os.WriteFile(filepath.Join(h.dir, "messages.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := h.m.defaultLarkConfig(h.c.ID)
	cfg.Target = inboxTarget(id)
	s := searchConfigHTTP(t, h, nil, cfg)
	if s.Search == nil || len(s.Search.Branches) != 3 {
		t.Fatal("HTTP did not create search state")
	}
	baseline := s.Baseline
	h.clock.add(3 * time.Second)
	s = finishLarkSearch(t, h, 101)
	if len(s.Inbox) != 4 {
		t.Fatalf("want private, @me, @all, own; got %d", len(s.Inbox))
	}
	before, _ := h.svc.store.LoadRoom()
	for range len(s.Inbox) {
		if err := h.deliver(); err != nil {
			t.Fatal(err)
		}
	}
	if len(h.state().Inbox) != 0 {
		t.Fatal("source delivery not acknowledged")
	}
	after, _ := h.svc.store.LoadRoom()
	if before.LatestSeq != after.LatestSeq {
		t.Fatal("delivery copied source into Room")
	}
	server := httptest.NewServer(h.m)
	defer server.Close()
	if err := h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 4, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 4}}); err != nil {
		t.Fatal(err)
	}
	cli := contractCLI(t, server.URL, h.svc, id)
	var inbox agentReferenceInboxPage
	if err := cli([]string{"inbox", "read"}, &inbox); err != nil || len(inbox.Entries) != 4 || len(inbox.Followups) != 3 {
		t.Fatalf("Agent CLI captured messages: entries=%d followups=%d error=%v", len(inbox.Entries), len(inbox.Followups), err)
	}
	// Saving and manual sync preserve the baseline and use the search codec.
	cfg.Groups.AtMe, cfg.Groups.AtAll = false, false
	s = searchConfigHTTP(t, h, &s, cfg)
	if !s.Baseline.Equal(baseline) || len(s.Search.Branches) != 2 || s.Search.Branches[1].Kind != "own" {
		t.Fatal("own-context configuration changed baseline/branches")
	}
	if err := h.m.syncLarkSubscription(context.Background(), s.Metadata().Key, s.SubscriptionVersion, cfg.Target); err != nil {
		t.Fatal("sync search", err)
	}
	// The saved own-context setting changes actual collection, including when
	// earlier messages have already been delivered to their frozen member.
	for _, own := range []bool{false, true} {
		m := base
		m.ID, m.ChatID, m.Created, m.Updated = "om_later_other", "oc_group", "103000", "103000"
		m.Mentions = []lark.Mention{{Key: "@_user_1", ID: "ou_reader", IDType: "open_id"}}
		if own {
			m.ID, m.Sender.ID, m.Mentions = "om_later_own", "ou_reader", nil
		}
		messages = append(messages, m)
	}
	raw, _ = json.Marshal(messages)
	if err := os.WriteFile(filepath.Join(h.dir, "messages.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	h.clock.add(3 * time.Second)
	s = finishLarkSearch(t, h, 104)
	if len(s.Inbox) != 1 {
		t.Fatalf("own-context setting admitted %d new messages", len(s.Inbox))
	}
	for _, entry := range s.Inbox {
		if entry.Message.ID != "om_later_own" {
			t.Fatal("own-context setting admitted another sender")
		}
	}
}

func TestLarkSearchHTTPPreservesSelectedFiveGroups(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkSearchHarness(t)
	var base []lark.Message
	raw, _ := os.ReadFile(filepath.Join(h.dir, "messages.json"))
	_ = json.Unmarshal(raw, &base)
	ids := []string{"oc_group", "oc_2", "oc_3", "oc_4", "oc_5"}
	messages := []lark.Message{}
	for _, id := range append(append([]string{}, ids...), "oc_outside") {
		m := base[0]
		m.ChatID, m.ID = id, "om_"+id
		messages = append(messages, m)
	}
	raw, _ = json.Marshal(messages)
	if err := os.WriteFile(filepath.Join(h.dir, "messages.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := lark.DefaultConfig(h.c.ID)
	cfg.Target = inboxTarget(notificationMemberIdentity(h.room.RoomIdentity, h.members[0]))
	cfg.Private.Enabled = false
	// Missing scope is the old five-groups/all-messages setting.
	cfg.Groups = lark.GroupConfig{Enabled: true, ChatIDs: ids, MessageMode: "all"}
	s := searchConfigHTTP(t, h, nil, cfg)
	if s.Config.Groups.Scope != "specified" || !reflect.DeepEqual(s.Config.Groups.ChatIDs, []string{"oc_2", "oc_3", "oc_4", "oc_5", "oc_group"}) {
		t.Fatal("saved range changed")
	}
	h.clock.add(3 * time.Second)
	s = finishLarkSearch(t, h, 101)
	if len(s.Inbox) != 5 || len(s.Chats) != 5 {
		t.Fatal("selected five groups changed receiving scope")
	}
	for _, entry := range s.Inbox {
		if entry.Message.ChatID == "oc_outside" {
			t.Fatal("outside group admitted")
		}
	}
}

func TestLarkSearchDefaultsOnboardingAndLegacyBoundary(t *testing.T) {
	parallelFixtureTest(t)
	for _, search := range []bool{false, true} {
		h := newLarkHarness(t)
		if search {
			composeLarkSearch(h)
		} else {
			a := h.m.notificationAdapters[lark.MessagesSourceID]
			a.Source = &legacyLarkCodec{Source: a.Source.(*lark.SearchSource).Source}
			h.m.notificationAdapters[lark.MessagesSourceID] = a
			h.m.initPlatformNotifications()
			h.m.notifications.Clock = h.clock.get
		}
		id := notificationMemberIdentity(h.room.RoomIdentity, h.members[0])
		query := notificationSettingsQuery(h.m, h.room.RoomIdentity, nil)
		r := managerHTTP(h.m, "/v1/platform/notifications?"+query.Encode(), nil)
		var result struct {
			Defaults      lark.Config        `json:"lark_defaults"`
			Subscriptions []notificationView `json:"subscriptions"`
		}
		if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &result) != nil || len(result.Subscriptions) != 0 || len(h.m.notifications.StatesLocked(h.room.RoomIdentity)) != 0 {
			t.Fatal("default read created subscription")
		}
		if result.Defaults.Groups.Enabled != search || (result.Defaults.Groups.Scope == "all") != search || result.Defaults.Groups.AtAll != search {
			t.Fatal("wrong adapter defaults")
		}
		h.m.mu.Lock()
		op, err := h.m.prepareOnboardingLocked(context.Background(), platformOnboardingRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, OperationID: strings.Repeat("a", 32), platformOnboardingSpec: platformOnboardingSpec{MemberIdentity: id, PlatformID: h.c.ID, Provider: "lark", Origin: h.c.URL, SkipTool: true}})
		h.m.mu.Unlock()
		if err != nil || op.Lark == nil || op.Lark.Groups.Enabled != search || op.Lark.Target != inboxTarget(id) {
			t.Fatal("onboarding defaults", err)
		}
		if !search {
			cfg := lark.DefaultSearchConfig(h.c.ID)
			cfg.Target = inboxTarget(id)
			_, err := h.m.configureLarkSubscription(context.Background(), notification.Key{Owner: h.room.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg)
			if err == nil {
				t.Fatal("production legacy adapter accepted all groups")
			}
		}
		op, err = h.m.applyOnboarding(context.Background(), op.ID, humanCaller.ID)
		if err != nil || op.Notifications.Status != "applied" {
			t.Fatal("apply default onboarding", err, op.Notifications.Status)
		}
		saved := h.state()
		if (saved.Search != nil) != search || saved.Config.Groups.Enabled != search || saved.Config.Target != inboxTarget(id) {
			t.Fatal("onboarding applied different defaults or target")
		}
	}
}

func TestLarkSearchDiagnosticsRedactPageAndResumeCredentials(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkSearchHarness(t)
	cfg := h.m.defaultLarkConfig(h.c.ID)
	cfg.Target = inboxTarget(notificationMemberIdentity(h.room.RoomIdentity, h.members[0]))
	s := searchConfigHTTP(t, h, nil, cfg)
	h.clock.add(3 * time.Second)
	stepNotificationCollector(t, h.m, s.Metadata().Key, 5)
	s = h.state()
	h.m.mu.Lock()
	v, err := h.m.larkNotificationViewLocked(h.room.RoomIdentity, &s)
	h.m.mu.Unlock()
	if err != nil || v.Lark.Search == nil || len(v.Lark.Search.Branches) != 3 || v.Lark.DirectoryComplete {
		t.Fatal("missing search diagnostics", err)
	}
	raw, _ := json.Marshal(v)
	for _, secret := range []string{"fingerprint", "page_token", "next_token", "member_token", "om_000", "请确认这条", "cli_profile"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("diagnostic leaked", secret)
		}
	}
	old := s.Search
	s.Status, s.ErrorCode = "credentials_invalid", "credential_rejected"
	h.m.mu.Lock()
	err = h.m.notifications.SaveLocked(s)
	if err == nil {
		err = h.m.resumeLarkSubscriptionsLocked(h.c.ID)
	}
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal("resume search", err)
	}
	next := h.state()
	if next.Search == nil || next.QueryPaused() || !reflect.DeepEqual(old.Branches, next.Search.Branches) || next.Baseline != s.Baseline {
		t.Fatal("credential recovery lost search progress")
	}
}
