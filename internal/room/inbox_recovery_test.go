package room

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform"
)

func stalledInboxAgent(t *testing.T, h *inboxHarness, b Backend, clock *atomic.Int64) *MemberAgent {
	t.Helper()
	state := AgentSnapshot{Version: formatVersion, MemberIdentity: h.id, TurnNumber: 1, LastOfferedInboxSeq: 25,
		Turn: &TurnRecord{Number: 1, Status: "completed", InboxSnapshotSeq: 25}}
	if err := h.store.SaveAgent(state); err != nil {
		t.Fatal(err)
	}
	return &MemberAgent{Options: SessionOptions{MemberIdentity: h.id}, Backend: b, Store: h.store, Room: h.roomHarness.addr, Restored: &state,
		PersistentScheduling: true, RestoredTurnsUsed: 1, MaxTurns: 16, now: func() time.Time { return time.Unix(0, clock.Load()).UTC() },
		Inbox: func(ctx context.Context) (InboxStatus, error) {
			return h.addr.Call[InboxStatus](ctx, InspectInbox{h.id})
		}}
}

func TestInboxRecoveryHistoricalStallDrainsPagesWithoutChat(t *testing.T) {
	// The deadline also covers 61 durable seed writes before recovery starts.
	h, _, access, token := mcpInboxFixtureWithTimeout(t, 30*time.Second)
	for n := 1; n <= 61; n++ {
		receiveReference(t, h, fmt.Sprint(n), referenceTestSource(fmt.Sprint(n)))
	}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	b := newControlledBackend()
	m := stalledInboxAgent(t, h, b, &clock)
	// Production may have completed a later Room-only turn after offering the
	// stalled Inbox. Its empty turn range must not hide LastOfferedInboxSeq.
	roomCall[PostReceipt](t, h.roomHarness, h.post(humanCaller, "later-room-input", "Later Room-only input"))
	caller := agentCallerIdentity(h.id)
	read := roomCall[ReadPage](t, h.roomHarness, ReadMessages{RoomIdentity: h.id.RoomIdentity, Caller: caller, Mode: ReadUnread})
	roomCall[ReadConfirmation](t, h.roomHarness, h.confirm(caller, read.ReadReceipt))
	m.Restored.LastOfferedUnreadSeq = 1
	m.Restored.Turn.SnapshotSeq, m.Restored.Turn.OfferedUnreadSeq = 1, 1
	m.Restored.Turn.InboxSnapshotSeq = 0
	if err := h.store.SaveAgent(*m.Restored); err != nil {
		t.Fatal(err)
	}
	addr, err := h.spawn(m)
	if err != nil {
		t.Fatal(err)
	}
	controlMember(t, h.roomHarness, addr, h.id, "run", UnreadChanged{MemberIdentity: h.id}, false, false)
	waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool { return s.InboxRecovery != nil })
	clock.Add(int64(15 * time.Second))
	for turn := 2; turn <= 4; turn++ {
		input := awaitTurn(t, h.roomHarness, b)
		if input.UnreadCount != 0 || input.Inbox == nil || input.Inbox.Through != uint64((turn-2)*25) || input.Inbox.SnapshotSeq != min(uint64((turn-1)*25), 61) || input.Inbox.Recovery != (turn == 2) {
			t.Fatal("wrong input", input)
		}
		cursor := ""
		pages := 0
		for {
			_, view := mustInboxCall(t, access, token, turn, cursor, 7)
			pages++
			if !view.HasMore {
				break
			}
			cursor = view.Next
		}
		if turn < 4 && pages != 4 {
			t.Fatal("expected multiple formal pages", pages)
		}
		b.finish <- nil
		waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool {
			return s.Agent.TurnNumber >= uint64(turn) && s.Inbox != nil && s.Inbox.ConsumedThrough >= input.Inbox.SnapshotSeq
		})
	}
	status := waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool { return !s.Active && s.InboxRecovery == nil && s.Inbox.ConsumedThrough == 61 })
	if status.Agent.TurnNumber != 4 || status.Error != "" || status.TurnBudget.Used != 4 {
		t.Fatal(status)
	}
}

