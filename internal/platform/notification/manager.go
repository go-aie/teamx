package notification

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform"
)

type worker struct {
	version                   uint64
	cancel                    context.CancelFunc
	done, wake                chan struct{}
	force, cancelled, restart bool
}
type deliveryFlight struct{ done chan struct{} }
type Manager struct {
	operations        atomic.Uint64
	retired           map[platform.RoomIdentity]bool
	host              Host
	sources           map[SourceID]Source
	mu                sync.Mutex // directory and lifecycle only; never held across callbacks or I/O
	owners            map[platform.RoomIdentity]*sourceOwner
	workers           map[Key]*worker
	deliveries        map[Key]*deliveryFlight
	runtime           *actorx.Runtime
	activation        uint64
	started, stopping bool
	// Configure before starting workers. Clock disables jitter in controlled runs.
	Clock  func() time.Time
	Limits Limits
}

func New(host Host, sources ...Source) *Manager {
	m := &Manager{retired: map[platform.RoomIdentity]bool{}, host: host, sources: map[SourceID]Source{}, owners: map[platform.RoomIdentity]*sourceOwner{}, workers: map[Key]*worker{}, deliveries: map[Key]*deliveryFlight{}, Limits: DefaultLimits()}
	for _, s := range sources {
		m.sources[s.ID()] = s
	}
	return m
}
func (m *Manager) Now() time.Time {
	if m.Clock != nil {
		return m.Clock().UTC()
	}
	return time.Now().UTC()
}
func (m *Manager) Jitter(d time.Duration) time.Duration {
	if m.Clock != nil {
		return d
	}
	return time.Duration(float64(d) * (0.95 + rand.Float64()*0.1))
}
func (m *Manager) Source(id SourceID) Source { return m.sources[id] }
func (m *Manager) Start() error {
	if err := m.host.Check(true); err != nil {
		return err
	}
	states := m.allStates()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping {
		return ErrClosing
	}
	m.started = true
	for key, s := range states {
		m.startLocked(key, s.state.Metadata())
	}
	return nil
}
func (m *Manager) startLocked(key Key, meta Metadata) {
	if !m.started || m.stopping || !meta.Enabled || m.retired[key.Owner] || m.workers[key] != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &worker{version: meta.Version, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	m.workers[key] = w
	go m.run(ctx, key, w, m.sources[key.Source].NewRunner(&session{m: m, key: key, version: w.version, worker: w}))
}
func (m *Manager) run(ctx context.Context, key Key, w *worker, runner Runner) {
	defer func() {
		current, ok := m.StateLocked(key)
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.workers[key] == w {
			delete(m.workers, key)
			if ok && (current.Metadata().Version != w.version || w.restart) {
				m.startLocked(key, current.Metadata())
			}
		}
		close(w.done)
	}()
	// The first attempt establishes target backpressure before growing a new
	// source backlog. Subsequent receipt attempts can overlap a remote query.
	_ = m.Deliver(ctx, key, w.version)
	deliveryCtx, cancelDelivery := context.WithCancel(ctx)
	deliveryDone := make(chan struct{})
	go func() {
		defer close(deliveryDone)
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		for {
			select {
			case <-deliveryCtx.Done():
				return
			case <-timer.C:
				_ = m.Deliver(deliveryCtx, key, w.version)
			}
		}
	}()
	defer func() { cancelDelivery(); <-deliveryDone }()
	queryStopped := false
	for ctx.Err() == nil {
		current, ok := m.StateLocked(key)
		if !ok || current.Metadata().Version != w.version {
			return
		}
		m.mu.Lock()
		force := w.force
		m.mu.Unlock()
		blocked := current.Metadata().DeliveryError == "inbox_capacity"
		if queryStopped {
			queryStopped = current.QueryPaused()
		}
		if force {
			queryStopped = false
		}
		delay, stop := time.Second, false
		if !queryStopped && !blocked {
			delay, stop = runner.Step(ctx)
		}
		if stop && ctx.Err() == nil {
			current, ok = m.StateLocked(key)
			queryStopped = ok && current.Metadata().Enabled && current.Metadata().Version == w.version && current.QueryPaused()
			if queryStopped {
				stop = false
			}
		}
		if stop {
			return
		}
		timer := time.NewTimer(max(time.Millisecond, min(delay, time.Second)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-w.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
func (m *Manager) ChangedLocked(key Key) <-chan struct{} {
	current, ok := m.StateLocked(key)
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.workers[key]; w != nil {
		w.cancel()
		return w.done
	}
	if ok {
		m.startLocked(key, current.Metadata())
	}
	return nil
}
func (m *Manager) WakeLocked(key Key) {
	current, ok := m.StateLocked(key)
	m.mu.Lock()
	defer m.mu.Unlock()
	if ok {
		m.startLocked(key, current.Metadata())
	}
	if w := m.workers[key]; w != nil {
		w.force = true
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}
func (m *Manager) WakeRoomLocked(owner platform.RoomIdentity) {
	for key := range m.projection(owner) {
		m.WakeLocked(key)
	}
}
func (m *Manager) CancelLocked(key Key) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.workers[key]; w != nil {
		w.cancelled = true
		w.cancel()
		return w.done
	}
	if f := m.deliveries[key]; f != nil {
		return f.done
	}
	return nil
}
func (m *Manager) CancelRoomLocked(owner platform.RoomIdentity) {
	for key := range m.projection(owner) {
		m.CancelLocked(key)
	}
}
func (m *Manager) DoneLocked(key Key) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.workers[key]; w != nil {
		return w.done
	}
	if f := m.deliveries[key]; f != nil {
		return f.done
	}
	return nil
}
func (m *Manager) RoomDoneLocked(owner platform.RoomIdentity) []<-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []<-chan struct{}
	for k, w := range m.workers {
		if k.Owner == owner {
			out = append(out, w.done)
		}
	}
	for k, f := range m.deliveries {
		if k.Owner == owner {
			out = append(out, f.done)
		}
	}
	return out
}
func Wait(ctx context.Context, done []<-chan struct{}) error {
	for _, ch := range done {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (m *Manager) WaitRoom(ctx context.Context, owner platform.RoomIdentity) error {
	return Wait(ctx, m.RoomDoneLocked(owner))
}
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	m.started = false
	m.stopping = true
	var done []<-chan struct{}
	for _, w := range m.workers {
		w.cancel()
		done = append(done, w.done)
	}
	for _, f := range m.deliveries {
		done = append(done, f.done)
	}
	owners := make([]*sourceOwner, 0, len(m.owners))
	for _, p := range m.owners {
		owners = append(owners, p)
	}
	rt := m.runtime
	m.mu.Unlock()
	if err := Wait(ctx, done); err != nil {
		return err
	}
	for _, p := range owners {
		if _, err := p.address.Call[struct{}](ctx, quiesceSources{p.store.owner}); err != nil && !errors.Is(err, actorx.ErrStopped) && !errors.Is(err, actorx.ErrClosed) {
			return err
		}
	}
	if rt != nil {
		err := rt.Close(ctx)
		if errors.Is(err, actorx.ErrClosed) {
			return nil
		}
		return err
	}
	return nil
}
func (m *Manager) InvalidatePlatformLocked(id string) error {
	for key, s := range m.allStates() {
		if s.state.Metadata().PlatformID != id {
			continue
		}
		err := m.mutate(key.Owner, func(store *sourceStore) error {
			next, err := store.states[key].state.InvalidateCredential()
			if err != nil {
				return err
			}
			return store.SaveLocked(next)
		})
		if err != nil {
			return err
		}
		m.CancelLocked(key)
	}
	return nil
}
func (m *Manager) PlatformDoneLocked(id string) []<-chan struct{} {
	var out []<-chan struct{}
	for key, s := range m.allStates() {
		if s.state.Metadata().PlatformID == id {
			if ch := m.DoneLocked(key); ch != nil {
				out = append(out, ch)
			}
		}
	}
	return out
}
func (m *Manager) NewRunnerLocked(key Key) Runner {
	s, ok := m.projection(key.Owner)[key]
	if !ok {
		return nil
	}
	return s.source.NewRunner(&session{m: m, key: key, version: s.state.Metadata().Version})
}
func (m *Manager) EnsureLocked(key Key) {
	current, ok := m.StateLocked(key)
	m.mu.Lock()
	defer m.mu.Unlock()
	if w := m.workers[key]; w != nil && w.cancelled {
		w.restart = true
	}
	if ok {
		m.startLocked(key, current.Metadata())
	}
}
func (m *Manager) EnsureRoomLocked(owner platform.RoomIdentity) {
	for key := range m.projection(owner) {
		m.EnsureLocked(key)
	}
}

type session struct {
	m                 *Manager
	key               Key
	version, revision uint64
	worker            *worker
	scope             Scope
	captured          bool
	context           context.Context
}

func (s *session) Now() time.Time                       { return s.m.Now() }
func (s *session) Jitter(d time.Duration) time.Duration { return s.m.Jitter(d) }
func (s *session) Limits() Limits                       { l := s.m.Limits; l.ExpireBefore = s.m.expiryCutoff(); return l }
func (s *session) ConsumeForce() bool {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	if s.worker == nil {
		return false
	}
	force := s.worker.force
	s.worker.force = false
	if force {
		s.captured = false
	}
	return force
}
func (s *session) Read(ctx context.Context) (State, Scope, error) {
	current, ok := s.m.projection(s.key.Owner)[s.key]
	if !ok || current.state.Metadata().Version != s.version || !current.state.Metadata().Enabled {
		return nil, Scope{}, ErrIdentity
	}
	state := current.state.CloneState()
	s.revision = current.revision
	s.context = ctx
	scope, err := s.m.host.Admit(ctx, state)
	if err == nil && s.captured && scope.CredentialStamp != s.scope.CredentialStamp {
		err = platform.ErrCredential
	}
	if errors.Is(err, ErrConflict) || errors.Is(err, platform.ErrCredential) {
		if saveErr := s.m.SaveExpected(state.ScopeFailed(err), s.revision); saveErr != nil {
			return state, scope, saveErr
		}
		s.revision = s.m.Revision(s.key)
	}
	if err == nil {
		s.scope = scope
		s.captured = true
	}
	return state, scope, err
}
func (s *session) Commit(next State) error {
	previous, ok := s.m.projection(s.key.Owner)[s.key]
	if !ok || previous.state.Metadata().Version != s.version || next.Metadata().Key != s.key || next.Metadata().Version != s.version {
		return ErrIdentity
	}
	if previous.revision != s.revision {
		return ErrBusy
	}
	ctx := s.context
	if ctx == nil {
		ctx = context.Background()
	}
	scope, err := s.m.host.Admit(ctx, previous.state)
	if err == nil && s.captured && scope.CredentialStamp != s.scope.CredentialStamp {
		err = platform.ErrCredential
	}
	if err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, platform.ErrCredential) {
			if e := s.m.SaveExpected(previous.state.ScopeFailed(err), s.revision); e != nil {
				return e
			}
		}
		return err
	}
	if !s.captured || scope.Revision != s.scope.Revision {
		return ErrBusy
	}
	if err = s.m.SaveExpected(next, s.revision); err != nil {
		return err
	}
	s.revision = s.m.Revision(s.key)
	return nil
}

func (m *Manager) accepting(owner platform.RoomIdentity) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.stopping && !m.retired[owner]
}

// QuiesceRoom retains the owner until all accepted receipts, acknowledgements
// and queued file operations settle. Deletion may remove files only afterwards.
func (m *Manager) QuiesceRoom(ctx context.Context, owner platform.RoomIdentity) error {
	m.mu.Lock()
	m.retired[owner] = true
	p := m.owners[owner]
	for k, w := range m.workers {
		if k.Owner == owner {
			w.cancel()
		}
	}
	m.mu.Unlock()
	if err := m.WaitRoom(ctx, owner); err != nil {
		return err
	}
	if p != nil {
		_, err := p.address.Call[struct{}](ctx, quiesceSources{owner})
		if !errors.Is(err, actorx.ErrStopped) {
			return err
		}
	}
	return nil
}
