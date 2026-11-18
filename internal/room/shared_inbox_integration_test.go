//go:build integration || e2e

package room

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func sharedEventFixtureAt(t *testing.T, root string) (*eventHarness, *service, gitlab.EventsState, []MemberIdentity) {
	t.Helper()
	h, connection := independentEventHarnessAt(t, root)
	h.m.config.unifiedInputs = true
	svc := eventLiveService(t, h)
	h.m.bindPlatformReferences(svc)
	list, err := svc.room.Call[MemberList](t.Context(), ListMembers{h.room.RoomIdentity})
	if err != nil || len(list.Members) < 2 {
		t.Fatal(list, err)
	}
	members := []MemberIdentity{}
	for _, member := range list.Members[:2] {
		members = append(members, MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID})
	}
	if _, err := svc.configureSharedMembers(t.Context(), 0, members); err != nil {
		t.Fatal(err)
	}
	target := notification.Target{Mode: "shared_inbox", InboxID: sharedProjectInbox}
	cfg := gitlab.NotificationConfig{Enabled: true, PlatformID: connection.ID, Target: target, Events: gitlab.EventSettings{Enabled: true, Project: "remote/project"}}
	if _, err := h.m.configureGitLabNotifications(t.Context(), h.room.RoomIdentity, 0, cfg); err != nil {
		t.Fatal(err)
	}
	current, err := h.m.notifications.CurrentLocked(h.room.RoomIdentity, gitlab.EventsSourceID, connection.ID, target)
	if err != nil || current == nil {
		t.Fatal(err)
	}
	previous := current.(gitlab.EventsState)
	next := previous.Clone()
	next.ProjectID = 7
	if _, _, err := next.Accept([]gitlab.Event{h.event(99, 0)}, h.clock.get()); err != nil {
		t.Fatal(err)
	}
	if err := h.m.notifications.SaveFrom(next, previous); err != nil {
		t.Fatal(err)
	}
	return h, svc, next, members
}
func sharedServiceReader(t *testing.T, s *service, id MemberIdentity, turn uint64) *inputSession {
	t.Helper()
	agent, err := s.store.LoadAgent(id)
	if errors.Is(err, os.ErrNotExist) {
		agent = AgentSnapshot{Version: formatVersion, MemberIdentity: id, InputStateRef: "input-state.json"}
		err = nil
	}
	if err != nil {
		t.Fatal(err)
	}
	agent.TurnNumber = turn
	agent.Turn = &TurnRecord{Number: turn, Status: "running"}
	if err := s.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	p, err := s.inputStorage(id)
	if err != nil {
		t.Fatal(err)
	}
	session, err := newInputSession(t.Context(), s, p, "shared-service-session", turn, false, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	return session
}
func TestSharedInboxSourceAcknowledgementAndNormalRestart(t *testing.T) {
	h, s, source, members := sharedEventFixtureAt(t, t.TempDir())
	if err := h.m.notifications.Deliver(t.Context(), source.Metadata().Key, source.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	current, _ := h.m.notifications.StateLocked(source.Metadata().Key)
	if current.Metadata().Pending != 0 {
		t.Fatal("source not acknowledged")
	}
	a := sharedServiceReader(t, s, members[0], 1)
	ref := sharedRef(t, a)
	_, confirm, err := a.readPage(t.Context(), inputstream.ReadRequest{Turn: 1, Stream: ref, ID: "first"}, a.renderInputPage)
	if err != nil {
		t.Fatal(err)
	}
	if err := confirm(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := s.store.sharedHandle.Load().view.Load()
	g, _ := before.grant(inputMember(members[0]))
	progress, err := inputstream.LoadSharedConsumption(s.store.dir, inputMember(members[0]), before.root.Inbox, g.ID)
	if err != nil || len(progress.Through) != 1 {
		t.Fatal(progress, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := s.close(ctx); err != nil {
		t.Fatal(err)
	}
	s = eventLiveService(t, h)
	h.m.bindPlatformReferences(s)
	after := s.store.sharedHandle.Load().view.Load()
	if len(after.records) != 1 || after.snapshot.Partition != before.snapshot.Partition {
		t.Fatal(after)
	}
	a = sharedServiceReader(t, s, members[0], 1)
	b := sharedServiceReader(t, s, members[1], 1)
	c, err := a.catalog(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	d, _ := c.ReadableList(inputstream.ListRequest{})
	for _, stream := range d.Streams {
		if stream.Kind == "shared" {
			t.Fatal("confirmed shared input replayed", d)
		}
	}
	if sharedRef(t, b) == ref {
		t.Fatal("member scope missing")
	}
	if err := s.closeSharedMember(t.Context(), members[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.close(ctx); err != nil {
		t.Fatal(err)
	}
	s = eventLiveService(t, h)
	h.m.bindPlatformReferences(s)
	for _, grant := range s.store.sharedHandle.Load().view.Load().root.Grants {
		if grant.Member == inputMember(members[0]) && grant.Status != "closed" {
			t.Fatal(grant)
		}
	}
	b = sharedServiceReader(t, s, members[1], 1)
	if _, err := b.catalog(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestSharedInboxReceiptCrashRecovery(t *testing.T) {
	const key = "TEAMX_SHARED_RECEIPT_ROOT"
	if root := os.Getenv(key); root != "" {
		h, _, source, _ := sharedEventFixtureAt(t, root)
		create := h.m.store.ops.createTemp
		h.m.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
			if dir == filepath.Join(h.m.roomDir(h.room.RoomID), "notifications") {
				os.Exit(86)
			}
			return create(dir, pattern)
		}
		_ = h.m.notifications.Deliver(t.Context(), source.Metadata().Key, source.SubscriptionVersion)
		t.Fatal("crash boundary not reached")
	}
	root := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), binary, "-test.run=^TestSharedInboxReceiptCrashRecovery$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), key+"="+root)
	output, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 86 {
		t.Fatal(err, string(output))
	}
	m := testManager(t, root, managerTestProfiles(t))
	m.config.unifiedInputs = true
	m.notifications.Clock = func() time.Time { return time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC) }
	room := m.index.Rooms[0].roomSummary
	var source gitlab.EventsState
	for _, s := range m.notifications.StatesLocked(room.RoomIdentity) {
		if e, ok := s.(gitlab.EventsState); ok && e.Config.Target.Mode == "shared_inbox" {
			source = e
		}
	}
	if source.Metadata().Pending != 1 {
		t.Fatal(source.Metadata())
	}
	h := &eventHarness{t: t, m: m, room: room}
	svc := eventLiveService(t, h)
	m.bindPlatformReferences(svc)
	p := svc.store.sharedHandle.Load()
	before := p.view.Load()
	if len(before.records) != 1 || len(before.records[1].Recipients) != 2 {
		t.Fatal(before)
	}
	if err := m.notifications.Deliver(t.Context(), source.Metadata().Key, source.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	current, _ := m.notifications.StateLocked(source.Metadata().Key)
	if current.Metadata().Pending != 0 || len(p.view.Load().records) != 1 {
		t.Fatal("redelivery duplicated body or missed source acknowledgement")
	}
	t.Log("fresh process owners recovered one shared body, the original two Grants and one source acknowledgement")
}

func TestSharedInboxDefaultExpirySkipsUnreadMembersWithoutPages(t *testing.T) {
	h, s, source, members := sharedEventFixtureAt(t, t.TempDir())
	if err := h.m.notifications.Deliver(t.Context(), source.Metadata().Key, source.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	p := s.store.sharedHandle.Load()
	before := p.view.Load()
	if len(before.root.Pages) != 0 {
		t.Fatal("fixture unexpectedly has a formal page")
	}
	h.clock.add(4 * 24 * time.Hour)
	if err := maintainSharedAndWait(t.Context(), h.m, s); err != nil {
		t.Fatal(err)
	}
	if len(p.view.Load().records) != 0 || p.view.Load().snapshot.Reclaimed != 1 {
		for _, id := range members {
			g, _ := before.grant(inputMember(id))
			c, err := inputstream.LoadSharedConsumption(s.store.dir, inputMember(id), before.root.Inbox, g.ID)
			t.Log("member", c.Skipped, err)
		}
		t.Fatal("unread expiry did not reclaim shared prefix", p.view.Load().snapshot)
	}
	for _, id := range members {
		g, _ := before.grant(inputMember(id))
		c, err := inputstream.LoadSharedConsumption(s.store.dir, inputMember(id), before.root.Inbox, g.ID)
		if err != nil || c.Skipped[1] != "expired_input" || len(c.Proofs) != 0 {
			t.Fatal(c, err)
		}
	}
}

func TestSharedInboxConsumedMemberReleaseWithoutLoadedOwner(t *testing.T) {
	h, s, source, members := sharedEventFixtureAt(t, t.TempDir())
	s.inboxExpiry = notification.ExpiryPolicy{AfterDays: 3, SkipExpired: false}
	if err := h.m.notifications.Deliver(t.Context(), source.Metadata().Key, source.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	a := sharedServiceReader(t, s, members[0], 1)
	_, confirm, err := a.readPage(t.Context(), inputstream.ReadRequest{Turn: 1, Stream: sharedRef(t, a), ID: "A"}, a.renderInputPage)
	if err != nil {
		t.Fatal(err)
	}
	if err := confirm(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.store.retireMemberInputs(t.Context(), members[0].MembershipID); err != nil {
		t.Fatal(err)
	}
	h.clock.add(4 * 24 * time.Hour)
	if err := maintainSharedAndWait(t.Context(), h.m, s); err != nil {
		t.Fatal(err)
	}
	if len(s.store.sharedHandle.Load().view.Load().records) != 1 {
		t.Fatal("unread member did not hold the shared record")
	}
	b := sharedServiceReader(t, s, members[1], 1)
	_, confirm, err = b.readPage(t.Context(), inputstream.ReadRequest{Turn: 1, Stream: sharedRef(t, b), ID: "B"}, b.renderInputPage)
	if err != nil {
		t.Fatal(err)
	}
	if err := confirm(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := maintainSharedAndWait(t.Context(), h.m, s); err != nil {
		t.Fatal(err)
	}
	if len(s.store.sharedHandle.Load().view.Load().records) != 0 {
		t.Fatal("unloaded confirmed owner could not reconstruct release from its durable ledger and association")
	}
}

func maintainSharedAndWait(ctx context.Context, m *roomManager, s *service) error {
	for {
		err := m.maintainSharedInbox(ctx, s)
		if !errors.Is(err, ErrBusy) {
			return err
		}
		p := s.store.sharedHandle.Load()
		p.maintenance.Lock()
		p.maintenance.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}
func TestSharedInboxPeriodicExpiryWithAllGrantsClosed(t *testing.T) {
	h, s, source, members := sharedEventFixtureAt(t, t.TempDir())
	if err := h.m.notifications.Deliver(t.Context(), source.Metadata().Key, source.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	for _, id := range members {
		if err := s.closeSharedMember(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	h.clock.add(4 * 24 * time.Hour)
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if len(s.store.sharedHandle.Load().view.Load().records) == 0 {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("Inbox owner did not reclaim without member or source wakes")
		case <-ticker.C:
		}
	}
}

func TestSharedInboxCapacityPausesSourceAndRetriesOriginalDelivery(t *testing.T) {
	h, s, source, _ := sharedEventFixtureAt(t, t.TempDir())
	p := s.store.sharedHandle.Load()
	create := s.store.ops.createTemp
	s.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		if dir == filepath.Join(s.store.dir, "shared-inboxes", sharedProjectInbox) {
			return nil, &inputstream.CapacityError{Scope: "metadata"}
		}
		return create(dir, pattern)
	}
	err := h.m.notifications.Deliver(t.Context(), source.Metadata().Key, source.SubscriptionVersion)
	if !errors.Is(err, notification.ErrCapacity) {
		t.Fatal("body capacity did not reach source backpressure", err)
	}
	current, _ := h.m.notifications.StateLocked(source.Metadata().Key)
	if current.Metadata().DeliveryError != "inbox_capacity" || current.Metadata().Pending != 1 || len(p.view.Load().records) != 0 {
		t.Fatal("source progressed after capacity rejection", current.Metadata())
	}
	original := source.PendingItems()[0].DeliveryID
	s.store.ops.createTemp = create
	if err := h.m.notifications.Deliver(t.Context(), source.Metadata().Key, source.SubscriptionVersion); err != nil {
		t.Fatal(err)
	}
	current, _ = h.m.notifications.StateLocked(source.Metadata().Key)
	if current.Metadata().Pending != 0 || current.Metadata().DeliveryError != "" || len(p.view.Load().records) != 1 || p.view.Load().records[1].DeliveryID != original {
		t.Fatal("capacity retry lost its original delivery identity", current.Metadata())
	}
}

func TestSharedInboxNoReceiversPausesQueriesButReplaysPublishedReceipt(t *testing.T) {
	h, s, source, members := sharedEventFixtureAt(t, t.TempDir())
	pending := source.PendingItems()[0]
	item, ok := source.Notification(pending.Key)
	if !ok {
		t.Fatal("missing source item")
	}
	if err := s.receiveSharedInbox(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	for _, id := range members {
		if err := s.closeSharedMember(t.Context(), id); err != nil {
			t.Fatal(err)
		}
	}
	runner := h.m.notifications.NewRunnerLocked(source.Metadata().Key)
	_, _ = runner.Step(t.Context())
	current, _ := h.m.notifications.StateLocked(source.Metadata().Key)
	if current.(gitlab.EventsState).Metrics.Calls != source.Metrics.Calls {
		t.Fatal("source queried without active receivers")
	}
	if err := h.m.notifications.Deliver(t.Context(), source.Metadata().Key, source.SubscriptionVersion); err != nil {
		t.Fatal("published receipt could not be acknowledged with all Grants closed", err)
	}
	current, _ = h.m.notifications.StateLocked(source.Metadata().Key)
	if current.Metadata().Pending != 0 || len(s.store.sharedHandle.Load().view.Load().records) != 1 {
		t.Fatal("receipt recovery duplicated or lost body")
	}
}
