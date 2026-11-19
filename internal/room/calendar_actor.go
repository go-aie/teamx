package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/calendar"
)

type calendarProjection struct {
	state  calendar.State
	err    error
	frozen bool
	next   time.Time
}
type calendarHandle struct {
	addr       actorx.Addr
	service    *service
	projection atomic.Pointer[calendarProjection]
	boot       string
	retired    atomic.Bool
}
type calendarOperation struct {
	run      func(context.Context, *calendar.State) (any, error)
	internal bool
	wake     bool
	reply    actorx.Reply[any]
}
type calendarWake struct {
	Boot     string
	Revision uint64
}
type calendarInspect struct{}
type calendarQuiesce struct{}

// CalendarActor alone orders state changes. Workers receive a private candidate;
// only a successful durable save installs a projection in the actor callback.
//
//actorx:actor
type CalendarActor struct {
	h        *calendarHandle
	state    calendar.State
	busy     bool
	closing  bool
	waiters  int
	queue    []calendarOperation
	waking   bool
	inFlight map[string]bool
	frozen   error
	warning  error
	quiesce  []actorx.Reply[struct{}]
}

func (a *CalendarActor) Inspect(_ actorx.Context, _ calendarInspect) (calendar.State, error) {
	return a.state.Clone(), a.frozen
}
func (a *CalendarActor) Operate(ctx actorx.Context, msg calendarOperation, reply actorx.Reply[any]) error {
	if a.frozen != nil {
		return a.frozen
	}
	if (a.closing || a.h.retired.Load() || a.h.service.closing.Load()) && !msg.internal {
		return ErrClosing
	}
	if !msg.internal && a.waiters >= 64 {
		return ErrBusy
	}
	if msg.run == nil {
		return ErrRequest
	}
	msg.reply = reply
	a.waiters++
	a.queue = append(a.queue, msg)
	a.dispatch(ctx)
	return nil
}
func (a *CalendarActor) dispatch(owner actorx.Context) {
	if a.busy || len(a.queue) == 0 {
		return
	}
	op := a.queue[0]
	a.queue = a.queue[1:]
	a.busy = true
	candidate := a.state.Clone()
	_, err := actorx.StartTask(owner.WithContext(context.WithoutCancel(owner)), func(ctx context.Context) (any, error) {
		result, err := op.run(ctx, &candidate)
		if err != nil {
			return nil, err
		}
		candidate.Revision++
		if err := candidate.Compact(time.Now().UTC()); err != nil {
			return nil, err
		}
		if err := a.h.service.store.save(filepath.Join(a.h.service.store.dir, "calendar", "state.json"), candidate, calendar.MaxStateBytes); err != nil {
			return nil, err
		}
		return result, nil
	}, func(v any, err error) bool {
		a.busy = false
		a.waiters--
		if op.wake {
			a.waking = false
		}
		if err == nil {
			a.state = candidate
			a.warning = nil
			a.publish()
			a.h.service.invalidate("calendar", MemberIdentity{RoomIdentity: a.h.service.info.RoomIdentity})
		}
		if err != nil && op.wake {
			a.warning = err
			a.publish()
			a.h.service.invalidate("calendar", MemberIdentity{RoomIdentity: a.h.service.info.RoomIdentity})
		}
		if errors.Is(err, ErrCommitUnknown) {
			a.frozen = err
			a.publish()
		}
		op.reply.Complete(v, err)
		a.dispatch(owner)
		a.deliver(owner)
		a.finishQuiesce()
		return true
	}, nil)
	if err != nil {
		a.busy = false
		a.waiters--
		op.reply.Complete(nil, err)
		a.dispatch(owner)
		a.finishQuiesce()
	}
}
func (a *CalendarActor) publish() {
	now := time.Now().UTC()
	issue := a.warning
	next := a.state.Next(now)
	if issue != nil {
		next = now.Add(5 * time.Second)
	}
	if a.frozen != nil {
		issue = a.frozen
	}
	a.h.projection.Store(&calendarProjection{state: a.state.Clone(), err: issue, frozen: a.frozen != nil, next: next})
}
func (a *CalendarActor) Wake(ctx actorx.Context, msg calendarWake) error {
	if !a.h.service.calendarRunning.Load() || a.closing || a.h.retired.Load() || a.h.service.closing.Load() || a.h.service.restarting.Load() || a.frozen != nil || msg.Boot != a.h.boot || a.waking {
		return nil
	}
	a.waking = true
	// Internal wake is coalesced independently of bounded external admissions.
	a.queue = append(a.queue, calendarOperation{internal: true, wake: true, run: func(ctx context.Context, s *calendar.State) (any, error) {
		s.Evaluate(time.Now().UTC())
		a.h.service.reconcileCalendar(ctx, s)
		return nil, nil
	}})
	a.waiters++
	a.dispatch(ctx)
	// Cleared after the queued candidate has committed; the scheduler can retry
	// at its next bounded tick. No wake carries a stale task definition.
	return nil
}
func (a *CalendarActor) Quiesce(_ actorx.Context, _ calendarQuiesce, reply actorx.Reply[struct{}]) error {
	a.closing = true
	a.quiesce = append(a.quiesce, reply)
	a.finishQuiesce()
	return nil
}
func (a *CalendarActor) finishQuiesce() {
	if a.busy || len(a.queue) > 0 || len(a.inFlight) > 0 {
		return
	}
	for _, r := range a.quiesce {
		r.Complete(struct{}{}, nil)
	}
	a.quiesce = nil
}
func (h *calendarHandle) call(ctx context.Context, internal bool, fn func(context.Context, *calendar.State) (any, error)) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Accepted operations complete behind the same barrier even if HTTP leaves.
	return h.addr.Call[any](context.WithoutCancel(ctx), calendarOperation{run: fn, internal: internal})
}
func loadCalendar(s *service) (calendar.State, error) {
	var state calendar.State
	err := readSnapshot(filepath.Join(s.store.dir, "calendar", "state.json"), &state, calendar.MaxStateBytes)
	if errors.Is(err, os.ErrNotExist) {
		return calendar.New(s.info.RoomID, s.info.RoomInstance), nil
	}
	if err != nil {
		return state, err
	}
	return state, state.Validate(s.info.RoomID, s.info.RoomInstance)
}
