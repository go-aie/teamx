package room

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const uiUpdateHeartbeat = 15 * time.Second
const uiUpdateWriteTimeout = 5 * time.Second
const uiUpdateMergeWindow = 100 * time.Millisecond

// Leases let session installation revoke streams immediately without calling a
// network writer under the authentication lock. Heartbeats recheck as a fallback.
type uiUpdateAccessLease struct {
	spaceID   string
	session   uiUpdateSession
	operator  string
	expires   time.Time
	anonymous bool
	done      chan struct{}
}

func (a *serverAccess) watchUIUpdates(r *http.Request) (*uiUpdateAccessLease, error) {
	// EventSource uses the existing browser session. Neither execution tokens
	// nor caller-supplied operator IDs grant a browser subscription.
	if a == nil || !a.ownsRequest(r) || r.Context().Value(accessContextKey{}) != a || r.Header.Get("Authorization") != "" {
		return nil, ErrIdentity
	}
	spaceAccess := a
	a = a.authority()
	a.mu.Lock()
	defer a.mu.Unlock()
	if spaceAccess.revoked.Load() {
		return nil, ErrIdentity
	}
	l := &uiUpdateAccessLease{spaceID: requestSpaceID(r), session: uiUpdateSession{access: a}, operator: requestOperator(r), anonymous: a.disabled, done: make(chan struct{})}
	if !a.disabled {
		cookie, err := r.Cookie(a.cookieName())
		if err != nil {
			return nil, ErrIdentity
		}
		l.session.hash = sha256.Sum256([]byte(cookie.Value))
		s, ok := a.sessions[l.session.hash]
		if !ok || s.Subject.UserID != l.operator || a.authorizeSubject(s.Subject) != nil || !a.now().Before(s.expires) {
			return nil, ErrIdentity
		}
		l.expires = s.expires
		if a.renewal != nil && s.renewal != nil {
			l.expires = a.renewal.usableUntil(l.session.hash, s)
		}
		if a.renewal != nil && (a.renewal.broken || a.renewal.closed || a.renewal.denied[l.session.hash]) || !a.now().Before(l.expires) {
			return nil, ErrIdentity
		}
	}
	if a.updateLeases == nil {
		a.updateLeases = make(map[*uiUpdateAccessLease]struct{})
	}
	a.updateLeases[l] = struct{}{}
	return l, nil
}

func (a *serverAccess) unwatchUIUpdates(l *uiUpdateAccessLease) {
	a = a.authority()
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.updateLeases, l)
}

// Caller holds a.mu. No callbacks, disk reads, or network writes.
func (a *serverAccess) revokeUIUpdatesLocked(all bool) {
	for l := range a.updateLeases {
		s, exists := a.sessions[l.session.hash]
		identityErr := a.authorizeSubject(s.Subject)
		if l.anonymous {
			identityErr = a.authorizeSubject(a.subject())
		}
		if exists && a.renewal != nil && s.renewal != nil && (l.expires.After(a.renewal.usableUntil(l.session.hash, s)) || !a.now().Before(l.expires)) {
			identityErr = ErrIdentity
		}
		valid := identityErr == nil && (l.anonymous && a.disabled || !l.anonymous && !a.disabled && exists && s.Subject.UserID == l.operator && a.now().Before(s.expires))
		if all || !valid {
			delete(a.updateLeases, l)
			close(l.done)
		}
	}
}

func parseUIUpdateRoom(r *http.Request, epoch string) (ServiceInfo, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return ServiceInfo{}, ErrRequest
	}
	for key, values := range q {
		if len(values) != 1 || len(values[0]) > 256 {
			return ServiceInfo{}, ErrRequest
		}
		switch key {
		case "service_epoch", "room_id", "room_instance", "room_service_epoch":
		default:
			return ServiceInfo{}, ErrRequest
		}
	}
	if q.Get("service_epoch") != epoch {
		return ServiceInfo{}, ErrIdentity
	}
	info := ServiceInfo{RoomIdentity: RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}, ServiceEpoch: q.Get("room_service_epoch")}
	if info != (ServiceInfo{}) && (info.RoomID == "" || info.RoomInstance == "" || info.ServiceEpoch == "") {
		return ServiceInfo{}, ErrIdentity
	}
	return info, nil
}

func (s *service) serveUIUpdates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	room, err := parseUIUpdateRoom(r, s.info.ServiceEpoch)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if room != (ServiceInfo{}) && room != s.info {
		writeAPIError(w, ErrIdentity)
		return
	}
	lease, err := s.access.watchUIUpdates(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	defer s.access.unwatchUIUpdates(lease)
	sub, err := s.updates.subscribe(lease.session, lease.operator, room)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	defer sub.close()
	serveUIUpdateStream(w, r, sub, lease, s.info.ServiceEpoch, s.updatesDone)
}

