//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestLarkVerificationResumesCredentialPausedSubscription(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	before := h.state()
	h.m.mu.Lock()
	err := h.m.notifications.InvalidatePlatformLocked(h.c.ID)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	paused := h.state()
	if paused.Status != "credentials_invalid" {
		t.Fatal("fixture did not pause subscription")
	}
	c, err := h.m.verifyPlatform(context.Background(), managedPlatformRequest{ID: h.c.ID, Version: h.c.Version})
	if err != nil || c.Validation.Status != "valid" {
		t.Fatal("verification failed", err)
	}
	resumed := h.state()
	if resumed.QueryPaused() || resumed.ErrorCode != "" || resumed.SubscriptionVersion != paused.SubscriptionVersion+1 || resumed.SubscriptionID != before.SubscriptionID || resumed.Binding != before.Binding || !reflect.DeepEqual(resumed.Config, before.Config) {
		t.Fatal("subscription did not resume under its original identity/configuration")
	}
	if !resumed.Baseline.Equal(before.Baseline) || !resumed.LastFull.Equal(before.LastFull) || !reflect.DeepEqual(resumed.Chats, before.Chats) || !reflect.DeepEqual(resumed.Inbox, before.Inbox) || !reflect.DeepEqual(resumed.Observed, before.Observed) || !reflect.DeepEqual(resumed.Accepted, before.Accepted) {
		t.Fatal("verification reset progress, deduplication or pending delivery")
	}
	// Re-verification of a running source is a no-op for subscription state.
	if _, err = h.m.verifyPlatform(context.Background(), managedPlatformRequest{ID: c.ID, Version: c.Version}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.state(), resumed) {
		t.Fatal("verification restarted an already-running subscription")
	}
	if err := h.deliver(); err != nil {
		t.Fatal("resumed backlog was not delivered", err)
	}
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	if page := inboxRoutingPreview(t, h.svc, id); page.SnapshotSeq != 1 {
		t.Fatal("backlog delivery missing", page)
	}
	h.clock.add(time.Minute)
	h.messages(2)
	h.m.mu.Lock()
	runner := h.m.notifications.NewRunnerLocked(resumed.Metadata().Key)
	h.m.mu.Unlock()
	for range 12 {
		if _, stop := runner.Step(context.Background()); stop {
			t.Fatal("resumed source did not query")
		}
	}
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	if page := inboxRoutingPreview(t, h.svc, id); page.SnapshotSeq != 2 {
		t.Fatal("new message was not delivered exactly once", page)
	}
}

func TestLarkVerificationPreservesOtherPauses(t *testing.T) {
	parallelFixtureTest(t)
	for _, mode := range []string{"disabled", "retired", "capacity", "binding_changed", "different_account", "different_generation", "failed_verification"} {
		t.Run(mode, func(t *testing.T) {
			h := newLarkHarness(t)
			h.start()
			s := h.state()
			s.RuntimeState.InvalidateCredential(true)
			switch mode {
			case "disabled", "retired":
				s.Config.Enabled = false
				s.RuntimeState.Reset(false, false)
				s.Retired = mode == "retired"
			case "capacity", "binding_changed":
				s.Status, s.ErrorCode = mode, mode
			case "different_account", "failed_verification":
				script := strings.ReplaceAll(larkFixtureScript(), "ou_reader", "ou_other")
				if mode == "failed_verification" {
					script = "#!/usr/bin/env -S python3 -S\nimport sys\nsys.exit(1)\n"
				}
				if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			case "different_generation":
				s.Binding.Generation = strings.Repeat("f", 32)
				s = refreshLarkSearchFixture(t, s)
			}
			h.m.mu.Lock()
			err := h.m.notifications.SaveLocked(s)
			h.m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.m.verifyPlatform(context.Background(), managedPlatformRequest{ID: h.c.ID, Version: h.c.Version}); err != nil {
				t.Fatal(err)
			}
			h.m.mu.Lock()
			got, ok := h.m.notifications.StateLocked(s.Metadata().Key)
			h.m.mu.Unlock()
			if !ok || !reflect.DeepEqual(got, s) {
				t.Fatal("verification changed an ineligible subscription")
			}
		})
	}
}

