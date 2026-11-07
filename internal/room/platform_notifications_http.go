package room

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type notificationView struct {
	Account platform.AccountIdentity `json:"account,omitzero"`
	Lark    *larkDiagnostics         `json:"lark,omitempty"`
	RoomIdentity
	notification.SourceID
	SubscriptionID    string                   `json:"subscription_id"`
	Available         bool                     `json:"available"`
	Version           uint64                   `json:"subscription_version"`
	Config            any                      `json:"config"`
	Status            string                   `json:"status"`
	Pending           int                      `json:"pending"`
	LastCheck         time.Time                `json:"last_check,omitzero"`
	LastFull          time.Time                `json:"last_full,omitzero"`
	RetryAt           time.Time                `json:"retry_at,omitzero"`
	ErrorCode         string                   `json:"error_code,omitempty"`
	DeliveryErrorCode string                   `json:"delivery_error_code,omitempty"`
	Gap               any                      `json:"gap,omitempty"`
	Metrics           notification.Metrics     `json:"metrics"`
	RecipientID       int64                    `json:"recipient_id,omitempty"`
	Username          string                   `json:"username,omitempty"`
	Instance          string                   `json:"instance,omitempty"`
	Retired           bool                     `json:"retired,omitempty"`
	Scan              *gitlab.TodosScan        `json:"scan,omitempty"`
	Skipped           map[string]int           `json:"skipped,omitempty"`
	Project           *platform.ProjectBinding `json:"project,omitempty"`
}

