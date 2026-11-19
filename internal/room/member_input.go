package room

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/inputstream"
)

const memberInputQueueLimit = 64

// MemberInputActor serializes a private Inbox's body and its member's consumer.
// Work runs in owned tasks; the mailbox keeps serving committed observations
// and lifecycle messages while disk or material preparation is blocked.
//
//actorx:actor
type MemberInputActor struct {
	lastMaintenance bool
	waiters         int
	p               *platformInputStorage
	busy            bool
	closing         bool
	queue           []memberInputOperation
	quiesce         []actorx.Reply[struct{}]
	lease           uint64
	nextLease       uint64
}

type memberInputOperation struct {
	Inbox   inputstream.Inbox
	Name    string
	waiting []actorx.Reply[any]
	MemberIdentity
	ID    string
	run   func(context.Context) (any, error)
	reply actorx.Reply[any]
}
type inspectMemberInputs struct{ MemberIdentity }
type inspectInputOwner struct{ MemberIdentity }
type inputOwnerState struct {
	Busy, Closing   bool
	Queued, Waiters int
}
type quiesceMemberInputs struct{ MemberIdentity }
type beginInputExecution struct{ MemberIdentity }
type inputReadLease struct {
	MemberIdentity
	Release uint64
}
type inputOwnerContext struct{}
type inputProjection struct {
	status  unifiedInputStatus
	history InboxStatus
	state   inputstream.CoordinatorState
	err     error
}

func (a *MemberInputActor) InspectOwner(_ actorx.Context, msg inspectInputOwner) (inputOwnerState, error) {
	if msg.MemberIdentity != a.p.id {
		return inputOwnerState{}, ErrIdentity
	}
	return inputOwnerState{a.busy, a.closing, len(a.queue), a.waiters}, nil
}

func (a *MemberInputActor) CheckExecution(_ actorx.Context, msg beginInputExecution) (struct{}, error) {
	if msg.MemberIdentity != a.p.id {
		return struct{}{}, ErrIdentity
	}
	if a.closing {
		return struct{}{}, ErrClosing
	}
	if a.lease != 0 {
		return struct{}{}, ErrBusy
	}
	return struct{}{}, nil
}

func (a *MemberInputActor) Operate(ctx actorx.Context, msg memberInputOperation, reply actorx.Reply[any]) error {
	if msg.MemberIdentity != a.p.id || msg.Inbox != a.p.engine().Inbox || msg.ID == "" || msg.run == nil {
		return ErrIdentity
	}
	if a.closing {
		return ErrClosing
	}
	if a.waiters >= memberInputQueueLimit {
		return ErrBusy
	}
	msg.reply = reply
	a.waiters++
	if msg.Name == "maintenance" {
		for i := range a.queue {
			if a.queue[i].Name == msg.Name {
				a.queue[i].run = msg.run
				a.queue[i].waiting = append(a.queue[i].waiting, reply)
				return nil
			}
		}
	}
	a.queue = append(a.queue, msg)
	a.dispatch(ctx)
	return nil
}

