package notification

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

type worker struct {
	version    uint64
	cancel     context.CancelFunc
	done, wake chan struct{}
	force      bool
}
type Manager struct {
	host    Host
	sources map[SourceID]Source
	states  map[Key]subscription
	workers map[Key]*worker
	started bool
	// Owner-serialized settings; Clock is optional and disables jitter for tests.
	Clock  func() time.Time
	Limits Limits
}

func New(host Host, sources ...Source) *Manager {
	m := &Manager{host: host, sources: map[SourceID]Source{}, states: map[Key]subscription{}, workers: map[Key]*worker{}, Limits: DefaultLimits()}
	for _, source := range sources {
		m.sources[source.ID()] = source
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
	return time.Duration(float64(d) * (0.95 + rand.Float64()*0.10))
}

// Start is reserved for the live service. Offline loading never
// executes queries. Every worker is keyed by Room instance, source and opt-in ID.
func (m *Manager) Start() error {
	m.host.Gate.Lock()
	defer m.host.Gate.Unlock()
	if err := m.host.Check(true); err != nil {
		return err
	}
	if m.started {
		return nil
	}
	m.started = true
	for key := range m.states {
		m.startLocked(key)
	}
	return nil
}
func (m *Manager) startLocked(key Key) {
	s, ok := m.states[key]
	if !m.started || !ok || !s.state.Metadata().Enabled || m.workers[key] != nil {
		return
	}
	if _, err := m.host.Admit(context.Background(), s.state); err != nil {
		if !s.state.QueryPaused() && (errors.Is(err, ErrConflict) || errors.Is(err, platform.ErrCredential)) {
			_ = m.SaveLocked(s.state.ScopeFailed(err))
		}
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &worker{version: s.state.Metadata().Version, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	m.workers[key] = w
	session := &session{m: m, key: key, version: w.version, worker: w}
	runner := s.source.NewRunner(session)
	go m.run(ctx, key, w, runner)
}
func (m *Manager) run(ctx context.Context, key Key, w *worker, runner Runner) {
	defer func() {
		m.host.Gate.Lock()
		defer m.host.Gate.Unlock()
		if m.workers[key] == w {
			delete(m.workers, key)
			if next, ok := m.states[key]; ok && next.state.Metadata().Version != w.version {
				m.startLocked(key)
			}
		}
		close(w.done)
	}()
	queryStopped := false
	for {
		_ = m.Deliver(ctx, key, w.version)
		delay, stop := time.Second, false
		m.host.Gate.Lock()
		force := w.force
		m.host.Gate.Unlock()
		if force {
			queryStopped = false
		}
		if !queryStopped {
			// The source bounds its remote request with QueryContext. Its step keeps
			// the lifecycle context so a request deadline can still commit backoff.
			delay, stop = runner.Step(ctx)
		}
		if stop && ctx.Err() == nil {
			m.host.Gate.Lock()
			s, ok := m.states[key]
			queryStopped = ok && s.state.Metadata().Enabled && s.state.Metadata().Version == w.version && s.state.QueryPaused()
			m.host.Gate.Unlock()
			if queryStopped {
				stop = false
			}
		}
		if stop {
			return
		}
		delay = max(time.Millisecond, min(delay, time.Second))
		timer := time.NewTimer(delay)
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

// ChangedLocked fences an old configuration before starting a replacement.
// The caller may wait for the returned predecessor outside the shared gate.
func (m *Manager) ChangedLocked(key Key) <-chan struct{} {
	if w := m.workers[key]; w != nil {
		w.cancel()
		return w.done
	}
	m.startLocked(key)
	return nil
}
func (m *Manager) WakeLocked(key Key) {
	m.startLocked(key)
	if w := m.workers[key]; w != nil {
		w.force = true
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}
func (m *Manager) WakeRoomLocked(owner platform.RoomIdentity) {
	for key := range m.states {
		if key.Owner == owner {
			m.WakeLocked(key)
		}
	}
}
func (m *Manager) CancelRoomLocked(owner platform.RoomIdentity) {
	for key, w := range m.workers {
		if key.Owner == owner {
			w.cancel()
		}
	}
}

// CancelLocked stops only the selected subscription; settlement remains the
// caller's responsibility after releasing the shared gate.
func (m *Manager) CancelLocked(key Key) <-chan struct{} {
	if w := m.workers[key]; w != nil {
		w.cancel()
		return w.done
	}
	return nil
}
func (m *Manager) DoneLocked(key Key) <-chan struct{} {
	if w := m.workers[key]; w != nil {
		return w.done
	}
	return nil
}
func (m *Manager) RoomDoneLocked(owner platform.RoomIdentity) []<-chan struct{} {
	var out []<-chan struct{}
	for key, w := range m.workers {
		if key.Owner == owner {
			out = append(out, w.done)
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
	m.host.Gate.Lock()
	done := m.RoomDoneLocked(owner)
	m.host.Gate.Unlock()
	return Wait(ctx, done)
}
func (m *Manager) Stop(ctx context.Context) error {
	m.host.Gate.Lock()
	m.started = false
	var done []<-chan struct{}
	for _, w := range m.workers {
		w.cancel()
		done = append(done, w.done)
	}
	m.host.Gate.Unlock()
	return Wait(ctx, done)
}

// InvalidateConnectionLocked pauses all referencing subscriptions before a new
// credential is saved. It returns exactly the query owners requiring settlement.
func (m *Manager) InvalidateConnectionLocked(id string) error {
	for key, s := range m.states {
		if s.state.Metadata().ConnectionID != id {
			continue
		}
		next, err := s.state.InvalidateCredential()
		if err != nil {
			return err
		}
		if err = m.SaveLocked(next); err != nil {
			return err
		}
		if w := m.workers[key]; w != nil {
			w.cancel()
		}
	}
	return nil
}
func (m *Manager) ConnectionDoneLocked(id string) []<-chan struct{} {
	var out []<-chan struct{}
	for key, s := range m.states {
		if s.state.Metadata().ConnectionID == id {
			if w := m.workers[key]; w != nil {
				out = append(out, w.done)
			}
		}
	}
	return out
}

// NewRunnerLocked uses the same checked execution path without starting a timer.
// It supports deterministic one-step tests and controlled single-step operation.
func (m *Manager) NewRunnerLocked(key Key) Runner {
	s, ok := m.states[key]
	if !ok {
		return nil
	}
	return s.source.NewRunner(&session{m: m, key: key, version: s.state.Metadata().Version})
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
func (s *session) Limits() Limits {
	s.m.host.Gate.Lock()
	defer s.m.host.Gate.Unlock()
	return s.m.Limits
}
func (s *session) ConsumeForce() bool {
	s.m.host.Gate.Lock()
	defer s.m.host.Gate.Unlock()
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
func (s *session) readLocked(ctx context.Context) (State, Scope, error) {
	current, ok := s.m.states[s.key]
	if !ok || current.state.Metadata().Version != s.version || !current.state.Metadata().Enabled {
		return nil, Scope{}, ErrIdentity
	}
	state := current.state.CloneState()
	s.revision = current.revision
	scope, err := s.m.host.Admit(ctx, state)
	if err == nil && s.captured && scope.CredentialStamp != s.scope.CredentialStamp {
		err = platform.ErrCredential
	}
	if errors.Is(err, ErrConflict) || errors.Is(err, platform.ErrCredential) {
		if saveErr := s.m.SaveLocked(state.ScopeFailed(err)); saveErr != nil {
			return state, scope, saveErr
		}
		s.revision = s.m.states[s.key].revision
	}
	if err == nil {
		s.scope = scope
		s.captured = true
	}
	return state, scope, err
}
func (s *session) Read(ctx context.Context) (State, Scope, error) {
	s.m.host.Gate.Lock()
	defer s.m.host.Gate.Unlock()
	s.context = ctx
	return s.readLocked(ctx)
}
func (s *session) Commit(next State) error {
	s.m.host.Gate.Lock()
	defer s.m.host.Gate.Unlock()
	previous, ok := s.m.states[s.key]
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
			if saveErr := s.m.SaveLocked(previous.state.ScopeFailed(err)); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	if !s.captured || scope.Revision != s.scope.Revision {
		return ErrBusy
	}
	if err = s.m.SaveLocked(next); err != nil {
		return err
	}
	s.revision = s.m.states[s.key].revision
	return nil
}

// Source returns an explicitly assembled provider capability; sources are fixed
// at construction, not discovered or registered dynamically.
func (m *Manager) Source(id SourceID) Source { return m.sources[id] }

func (m *Manager) EnsureLocked(key Key) { m.startLocked(key) }
func (m *Manager) EnsureRoomLocked(owner platform.RoomIdentity) {
	for key := range m.states {
		if key.Owner == owner {
			m.startLocked(key)
		}
	}
}
