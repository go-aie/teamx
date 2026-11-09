package room

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/actorx"
)

type schedulerSnapshot struct {
	Holder turnGranted
	Queue  []turnGranted
	Closed bool
}

type inspectScheduler struct{}
type schedulerProbe struct{ *turnScheduler }

func (p schedulerProbe) Bindings() []actorx.Binding {
	return append(p.turnScheduler.Bindings(), actorx.Handle(func(actorx.Context, inspectScheduler) (schedulerSnapshot, error) {
		snapshot := schedulerSnapshot{Closed: p.closing}
		if p.holder != nil {
			snapshot.Holder = turnGranted{p.holder.MemberIdentity, p.holder.ID}
		}
		for _, a := range p.queue {
			snapshot.Queue = append(snapshot.Queue, turnGranted{a.MemberIdentity, a.ID})
		}
		return snapshot, nil
	}))
}

type grantSink struct {
	grants  chan turnGranted
	blocked chan struct{}
	unblock chan struct{}
}

func (g grantSink) Bindings() []actorx.Binding {
	return []actorx.Binding{
		actorx.HandleMessage(func(_ actorx.Context, msg turnGranted) error {
			select {
			case g.grants <- msg:
			default:
			}
			return nil
		}),
		actorx.HandleMessage(func(ctx actorx.Context, _ struct{}) error {
			close(g.blocked)
			select {
			case <-g.unblock:
			case <-ctx.Done():
			}
			return nil
		}),
	}
}

func schedulerCall(t *testing.T, h *roomHarness, addr actorx.Addr, msg any) {
	t.Helper()
	if _, err := addr.Call[any](h.ctx, msg); err != nil {
		t.Fatal(err)
	}
}

func schedulerState(t *testing.T, h *roomHarness, addr actorx.Addr) schedulerSnapshot {
	t.Helper()
	snapshot, err := addr.Call[schedulerSnapshot](h.ctx, inspectScheduler{})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func awaitGrant(t *testing.T, h *roomHarness, grants <-chan turnGranted, want turnGranted) {
	t.Helper()
	for {
		select {
		case got := <-grants:
			if got == want {
				return
			}
		case <-h.ctx.Done():
			t.Fatal("grant not delivered", want, h.ctx.Err())
		}
	}
}

func TestTurnSchedulerFIFOAndExactRelease(t *testing.T) {
	h := newRoomHarness(t, emptyRoom(), nil)
	scheduler, err := h.rt.Spawn(schedulerProbe{&turnScheduler{RoomIdentity: h.id}})
	if err != nil {
		t.Fatal(err)
	}
	grants := make(chan turnGranted, 32)
	var requests []requestTurn
	for i, name := range []string{"A", "B", "C"} {
		target, err := h.rt.Spawn(grantSink{grants: grants})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, requestTurn{h.memberID(name, fmt.Sprint(i)), 1, target})
	}
	for _, r := range requests {
		schedulerCall(t, h, scheduler, r)
		schedulerCall(t, h, scheduler, r) // admission retry, same ID
	}
	a, b, c := turnGranted{requests[0].MemberIdentity, 1}, turnGranted{requests[1].MemberIdentity, 1}, turnGranted{requests[2].MemberIdentity, 1}
	s := schedulerState(t, h, scheduler)
	if s.Holder != a || len(s.Queue) != 2 || s.Queue[0] != b || s.Queue[1] != c {
		t.Fatal(s)
	}
	schedulerCall(t, h, scheduler, cancelTurnRequest{a})
	if s := schedulerState(t, h, scheduler); s.Holder != a {
		t.Fatal("cancel freed an unsettled holder", s)
	}
	schedulerCall(t, h, scheduler, releaseTurn{a})
	schedulerCall(t, h, scheduler, releaseTurn{a})
	schedulerCall(t, h, scheduler, requests[0]) // retired request cannot reenter
	if s := schedulerState(t, h, scheduler); s.Holder != b || len(s.Queue) != 1 {
		t.Fatal("late message changed the next holder", s)
	}
	requests[0].ID = 2
	schedulerCall(t, h, scheduler, requests[0])
	schedulerCall(t, h, scheduler, cancelTurnRequest{c})
	schedulerCall(t, h, scheduler, requests[2]) // canceled request stays retired
	schedulerCall(t, h, scheduler, releaseTurn{b})
	awaitGrant(t, h, grants, turnGranted{a.MemberIdentity, 2})
	schedulerCall(t, h, scheduler, releaseTurn{a})
	if s := schedulerState(t, h, scheduler); s.Holder.ID != 2 || len(s.Queue) != 0 {
		t.Fatal("old release freed the same member's new grant", s)
	}
	if _, err := scheduler.Call[struct{}](h.ctx, stopTurnGrants{}); err != nil {
		t.Fatal(err)
	}
	requests[1].ID = 2
	schedulerCall(t, h, scheduler, requests[1])
	schedulerCall(t, h, scheduler, releaseTurn{turnGranted{a.MemberIdentity, 2}})
	if s := schedulerState(t, h, scheduler); !s.Closed || s.Holder.ID != 0 || len(s.Queue) != 0 {
		t.Fatal("shutdown issued another grant", s)
	}
}

