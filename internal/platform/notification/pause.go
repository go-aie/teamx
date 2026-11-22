package notification

import "sync"

// PlatformPause closes worker and delivery admission before signaling old
// workers. Done contains only work accepted before this pause, even if the
// subscription version changes or another caller later requests a restart.
// Concurrent pauses are independent; only the last release reopens admission.
type PlatformPause struct {
	manager *Manager
	ids     map[string]bool
	done    []<-chan struct{}
	once    sync.Once
}

func (m *Manager) PausePlatforms(ids ...string) *PlatformPause {
	p := &PlatformPause{manager: m, ids: map[string]bool{}}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.platformPauses == nil {
		m.platformPauses = map[string]int{}
	}
	for _, id := range ids {
		if !p.ids[id] {
			p.ids[id] = true
			m.platformPauses[id]++
		}
	}
	for _, w := range m.workers {
		if p.ids[w.platformID] {
			p.done = append(p.done, w.done)
			w.cancelled, w.restart = true, false
			w.cancel()
		}
	}
	// A worker can be waiting for an accepted delivery, and independent
	// deliveries may also be in flight. Both receipts belong to this pause.
	for _, f := range m.deliveries {
		if p.ids[f.platformID] {
			p.done = append(p.done, f.done)
		}
	}
	return p
}

func (p *PlatformPause) Done() []<-chan struct{} {
	return append([]<-chan struct{}(nil), p.done...)
}

// Release is an explicit recovery boundary. Invalid credentials stay paused;
// successful same-account recovery has already installed runnable state.
// A timed-out predecessor still owns its resources until its receipt closes.
func (p *PlatformPause) Release() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		m := p.manager
		states := m.allStates()
		m.mu.Lock()
		defer m.mu.Unlock()
		for id := range p.ids {
			m.platformPauses[id]--
			if m.platformPauses[id] == 0 {
				delete(m.platformPauses, id)
			}
		}
		for key, s := range states {
			meta := s.state.Metadata()
			if !p.ids[meta.PlatformID] || m.platformPauses[meta.PlatformID] > 0 || s.state.QueryPaused() {
				continue
			}
			if w := m.workers[key]; w != nil && w.cancelled {
				w.restart = true
			}
			m.startLocked(key, meta)
		}
	})
}

func (m *Manager) platformPaused(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.platformPauses[id] > 0
}