func TestLarkVerificationRestartsPausedWorker(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	s := h.state()
	h.m.mu.Lock()
	err := h.m.notifications.InvalidatePlatformLocked(h.c.ID)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.messages(2)
	h.clock.add(time.Minute)
	if err := h.m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	oldWorker := h.m.notifications.DoneLocked(s.Metadata().Key)
	h.m.mu.Unlock()
	if oldWorker == nil {
		t.Fatal("paused query lost its worker")
	}
	if _, err := h.m.verifyPlatform(context.Background(), managedPlatformRequest{ID: h.c.ID, Version: h.c.Version}); err != nil {
		t.Fatal(err)
	}
	awaitEventSignal(t, oldWorker, "verification did not settle the paused worker")
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if page := inboxRoutingPreview(t, h.svc, id); page.SnapshotSeq == 2 && !h.state().QueryPaused() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("verification did not restart polling and deliver the new message")
}

func TestLarkNotificationsInboxReferencesAndRoomQuestion(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	if len(h.state().Inbox) != 1 {
		t.Fatal("missing source candidate")
	}
	member := h.members[0]
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	// Receipt persists while member scheduling remains paused.
	before, _ := h.svc.store.LoadRoom()
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	after, _ := h.svc.store.LoadRoom()
	if after.LatestSeq != before.LatestSeq {
		t.Fatal("delivery enabled member or copied to Room")
	}
	server := httptest.NewServer(h.m)
	defer server.Close()
	cli := contractCLI(t, server.URL, h.svc, id)
	// Collection has already captured the message. Inbox reading and followup
	// creation need neither a member binding nor an available Lark executable.
	bin := filepath.Join(h.dir, "lark-cli")
	if err := os.Rename(bin, bin+".disabled"); err != nil {
		t.Fatal(err)
	}
	if e := h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 1, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}}); e != nil {
		t.Fatal(e)
	}
	var inbox agentReferenceInboxPage
	if e := cli([]string{"inbox", "read"}, &inbox); e != nil || len(inbox.Followups) != 1 || len(inbox.Entries) != 1 {
		t.Fatal("inbox source resolution", e)
	}
	if inbox.Entries[0].Source.EvidenceSource != lark.NotificationEvidence || !strings.Contains(inbox.Entries[0].Item.Text, "脱敏测试消息") {
		t.Fatal("lost captured body or evidence provenance", inbox)
	}
	object := inbox.Followups[0].ObjectRef
	unit := inbox.Followups[0].Ref
	u, e := h.svc.store.readFollowup(id, object, unit)
	if e != nil || len(u.Actions) != 0 {
		t.Fatal(e)
	}
	var content larkTargetPage
	if e = cli([]string{"lark", "read", "--object", object, "--target", object}, &content); e == nil {
		t.Fatal("notification identity granted remote read access")
	}
	if err := os.Rename(bin+".disabled", bin); err != nil {
		t.Fatal(err)
	}
	bindings := bindCLITool(t, h.m, h.room.RoomIdentity, member, h.c, "lark", 0)
	for _, binding := range bindings.Bindings {
		if binding.ActionAuthorization != nil {
			t.Fatal("read-only binding gained write capabilities")
		}
	}
	if e = cli([]string{"lark", "read", "--object", object, "--target", object}, &content); e != nil || content.Status != platform.ResolutionResolved || len(content.Contents) != 1 || !strings.Contains(content.Contents[0].Body, "脱敏测试消息") || !strings.Contains(content.Contents[0].Body, "发送者：测试发送者（open_id: ou_sender") {
		t.Fatal(content, e)
	}
	if len(h.state().Inbox) != 0 {
		t.Fatal("source ack missing")
	}
	again, e := h.svc.store.readFollowup(id, object, unit)
	if e != nil || again.ObjectRef != object || again.Ref != unit {
		t.Fatal("stable reference changed", e)
	}
	// The normal Room question path works without granting a Lark action.
	var question agentFollowupReceipt
	change := followupQuestionChange{followupCommand: followupCommand{ObjectRef: object, UnitRef: unit, Revision: u.Revision, TurnNumber: 1}, Text: "@operator 请确认如何处理这条脱敏消息"}
	if e = cli(entryChange(t, "matters", "notify", change), &question); e != nil {
		t.Fatal(e)
	}
	saved, _ := h.svc.store.LoadRoom()
	if saved.LatestSeq == 0 || !strings.Contains(saved.Messages[len(saved.Messages)-1].Text, "请确认") {
		t.Fatal("question not in Room")
	}
	if caps, _ := h.m.referenceCapabilities("lark"); caps.Read == nil || len(caps.Actions) != 3 || caps.Actions[platform.OperationMessageReply].Spec.DisabledReason != "" || caps.Actions[platform.OperationMessageSend].Spec.DisabledReason != "" || h.m.referenceExecutors["lark"] == nil {
		t.Fatal("accepted capability registration missing")
	}
	// Another member cannot borrow the source account to read a saved target.
	other := larkReadRequest{ServiceInfo: h.svc.info, Caller: agentCaller(h.members[1].AgentID, h.members[1].MembershipID), Target: &larkTargetRequest{ObjectRef: object, TargetRef: object, Scope: "content"}}
	if w := managerHTTP(h.m, "/v1/platform/lark/target", other); w.Code == 200 {
		t.Fatal("read crossed Membership")
	}
}

