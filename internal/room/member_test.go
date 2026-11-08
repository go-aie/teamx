package room

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"
)

type controlledBackend struct {
	opens    atomic.Int32
	closes   atomic.Int32
	entered  chan UnreadChanged
	finish   chan error
	canceled chan struct{}
	settle   chan struct{}
}

func newControlledBackend() *controlledBackend {
	return &controlledBackend{entered: make(chan UnreadChanged, 4), finish: make(chan error, 4), canceled: make(chan struct{}, 4)}
}

func (b *controlledBackend) OpenSession(context.Context, SessionOptions) (Session, error) {
	b.opens.Add(1)
	return &controlledSession{b: b}, nil
}

type controlledSession struct{ b *controlledBackend }

func (s *controlledSession) Run(ctx context.Context, input UnreadChanged) error {
	s.b.entered <- input
	select {
	case err := <-s.b.finish:
		return err
	case <-ctx.Done():
		s.b.canceled <- struct{}{}
		if s.b.settle != nil {
			<-s.b.settle
		}
		return ctx.Err()
	}
}

func (s *controlledSession) Close(context.Context) error { s.b.closes.Add(1); return nil }

func memberHarness(t *testing.T, b Backend, configure func(*Store), tweak ...func(*MemberAgent)) (*roomHarness, actorx.Addr, MemberIdentity) {
	t.Helper()
	h := newRoomHarness(t, historyRoom(1, "body"), func(_ *Room, store *Store) {
		if configure != nil {
			configure(store)
		}
	})
	id := h.memberID("A", "generation-A")
	member := &MemberAgent{Options: SessionOptions{MemberIdentity: id}, Backend: b, Store: h.store, Room: h.addr}
	for _, change := range tweak {
		change(member)
	}
	addr, err := h.spawn(member)
	if err != nil {
		t.Fatal(err)
	}
	return h, addr, id
}

func TestMemberAgentMergeWindow(t *testing.T) {
	b := newControlledBackend()
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	h, addr, id := memberHarness(t, b, nil, func(m *MemberAgent) {
		m.MergeWindow = time.Second
		m.now = func() time.Time { return time.Unix(0, clock.Load()) }
	})
	n := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, addr, id, "run", n, false, false)
	appendMemberInput(t, h, 2, 2)
	n.Revision, n.LatestSeq, n.NewestUnreadSeq, n.UnreadCount = 2, 2, 2, 2
	if err := addr.Send(h.ctx, n); err != nil {
		t.Fatal(err)
	}
	if s := inspectMember(t, h.ctx, addr, id); s.Active || b.opens.Load() != 0 {
		t.Fatal("ran before merge deadline", s)
	}
	clock.Add(int64(time.Second))
	// A duplicate triggers scheduling but must not change the merged snapshot.
	if err := addr.Send(h.ctx, n); err != nil {
		t.Fatal(err)
	}
	if input := awaitTurn(t, h, b); input.NewestUnreadSeq != 2 || input.UnreadCount != 2 {
		t.Fatal(input)
	}
	b.finish <- nil
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
}