func TestInboxRecoveryBackoffExhaustionAndExplicitContinue(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	for n := 1; n <= 26; n++ {
		inboxCall[InboxReceipt](t, h, h.receive(fmt.Sprint(n)))
	}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	b := newControlledBackend()
	m := stalledInboxAgent(t, h, b, &clock)
	addr, err := h.spawn(m)
	if err != nil {
		t.Fatal(err)
	}
	controlMember(t, h.roomHarness, addr, h.id, "run", UnreadChanged{MemberIdentity: h.id}, false, false)
	for n, delay := range inboxRecoveryBackoff {
		waiting := waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool {
			return !s.Active && s.InboxRecovery != nil && s.InboxRecovery.Attempts == n && !s.InboxRecovery.NextAt.IsZero()
		})
		if waiting.InboxRecovery.NextAt.Sub(time.Unix(0, clock.Load())) != delay || waiting.Status != "inbox_waiting" {
			t.Fatal(waiting)
		}
		clock.Add(int64(delay - time.Second))
		// A later arrival must not renew the range or shorten the backoff.
		inboxCall[InboxReceipt](t, h, h.receive(fmt.Sprint(27+n)))
		time.Sleep(80 * time.Millisecond)
		if s := inspectMember(t, h.ctx, addr, h.id); s.Active || s.Agent.TurnNumber != uint64(n+1) {
			t.Fatal("early recovery", s)
		}
		clock.Add(int64(time.Second))
		input := awaitTurn(t, h.roomHarness, b)
		if input.Inbox == nil || !input.Inbox.Recovery || input.Inbox.SnapshotSeq != 25 {
			t.Fatal(input)
		}
		b.finish <- nil
	}
	exhausted := waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool { return !s.Active && s.Status == "inbox_stalled" })
	if exhausted.InboxRecovery.Attempts != 3 || exhausted.Notice.Code != "inbox_recovery_exhausted" {
		t.Fatal(exhausted)
	}
	clock.Add(int64(time.Hour))
	inboxCall[InboxReceipt](t, h, h.receive("new-after-exhaustion"))
	time.Sleep(100 * time.Millisecond)
	if s := inspectMember(t, h.ctx, addr, h.id); s.Active || s.Agent.TurnNumber != 4 {
		t.Fatal("exhaustion bypassed", s)
	}
	saved, err := h.store.loadScheduling(h.id)
	if err != nil || saved.InboxRecovery.Attempts != 3 || saved.Used != 4 {
		t.Fatal(saved, err)
	}
	// Startup loads the same scheduling file, independently of Session memory.
	restored := &MemberAgent{Options: adapterOptions(h.store, h.id), Backend: dshBackend{store: h.store}, Store: h.store, turnLimit: 16}
	if err := restored.initializeScheduling(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.inboxRecovery, saved.InboxRecovery) {
		t.Fatal("startup renewed recovery allowance")
	}
	// A generation switch updates only the baseline, preserving recovery fields.
	restored.sessionGeneration = "session.json.dsh-" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := restored.persistScheduling(saved.Enabled, saved.Used); err != nil {
		t.Fatal(err)
	}
	persisted, err := h.store.loadScheduling(h.id)
	if err != nil || !reflect.DeepEqual(persisted.InboxRecovery, saved.InboxRecovery) {
		t.Fatal(persisted, err)
	}
	if _, err = addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: h.id, Action: "inbox_continue"}); err != nil {
		t.Fatal(err)
	}
	input := awaitTurn(t, h.roomHarness, b)
	if !input.Inbox.Recovery {
		t.Fatal(input)
	}
	after, err := h.store.loadScheduling(h.id)
	if err != nil || after.InboxContinueCount != 1 || after.InboxContinuedAt.IsZero() || after.Limit != saved.Limit || after.Used != 5 {
		t.Fatal(after, err)
	}
	b.finish <- nil
}

