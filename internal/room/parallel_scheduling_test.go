package room

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-aie/actorx"
)

func TestTurnSchedulerParallelExactOwnershipAndExit(t *testing.T) {
	h := newRoomHarness(t, emptyRoom(), nil, actorx.WithMailboxCapacity(2))
	scheduler, err := h.rt.Spawn(schedulerProbe{&turnScheduler{RoomIdentity: h.id, mode: schedulingParallel}})
	if err != nil {
		t.Fatal(err)
	}
	grants := make(chan turnGranted, 128)
	blocked, unblock := make(chan struct{}), make(chan struct{})
	defer onceRelease(unblock)()
	a, err := h.rt.Spawn(grantSink{grants: grants, blocked: blocked, unblock: unblock})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Send(h.ctx, struct{}{}); err != nil {
		t.Fatal(err)
	}
	<-blocked
	for range 2 {
		if err := a.Send(h.ctx, turnGranted{}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := h.rt.Spawn(grantSink{grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	ra, rb := requestTurn{h.memberID("A", "a"), 1, a}, requestTurn{h.memberID("B", "b"), 1, b}
	ga, gb := turnGranted{ra.MemberIdentity, 1}, turnGranted{rb.MemberIdentity, 1}
	schedulerCall(t, h, scheduler, ra)
	schedulerCall(t, h, scheduler, rb)
	awaitGrant(t, h, grants, gb) // A's full mailbox does not block B.
	schedulerCall(t, h, scheduler, ra)
	newer := ra
	newer.ID = 2
	schedulerCall(t, h, scheduler, newer) // cannot overwrite an active grant
	schedulerCall(t, h, scheduler, cancelTurnRequest{ga})
	schedulerCall(t, h, scheduler, releaseTurn{turnGranted{ra.MemberIdentity, 2}})
	state := schedulerState(t, h, scheduler)
	if len(state.Holders) != 2 || state.Holders[ra.MemberIdentity] != ga {
		t.Fatal(state)
	}
	close(unblock)
	awaitGrant(t, h, grants, ga) // maintenance retries the failed admission
	schedulerCall(t, h, scheduler, releaseTurn{ga})
	schedulerCall(t, h, scheduler, newer)
	schedulerCall(t, h, scheduler, releaseTurn{ga})
	if state := schedulerState(t, h, scheduler); len(state.Holders) != 2 || state.Holders[ra.MemberIdentity].ID != 2 {
		t.Fatal("stale release changed ownership", state)
	}
	_ = a.Stop(nil)
	if _, err := a.Wait(h.ctx); err != nil {
		t.Fatal(err)
	}
	for {
		state = schedulerState(t, h, scheduler)
		if len(state.Holders) == 1 {
			break
		}
		select {
		case <-h.ctx.Done():
			t.Fatal(h.ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	if state.Holders[rb.MemberIdentity] != gb {
		t.Fatal("exit reclaimed another member", state)
	}
	schedulerCall(t, h, scheduler, stopTurnGrants{})
	newer = rb
	newer.ID = 2
	schedulerCall(t, h, scheduler, newer)
	if state := schedulerState(t, h, scheduler); !state.Closed || len(state.Holders) != 1 || state.Holders[rb.MemberIdentity] != gb {
		t.Fatal("quiesce forgot unsettled holder", state)
	}
	schedulerCall(t, h, scheduler, releaseTurn{gb})
	schedulerCall(t, h, scheduler, newer)
	if state := schedulerState(t, h, scheduler); len(state.Holders) != 0 || len(state.Queue) != 0 {
		t.Fatal(state)
	}
}

func TestRoomTurnsParallelOverlapFreshInputAndDynamicMember(t *testing.T) {
	h, targets, ids, b := scheduledMembers(t, schedulingParallel, 3)
	for i := range targets {
		runSerialMember(t, h, targets[i], ids[i])
	}
	for i := range targets {
		awaitTurn(t, h, b[i]) // no backend may finish until all three have entered
		if _, err := targets[i].Call[any](h.ctx, turnGranted{ids[i], 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := targets[i].Call[any](h.ctx, turnGranted{ids[i], 99}); err != nil {
			t.Fatal(err)
		}
		status := inspectMember(t, h.ctx, targets[i], ids[i])
		if !status.Active || status.TurnBudget.Used != 1 || b[i].opens.Load() != 1 {
			t.Fatal("duplicate grant reentered", status)
		}
	}
	// A newly added member can enter while all creation-time members are active.
	id := h.memberID("D", "generation-D")
	backend := newControlledBackend()
	target, err := h.spawn(&MemberAgent{Options: SessionOptions{MemberIdentity: id}, Store: h.store, Room: h.addr, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	roomCall[MemberInfo](t, h, AddMember{MemberIdentity: id, Target: target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}})
	roomCall[PostReceipt](t, h, h.post(humanCaller, "mid-turn", "new input"))
	h.idle(t)
	runSerialMember(t, h, target, id)
	input := awaitTurn(t, h, backend)
	if input.LatestSeq != 2 {
		t.Fatal(input)
	}
	// Another Room still uses FIFO while this Room has four live turns.
	other, ot, oi, ob := serialMembers(t, 2)
	runSerialMember(t, other, ot[0], oi[0])
	awaitTurn(t, other, ob[0])
	runSerialMember(t, other, ot[1], oi[1])
	waitMember(t, other.ctx, ot[1], oi[1], func(s MemberStatus) bool { return s.Status == "waiting" })
	if ob[1].opens.Load() != 0 {
		t.Fatal("parallel mode leaked to another Room")
	}
	ob[0].finish <- nil
	awaitTurn(t, other, ob[1])
	ob[1].finish <- nil
	for i := range targets {
		b[i].finish <- nil
	}
	backend.finish <- nil
	for i := range targets {
		input := awaitTurn(t, h, b[i])
		if input.LatestSeq != 2 || input.NewestUnreadSeq != 2 {
			t.Fatal("lost mid-turn input", input)
		}
		b[i].finish <- nil
	}
}

func TestRoomTurnsParallelMemberSettlementIsIsolated(t *testing.T) {
	for _, action := range []string{"pause", "remove", "rotate", "failure"} {
		t.Run(action, func(t *testing.T) {
			h, targets, ids, b := scheduledMembers(t, schedulingParallel, 3)
			b[0].settle = make(chan struct{})
			defer onceRelease(b[0].settle)()
			for i := range targets {
				runSerialMember(t, h, targets[i], ids[i])
				awaitTurn(t, h, b[i])
			}
			switch action {
			case "remove":
				roomCall[MemberInfo](t, h, RemoveMember{ids[0]})
				_ = targets[0].Stop(nil)
			case "pause":
				controlMember(t, h, targets[0], ids[0], "pause", UnreadChanged{}, false, true)
			case "rotate":
				controlMember(t, h, targets[0], ids[0], "rotate", UnreadChanged{}, false, true)
			case "failure":
				b[0].finish <- errors.New("isolated model failure")
			}
			if action != "failure" {
				select {
				case <-b[0].canceled:
				case <-h.ctx.Done():
					t.Fatal(h.ctx.Err())
				}
			}
			// Another member can finish and run again before A's canceled work settles.
			roomCall[PostReceipt](t, h, h.post(humanCaller, "next", "next input"))
			h.idle(t)
			b[1].finish <- nil
			awaitTurn(t, h, b[1])
			if !inspectMember(t, h.ctx, targets[2], ids[2]).Active {
				t.Fatal("healthy member interrupted")
			}
			if action != "failure" {
				if state := schedulerState(t, h, h.scheduler); len(state.Holders) != 3 {
					t.Fatal("cancel freed unsettled ownership", state)
				}
				close(b[0].settle)
			}
			b[1].finish <- nil
			b[2].finish <- nil
		})
	}
}

func TestServiceShutdownParallelJoinsAllHolders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	svc, err := newFixtureService(t.TempDir(), "parallel-close", serviceConfig{scheduling: schedulingParallel})
	if err != nil {
		t.Fatal(err)
	}
	var backends []*controlledBackend
	defer func() {
		for _, b := range backends {
			onceRelease(b.settle)()
		}
		closeService(t, svc)
	}()
	for i := range 3 {
		id := MemberIdentity{svc.info.RoomIdentity, fmt.Sprint("M", i), fmt.Sprint("generation-", i)}
		b := newControlledBackend()
		b.settle = make(chan struct{})
		backends = append(backends, b)
		target, err := spawnScheduledMember(svc.rt, &MemberAgent{Options: SessionOptions{MemberIdentity: id}, Store: svc.store, Room: svc.room, Backend: b}, svc.scheduler)
		if err != nil {
			t.Fatal(err)
		}
		svc.setTarget(id.MembershipID, target)
		if _, err := svc.room.Call[MemberInfo](ctx, AddMember{MemberIdentity: id, Target: target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: svc.info.RoomIdentity, Caller: humanCaller, ClientMessageID: "start", Text: "start"}); err != nil {
		t.Fatal(err)
	}
	for i, b := range backends {
		if _, err := svc.control(ctx, apiRequest{AgentID: fmt.Sprint("M", i), MembershipID: fmt.Sprint("generation-", i)}, "run"); err != nil {
			t.Fatal(err)
		}
		select {
		case <-b.entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	limited, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- svc.close(limited) }()
	for _, b := range backends {
		select {
		case <-b.canceled:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	// Even after two holders settle, the third still owns the Store.
	close(backends[0].settle)
	close(backends[1].settle)
	stop()
	if err := <-done; err == nil {
		t.Fatal("close completed with an unsettled member")
	}
	if store, err := OpenStore(svc.store.dir, svc.info.RoomID); !errors.Is(err, ErrLocked) {
		if store != nil {
			_ = store.Close()
		}
		t.Fatal("lost Store ownership", err)
	}
	close(backends[2].settle)
	if err := svc.close(ctx); err != nil {
		t.Fatal(err)
	}
	for _, b := range backends {
		if b.opens.Load() != 1 || b.closes.Load() != 1 {
			t.Fatal("shutdown started another turn or failed cleanup")
		}
	}
}

// Hold the asynchronous grant-time roster check, allowing cancellation or a
// membership/input change after the scheduler grants and before execution.
type turnRosterGate struct {
	room             actorx.Addr
	entered, release chan struct{}
}

func (g turnRosterGate) Bindings() []actorx.Binding {
	return []actorx.Binding{actorx.Handle(func(ctx actorx.Context, msg ListMembers) (MemberList, error) {
		close(g.entered)
		select {
		case <-g.release:
		case <-ctx.Done():
			return MemberList{}, ctx.Err()
		}
		return g.room.Call[MemberList](ctx, msg)
	})}
}

func TestRoomTurnsParallelRechecksGrantedMemberBeforeOpening(t *testing.T) {
	for _, action := range []string{"pause", "remove", "rotate", "read"} {
		t.Run(action, func(t *testing.T) {
			h, targets, ids, b := scheduledMembers(t, schedulingParallel, 2)
			_ = targets[0].Stop(nil)
			_, _ = targets[0].Wait(h.ctx)
			entered, release := make(chan struct{}), make(chan struct{})
			defer onceRelease(release)()
			gate, err := h.rt.Spawn(turnRosterGate{room: h.addr, entered: entered, release: release})
			if err != nil {
				t.Fatal(err)
			}
			targets[0], err = h.spawn(&MemberAgent{Options: SessionOptions{MemberIdentity: ids[0]}, Room: gate, Store: h.store, Backend: rotatingControlledBackend{b[0]}})
			if err != nil {
				t.Fatal(err)
			}
			runSerialMember(t, h, targets[0], ids[0])
			select {
			case <-entered:
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			runSerialMember(t, h, targets[1], ids[1])
			awaitTurn(t, h, b[1])
			switch action {
			case "pause", "rotate":
				controlMember(t, h, targets[0], ids[0], action, UnreadChanged{}, false, false)
			case "remove":
				roomCall[MemberInfo](t, h, RemoveMember{ids[0]})
			case "read":
				page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: agentCaller(ids[0].AgentID, ids[0].MembershipID), Mode: ReadUnread})
				roomCall[ReadConfirmation](t, h, ConfirmRead{MemberIdentity: ids[0], ReadReceipt: page.ReadReceipt})
			}
			close(release)
			// Once the held preflight has returned, all ownership must be withdrawn.
			for {
				state := schedulerState(t, h, h.scheduler)
				if _, held := state.Holders[ids[0]]; !held {
					break
				}
				select {
				case <-h.ctx.Done():
					t.Fatal(h.ctx.Err())
				case <-time.After(time.Millisecond):
				}
			}
			status := inspectMember(t, h.ctx, targets[0], ids[0])
			if status.Active || status.TurnBudget.Used != 0 || b[0].opens.Load() != 0 {
				t.Fatal("ineligible granted member executed", status)
			}
			if !inspectMember(t, h.ctx, targets[1], ids[1]).Active {
				t.Fatal("targeted withdrawal affected another holder")
			}
			b[1].finish <- nil
		})
	}
}

func TestRoomTurnsParallelRoomAndInboxShareMemberExecution(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	_ = h.scheduler.Stop(nil)
	_, _ = h.scheduler.Wait(h.ctx)
	var err error
	h.scheduler, err = h.rt.Spawn(&turnScheduler{RoomIdentity: h.id.RoomIdentity, mode: schedulingParallel})
	if err != nil {
		t.Fatal(err)
	}
	b := newControlledBackend()
	target, err := h.spawn(&MemberAgent{Options: SessionOptions{MemberIdentity: h.id}, Backend: b, Store: h.store, Room: h.roomHarness.addr,
		Inbox: func(ctx context.Context) (InboxStatus, error) {
			return h.addr.Call[InboxStatus](ctx, InspectInbox{h.id})
		}})
	if err != nil {
		t.Fatal(err)
	}
	roomCall[MemberInfo](t, h.roomHarness, AttachMember{MemberIdentity: h.id, Target: target})
	inboxCall[InboxReceipt](t, h, h.receive("1"))
	waitMember(t, h.ctx, target, h.id, func(s MemberStatus) bool { return s.Inbox != nil && s.Inbox.LatestSeq == 1 })
	controlMember(t, h.roomHarness, target, h.id, "run", UnreadChanged{MemberIdentity: h.id}, false, false)
	first := awaitTurn(t, h.roomHarness, b)
	if first.Inbox == nil || first.LatestSeq != 0 {
		t.Fatal(first)
	}
	idB := h.memberID("B", "generation-B")
	other := newControlledBackend()
	targetB, err := h.spawn(&MemberAgent{Options: SessionOptions{MemberIdentity: idB}, Backend: other, Store: h.store, Room: h.roomHarness.addr})
	if err != nil {
		t.Fatal(err)
	}
	roomCall[PostReceipt](t, h.roomHarness, h.post(humanCaller, "room-arrival", "shared input"))
	h.idle(t)
	runSerialMember(t, h.roomHarness, targetB, idB)
	awaitTurn(t, h.roomHarness, other)
	waitMember(t, h.ctx, target, h.id, func(s MemberStatus) bool { return s.Notification != nil && s.Notification.LatestSeq == 1 })
	if b.opens.Load() != 1 || inspectMember(t, h.ctx, target, h.id).TurnBudget.Used != 1 || len(b.entered) != 0 {
		t.Fatal("Room input reentered an Inbox turn")
	}
	b.finish <- nil
	second := awaitTurn(t, h.roomHarness, b)
	if second.LatestSeq != 1 || second.Inbox != nil {
		t.Fatal("pending Room input lost or Inbox reoffered", second)
	}
	if !inspectMember(t, h.ctx, targetB, idB).Active {
		t.Fatal("other member did not remain parallel")
	}
	b.finish <- nil
	other.finish <- nil
}

func TestRoomSlotParallelRetainsOwnershipDuringUncertainCleanup(t *testing.T) {
	for _, mode := range []string{"panic", "uncertain", "discard"} {
		t.Run(mode, func(t *testing.T) {
			h, targets, ids, b := scheduledMembers(t, schedulingParallel, 2)
			_ = targets[0].Stop(nil)
			_, _ = targets[0].Wait(h.ctx)
			entered, finish := make(chan struct{}), make(chan struct{})
			cleaning, cleaned := make(chan struct{}), make(chan struct{})
			defer onceRelease(cleaned)()
			backend := turnBackendFunc(func(context.Context, SessionOptions) (Session, error) {
				return turnSessionFuncs{
					run: func(ctx context.Context, _ UnreadChanged) error {
						close(entered)
						select {
						case <-finish:
							if mode == "panic" {
								panic("abandoned work")
							}
							return errDSHDisabled
						case <-ctx.Done():
							return ctx.Err()
						}
					},
					close: func(context.Context) error { close(cleaning); <-cleaned; return nil },
				}, nil
			})
			var err error
			targets[0], err = h.spawn(&MemberAgent{Options: SessionOptions{MemberIdentity: ids[0]}, Room: h.addr, Store: h.store, Backend: backend})
			if err != nil {
				t.Fatal(err)
			}
			runSerialMember(t, h, targets[0], ids[0])
			select {
			case <-entered:
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			runSerialMember(t, h, targets[1], ids[1])
			awaitTurn(t, h, b[1])
			if mode == "discard" {
				_ = targets[0].Stop(nil)
			} else {
				close(finish)
			}
			select {
			case <-cleaning:
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			if state := schedulerState(t, h, h.scheduler); len(state.Holders) != 2 {
				t.Fatal("cleanup released unsettled ownership", state)
			}
			roomCall[PostReceipt](t, h, h.post(humanCaller, "next", "healthy member continues"))
			h.idle(t)
			b[1].finish <- nil
			awaitTurn(t, h, b[1])
			close(cleaned)
			if mode == "discard" {
				if _, err := targets[0].Wait(h.ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				waitMember(t, h.ctx, targets[0], ids[0], func(s MemberStatus) bool { return !s.Active })
			}
			b[1].finish <- nil
		})
	}
}