func (m *roomManager) serveUIUpdates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	room, err := parseUIUpdateRoom(r, m.epoch)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	lease, err := m.config.access.watchUIUpdates(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	defer m.config.access.unwatchUIUpdates(lease)
	// Validate and register under the runtime registry gate, then release it
	// before hello or any network work. Subscribing never opens a runtime.
	m.mu.Lock()
	var runtimeDone <-chan struct{}
	if m.closing {
		err = ErrClosing
	} else if room != (ServiceInfo{}) {
		svc, _ := m.runtimeServiceLocked(room.RoomIdentity)
		if svc == nil || svc.info != room || svc.closing.Load() {
			err = ErrIdentity
		} else {
			runtimeDone = svc.updatesDone
		}
	}
	var sub *uiUpdateSubscription
	if err == nil {
		sub, err = m.updates.subscribe(lease.session, lease.operator, room)
	}
	m.mu.Unlock()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	defer sub.close()
	serveUIUpdateStream(w, r, sub, lease, m.epoch, runtimeDone)
}

func serveUIUpdateStream(w http.ResponseWriter, r *http.Request, sub *uiUpdateSubscription, lease *uiUpdateAccessLease, epoch string, runtimeDone <-chan struct{}) {
	connection, err := newOpaqueID()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	rc := http.NewResponseController(w)
	// Fail before committing headers if this transport cannot enforce writes.
	if err := rc.SetWriteDeadline(time.Now().Add(uiUpdateWriteTimeout)); err != nil {
		writeAPIError(w, err)
		return
	}
	// Renewable browser authentication restores the ordinary five-second read
	// deadline. On HTTP/1 net/http's background read would hit it and cancel this
	// GET's context, even though SSE only writes. The stream owns its lifetime
	// through cancellation, authorization leases and bounded per-frame writes.
	if err := rc.SetReadDeadline(time.Time{}); err != nil {
		writeAPIError(w, err)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var expiry <-chan time.Time
	if !lease.expires.IsZero() {
		a := lease.session.access
		a.mu.Lock()
		remaining := lease.expires.Sub(a.now())
		a.mu.Unlock()
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		expiry = timer.C
	}
	// ResponseController may interrupt a blocked Write/Flush. Cancellation does
	// not wait for its five-second timeout or hold any business/authentication gate.
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-ctx.Done():
		case <-sub.done:
		case <-lease.done:
		case <-runtimeDone:
		case <-expiry:
		}
		cancel()
		_ = rc.SetWriteDeadline(time.Now())
	}()
	defer func() { cancel(); <-watchDone }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, value any) error {
		select {
		case <-sub.done:
			cancel()
		case <-lease.done:
			cancel()
		case <-runtimeDone:
			cancel()
		default:
		}
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		frame := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
		if len(frame) > uiUpdateMaxFrame {
			return ErrCapacity
		}
		if err := rc.SetWriteDeadline(time.Now().Add(uiUpdateWriteTimeout)); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := fmt.Fprint(w, frame); err != nil {
			return err
		}
		if err := rc.Flush(); err != nil {
			return err
		}
		return rc.SetWriteDeadline(time.Time{})
	}
	// subscribe already ran. Changes concurrent with hello remain pending and
	// cannot overtake it, including while hello itself is blocked in Flush.
	if send("hello", struct {
		Protocol       int               `json:"protocol"`
		ConnectionID   string            `json:"connection_id"`
		InstallationID string            `json:"installation_id"`
		SpaceID        string            `json:"space_id"`
		ServiceEpoch   string            `json:"service_epoch"`
		GlobalVersions map[string]string `json:"global_versions"`
	}{1, connection, requestInstallationID(r), requestSpaceID(r), epoch, sub.globalVersions}) != nil {
		return
	}
	heartbeat := time.NewTicker(uiUpdateHeartbeat)
	defer heartbeat.Stop()
	var merge <-chan time.Time
	var mergeTimer *time.Timer
	defer func() {
		if mergeTimer != nil {
			mergeTimer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			a := lease.session.access
			a.mu.Lock()
			a.revokeUIUpdatesLocked(false)
			a.mu.Unlock()
			select {
			case <-lease.done:
				return
			default:
			}
			if send("heartbeat", map[string]string{"connection_id": connection}) != nil {
				return
			}
		case <-sub.wake:
			if merge == nil {
				mergeTimer = time.NewTimer(uiUpdateMergeWindow)
				merge = mergeTimer.C
			}
		case <-merge:
			merge = nil
			for range uiUpdateMaxKeys {
				u, resync, ok := sub.next()
				if !ok {
					break
				}
				if resync {
					if send("resync", map[string]string{"connection_id": connection}) != nil {
						return
					}
					break
				}
				if send("invalidate", struct {
					ConnectionID string `json:"connection_id"`
					uiUpdate
				}{connection, u}) != nil {
					return
				}
			}
		}
	}
}