func TestInboxRecoveryPauseCancelFaultAndBudgetGates(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	for n := 1; n <= 25; n++ {
		inboxCall[InboxReceipt](t, h, h.receive(fmt.Sprint(n)))
	}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	m := stalledInboxAgent(t, h, newControlledBackend(), &clock)
	m.state = *m.Restored
	m.now = func() time.Time { return time.Unix(0, clock.Load()) }
	m.turnLimit = 16
	status := InboxStatus{LatestSeq: 26}
	m.inbox = &status
	m.reconcileInboxRecovery(status)
	clock.Add(int64(time.Hour))
	if !m.inboxReady() {
		t.Fatal("not armed")
	}
	for _, tc := range []struct {
		name  string
		apply func()
		clear func()
		want  error
	}{
		{"paused", func() { m.paused = true }, func() { m.paused = false }, ErrBusy},
		{"active", func() { m.active = true }, func() { m.active = false }, ErrBusy},
		{"checkpoint", func() { m.fault = true }, func() { m.fault = false }, ErrBusy},
		{"budget", func() { m.turnsUsed = 16 }, func() { m.turnsUsed = 0 }, ErrTurnBudget},
		{"unknown", func() { m.inboxRecoveryBlock = "unknown action outcomes require inspection" }, func() { m.inboxRecoveryBlock = "" }, ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.apply()
			if err := m.continueInbox(); !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
			tc.clear()
		})
	}
	// Failed/interrupted business never schedules automatically. A settled,
	// explicit continue is allowed after pause/model/unknown checks above.
	for _, terminal := range []string{"failed", "interrupted"} {
		m.state.Turn.Status = terminal
		m.inboxContinuePending = false
		if m.inboxReady() {
			t.Fatal("failed business auto retried")
		}
		if err := m.continueInbox(); err != nil || !m.inboxReady() {
			t.Fatal("explicit continuation unavailable", err)
		}
	}
	m.state.Turn.Status = "completed"
	// Expiry and later arrivals are not consumption progress and cannot reset attempts.
	m.inboxRecovery.Attempts = 3
	status.LatestSeq = 70
	status.ExpiredThrough = 25
	m.reconcileInboxRecovery(status)
	if m.inboxRecovery == nil || m.inboxRecovery.Attempts != 3 || m.inboxReady() {
		t.Fatal("expiry renewed allowance")
	}
	status.ConsumedThrough = 25
	m.reconcileInboxRecovery(status)
	if m.inboxRecovery != nil {
		t.Fatal("formal progress failed to reset allowance")
	}
}

func TestInboxRecoveryConfirmedReadThenBusinessFailureDoesNotStall(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	for n := 1; n <= 25; n++ {
		inboxCall[InboxReceipt](t, h, h.receive(fmt.Sprint(n)))
	}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	m := stalledInboxAgent(t, h, newControlledBackend(), &clock)
	m.state = *m.Restored
	m.turnLimit = 16
	m.reconcileInboxRecovery(InboxStatus{LatestSeq: 25})
	m.state.Turn.Status = "failed"
	m.reconcileInboxRecovery(InboxStatus{LatestSeq: 25, ConsumedThrough: 25})
	if m.inboxRecovery != nil {
		t.Fatal("business failure rolled back progress")
	}
}

func TestInboxRecoverySurvivesAutomaticSessionRotation(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	for n := 1; n <= 25; n++ {
		inboxCall[InboxReceipt](t, h, h.receive(fmt.Sprint(n)))
	}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	svc := referenceTestService(h)
	svc.ctx = h.ctx
	svc.workspaceOperationID = "fixture"
	svc.targets = map[string]actorx.Addr{}
	list := roomCall[MemberList](t, h.roomHarness, ListMembers{h.id.RoomIdentity})
	svc.dshProfiles = map[string]dshProfile{list.Members[0].Runner.Profile: {}}
	backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) { return dsh.TurnResult{}, nil }}, nil
	}}
	member := stalledInboxAgent(t, h, backend, &clock)
	member.Options = adapterOptions(h.store, h.id)
	member.SessionMaxTurns = 1
	member.RequestRotation = svc.requestMemberRotation
	generation, _ := backend.sessionGeneration(member.Options)
	if err := h.store.saveScheduling(memberScheduling{Version: schedulingVersion, MemberIdentity: h.id, Enabled: true, Used: 1, SessionGeneration: generation}); err != nil {
		t.Fatal(err)
	}
	addr, err := h.spawn(member)
	if err != nil {
		t.Fatal(err)
	}
	svc.targets[h.id.MembershipID] = addr
	controlMember(t, h.roomHarness, addr, h.id, "run", UnreadChanged{MemberIdentity: h.id}, false, false)
	var since time.Time
	for attempt, delay := range inboxRecoveryBackoff {
		waiting := waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool {
			return !s.Active && s.InboxRecovery != nil && s.InboxRecovery.Attempts == attempt && !s.InboxRecovery.NextAt.IsZero()
		})
		if attempt == 0 {
			since = waiting.InboxRecovery.Since
		}
		clock.Add(int64(delay))
		done := waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool {
			return !s.Active && s.Agent.TurnNumber == uint64(attempt+2) && s.InboxRecovery != nil && s.InboxRecovery.Attempts == attempt+1
		})
		if done.SessionGeneration == generation || done.Rotation.Status != "completed" || done.InboxRecovery.Since != since {
			t.Fatal("rotation reset recovery", done)
		}
		generation = done.SessionGeneration
	}
	final := inspectMember(t, h.ctx, addr, h.id)
	if final.Status != "inbox_stalled" || final.SessionTurns.Total != 4 {
		t.Fatal(final)
	}
}