func TestTurnSchedulerRetriesFailedGrantAndJoinsStoppedHolder(t *testing.T) {
	h := newRoomHarness(t, emptyRoom(), nil, actorx.WithMailboxCapacity(2))
	scheduler, err := h.rt.Spawn(schedulerProbe{&turnScheduler{RoomIdentity: h.id}})
	if err != nil {
		t.Fatal(err)
	}
	grants := make(chan turnGranted, 32)
	blocked, unblock := make(chan struct{}), make(chan struct{})
	a, err := h.rt.Spawn(grantSink{grants: grants, blocked: blocked, unblock: unblock})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Send(h.ctx, struct{}{}); err != nil {
		t.Fatal(err)
	}
	<-blocked
	// Fill both queued slots while the first handler is blocked.
	for range 2 {
		if err := a.Send(h.ctx, turnGranted{}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := h.rt.Spawn(grantSink{grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	ra := requestTurn{h.memberID("A", "a"), 1, a}
	rb := requestTurn{h.memberID("B", "b"), 1, b}
	schedulerCall(t, h, scheduler, ra)
	schedulerCall(t, h, scheduler, rb)
	if s := schedulerState(t, h, scheduler); s.Holder.MemberIdentity != ra.MemberIdentity || len(s.Queue) != 1 {
		t.Fatal("failed grant lost its handoff responsibility", s)
	}
	close(unblock)
	awaitGrant(t, h, grants, turnGranted{ra.MemberIdentity, 1})
	// Simulate loss of the member's release message. Full local exit (including
	// owned work/discard/Stopped) is a second source of settlement evidence.
	_ = a.Stop(nil)
	if _, err := a.Wait(h.ctx); err != nil {
		t.Fatal(err)
	}
	awaitGrant(t, h, grants, turnGranted{rb.MemberIdentity, 1})
}

func serialMembers(t *testing.T, count int, options ...actorx.Option) (*roomHarness, []actorx.Addr, []MemberIdentity, []*controlledBackend) {
	t.Helper()
	state := historyRoom(1, "body")
	for i := 1; i < count; i++ {
		name := string(rune('A' + i))
		state.Members = append(state.Members, MemberState{AgentID: name, MembershipID: "generation-" + name, Runner: state.Members[0].Runner})
	}
	h := newRoomHarness(t, state, nil, options...)
	var targets []actorx.Addr
	var ids []MemberIdentity
	var backends []*controlledBackend
	for _, member := range state.Members {
		id := h.memberID(member.AgentID, member.MembershipID)
		backend := newControlledBackend()
		target, err := h.spawn(&MemberAgent{Options: SessionOptions{MemberIdentity: id}, Store: h.store, Room: h.addr, Backend: rotatingControlledBackend{backend}})
		if err != nil {
			t.Fatal(err)
		}
		roomCall[MemberInfo](t, h, AttachMember{MemberIdentity: id, Target: target})
		targets, ids, backends = append(targets, target), append(ids, id), append(backends, backend)
	}
	return h, targets, ids, backends
}

func runSerialMember(t *testing.T, h *roomHarness, target actorx.Addr, id MemberIdentity) {
	t.Helper()
	list := roomCall[MemberList](t, h, ListMembers{h.id})
	for _, member := range list.Members {
		if member.MembershipID == id.MembershipID {
			controlMember(t, h, target, id, "run", UnreadChanged{MemberIdentity: id, Revision: list.Revision,
				LatestSeq: list.LatestSeq, NewestUnreadSeq: member.NewestUnreadSeq, UnreadCount: member.UnreadCount}, false, false)
			return
		}
	}
	t.Fatal("missing member", id)
}

func TestRoomTurnsFreshFairAndSilent(t *testing.T) {
	// Three task slots cover the maintenance callback renewing its timer
	// while one preflight or Run is owned.
	h, targets, ids, backends := serialMembers(t, 3, actorx.WithTaskLimit(3))
	runSerialMember(t, h, targets[0], ids[0])
	awaitTurn(t, h, backends[0])
	for i := 1; i < 3; i++ {
		runSerialMember(t, h, targets[i], ids[i])
		waitMember(t, h.ctx, targets[i], ids[i], func(s MemberStatus) bool { return s.Status == "waiting" })
	}
	roomCall[PostReceipt](t, h, h.post(humanCaller, "follow-up", "new input"))
	h.idle(t)
	roomCall[PostReceipt](t, h, h.post(agentCaller(ids[0].AgentID, ids[0].MembershipID), "a-proposal", "proposal"))
	h.idle(t)
	for i := 1; i < 3; i++ {
		s := waitMember(t, h.ctx, targets[i], ids[i], func(s MemberStatus) bool { return s.Notification.LatestSeq == 3 })
		for range 5 {
			if err := targets[i].Send(h.ctx, *s.Notification); err != nil {
				t.Fatal(err)
			}
		}
		if s.Active || s.TurnBudget.Used != 0 || s.Agent.TurnNumber != 0 || s.Agent.LastOfferedUnreadSeq != 0 || backends[i].opens.Load() != 0 {
			t.Fatal("waiting consumed execution or budget", s)
		}
	}
	backends[0].finish <- nil
	for i := 1; i < 3; i++ {
		input := awaitTurn(t, h, backends[i])
		if input.LatestSeq != 3 || input.NewestUnreadSeq != 3 {
			t.Fatal("grant used an old notification", input)
		}
		if s := inspectMember(t, h.ctx, targets[0], ids[0]); s.Active || backends[0].opens.Load() != 1 {
			t.Fatal("previous holder bypassed the FIFO", s)
		}
		// Reading and remaining silent must not create a follow-up turn.
		page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: agentCaller(ids[i].AgentID, ids[i].MembershipID), Mode: ReadUnread})
		roomCall[ReadConfirmation](t, h, ConfirmRead{MemberIdentity: ids[i], ReadReceipt: page.ReadReceipt})
		h.idle(t)
		backends[i].finish <- nil
	}
	if input := awaitTurn(t, h, backends[0]); input.NewestUnreadSeq != 2 || input.LatestSeq != 3 {
		t.Fatal(input)
	}
	backends[0].finish <- nil
	for i := range targets {
		want := uint64(1)
		if i == 0 {
			want = 2
		}
		s := waitMember(t, h.ctx, targets[i], ids[i], func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == want })
		if s.Status == "waiting" || backends[i].opens.Load() != 1 || s.Agent.LastOfferedUnreadSeq == 0 {
			t.Fatal("silent turn self-scheduled or lost its Session", s)
		}
		if err := targets[i].Send(h.ctx, *s.Notification); err != nil {
			t.Fatal(err)
		}
		if after := inspectMember(t, h.ctx, targets[i], ids[i]); after.Active || after.Status == "waiting" {
			t.Fatal("duplicate notification created a turn", after)
		}
	}
}

func TestRoomTurnsCancelRetainsSlotUntilSettlement(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprint("stop=", stop), func(t *testing.T) {
			h, targets, ids, b := serialMembers(t, 2)
			b[0].settle = make(chan struct{})
			t.Cleanup(func() { close(b[0].settle) })
			runSerialMember(t, h, targets[0], ids[0])
			awaitTurn(t, h, b[0])
			runSerialMember(t, h, targets[1], ids[1])
			waitMember(t, h.ctx, targets[1], ids[1], func(s MemberStatus) bool { return s.Status == "waiting" })
			if stop {
				roomCall[MemberInfo](t, h, RemoveMember{ids[0]})
				_ = targets[0].Stop(nil)
			} else {
				controlMember(t, h, targets[0], ids[0], "pause", UnreadChanged{}, false, true)
			}
			select {
			case <-b[0].canceled:
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			if !stop && !inspectMember(t, h.ctx, targets[0], ids[0]).Active {
				t.Fatal("cancel presented unsettled work as inactive")
			}
			if s := inspectMember(t, h.ctx, targets[1], ids[1]); s.Active || b[1].opens.Load() != 0 {
				t.Fatal("cancellation released the slot early", s)
			}
			b[0].settle <- struct{}{}
			awaitTurn(t, h, b[1])
			b[1].finish <- nil
		})
	}
}

