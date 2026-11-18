//go:generate go run github.com/go-aie/actorx/cmd/actorx-gen

package notification

import (
	"context"
	"reflect"
	"sync/atomic"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform"
)

const ownerQueueLimit = 64

type sourceProjection struct{ states map[Key]subscription }
type sourceOwner struct {
	address    actorx.Addr
	committed  atomic.Pointer[sourceProjection]
	store      *sourceStore
	activation uint64
	epoch      atomic.Uint64
}
type sourceStore struct {
	expiry map[Key]expirySchedule
	*Manager
	owner     platform.RoomIdentity
	states    map[Key]subscription
	loaded    bool
	published *sourceOwner
}

func (s *sourceStore) publish() {
	states := make(map[Key]subscription, len(s.states))
	for k, v := range s.states {
		states[k] = v
	}
	s.published.committed.Store(&sourceProjection{states})
}

// NotificationActor owns a Room's source mutations. The mailbox remains free
// while one bounded, accepted persistence operation runs in a task.
//
//actorx:actor
type NotificationActor struct {
	owner         *sourceOwner
	busy, closing bool
	queue         []sourceOperation
	quiesce       []actorx.Reply[struct{}]
}
type sourceOperation struct {
	owner      platform.RoomIdentity
	activation uint64
	ID         uint64
	run        func(*sourceStore) (any, error)
	reply      actorx.Reply[any]
}
type quiesceSources struct{ owner platform.RoomIdentity }

func (a *NotificationActor) Operate(ctx actorx.Context, op sourceOperation, reply actorx.Reply[any]) error {
	if op.owner != a.owner.store.owner || op.activation != a.owner.activation || op.run == nil || op.ID == 0 {
		return ErrIdentity
	}
	if a.closing {
		return ErrClosing
	}
	if len(a.queue) >= ownerQueueLimit {
		return ErrBusy
	}
	op.reply = reply
	a.queue = append(a.queue, op)
	a.dispatch(ctx)
	return nil
}
func (a *NotificationActor) dispatch(ctx actorx.Context) {
	if a.busy || len(a.queue) == 0 {
		a.finish()
		return
	}
	op := a.queue[0]
	a.queue[0] = sourceOperation{}
	a.queue = a.queue[1:]
	a.busy = true
	_, err := actorx.StartTask(ctx.WithContext(context.WithoutCancel(ctx)), func(context.Context) (any, error) {
		return op.run(a.owner.store)
	}, func(v any, err error) bool {
		a.busy = false
		op.reply.Complete(v, err)
		a.dispatch(ctx)
		return true
	}, nil)
	if err != nil {
		a.busy = false
		op.reply.Complete(nil, err)
		a.dispatch(ctx)
	}
}
func (a *NotificationActor) Quiesce(_ actorx.Context, q quiesceSources, reply actorx.Reply[struct{}]) error {
	if q.owner != a.owner.store.owner {
		return ErrIdentity
	}
	a.closing = true
	a.quiesce = append(a.quiesce, reply)
	a.finish()
	return nil
}
func (a *NotificationActor) finish() {
	if a.busy || len(a.queue) > 0 {
		return
	}
	for _, r := range a.quiesce {
		r.Complete(struct{}{}, nil)
	}
	a.quiesce = nil
}

func (m *Manager) sourceOwner(owner platform.RoomIdentity) (*sourceOwner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.owners[owner]; p != nil {
		return p, nil
	}
	if m.stopping || m.retired[owner] {
		return nil, ErrClosing
	}
	if m.runtime == nil {
		var err error
		m.runtime, err = actorx.NewRuntime(context.Background())
		if err != nil {
			return nil, err
		}
	}
	m.activation++
	p := &sourceOwner{activation: m.activation}
	p.epoch.Store(m.activation)
	p.store = &sourceStore{Manager: m, owner: owner, states: map[Key]subscription{}, published: p}
	p.store.publish()
	addr, err := m.runtime.Spawn(&NotificationActor{owner: p})
	if err != nil {
		return nil, err
	}
	p.address = addr
	m.owners[owner] = p
	return p, nil
}
func (m *Manager) operate(owner platform.RoomIdentity, fn func(*sourceStore) (any, error)) (any, error) {
	p, err := m.sourceOwner(owner)
	if err != nil {
		return nil, err
	}
	// Once accepted, cancellation of a caller does not release file ownership.
	return p.address.Call[any](context.Background(), sourceOperation{owner: owner, activation: p.activation, ID: m.operations.Add(1), run: fn})
}
func (m *Manager) mutate(owner platform.RoomIdentity, fn func(*sourceStore) error) error {
	_, err := m.operate(owner, func(s *sourceStore) (any, error) { return nil, fn(s) })
	return err
}
func (m *Manager) projection(owner platform.RoomIdentity) map[Key]subscription {
	m.mu.Lock()
	p := m.owners[owner]
	m.mu.Unlock()
	if p == nil {
		return nil
	}
	return p.committed.Load().states
}
func (m *Manager) allStates() map[Key]subscription {
	m.mu.Lock()
	owners := make([]*sourceOwner, 0, len(m.owners))
	for _, p := range m.owners {
		owners = append(owners, p)
	}
	m.mu.Unlock()
	out := map[Key]subscription{}
	for _, p := range owners {
		for k, s := range p.committed.Load().states {
			out[k] = s
		}
	}
	return out
}
func (m *Manager) Revision(key Key) uint64 { return m.projection(key.Owner)[key].revision }
func (m *Manager) StateLocked(key Key) (State, bool) {
	s, ok := m.projection(key.Owner)[key]
	if !ok {
		return nil, false
	}
	return s.state.CloneState(), true
}
func (m *Manager) StatesLocked(owner platform.RoomIdentity) []State {
	s := &sourceStore{Manager: m, states: m.projection(owner)}
	return s.StatesLocked(owner)
}
func (m *Manager) StatePathLocked(key Key) (string, error) {
	return (&sourceStore{Manager: m}).StatePathLocked(key)
}
func (m *Manager) LoadLocked(owner platform.RoomIdentity) error {
	if !m.accepting(owner) {
		return ErrClosing
	}
	return m.mutate(owner, func(s *sourceStore) error {
		if s.loaded {
			return nil
		}
		if err := s.LoadLocked(owner); err != nil {
			s.states = map[Key]subscription{}
			s.publish()
			return err
		}
		s.loaded = true
		s.publish()
		return nil
	})
}
func (m *Manager) SaveLocked(state State) error {
	return m.SaveExpected(state, m.Revision(state.Metadata().Key))
}