func (a *MemberInputActor) dispatch(ctx actorx.Context) {
	if a.busy || len(a.queue) == 0 {
		return
	}
	index := 0
	if a.lastMaintenance && a.queue[0].Name == "maintenance" {
		for i, op := range a.queue {
			if op.Name != "maintenance" {
				index = i
				break
			}
		}
	}
	op := a.queue[index]
	copy(a.queue[index:], a.queue[index+1:])
	a.queue[len(a.queue)-1] = memberInputOperation{}
	a.queue = a.queue[:len(a.queue)-1]
	a.busy = true
	_, err := actorx.StartTask(ctx.WithContext(context.WithoutCancel(ctx)), func(ctx context.Context) (any, error) {
		ctx = context.WithValue(ctx, inputOwnerContext{}, a.p)
		v, err := op.run(ctx)
		// Install before replying or notifying observers. Errors retain the last
		// readable projection and fence uncertain persistence through Store.
		var refreshErr error
		switch op.Name {
		case "execution_state", "saved_result", "lookup_page", "read_entries", "maintenance_candidates", "admission_candidates", "check_execution", "catalog", "cache_catalog", "barrier", "input_context", "list_streams", "charge_discovery", "save_exposure", "configure_preferences":
		case "maintenance", "preview_history":
			refreshErr = a.p.refreshHistoryProjection()
		default:
			refreshErr = a.p.refreshInputProjection(ctx)
		}
		if refreshErr != nil || errors.Is(err, inputstream.ErrCommitUnknown) {
			next := inputProjection{}
			if previous := a.p.projection.Load(); previous != nil {
				next = *previous
			}
			next.err = errors.Join(err, refreshErr)
			next.status.Ready, next.status.Blocked = false, "input_projection_unavailable"
			a.p.projection.Store(&next)
		}
		// A failed derived observation cannot turn an already durable receipt or
		// confirmation into a failed write. Inspect reports the local view fault.
		return v, err
	}, func(v any, err error) bool {
		a.busy = false
		op.reply.Complete(v, err)
		for _, reply := range op.waiting {
			reply.Complete(v, err)
		}
		a.waiters -= 1 + len(op.waiting)
		a.lastMaintenance = op.Name == "maintenance"
		a.p.store.changed("inbox", a.p.id)
		a.dispatch(ctx)
		a.finishQuiesce()
		return true
	}, nil)
	if err != nil {
		a.busy = false
		op.reply.Complete(nil, err)
		for _, reply := range op.waiting {
			reply.Complete(nil, err)
		}
		a.waiters -= 1 + len(op.waiting)
		a.dispatch(ctx)
	}
}

func (a *MemberInputActor) Inspect(_ actorx.Context, msg inspectMemberInputs) (unifiedInputStatus, error) {
	if msg.MemberIdentity != a.p.id {
		return unifiedInputStatus{}, ErrIdentity
	}
	v := a.p.projection.Load()
	if v == nil {
		return unifiedInputStatus{Blocked: "input_owner_loading"}, nil
	}
	status := v.status
	if a.closing || a.p.retired.Load() {
		status.Ready = false
		status.Blocked = "input_owner_closing"
		return status, v.err
	}
	if a.p.projectionDirty.Load() {
		status.Ready = false
		status.Blocked = "input_projection_refresh"
		return status, v.err
	}
	if status.Recovery != nil {
		copy := *status.Recovery
		copy.Bounds = make(map[string]inputstream.Snapshot, len(status.Recovery.Bounds))
		for key, bound := range status.Recovery.Bounds {
			copy.Bounds[key] = bound
		}
		status.Recovery = &copy
		status.Ready = status.Pending > 0 && copy.Attempts < 3 && !copy.Next.IsZero() && !time.Now().Before(copy.Next)
	}
	return status, v.err
}

func (a *MemberInputActor) Lease(_ actorx.Context, msg inputReadLease) (uint64, error) {
	if msg.MemberIdentity != a.p.id {
		return 0, ErrIdentity
	}
	if msg.Release != 0 {
		if a.lease == msg.Release {
			a.lease = 0
		}
		a.finishQuiesce()
		return 0, nil
	}
	if a.closing {
		return 0, ErrClosing
	}
	if a.lease != 0 {
		return 0, ErrBusy
	}
	a.nextLease++
	a.lease = a.nextLease
	return a.lease, nil
}

func (a *MemberInputActor) Quiesce(_ actorx.Context, msg quiesceMemberInputs, reply actorx.Reply[struct{}]) error {
	if msg.MemberIdentity != a.p.id {
		return ErrIdentity
	}
	a.closing = true
	a.quiesce = append(a.quiesce, reply)
	a.finishQuiesce()
	return nil
}