func TestRoomTurnsWithdrawWaitingMemberAndRecheckUnread(t *testing.T) {
	for _, action := range []string{"pause", "remove", "read", "rotate"} {
		t.Run(action, func(t *testing.T) {
			h, targets, ids, b := serialMembers(t, 3)

			runSerialMember(t, h, targets[0], ids[0])
			awaitTurn(t, h, b[0])
			runSerialMember(t, h, targets[1], ids[1])
			runSerialMember(t, h, targets[2], ids[2])
			waitMember(t, h.ctx, targets[1], ids[1], func(s MemberStatus) bool { return s.Status == "waiting" })
			switch action {
			case "rotate":
				controlMember(t, h, targets[1], ids[1], "rotate", UnreadChanged{}, false, false)
			case "pause":
				controlMember(t, h, targets[1], ids[1], "pause", UnreadChanged{}, false, false)
				// A canceled request can still receive an already admitted grant.
				if _, err := targets[1].Call[any](h.ctx, turnGranted{ids[1], 1}); err != nil {
					t.Fatal(err)
				}
			case "remove":
				roomCall[MemberInfo](t, h, RemoveMember{ids[1]})
				// Deliberately leave its Actor alive: grant-time Room recheck
				// must reject this stale membership before opening a Session.
			case "read":
				page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: agentCaller(ids[1].AgentID, ids[1].MembershipID), Mode: ReadUnread})
				roomCall[ReadConfirmation](t, h, ConfirmRead{MemberIdentity: ids[1], ReadReceipt: page.ReadReceipt})
			}
			b[0].finish <- errors.New("model failure still releases settled execution")
			awaitTurn(t, h, b[2])
			if s := inspectMember(t, h.ctx, targets[1], ids[1]); s.Active || s.TurnBudget.Used != 0 || s.Agent.TurnNumber != 0 || b[1].opens.Load() != 0 {
				t.Fatal("withdrawn waiter executed", s)
			}
			b[2].finish <- nil
		})
	}
}