// SaveFrom fences the complete source snapshot used to construct a domain
// configuration candidate, including acknowledgements accepted during lookup.
func (m *Manager) SaveFrom(next, previous State) error {
	key := next.Metadata().Key
	saved, exists := m.projection(key.Owner)[key]
	absent := previous == nil || reflect.ValueOf(previous).Kind() == reflect.Pointer && reflect.ValueOf(previous).IsNil()
	if absent {
		if exists {
			return ErrBusy
		}
		return m.SaveExpected(next, 0)
	}
	if !exists || !reflect.DeepEqual(saved.state, previous.CloneState()) {
		return ErrBusy
	}
	return m.SaveExpected(next, saved.revision)
}
func (m *Manager) SaveExpected(state State, revision uint64) error {
	state = state.CloneState()
	key := state.Metadata().Key
	if !m.accepting(key.Owner) {
		return ErrClosing
	}
	return m.mutate(key.Owner, func(s *sourceStore) error {
		if s.states[key].revision != revision {
			return ErrBusy
		}
		if old, ok := s.states[key]; ok && old.state.Metadata().Version > state.Metadata().Version {
			return ErrIdentity
		}
		return s.SaveLocked(state)
	})
}
func (m *Manager) CurrentLocked(owner platform.RoomIdentity, id SourceID, platformID string, target Target) (State, error) {
	return (&sourceStore{Manager: m, states: m.projection(owner)}).CurrentLocked(owner, id, platformID, target)
}
func (m *Manager) ReferencesLocked(id string) []platform.RoomIdentity {
	return (&sourceStore{Manager: m, states: m.allStates()}).ReferencesLocked(id)
}
func (m *Manager) ForgetRoomLocked(owner platform.RoomIdentity) {
	m.mu.Lock()
	if m.retired[owner] {
		p := m.owners[owner]
		delete(m.owners, owner)
		m.mu.Unlock()
		if p != nil {
			_ = p.address.Stop(nil)
		}
		return
	}
	m.mu.Unlock()
	_ = m.mutate(owner, func(s *sourceStore) error {
		s.ForgetRoomLocked(owner)
		s.published.epoch.Add(1)
		s.loaded = false
		s.publish()
		return nil
	})
}

func (m *Manager) PendingDeliveriesLocked(owner platform.RoomIdentity, target Target) map[string]bool {
	return (&sourceStore{Manager: m, states: m.projection(owner)}).PendingDeliveriesLocked(owner, target)
}
func (m *Manager) PendingInputDeliveriesLocked(owner platform.RoomIdentity, target Target) map[SourceID]map[string]bool {
	return (&sourceStore{Manager: m, states: m.projection(owner)}).PendingInputDeliveriesLocked(owner, target)
}
func (m *Manager) ExpireTargetLocked(owner platform.RoomIdentity, target Target) error {
	if !m.accepting(owner) {
		return ErrClosing
	}
	return m.mutate(owner, func(s *sourceStore) error { return s.ExpireTargetLocked(owner, target) })
}

// Configure builds against the latest installed progress while fencing the
// domain configuration version checked by the caller. It preserves concurrent
// acknowledgements without trusting a stale whole-state replacement.
func (m *Manager) Configure(key Key, version uint64, build func(State) (State, error)) (State, error) {
	if !m.accepting(key.Owner) {
		return nil, ErrClosing
	}
	value, err := m.operate(key.Owner, func(s *sourceStore) (any, error) {
		previous, ok := s.states[key]
		if !ok || previous.state.Metadata().Version != version {
			return nil, ErrBusy
		}
		next, err := build(previous.state.CloneState())
		if err != nil {
			return nil, err
		}
		if next.Metadata().Key != key {
			return nil, ErrIdentity
		}
		if err := s.SaveLocked(next); err != nil {
			return nil, err
		}
		return next.CloneState(), nil
	})
	if err != nil {
		return nil, err
	}
	state, ok := value.(State)
	if !ok {
		return nil, ErrInvalid
	}
	return state, nil
}
