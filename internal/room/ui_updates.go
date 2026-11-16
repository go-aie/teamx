package room

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	uiUpdateMaxFrame              = 4 << 10
	uiUpdateMaxKeys               = 64
	uiUpdateMaxBytes              = 64 << 10
	uiUpdateMaxConnections        = 64
	uiUpdateMaxSessionConnections = 8
)

// uiUpdate is an invalidation key, never a business result or a payload. The
// optional observer is called only after state installation; it cannot fail a
// commit or query its producer. Operator is routing metadata, excluded from SSE.
type uiUpdate struct {
	Topic            string `json:"topic"`
	Kind             string `json:"kind,omitempty"`
	RoomID           string `json:"room_id,omitempty"`
	RoomInstance     string `json:"room_instance,omitempty"`
	RoomServiceEpoch string `json:"room_service_epoch,omitempty"`
	AgentID          string `json:"agent_id,omitempty"`
	MembershipID     string `json:"membership_id,omitempty"`
	Operator         string `json:"-"`
}

func (u uiUpdate) valid() bool {
	if u.Kind != "" && (u.Topic != "notifications" || u.Kind != "summary" && u.Kind != "diagnostics") {
		return false
	}
	switch u.Topic {
	case "settings", "catalog", "platforms", "models", "authorization":
		if u.RoomID != "" || u.RoomInstance != "" || u.RoomServiceEpoch != "" || u.AgentID != "" || u.MembershipID != "" {
			return false
		}
	case "messages", "members", "workspace", "notifications", "tools", "human_read", "interactions", "activity", "inbox", "policy":
		if u.RoomID == "" || u.RoomInstance == "" || u.RoomServiceEpoch == "" {
			return false
		}
	default:
		return false
	}
	if (u.AgentID == "") != (u.MembershipID == "") {
		return false
	}
	for _, value := range []string{u.RoomID, u.RoomInstance, u.RoomServiceEpoch, u.AgentID, u.MembershipID, u.Operator} {
		if len(value) > 256 {
			return false
		}
	}
	return true
}

func roomUIUpdate(topic string, info ServiceInfo) uiUpdate {
	return uiUpdate{Topic: topic, RoomID: info.RoomID, RoomInstance: info.RoomInstance, RoomServiceEpoch: info.ServiceEpoch}
}

type uiUpdateSession struct {
	access *serverAccess
	hash   [32]byte
}

// One bounded process-local center is shared by managed services. Mark only
// holds this memory gate; network writers and all business reads run outside it.
type uiUpdateHub struct {
	mu        sync.Mutex
	listeners atomic.Int32
	closed    bool
	subs      map[*uiUpdateSubscription]struct{}
}

type uiUpdateSubscription struct {
	hub       *uiUpdateHub
	session   uiUpdateSession
	operator  string
	room      ServiceInfo
	wake      chan struct{}
	done      chan struct{}
	pending   map[uiUpdate]struct{}
	bytes     int
	resync    bool
	deadlines map[uiUpdate]*uiUpdateDeadline
}

func newUIUpdateHub() *uiUpdateHub {
	return &uiUpdateHub{subs: make(map[*uiUpdateSubscription]struct{})}
}

func (h *uiUpdateHub) subscribe(session uiUpdateSession, operator string, room ServiceInfo) (*uiUpdateSubscription, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, ErrClosing
	}
	count := 0
	for s := range h.subs {
		if s.session == session {
			count++
		}
	}
	if len(h.subs) >= uiUpdateMaxConnections || count >= uiUpdateMaxSessionConnections {
		return nil, ErrBusy
	}
	s := &uiUpdateSubscription{hub: h, session: session, operator: operator, room: room,
		wake: make(chan struct{}, 1), done: make(chan struct{}), pending: make(map[uiUpdate]struct{}), deadlines: make(map[uiUpdate]*uiUpdateDeadline)}
	h.subs[s] = struct{}{}
	h.listeners.Add(1)
	return s, nil
}

func (h *uiUpdateHub) observesRoom(info ServiceInfo) bool {
	if h == nil || h.listeners.Load() == 0 {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		if sub.room == info {
			return true
		}
	}
	return false
}

func (h *uiUpdateHub) mark(u uiUpdate) {
	if h == nil || !u.valid() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if u.Operator != "" && u.Operator != s.operator {
			continue
		}
		if u.RoomID != "" && (u.RoomID != s.room.RoomID || u.RoomInstance != s.room.RoomInstance || u.RoomServiceEpoch != s.room.ServiceEpoch) {
			continue
		}
		if !s.resync {
			// The same subscription key owns both categories. Summary wins in
			// either arrival order, including legacy events with no kind.
			if u.Topic == "notifications" {
				diagnostic, summary, legacy := u, u, u
				diagnostic.Kind, summary.Kind, legacy.Kind = "diagnostics", "summary", ""
				if u.Kind == "diagnostics" {
					if _, ok := s.pending[summary]; ok {
						continue
					}
					if _, ok := s.pending[legacy]; ok {
						continue
					}
				} else {
					if _, ok := s.pending[diagnostic]; ok {
						delete(s.pending, diagnostic)
						s.bytes -= diagnostic.budgetBytes()
					}
					other := legacy
					if u.Kind == "" {
						other = summary
					}
					if _, ok := s.pending[other]; ok {
						continue
					}
				}
			}
			if _, found := s.pending[u]; !found {
				// Conservative JSON escaping and envelope allowance, without encoding
				// details or allocating frames on the producer's commit path.
				size := u.budgetBytes()
				if size > uiUpdateMaxFrame || len(s.pending) >= uiUpdateMaxKeys || s.bytes+size > uiUpdateMaxBytes {
					clear(s.pending)
					s.bytes = 0
					s.resync = true
				} else {
					s.pending[u] = struct{}{}
					s.bytes += size
				}
			}
		}
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}