func TestLarkNotificationsRestartImmutableTargetAndDeliveryCapacity(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.messages(2)
	h.start()
	old := h.state()
	if len(old.Inbox) != 2 {
		t.Fatal("missing pending")
	}
	cfg := old.Config
	cfg.Enabled = false
	h.configure(cfg)
	if e := h.deliver(); !errors.Is(e, notification.ErrIdentity) {
		t.Fatal(e)
	}
	if len(h.state().Inbox) != 2 {
		t.Fatal("pause discarded backlog")
	}
	cfg.Enabled = true
	cfg.Target = notification.Target{Mode: "inbox", AgentID: h.members[1].AgentID, MembershipID: h.members[1].MembershipID}
	paused := h.state()
	if _, err := h.m.configureLarkSubscription(t.Context(), paused.Metadata().Key, paused.SubscriptionVersion, cfg); !errors.Is(err, ErrIdentity) {
		t.Fatal("existing subscription changed receiver", err)
	}
	cfg.Target = old.Config.Target
	next := h.configure(cfg)
	if next.Baseline != old.Baseline || next.SubscriptionID != old.SubscriptionID {
		t.Fatal("switch reset baseline")
	}
	for _, e := range next.Inbox {
		if e.Item.Route.Target != old.Config.Target {
			t.Fatal("pending rerouted")
		}
	}
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	limits := DefaultInboxLimits()
	limits.Entries = 1
	addr, e := SpawnAgentInbox(h.svc.rt, h.svc.store, id, limits)
	if e != nil {
		t.Fatal(e)
	}
	h.svc.inboxes[id.MembershipID] = addr
	if e = h.deliver(); e != nil {
		t.Fatal(e)
	}
	if e = h.deliver(); !errors.Is(e, notification.ErrCapacity) {
		t.Fatal(e)
	}
	if h.state().DeliveryErrorCode != "inbox_capacity" || len(h.state().Inbox) != 1 {
		t.Fatal("capacity lost pending")
	}
	root := h.m.store.dir
	if e = h.m.close(); e != nil {
		t.Fatal(e)
	}
	h.m = testManager(t, root, managerTestProfiles(t))
	h.m.notifications.Clock = h.clock.get
	h.svc = eventLiveService(t, &eventHarness{m: h.m, room: h.room})
	if len(h.state().Inbox) != 1 || h.state().Baseline != old.Baseline {
		t.Fatal("restart lost source")
	}
	if e = h.deliver(); !errors.Is(e, notification.ErrCapacity) {
		t.Fatal(e)
	}
	if inboxRoutingPreview(t, h.svc, id).SnapshotSeq != 1 || len(h.state().Inbox) != 1 {
		t.Fatal("restart lost full Inbox backlog")
	}
}

func TestLarkNotificationsAckCrashReusesDeliveryID(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	old := h.state()
	member := h.members[0]
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	original := h.m.store.ops.createTemp
	h.m.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("source ack failure") }
	if err := h.deliver(); err == nil {
		t.Fatal("ack failure hidden")
	}
	h.m.store.ops.createTemp = original
	if len(h.state().Inbox) != 1 || inboxRoutingPreview(t, h.svc, id).SnapshotSeq != 1 {
		t.Fatal("handoff lost")
	}
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	h.m = testManager(t, root, managerTestProfiles(t))
	h.m.notifications.Clock = h.clock.get
	h.svc = eventLiveService(t, &eventHarness{m: h.m, room: h.room})
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	if len(h.state().Inbox) != 0 || inboxRoutingPreview(t, h.svc, id).SnapshotSeq != 1 || h.state().SubscriptionID != old.SubscriptionID {
		t.Fatal("crash duplicated delivery")
	}
}