// Only public settings and bounded diagnostics cross the HTTP boundary.
// Query credentials, identity stamps and inbox bodies remain private.
func (m *roomManager) eventsNotificationViewLocked(owner RoomIdentity, state *gitlab.EventsState) (notificationView, error) {
	entry, err := m.workspaceRoom(owner)
	if err != nil || entry.Status != "ready" {
		return notificationView{}, ErrIdentity
	}
	v := notificationView{RoomIdentity: owner, SourceID: gitlab.EventsSourceID,
		Available: true, Status: "disabled", Config: gitlab.EventsConfig{IntervalSeconds: gitlab.DefaultIntervalSeconds}}
	if state != nil {
		s := *state
		v.Instance, v.RecipientID, v.Username, v.Retired = s.Binding.URL, s.UserID, s.Username, s.Retired
		v.Project = &platform.ProjectBinding{Provider: "gitlab", URL: s.Binding.URL, Project: s.Binding.Project, ProjectID: s.Binding.ProjectID, ConnectionID: s.Binding.ConnectionID}
		if s.ProjectID > 0 {
			v.Project.ProjectID = s.ProjectID
		}
		v.SubscriptionID, v.Version, v.Config, v.Status, v.Pending = s.SubscriptionID, s.SubscriptionVersion, s.Config, s.Status, len(s.Inbox)
		v.LastCheck, v.LastFull, v.RetryAt, v.ErrorCode, v.Metrics, v.DeliveryErrorCode = s.LastCheck, s.LastFull, s.RetryAt, s.ErrorCode, s.Metrics, s.DeliveryErrorCode
		if code := m.notificationTargetErrorLocked(owner, s.PendingItems()); code != "" {
			v.DeliveryErrorCode = code
		}
		if s.Gap != nil {
			v.Gap = s.Gap
		}
	}
	return v, nil
}
func (m *roomManager) todosNotificationViewLocked(owner RoomIdentity, state *gitlab.TodosState) (notificationView, error) {
	entry, err := m.workspaceRoom(owner)
	if err != nil || entry.Status != "ready" {
		return notificationView{}, ErrIdentity
	}
	v := notificationView{RoomIdentity: owner, SourceID: gitlab.TodosSourceID, Available: true, Status: "disabled", Config: gitlab.TodosConfig{IntervalSeconds: gitlab.DefaultIntervalSeconds}}
	if state != nil {
		s := *state
		v.SubscriptionID, v.Version, v.Config, v.Status, v.Pending = s.SubscriptionID, s.SubscriptionVersion, s.Config, s.Status, len(s.Inbox)
		v.LastCheck, v.LastFull, v.RetryAt, v.ErrorCode, v.Metrics, v.DeliveryErrorCode = s.LastCheck, s.LastFull, s.RetryAt, s.ErrorCode, s.Metrics, s.DeliveryErrorCode
		if code := m.notificationTargetErrorLocked(owner, s.PendingItems()); code != "" {
			v.DeliveryErrorCode = code
		}
		v.RecipientID, v.Username, v.Instance, v.Retired, v.Scan = s.Binding.UserID, s.Binding.Username, s.Binding.URL, s.Retired, s.Scan
		v.Skipped = map[string]int{}
		for _, o := range s.Observed {
			if o.Reason != "accepted" {
				v.Skipped[o.Reason]++
			}
		}
	}
	return v, nil
}
func (m *roomManager) notificationViewsLocked(owner RoomIdentity) ([]notificationView, error) {
	var events *gitlab.EventsState
	var todos *gitlab.TodosState
	var messages *lark.State
	for _, state := range m.notifications.StatesLocked(owner) {
		switch s := state.(type) {
		case lark.State:
			if s.Retired {
				continue
			}
			if messages != nil {
				return nil, ErrInvalid
			}
			messages = &s
		case gitlab.EventsState:
			if s.Retired {
				continue
			}
			if events != nil {
				return nil, ErrInvalid
			}
			events = &s
		case gitlab.TodosState:
			if s.Retired {
				continue
			}
			if todos != nil {
				return nil, ErrInvalid
			}
			todos = &s
		default:
			return nil, ErrInvalid
		}
	}
	ev, err := m.eventsNotificationViewLocked(owner, events)
	if err != nil {
		return nil, err
	}
	tv, err := m.todosNotificationViewLocked(owner, todos)
	if err != nil {
		return nil, err
	}
	lv, err := m.larkNotificationViewLocked(owner, messages)
	if err != nil {
		return nil, err
	}
	return []notificationView{ev, tv, lv}, nil
}
func (m *roomManager) retiredNotificationViewsLocked(owner RoomIdentity) ([]notificationView, error) {
	views := []notificationView{}
	for _, state := range m.notifications.StatesLocked(owner) {
		var view notificationView
		var err error
		switch s := state.(type) {
		case lark.State:
			if !s.Retired {
				continue
			}
			view, err = m.larkNotificationViewLocked(owner, &s)
		case gitlab.EventsState:
			if !s.Retired {
				continue
			}
			view, err = m.eventsNotificationViewLocked(owner, &s)
		case gitlab.TodosState:
			if !s.Retired {
				continue
			}
			view, err = m.todosNotificationViewLocked(owner, &s)
		default:
			continue
		}
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (m *roomManager) servePlatformNotifications(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/platform/notifications" {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		m.mu.Lock()
		defer m.mu.Unlock()
		if q.Get("service_epoch") != m.epoch || !validText(q.Get("caller_id")) {
			writeAPIError(w, ErrIdentity)
			return
		}
		if err := m.store.check(false); err != nil {
			writeAPIError(w, err)
			return
		}
		views, err := m.notificationViewsLocked(RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")})
		if err != nil {
			writeAPIError(w, err)
			return
		}
		retired, err := m.retiredNotificationViewsLocked(RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")})
		if err != nil {
			writeAPIError(w, err)
			return
		}
		gitlabView, err := m.gitlabNotificationViewLocked(RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")})
		if err != nil {
			writeAPIError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(struct {
			Subscriptions []notificationView     `json:"subscriptions"`
			Retired       []notificationView     `json:"retired_subscriptions"`
			GitLab        gitlabNotificationView `json:"gitlab"`
		}{views, retired, gitlabView})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ServiceEpoch string `json:"service_epoch"`
		RoomIdentity
		notification.SourceID
		SubscriptionID string          `json:"subscription_id"`
		Caller         Caller          `json:"caller"`
		Version        uint64          `json:"subscription_version"`
		Config         json.RawMessage `json:"config,omitempty"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	// GitLab configuration and sync have one provider-level authority, including
	// before the first save. Per-source writes cannot split the common interval.
	if req.SourceID != lark.MessagesSourceID {
		writeAPIError(w, ErrRequest)
		return
	}
	key := notification.Key{Owner: req.RoomIdentity, Source: req.SourceID, SubscriptionID: req.SubscriptionID}
	var err error
	if r.URL.Path == "/v1/platform/notifications/configure" {
		var cfg lark.Config
		decoder := json.NewDecoder(bytes.NewReader(req.Config))
		decoder.DisallowUnknownFields()
		if bytes.Equal(bytes.TrimSpace(req.Config), []byte("null")) || decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF {
			writeAPIError(w, ErrRequest)
			return
		}
		var next lark.State
		next, err = m.configureLarkSubscription(r.Context(), key, req.Version, cfg)
		if err == nil {
			key = next.Metadata().Key
		}
	} else {
		if len(req.Config) != 0 || req.SubscriptionID == "" {
			writeAPIError(w, ErrRequest)
			return
		}
		err = m.syncLarkSubscription(r.Context(), key, req.Version)
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.notifications.StateLocked(key)
	if !ok {
		writeAPIError(w, ErrIdentity)
		return
	}
	s, ok := state.(lark.State)
	if !ok {
		writeAPIError(w, ErrIdentity)
		return
	}
	view, err := m.larkNotificationViewLocked(req.RoomIdentity, &s)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(view)
}
