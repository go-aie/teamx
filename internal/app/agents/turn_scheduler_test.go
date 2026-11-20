package agents

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/identity"
)

type RoomIdentity = identity.RoomIdentity
type MemberIdentity = identity.MemberIdentity

type schedulerHarness struct {
	rt  *actorx.Runtime
	id  RoomIdentity
	ctx context.Context
}

func newSchedulerHarness(t *testing.T, options ...actorx.Option) *schedulerHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	rt, err := actorx.NewRuntime(ctx, options...)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer cancel()
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		if err := rt.Close(closeCtx); err != nil {
			t.Error(err)
		}
	})
	return &schedulerHarness{rt: rt, id: RoomIdentity{RoomID: "design-review", RoomInstance: "room-1"}, ctx: ctx}
}

func (h *schedulerHarness) memberID(agent, generation string) MemberIdentity {
	return MemberIdentity{RoomIdentity: h.id, AgentID: agent, MembershipID: generation}
}

func onceRelease(ch chan struct{}) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			select {
			case <-ch:
			default:
				close(ch)
			}
		})
	}
}

type schedulerSnapshot struct {
	Holder  TurnGranted
	Holders map[MemberIdentity]TurnGranted
	Queue   []TurnGranted
	Closed  bool
}

type grantSink struct {
	grants  chan TurnGranted
	blocked chan struct{}
	unblock chan struct{}
}