type rotatingControlledBackend struct{ *controlledBackend }

type turnBackendFunc func(context.Context, SessionOptions) (Session, error)

func (f turnBackendFunc) OpenSession(ctx context.Context, opts SessionOptions) (Session, error) {
	return f(ctx, opts)
}

type turnSessionFuncs struct {
	run   func(context.Context, UnreadChanged) error
	close func(context.Context) error
}

func (s turnSessionFuncs) Run(ctx context.Context, input UnreadChanged) error {
	return s.run(ctx, input)
}
func (s turnSessionFuncs) Close(ctx context.Context) error { return s.close(ctx) }

func TestRoomSlotCoversOpenSessionAndRoomsAreIndependent(t *testing.T) {
	h, targets, ids, b := serialMembers(t, 2)
	_ = targets[0].Stop(nil)
	_, _ = targets[0].Wait(h.ctx)
	opening, opened := make(chan struct{}), make(chan struct{})
	backend := turnBackendFunc(func(ctx context.Context, opts SessionOptions) (Session, error) {
		close(opening)
		select {
		case <-opened:
			return b[0].OpenSession(ctx, opts)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	var err error
	targets[0], err = h.spawn(&MemberAgent{Options: SessionOptions{MemberIdentity: ids[0]}, Room: h.addr, Store: h.store, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	runSerialMember(t, h, targets[0], ids[0])
	select {
	case <-opening:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	runSerialMember(t, h, targets[1], ids[1])
	if s := inspectMember(t, h.ctx, targets[1], ids[1]); s.Active || b[1].opens.Load() != 0 {
		t.Fatal("Session open did not own the Room slot", s)
	}
	other, otherTargets, otherIDs, otherBackends := serialMembers(t, 1)
	runSerialMember(t, other, otherTargets[0], otherIDs[0])
	awaitTurn(t, other, otherBackends[0]) // enters before the first Room is unblocked
	otherBackends[0].finish <- nil
	close(opened)
	awaitTurn(t, h, b[0])
	b[0].finish <- nil
	awaitTurn(t, h, b[1])
	b[1].finish <- nil
}

func TestRoomSlotJoinsPanicAndUncertainBackendCleanup(t *testing.T) {
	for _, mode := range []string{"panic", "uncertain", "discard"} {
		t.Run(mode, func(t *testing.T) {
			h, targets, ids, b := serialMembers(t, 2)
			_ = targets[0].Stop(nil)
			_, _ = targets[0].Wait(h.ctx)
			entered, finish := make(chan struct{}), make(chan struct{})
			closing, closed := make(chan struct{}), make(chan struct{})
			backend := turnBackendFunc(func(context.Context, SessionOptions) (Session, error) {
				return turnSessionFuncs{run: func(ctx context.Context, _ UnreadChanged) error {
					close(entered)
					select {
					case <-finish:
						if mode == "panic" {
							panic("abandoned backend activity")
						}
						return errDSHDisabled
					case <-ctx.Done():
						return ctx.Err()
					}
				}, close: func(context.Context) error {
					close(closing)
					<-closed
					return nil
				}}, nil
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
			if mode == "discard" {
				_ = targets[0].Stop(nil)
			} else {
				close(finish)
			}
			select {
			case <-closing:
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			if s := inspectMember(t, h.ctx, targets[1], ids[1]); s.Active || b[1].opens.Load() != 0 {
				t.Fatal("cleanup released a live slot", s)
			}
			if mode != "discard" {
				status := inspectMember(t, h.ctx, targets[0], ids[0])
				if !status.Active || !strings.Contains(status.Error, "waiting for backend cleanup") {
					t.Fatal("cleanup reason concealed while retaining the slot", status)
				}
			}
			close(closed)
			awaitTurn(t, h, b[1])
			b[1].finish <- nil
		})
	}
}

func TestServiceShutdownDoesNotGrantWaitingTurns(t *testing.T) {
	svc, a, _, ctx := runningControlledService(t)
	b := newControlledBackend()
	id := MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: "B", MembershipID: "generation-B"}
	target, err := spawnScheduledMember(svc.rt, &MemberAgent{Options: SessionOptions{MemberIdentity: id}, Store: svc.store, Room: svc.room, Backend: b}, svc.scheduler)
	if err != nil {
		t.Fatal(err)
	}
	svc.setTarget(id.MembershipID, target)
	if _, err := svc.room.Call[MemberInfo](ctx, AddMember{MemberIdentity: id, Target: target, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller,
		ClientMessageID: "queued-input", Text: "new input"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	waitMember(t, ctx, target, id, func(s MemberStatus) bool { return s.Status == "waiting" })
	members, err := svc.members(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members.Members {
		if member.MembershipID == id.MembershipID && (member.Status != "waiting" || member.Execution.Active || member.Execution.TurnBudget.Used != 0) {
			t.Fatal("member list concealed the waiting state", member)
		}
	}
	// An admitted management operation may still own cleanup. Shutdown must
	// stop grants before waiting for that operation, even if its caller times out.
	svc.management.Lock()
	limited, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	closeErr := svc.close(limited)
	cancel()
	svc.management.Unlock()
	if closeErr == nil || !strings.Contains(closeErr.Error(), "management cleanup incomplete") || !svc.grantsStopped.Load() {
		t.Fatal("shutdown waited for management before stopping grants", closeErr)
	}
	done := make(chan error, 1)
	go func() { done <- svc.close(ctx) }()
	select {
	case <-a.canceled:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if b.opens.Load() != 0 {
		t.Fatal("shutdown started the queued member")
	}
	a.settle <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if b.opens.Load() != 0 {
		t.Fatal("shutdown handed off after the old holder settled")
	}
}