func inspectMember(t *testing.T, ctx context.Context, addr actorx.Addr, id MemberIdentity) MemberStatus {
	t.Helper()
	status, err := addr.Call[MemberStatus](ctx, InspectMember{id})
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func waitMember(t *testing.T, ctx context.Context, addr actorx.Addr, id MemberIdentity, done func(MemberStatus) bool) MemberStatus {
	t.Helper()
	for {
		status := inspectMember(t, ctx, addr, id)
		if done(status) {
			return status
		}
		select {
		case <-ctx.Done():
			t.Fatal("member did not settle", status, ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
}

func controlMember(t *testing.T, h *roomHarness, addr actorx.Addr, id MemberIdentity, action string, state UnreadChanged, retry, cancel bool) MemberStatus {
	t.Helper()
	status, err := addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: action, State: state, Retry: retry, Cancel: cancel})
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func awaitTurn(t *testing.T, h *roomHarness, b *controlledBackend) UnreadChanged {
	t.Helper()
	select {
	case input := <-b.entered:
		return input
	case <-h.ctx.Done():
		t.Fatal("turn did not enter", h.ctx.Err())
		return UnreadChanged{}
	}
}

func TestMemberAgentMergesBusyInputAndReusesSession(t *testing.T) {
	b := newControlledBackend()
	h, addr, id := memberHarness(t, b, nil)
	n := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	if err := addr.Send(h.ctx, n); err != nil {
		t.Fatal(err)
	}
	appendMemberInput(t, h, 2, 2)
	n.Revision, n.LatestSeq, n.NewestUnreadSeq, n.UnreadCount = 2, 2, 2, 2
	if err := addr.Send(h.ctx, n); err != nil {
		t.Fatal(err)
	}
	paused := inspectMember(t, h.ctx, addr, id)
	if !paused.Paused || paused.Active || b.opens.Load() != 0 {
		t.Fatal(paused)
	}
	controlMember(t, h, addr, id, "run", n, false, false)
	if input := awaitTurn(t, h, b); input.NewestUnreadSeq != 2 {
		t.Fatal(input)
	}
	start, err := h.store.LoadAgent(id)
	if err != nil || start.Turn.Status != "running" || start.Turn.OfferedUnreadSeq != 2 || start.Turn.StartReadThrough != 0 {
		t.Fatal(start, err)
	}
	for seq := uint64(3); seq <= 8; seq++ {
		appendMemberInput(t, h, seq, seq)
		n.Revision, n.LatestSeq, n.NewestUnreadSeq, n.UnreadCount = seq, seq, seq, int(seq)
		if err := addr.Send(h.ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	old := n
	old.Revision, old.NewestUnreadSeq = 1, 1
	if err := addr.Send(h.ctx, old); err != nil {
		t.Fatal(err)
	}
	busy := inspectMember(t, h.ctx, addr, id)
	if !busy.Active || busy.Notification.NewestUnreadSeq != 8 || b.opens.Load() != 1 {
		t.Fatal(busy)
	}
	b.finish <- nil
	if input := awaitTurn(t, h, b); input.NewestUnreadSeq != 8 || input.UnreadCount != 8 {
		t.Fatal(input)
	}
	b.finish <- nil
	finished := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
	if finished.Agent.LastOfferedUnreadSeq != 8 || b.opens.Load() != 1 {
		t.Fatal(finished, b.opens.Load())
	}
	// Resends, old versions and a cursor-only update cannot create work.
	if err := addr.Send(h.ctx, n); err != nil {
		t.Fatal(err)
	}
	n.Revision, n.UnreadCount, n.NewestUnreadSeq = 9, 0, 0
	if err := addr.Send(h.ctx, n); err != nil {
		t.Fatal(err)
	}
	final := inspectMember(t, h.ctx, addr, id)
	if final.Active || final.Agent.TurnNumber != 2 {
		t.Fatal(final)
	}
	end, err := h.store.LoadAgent(id)
	if err != nil || end.Turn.Status != "completed" || end.TurnNumber != 2 {
		t.Fatal(end, err)
	}
	if err := addr.Stop(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := addr.Wait(h.ctx); err != nil || b.closes.Load() != 1 {
		t.Fatal(err, b.closes.Load())
	}
}

func TestMemberAgentFailureCancelAndExplicitRetry(t *testing.T) {
	b := newControlledBackend()
	b.settle = make(chan struct{})
	h, addr, id := memberHarness(t, b, nil)
	n := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, addr, id, "run", n, false, false)
	awaitTurn(t, h, b)
	b.finish <- errors.New("failed after possible effects")
	failed := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	if failed.Agent.Turn.Status != "failed" {
		t.Fatal(failed)
	}
	if err := addr.Send(h.ctx, n); err != nil {
		t.Fatal(err)
	}
	if s := inspectMember(t, h.ctx, addr, id); s.Active || s.Agent.TurnNumber != 1 {
		t.Fatal(s)
	}
	controlMember(t, h, addr, id, "run", n, true, false)
	awaitTurn(t, h, b)
	controlMember(t, h, addr, id, "pause", n, false, true)
	select {
	case <-b.canceled:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	appendMemberInput(t, h, 2, 8)
	n.Revision, n.LatestSeq, n.NewestUnreadSeq = 8, 8, 8
	if err := addr.Send(h.ctx, n); err != nil {
		t.Fatal(err)
	}
	// Cancellation is only a request. Backend settlement is a separate barrier.
	if _, err := addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "run", State: n, Retry: true}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if s := inspectMember(t, h.ctx, addr, id); !s.Active {
		t.Fatal(s)
	}
	resumed := controlMember(t, h, addr, id, "run", n, false, false)
	if !resumed.Active || resumed.Error != "cancellation requested; waiting for turn settlement" {
		t.Fatal("resume hid an unsettled cancellation", resumed)
	}
	controlMember(t, h, addr, id, "pause", n, false, false)
	close(b.settle)
	interrupted := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active })
	if !interrupted.Paused || interrupted.Agent.Turn.Status != "interrupted" {
		t.Fatal(interrupted)
	}
	controlMember(t, h, addr, id, "run", n, false, false)
	if input := awaitTurn(t, h, b); input.NewestUnreadSeq != 8 {
		t.Fatal(input)
	}
	b.finish <- nil
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 3 })
	if b.opens.Load() != 1 {
		t.Fatal("Session was recreated", b.opens.Load())
	}
}

func TestMemberAgentResumeClearsHistoricalError(t *testing.T) {
	for _, outcome := range []string{"failed", "interrupted"} {
		t.Run(outcome, func(t *testing.T) {
			b := newControlledBackend()
			h, addr, id := memberHarness(t, b, nil)
			n := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
			controlMember(t, h, addr, id, "run", n, false, false)
			awaitTurn(t, h, b)
			// The member read all input before the turn failed or was cancelled.
			caller := agentCaller(id.AgentID, id.MembershipID)
			page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: caller, Mode: ReadUnread})
			confirmed := roomCall[ReadConfirmation](t, h, h.confirm(caller, page.ReadReceipt))
			if outcome == "interrupted" {
				controlMember(t, h, addr, id, "pause", n, false, true)
			} else {
				b.finish <- errors.Join(errDSHPaused, errors.New("backend failed"))
			}
			ended := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active })
			if !ended.Paused || ended.Error == "" || ended.Agent.Turn.Status != outcome {
				t.Fatal(ended)
			}
			n.Revision, n.UnreadCount, n.NewestUnreadSeq = confirmed.Revision, 0, 0
			resumed := controlMember(t, h, addr, id, "run", n, false, false)
			if resumed.Status != "idle" || resumed.Error != "" || resumed.Agent.Turn.Error != ended.Agent.Turn.Error {
				t.Fatal("resume must clear the current error and preserve turn history", resumed)
			}
			saved, err := h.store.LoadAgent(id)
			if err != nil || saved.Turn.Status != outcome || saved.Turn.Error != ended.Agent.Turn.Error {
				t.Fatal("historical failure was lost", saved, err)
			}
		})
	}
}