func (g grantSink) Bindings() []actorx.Binding {
	return []actorx.Binding{
		actorx.HandleMessage(func(_ actorx.Context, msg TurnGranted) error {
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

func schedulerCall(t *testing.T, h *schedulerHarness, addr actorx.Addr, msg any) {
	t.Helper()
	if _, err := addr.Call[any](h.ctx, msg); err != nil {
		t.Fatal(err)
	}
}

func schedulerState(t *testing.T, h *schedulerHarness, addr actorx.Addr) schedulerSnapshot {
	t.Helper()
	state, err := addr.Call[TurnSchedulerState](h.ctx, InspectTurnScheduler{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := schedulerSnapshot{Closed: state.Closing, Holders: state.Holders, Queue: state.Queue}
	for _, grant := range state.Holders {
		snapshot.Holder = grant
	}
	return snapshot
}

func awaitGrant(t *testing.T, h *schedulerHarness, grants <-chan TurnGranted, want TurnGranted) {
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
	h := newSchedulerHarness(t)
	scheduler, err := h.rt.Spawn(NewTurnScheduler(h.id, SchedulingSerial, nil))
	if err != nil {
		t.Fatal(err)
	}
	grants := make(chan TurnGranted, 32)
	var requests []RequestTurn
	for i, name := range []string{"A", "B", "C"} {
		target, err := h.rt.Spawn(grantSink{grants: grants})
		if err != nil {
			t.Fatal(err)
		}
		requests = append(requests, RequestTurn{h.memberID(name, fmt.Sprint(i)), 1, target})
	}
	for _, r := range requests {
		schedulerCall(t, h, scheduler, r)
		schedulerCall(t, h, scheduler, r) // admission retry, same ID
	}
	a, b, c := TurnGranted{requests[0].MemberIdentity, 1}, TurnGranted{requests[1].MemberIdentity, 1}, TurnGranted{requests[2].MemberIdentity, 1}
	s := schedulerState(t, h, scheduler)
	if s.Holder != a || len(s.Queue) != 2 || s.Queue[0] != b || s.Queue[1] != c {
		t.Fatal(s)
	}
	schedulerCall(t, h, scheduler, CancelTurnRequest{a})
	if s := schedulerState(t, h, scheduler); s.Holder != a {
		t.Fatal("cancel freed an unsettled holder", s)
	}
	schedulerCall(t, h, scheduler, ReleaseTurn{a})
	schedulerCall(t, h, scheduler, ReleaseTurn{a})
	schedulerCall(t, h, scheduler, requests[0]) // retired request cannot reenter
	if s := schedulerState(t, h, scheduler); s.Holder != b || len(s.Queue) != 1 {
		t.Fatal("late message changed the next holder", s)
	}
	requests[0].ID = 2
	schedulerCall(t, h, scheduler, requests[0])
	schedulerCall(t, h, scheduler, CancelTurnRequest{c})
	schedulerCall(t, h, scheduler, requests[2]) // canceled request stays retired
	schedulerCall(t, h, scheduler, ReleaseTurn{b})
	awaitGrant(t, h, grants, TurnGranted{a.MemberIdentity, 2})
	schedulerCall(t, h, scheduler, ReleaseTurn{a})
	if s := schedulerState(t, h, scheduler); s.Holder.ID != 2 || len(s.Queue) != 0 {
		t.Fatal("old release freed the same member's new grant", s)
	}
	if _, err := scheduler.Call[struct{}](h.ctx, StopTurnGrants{}); err != nil {
		t.Fatal(err)
	}
	requests[1].ID = 2
	schedulerCall(t, h, scheduler, requests[1])
	schedulerCall(t, h, scheduler, ReleaseTurn{TurnGranted{a.MemberIdentity, 2}})
	if s := schedulerState(t, h, scheduler); !s.Closed || s.Holder.ID != 0 || len(s.Queue) != 0 {
		t.Fatal("shutdown issued another grant", s)
	}
}

func TestTurnSchedulerRetriesFailedGrantAndJoinsStoppedHolder(t *testing.T) {
	h := newSchedulerHarness(t, actorx.WithMailboxCapacity(2))
	scheduler, err := h.rt.Spawn(NewTurnScheduler(h.id, SchedulingSerial, nil))
	if err != nil {
		t.Fatal(err)
	}
	grants := make(chan TurnGranted, 32)
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
		if err := a.Send(h.ctx, TurnGranted{}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := h.rt.Spawn(grantSink{grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	ra := RequestTurn{h.memberID("A", "a"), 1, a}
	rb := RequestTurn{h.memberID("B", "b"), 1, b}
	schedulerCall(t, h, scheduler, ra)
	schedulerCall(t, h, scheduler, rb)
	if s := schedulerState(t, h, scheduler); s.Holder.MemberIdentity != ra.MemberIdentity || len(s.Queue) != 1 {
		t.Fatal("failed grant lost its handoff responsibility", s)
	}
	close(unblock)
	awaitGrant(t, h, grants, TurnGranted{ra.MemberIdentity, 1})
	// Simulate loss of the member's release message. Full local exit (including
	// owned work/discard/Stopped) is a second source of settlement evidence.
	_ = a.Stop(nil)
	if _, err := a.Wait(h.ctx); err != nil {
		t.Fatal(err)
	}
	awaitGrant(t, h, grants, TurnGranted{rb.MemberIdentity, 1})
}

func TestTurnSchedulerParallelExactOwnershipAndExit(t *testing.T) {
	h := newSchedulerHarness(t, actorx.WithMailboxCapacity(2))
	scheduler, err := h.rt.Spawn(NewTurnScheduler(h.id, SchedulingParallel, nil))
	if err != nil {
		t.Fatal(err)
	}
	grants := make(chan TurnGranted, 128)
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
		if err := a.Send(h.ctx, TurnGranted{}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := h.rt.Spawn(grantSink{grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	ra, rb := RequestTurn{h.memberID("A", "a"), 1, a}, RequestTurn{h.memberID("B", "b"), 1, b}
	ga, gb := TurnGranted{ra.MemberIdentity, 1}, TurnGranted{rb.MemberIdentity, 1}
	schedulerCall(t, h, scheduler, ra)
	schedulerCall(t, h, scheduler, rb)
	awaitGrant(t, h, grants, gb) // A's full mailbox does not block B.
	schedulerCall(t, h, scheduler, ra)
	newer := ra
	newer.ID = 2
	schedulerCall(t, h, scheduler, newer) // cannot overwrite an active grant
	schedulerCall(t, h, scheduler, CancelTurnRequest{ga})
	schedulerCall(t, h, scheduler, ReleaseTurn{TurnGranted{ra.MemberIdentity, 2}})
	state := schedulerState(t, h, scheduler)
	if len(state.Holders) != 2 || state.Holders[ra.MemberIdentity] != ga {
		t.Fatal(state)
	}
	close(unblock)
	awaitGrant(t, h, grants, ga) // maintenance retries the failed admission
	schedulerCall(t, h, scheduler, ReleaseTurn{ga})
	schedulerCall(t, h, scheduler, newer)
	schedulerCall(t, h, scheduler, ReleaseTurn{ga})
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
	schedulerCall(t, h, scheduler, StopTurnGrants{})
	newer = rb
	newer.ID = 2
	schedulerCall(t, h, scheduler, newer)
	if state := schedulerState(t, h, scheduler); !state.Closed || len(state.Holders) != 1 || state.Holders[rb.MemberIdentity] != gb {
		t.Fatal("quiesce forgot unsettled holder", state)
	}
	schedulerCall(t, h, scheduler, ReleaseTurn{gb})
	schedulerCall(t, h, scheduler, newer)
	if state := schedulerState(t, h, scheduler); len(state.Holders) != 0 || len(state.Queue) != 0 {
		t.Fatal(state)
	}
}