// Connection changes affect the bindings/projections of subscribed Rooms only.
// Bounded by live streams, never by the number of persisted or open Rooms.
func (h *uiUpdateHub) markSubscribedRooms(topics ...string) {
	if h == nil {
		return
	}
	h.mu.Lock()
	rooms := make(map[ServiceInfo]struct{})
	for s := range h.subs {
		if s.room.RoomID != "" {
			rooms[s.room] = struct{}{}
		}
	}
	h.mu.Unlock()
	for info := range rooms {
		for _, topic := range topics {
			h.mark(roomUIUpdate(topic, info))
		}
	}
}

func (u uiUpdate) budgetBytes() int {
	return 512 + 6*(len(u.Topic)+len(u.Kind)+len(u.RoomID)+len(u.RoomInstance)+len(u.RoomServiceEpoch)+len(u.AgentID)+len(u.MembershipID))
}

// Take one key per write, so a slow writer cannot retain an entire drained
// batch in addition to its bounded queue. Only one small frame is in flight.
func (s *uiUpdateSubscription) next() (uiUpdate, bool, bool) {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	if s.resync {
		s.resync = false
		return uiUpdate{}, true, true
	}
	for u := range s.pending {
		delete(s.pending, u)
		s.bytes -= u.budgetBytes()
		return u, false, true
	}
	return uiUpdate{}, false, false
}

func (s *uiUpdateSubscription) close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	if _, exists := s.hub.subs[s]; exists {
		delete(s.hub.subs, s)
		s.hub.listeners.Add(-1)
		s.stopDeadlinesLocked()
		close(s.done)
	}
}

func (h *uiUpdateHub) close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.listeners.Store(0)
	for s := range h.subs {
		delete(h.subs, s)
		s.stopDeadlinesLocked()
		close(s.done)
	}
}

type uiUpdateDeadline struct {
	timer *time.Timer
	at    time.Time
}

func (s *uiUpdateSubscription) stopDeadlinesLocked() {
	for _, timer := range s.deadlines {
		timer.timer.Stop()
	}
	clear(s.deadlines)
}

// One one-shot wake per resource and live subscription. Reads register the next
// deadline; producers never scan policies or query authentication from here.
func (h *uiUpdateHub) at(u uiUpdate, deadline time.Time) {
	h.scheduleDeadline(u, deadline, false)
}

// Several operations/connections may share one public topic key. Keep their
// earliest wake; reconciliation registers the next remaining deadline.
func (h *uiUpdateHub) atEarliest(u uiUpdate, deadline time.Time) {
	h.scheduleDeadline(u, deadline, true)
}

func (h *uiUpdateHub) scheduleDeadline(u uiUpdate, deadline time.Time, earliest bool) {
	if h == nil || !u.valid() {
		return
	}
	if u.budgetBytes() > uiUpdateMaxFrame {
		h.mark(u)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		if u.Operator != "" && u.Operator != s.operator || u.RoomID != "" && (u.RoomID != s.room.RoomID || u.RoomInstance != s.room.RoomInstance || u.RoomServiceEpoch != s.room.ServiceEpoch) {
			continue
		}
		if timer := s.deadlines[u]; timer != nil {
			if earliest && !deadline.IsZero() && !deadline.Before(timer.at) {
				continue
			}
			timer.timer.Stop()
			delete(s.deadlines, u)
		}
		if deadline.IsZero() {
			continue
		}
		if len(s.deadlines) >= uiUpdateMaxKeys {
			// Reconciliation is bounded by the same resource budget as delivery.
			s.resync = true
			clear(s.pending)
			s.bytes = 0
			select {
			case s.wake <- struct{}{}:
			default:
			}
			continue
		}
		entry := &uiUpdateDeadline{at: deadline}
		entry.timer = time.AfterFunc(time.Until(deadline), func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if s.deadlines[u] != entry {
				return
			}
			delete(s.deadlines, u)
			// The key was budgeted at registration. Coalesce with intervening writes.
			if _, exists := s.pending[u]; !exists && !s.resync {
				if len(s.pending) >= uiUpdateMaxKeys || s.bytes+u.budgetBytes() > uiUpdateMaxBytes {
					s.resync = true
					clear(s.pending)
					s.bytes = 0
				} else {
					s.pending[u] = struct{}{}
					s.bytes += u.budgetBytes()
				}
			}
			select {
			case s.wake <- struct{}{}:
			default:
			}
		})
		s.deadlines[u] = entry
	}
}