func TestLarkAdditionalPlatformRetainsOldSource(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	old := h.state()
	c := platformTestSave(t, h.m, managedPlatformRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "另一个显式连接", Provider: "lark", URL: h.c.URL, CLIProfile: h.c.CLIProfile})
	c, e := h.m.verifyPlatform(context.Background(), managedPlatformRequest{ID: c.ID, Version: c.Version})
	if e != nil || c.Validation.Status != "valid" {
		t.Fatal(e)
	}
	cfg := old.Config
	cfg.PlatformID = c.ID
	bad := cfg
	bad.Private.ChatIDs = []string{"invalid"}
	if _, e = h.m.configureLarkSubscription(context.Background(), old.Metadata().Key, old.SubscriptionVersion, bad); !errors.Is(e, ErrRequest) {
		t.Fatal(e)
	}
	if h.state().Retired || h.state().SubscriptionID != old.SubscriptionID {
		t.Fatal("invalid change retired source")
	}
	h.clock.add(time.Minute)
	if _, e := h.m.configureLarkSubscription(t.Context(), old.Metadata().Key, old.SubscriptionVersion, cfg); !errors.Is(e, ErrIdentity) {
		t.Fatal("moved existing subscription", e)
	}
	next, e := h.m.configureLarkSubscription(t.Context(), notification.Key{Owner: h.room.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg)
	if e != nil || next.SubscriptionID == old.SubscriptionID || len(next.Inbox) != 0 || !next.Baseline.After(old.Baseline) {
		t.Fatal("additional platform adopted old progress", e)
	}
	h.m.mu.Lock()
	stored, ok := h.m.notifications.StateLocked(old.Metadata().Key)
	history, e := h.m.retiredNotificationViewsLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	if !ok || !reflect.DeepEqual(stored.(lark.State), old) || e != nil || len(history) != 0 {
		t.Fatal("old platform changed", e)
	}

}

func TestLarkBackgroundPollingDeliversWithoutEnablingMember(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.messages(2)
	h.start()
	if e := h.m.notifications.Start(); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		s := h.state()
		if len(s.Inbox) == 0 && len(s.Observed) == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("background handoff incomplete")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if e := h.m.notifications.Stop(ctx); e != nil {
		t.Fatal(e)
	}
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	if inboxRoutingPreview(t, h.svc, id).SnapshotSeq != 2 {
		t.Fatal("background delivery missing")
	}
	snapshot, e := h.svc.store.LoadRoom()
	if e != nil || snapshot.LatestSeq != 0 {
		t.Fatal("background source posted raw messages to Room", e)
	}
}

func TestLarkAllPrivateSearchPersistsAndProjectsSafeDiagnostics(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	// Directory discovery is irrelevant to search, including an unreadable page.
	if err := os.WriteFile(filepath.Join(h.dir, "directory.json"), []byte(`{"invalid":"directory fixture must not be used"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := lark.Config{Enabled: true, PlatformID: h.c.ID, Private: lark.PrivateConfig{Enabled: true, Scope: "all"}, Target: notification.Target{Mode: "inbox", AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}}
	s := h.configure(cfg)
	h.clock.add(3 * time.Second)
	stepNotificationCollector(t, h.m, s.Metadata().Key, 2)
	s = h.state()
	if s.Directory != nil || s.Search == nil || s.Search.Branches[0].Scan == nil || s.Search.Branches[0].Scan.Page == nil {
		t.Fatal("search page checkpoint missing")
	}
	h.m.mu.Lock()
	view, err := h.m.larkNotificationViewLocked(h.room.RoomIdentity, &s)
	h.m.mu.Unlock()
	encoded, _ := json.Marshal(view)
	if err != nil || view.Lark.Search == nil || view.Lark.DirectoryScanning || strings.Contains(string(encoded), `"profile"`) || strings.Contains(string(encoded), "page_token") {
		t.Fatal("inaccurate or sensitive diagnostics", err)
	}
	root := h.m.store.dir
	if err = h.m.close(); err != nil {
		t.Fatal(err)
	}
	h.m = testManager(t, root, managerTestProfiles(t))
	h.m.notifications.Clock = h.clock.get
	h.svc = eventLiveService(t, &eventHarness{m: h.m, room: h.room})
	restored := h.state()
	if !reflect.DeepEqual(restored.Search, s.Search) || restored.Baseline != s.Baseline {
		t.Fatal("restart lost search page checkpoint")
	}
	stepNotificationCollector(t, h.m, s.Metadata().Key, 10)
	s = h.state()
	if s.LastFull.IsZero() || len(s.Inbox) != 1 || s.Directory != nil {
		t.Fatal("search resume failed")
	}
	if err = h.deliver(); err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	if inboxRoutingPreview(t, h.svc, id).SnapshotSeq != 1 {
		t.Fatal("all-private search did not reach Inbox")
	}
}