func TestMemberAgentCheckpointFailureStopsAutomaticWork(t *testing.T) {
	for _, failAt := range []int32{1, 2} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			b := newControlledBackend()
			var saves atomic.Int32
			h, addr, id := memberHarness(t, b, func(store *Store) {
				original := store.ops.createTemp
				store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
					if saves.Add(1) == failAt {
						return nil, errors.New("checkpoint disk failure")
					}
					return original(dir, pattern)
				}
			})
			n := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
			controlMember(t, h, addr, id, "run", n, false, false)
			if failAt == 2 {
				awaitTurn(t, h, b)
				b.finish <- nil
			}
			status := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.Status == "checkpoint_failed" })
			if status.Active || (failAt == 1 && b.opens.Load() != 0) {
				t.Fatal(status)
			}
			n.Revision, n.NewestUnreadSeq, n.LatestSeq = 8, 8, 8
			if err := addr.Send(h.ctx, n); err != nil {
				t.Fatal(err)
			}
			if s := inspectMember(t, h.ctx, addr, id); s.Active {
				t.Fatal(s)
			}
		})
	}
}

func TestMemberAgentDiscardClosesUnadoptedSession(t *testing.T) {
	b := newControlledBackend()
	h, addr, id := memberHarness(t, b, nil)
	n := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, addr, id, "run", n, false, false)
	awaitTurn(t, h, b)
	if err := addr.Stop(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := addr.Wait(h.ctx); err != nil || b.closes.Load() != 1 {
		t.Fatal(err, b.closes.Load())
	}
	checkpoint, err := h.store.LoadAgent(id)
	if err != nil || checkpoint.Turn.Status != "interrupted" {
		t.Fatal(checkpoint, err)
	}
}