func (p *platformInputStorage) owns(ctx context.Context) bool {
	return ctx.Value(inputOwnerContext{}) == p
}

// Accepted operations outlive cancellation of a waiter. Internal callers join
// their result so captured immutable parameters/results cannot escape early.
func inputOwnerValue[T any](ctx context.Context, p *platformInputStorage, name string, fn func(context.Context) (T, error)) (T, error) {
	if p.owns(ctx) {
		return fn(ctx)
	}
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	id := inputstream.Ref("input_operation_", p.id, name, p.operations.Add(1))
	v, err := p.owner.Call[any](context.WithoutCancel(ctx), memberInputOperation{MemberIdentity: p.id, Inbox: p.engine().Inbox, Name: name, ID: id, run: func(ctx context.Context) (any, error) { return fn(ctx) }})
	out, ok := v.(T)
	if ok {
		return out, err
	}
	if err != nil {
		return zero, err
	}
	return zero, ErrInvalid
}
func (p *platformInputStorage) run(ctx context.Context, name string, fn func(context.Context) error) error {
	_, err := inputOwnerValue(ctx, p, name, func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) })
	return err
}

func (p *platformInputStorage) acquireRead(ctx context.Context) (func(), error) {
	lease, err := p.owner.Call[uint64](context.WithoutCancel(ctx), inputReadLease{MemberIdentity: p.id})
	if err != nil {
		return nil, err
	}
	return sync.OnceFunc(func() {
		_, _ = p.owner.Call[uint64](context.Background(), inputReadLease{MemberIdentity: p.id, Release: lease})
	}), nil
}

// Store owns this runtime's lifetime and joins it before releasing the process
// directory lock. The service quiesces individual owners on member removal.
type inputOwners struct {
	closeMu sync.Mutex
	mu      sync.Mutex
	runtime *actorx.Runtime
	closed  bool
}

func (s *inputSession) inputState(ctx context.Context) (inputstream.CoordinatorState, error) {
	return inputOwnerValue(ctx, s.storage, "execution_state", func(context.Context) (inputstream.CoordinatorState, error) {
		return s.storage.repository.State(s.execution)
	})
}
func (s *inputSession) inputResult(ctx context.Context, page string) ([]byte, error) {
	return inputOwnerValue(ctx, s.storage, "saved_result", func(context.Context) ([]byte, error) {
		b, err := s.storage.repository.Result(s.execution, page)
		return []byte(b), err
	})
}

type inputReadLookup struct{ previous, parent *inputstream.ReadAttempt }

func (s *inputSession) lookupInputRead(ctx context.Context, q inputstream.ReadRequest) (inputReadLookup, error) {
	return inputOwnerValue(ctx, s.storage, "lookup_page", func(context.Context) (inputReadLookup, error) {
		a, b, err := s.storage.repository.LookupRead(s.execution, q)
		return inputReadLookup{a, b}, err
	})
}

func (a *MemberInputActor) Started(ctx actorx.Context, _ actorx.Started) error {
	return a.startRefresh(ctx)
}

// This task owns no engine mutation slot while waiting on source evidence or a
// remote admission resolver. Its bounded local work re-enters Operate normally.
func (a *MemberInputActor) startRefresh(owner actorx.Context) error {
	_, err := actorx.StartTask(owner, func(ctx context.Context) (struct{}, error) {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return struct{}{}, ctx.Err()
		case <-timer.C:
		}
		svc := a.p.service.Load()
		if svc == nil || svc.closing.Load() || svc.restarting.Load() {
			return struct{}{}, nil
		}
		if hook := svc.maintainInbox.Load(); hook != nil {
			if err := hook.run(ctx, a.p.id); err != nil && !errors.Is(err, ErrBusy) {
				return struct{}{}, err
			}
		}
		return struct{}{}, svc.reconcileMemberInputAdmission(ctx, a.p)
	}, func(_ struct{}, err error) bool {
		if !a.closing && owner.Err() == nil {
			if e := a.startRefresh(owner); e != nil {
				owner.Stop(e)
			}
		}
		return true
	}, nil)
	return err
}

