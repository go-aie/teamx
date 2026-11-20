package agents

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/identity"
)

func TestTurnSchedulerFrozenAdmissionAndMaintenance(t *testing.T) {
	for _, mode := range []SchedulingMode{SchedulingSerial, SchedulingParallel} {
		t.Run(string(mode), func(t *testing.T) {
			h := newSchedulerHarness(t)
			frozen := &atomic.Bool{}
			scheduler, err := h.rt.Spawn(NewTurnScheduler(h.id, mode, frozen))
			if err != nil {
				t.Fatal(err)
			}
			grants := make(chan TurnGranted, 64)
			target, err := h.rt.Spawn(grantSink{grants: grants})
			if err != nil {
				t.Fatal(err)
			}
			a := RequestTurn{h.memberID("A", "a"), 1, target}
			b := RequestTurn{h.memberID("B", "b"), 1, target}
			schedulerCall(t, h, scheduler, a)
			schedulerCall(t, h, scheduler, b)
			frozen.Store(true)
			// The service freezes synchronously, before Quiesce reaches the
			// mailbox. Release and the periodic retry must respect that gate.
			schedulerCall(t, h, scheduler, ReleaseTurn{TurnGranted{a.MemberIdentity, a.ID}})
			before := schedulerState(t, h, scheduler)
			newRequest := RequestTurn{h.memberID("C", "c"), 1, target}
			schedulerCall(t, h, scheduler, newRequest)
			// Drain only messages admitted before freezing, then wait across
			// maintenance ticks to detect any new sends from existing holders.
			if _, err := target.Call[any](h.ctx, TurnGranted{}); err != nil {
				t.Fatal(err)
			}
			for len(grants) > 0 {
				<-grants
			}
			select {
			case grant := <-grants:
				t.Fatal("frozen scheduler sent a grant", grant)
			case <-time.After(100 * time.Millisecond):
			}
			after := schedulerState(t, h, scheduler)
			if len(before.Holders) != len(after.Holders) || len(before.Queue) != len(after.Queue) {
				t.Fatal("frozen admission changed ownership", before, after)
			}
			if mode == SchedulingSerial && (len(after.Holders) != 0 || len(after.Queue) != 1) {
				t.Fatal("frozen release granted the serial waiter", after)
			}
			if mode == SchedulingParallel && len(after.Holders) != 1 {
				t.Fatal("freeze forgot an unsettled parallel holder", after)
			}
		})
	}
}

func TestTurnSchedulerValidationCapacityAndRetirement(t *testing.T) {
	h := newSchedulerHarness(t)
	scheduler, err := h.rt.Spawn(NewTurnScheduler(h.id, "", nil))
	if err != nil {
		t.Fatal(err)
	}
	target, err := h.rt.Spawn(grantSink{})
	if err != nil {
		t.Fatal(err)
	}
	valid := RequestTurn{h.memberID("A", "a"), 1, target}
	wrongRoom, zeroID, zeroTarget := valid, valid, valid
	wrongRoom.RoomInstance = "stale-instance"
	zeroID.ID = 0
	zeroTarget.Target = actorx.Addr{}
	for _, request := range []RequestTurn{wrongRoom, zeroID, zeroTarget} {
		if _, err := scheduler.Call[any](h.ctx, request); !errors.Is(err, identity.ErrMismatch) {
			t.Fatal("invalid request admitted", request, err)
		}
	}
	for i := range MaxMembers {
		request := RequestTurn{h.memberID(fmt.Sprint(i), fmt.Sprint(i)), 1, target}
		schedulerCall(t, h, scheduler, request)
		schedulerCall(t, h, scheduler, CancelTurnRequest{TurnGranted{request.MemberIdentity, request.ID}})
	}
	if _, err := scheduler.Call[any](h.ctx, valid); !errors.Is(err, ErrCapacity) {
		t.Fatal("cancellation discarded retired identities", err)
	}
	_ = target.Stop(nil)
	if _, err := target.Wait(h.ctx); err != nil {
		t.Fatal(err)
	}
	// Complete actor exit retires its memberships and frees the bounded slots.
	for schedulerState(t, h, scheduler).Holder.ID != 0 {
		select {
		case <-h.ctx.Done():
			t.Fatal(h.ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	grants := make(chan TurnGranted, 64)
	valid.Target, err = h.rt.Spawn(grantSink{grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	schedulerCall(t, h, scheduler, valid)
	awaitGrant(t, h, grants, TurnGranted{valid.MemberIdentity, valid.ID})
	if _, err := h.rt.Spawn(NewTurnScheduler(h.id, "Parallel", nil)); err == nil {
		t.Fatal("invalid mode started")
	}
}

func TestTurnSchedulerExitWaitsForStoppedCleanup(t *testing.T) {
	h := newSchedulerHarness(t)
	scheduler, err := h.rt.Spawn(NewTurnScheduler(h.id, SchedulingSerial, nil))
	if err != nil {
		t.Fatal(err)
	}
	cleaning, settled := make(chan struct{}), make(chan struct{})
	defer onceRelease(settled)()
	grants := make(chan TurnGranted, 64)
	a, err := h.rt.Spawn(settlingGrantSink{grantSink{grants: grants}, cleaning, settled})
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.rt.Spawn(grantSink{grants: grants})
	if err != nil {
		t.Fatal(err)
	}
	ra, rb := RequestTurn{h.memberID("A", "a"), 1, a}, RequestTurn{h.memberID("B", "b"), 1, b}
	schedulerCall(t, h, scheduler, ra)
	schedulerCall(t, h, scheduler, rb)
	_ = a.Stop(nil)
	select {
	case <-cleaning:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	// Wait across maintenance scans while Stopped still owns cleanup.
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	for {
		state := schedulerState(t, h, scheduler)
		if state.Holder.MemberIdentity != ra.MemberIdentity || len(state.Queue) != 1 {
			t.Fatal("stop request was treated as settled exit", state)
		}
		select {
		case <-timer.C:
			close(settled)
			awaitGrant(t, h, grants, TurnGranted{rb.MemberIdentity, 1})
			return
		case <-h.ctx.Done():
			t.Fatal(h.ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

type settlingGrantSink struct {
	grantSink
	cleaning chan struct{}
	settled  chan struct{}
}

func (s settlingGrantSink) Bindings() []actorx.Binding {
	return append(s.grantSink.Bindings(), actorx.HandleMessage(func(actorx.Context, actorx.Stopped) error {
		close(s.cleaning)
		<-s.settled
		return nil
	}))
}