// Advance authoritative Room state as well as notifications. Grants must query
// the Room rather than trust a fabricated notification snapshot.
func appendMemberInput(t *testing.T, h *roomHarness, first, last uint64) {
	t.Helper()
	for seq := first; seq <= last; seq++ {
		roomCall[PostReceipt](t, h, h.post(humanCaller, fmt.Sprintf("input-%d", seq), "new input"))
		h.idle(t)
	}
}

func TestMemberAgentBudgetPausesWithoutLosingPendingInput(t *testing.T) {
	for _, cancelTurn := range []bool{false, true} {
		t.Run(map[bool]string{false: "failure", true: "cancellation"}[cancelTurn], func(t *testing.T) {
			b := newControlledBackend()
			h, addr, id := memberHarness(t, b, nil, func(m *MemberAgent) { m.MaxTurns = 1 })
			input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
			controlMember(t, h, addr, id, "run", input, false, false)
			awaitTurn(t, h, b)
			appendMemberInput(t, h, 2, 8)
			input.Revision, input.LatestSeq, input.NewestUnreadSeq, input.UnreadCount = 8, 8, 8, 8
			if err := addr.Send(h.ctx, input); err != nil {
				t.Fatal(err)
			}
			if cancelTurn {
				controlMember(t, h, addr, id, "pause", input, false, true)
			} else {
				b.finish <- errors.New("failure consumes one attempt")
			}
			settled := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active })
			if settled.Status != "budget_exhausted" || !settled.Paused || settled.TurnBudget.Limit != 1 || settled.TurnBudget.Used != 1 ||
				settled.Notification.NewestUnreadSeq != 8 || settled.Agent.LastOfferedUnreadSeq != 1 {
				t.Fatal(settled)
			}
			for _, retry := range []bool{false, true} {
				if _, err := addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "run", State: input, Retry: retry}); !errors.Is(err, ErrTurnBudget) {
					t.Fatal("run renewed exhausted budget", err)
				}
			}
			if err := addr.Send(h.ctx, input); err != nil {
				t.Fatal(err)
			}
			if status := inspectMember(t, h.ctx, addr, id); status.Active || status.Agent.TurnNumber != 1 || b.opens.Load() != 1 {
				t.Fatal(status, b.opens.Load())
			}
		})
	}
}

func TestMemberAgentFailedTaskAdmissionDoesNotConsumeBudget(t *testing.T) {
	h := newRoomHarness(t, historyRoom(8, "body"), nil, actorx.WithTaskLimit(1))
	id := h.memberID("A", "generation-A")
	b := newControlledBackend()
	addr, err := h.spawn(&MemberAgent{Options: SessionOptions{MemberIdentity: id}, Store: h.store, Room: h.addr, Backend: b, MaxTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	input := UnreadChanged{MemberIdentity: id, Revision: 8, LatestSeq: 8, NewestUnreadSeq: 8, UnreadCount: 8}
	controlMember(t, h, addr, id, "run", input, false, false)
	status := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.Error != "" })
	// The member's maintenance task occupies its sole worker slot.
	if status.Active || status.TurnBudget.Used != 0 || status.Error == "" || b.opens.Load() != 0 {
		t.Fatal(status, b.opens.Load())
	}
}