func TestInboxRecoveryUnknownActionBlocksAutomaticAndManualReading(t *testing.T) {
	f := referenceExecuteTest(t, "discussion")
	f.result = func(int) platform.TargetActionResult { return f.outcome("unknown", "never") }
	if _, err := f.run(); err != nil {
		t.Fatal(err)
	}
	agent, err := f.h.store.LoadAgent(f.h.id)
	if err != nil {
		t.Fatal(err)
	}
	agent.Turn.Status = "completed"
	agent.Turn.InboxSnapshotSeq, agent.Turn.InboxStart = 0, 0
	agent.LastOfferedUnreadSeq, agent.Turn.OfferedUnreadSeq = 1, 1
	agent.Turn.SnapshotSeq = max(agent.Turn.SnapshotSeq, 1)
	if err = f.h.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	svc := referenceTestService(f.h)
	svc.workspaceOperationID = "fixture"
	status, err := svc.memberInboxStatus(f.h.ctx, f.h.id)
	if err != nil || status.RecoveryBlocked == "" {
		t.Fatal("unknown action not observed", status, err)
	}
	m := &MemberAgent{state: agent, Options: SessionOptions{MemberIdentity: f.h.id}, Store: f.h.store, inbox: &status, now: time.Now}
	m.reconcileInboxRecovery(status)
	if m.inboxReady() || m.inboxRecovery == nil {
		t.Fatal("unknown outcome scheduled recovery")
	}
	m.turnLimit = 16
	if err = m.continueInbox(); !errors.Is(err, ErrConflict) {
		t.Fatal("manual recovery bypassed unknown", err)
	}
}

func TestInboxRecoveryCancellationNeedsExplicitContinue(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	for n := 1; n <= 25; n++ {
		inboxCall[InboxReceipt](t, h, h.receive(fmt.Sprint(n)))
	}
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	backend := newControlledBackend()
	m := stalledInboxAgent(t, h, backend, &clock)
	addr, err := h.spawn(m)
	if err != nil {
		t.Fatal(err)
	}
	controlMember(t, h.roomHarness, addr, h.id, "run", UnreadChanged{MemberIdentity: h.id}, false, false)
	waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool { return s.InboxRecovery != nil })
	clock.Add(int64(15 * time.Second))
	awaitTurn(t, h.roomHarness, backend)
	running := inspectMember(t, h.ctx, addr, h.id)
	if _, err = addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: h.id, Action: "cancel", ExecutionID: running.ExecutionID}); err != nil {
		t.Fatal(err)
	}
	<-backend.canceled
	stopped := waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool { return !s.Active && s.Agent.Turn.Status == "interrupted" })
	clock.Add(int64(time.Hour))
	time.Sleep(60 * time.Millisecond)
	if current := inspectMember(t, h.ctx, addr, h.id); current.Active || current.Agent.TurnNumber != stopped.Agent.TurnNumber {
		t.Fatal("cancellation auto retried", current)
	}
	if _, err = addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: h.id, Action: "inbox_continue"}); err != nil {
		t.Fatal(err)
	}
	input := awaitTurn(t, h.roomHarness, backend)
	if input.Inbox == nil || !input.Inbox.Recovery {
		t.Fatal(input)
	}
	backend.finish <- nil
}