func (s *Store) closeInputOwners(ctx context.Context) error {
	s.inputOwners.closeMu.Lock()
	defer s.inputOwners.closeMu.Unlock()
	s.inputOwners.mu.Lock()
	s.inputOwners.closed = true
	runtime := s.inputOwners.runtime
	s.inputOwners.mu.Unlock()
	if runtime == nil {
		return nil
	}
	s.mu.Lock()
	owners := make([]*platformInputStorage, 0, len(s.platformInputs))
	for _, p := range s.platformInputs {
		if !p.owner.IsZero() {
			owners = append(owners, p)
		}
	}
	s.mu.Unlock()
	for _, p := range owners {
		if _, err := p.owner.Call[struct{}](ctx, quiesceMemberInputs{p.id}); err != nil && !errors.Is(err, actorx.ErrStopped) {
			return err
		}
		p.clearHistory(0)
	}
	if shared := s.sharedHandle.Load(); shared != nil {
		if _, err := shared.owner.Call[struct{}](ctx, quiesceSharedInputs{}); err != nil && !errors.Is(err, actorx.ErrStopped) {
			return err
		}
	}
	if err := runtime.Close(ctx); err != nil {
		return err
	}
	s.inputOwners.mu.Lock()
	s.inputOwners.runtime = nil
	s.inputOwners.mu.Unlock()
	return nil
}

func (p *platformInputStorage) barrier(e inputstream.Execution) error {
	return p.run(context.Background(), "barrier", func(context.Context) error { return p.repository.Barrier(e) })
}
func (s *Store) retireMemberInputs(ctx context.Context, membership string) error {
	v, ok := s.inputHandles.Load(membership)
	if !ok {
		return nil
	}
	p := v.(*platformInputStorage)
	p.retired.Store(true)
	if _, err := p.owner.Call[struct{}](ctx, quiesceMemberInputs{p.id}); err != nil && !errors.Is(err, actorx.ErrStopped) {
		return err
	}
	p.clearHistory(0)
	_ = p.owner.Stop(nil)
	exit, err := p.owner.Wait(ctx)
	if err != nil {
		return err
	}
	// The Room roster already removed this exact Membership. Its old handle
	// remains retired, while the Store can release its body/index memory.
	s.mu.Lock()
	if s.platformInputs[membership] == p {
		delete(s.platformInputs, membership)
	}
	s.inputHandles.CompareAndDelete(membership, p)
	s.mu.Unlock()
	return exit.Reason
}

// Static composition keeps body identity separate from consumer identity even
// while a private Inbox deliberately maps both to the same Membership.
func (p *platformInputStorage) engine() inputstream.Capabilities {
	m := inputstream.Member{RoomID: p.id.RoomID, RoomInstance: p.id.RoomInstance, MembershipID: p.id.MembershipID}
	return inputstream.Capabilities{Inbox: inputstream.PrivateInbox(m), Consumer: m, Body: p.repository, Consumption: p.repository, Execution: p.repository, Reclamation: p.repository}
}
func (s *inputSession) coordinate(ctx context.Context, fn func(inputstream.ExecutionStorage) error) error {
	return s.mutateInput(ctx, func(*inputstream.Repository) error { return fn(s.storage.engine().Execution) })
}
func (s *inputSession) consume(ctx context.Context, fn func(inputstream.MemberConsumption) error) error {
	return s.mutateInput(ctx, func(*inputstream.Repository) error { return fn(s.storage.engine().Consumption) })
}

func (a *MemberInputActor) finishQuiesce() {
	if a.busy || len(a.queue) > 0 || a.lease != 0 {
		return
	}
	for _, reply := range a.quiesce {
		reply.Complete(struct{}{}, nil)
	}
	a.quiesce = nil
}
