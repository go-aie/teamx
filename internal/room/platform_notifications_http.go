package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type notificationView struct {
	PlatformID      string                   `json:"platform_id,omitempty"`
	Target          notification.Target      `json:"target"`
	TargetErrorCode string                   `json:"target_error_code,omitempty"`
	RuntimePaused   bool                     `json:"runtime_paused"`
	Account         platform.AccountIdentity `json:"account,omitzero"`
	Lark            *larkDiagnostics         `json:"lark,omitempty"`
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
	Baseline          time.Time                `json:"baseline,omitzero"`
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
		m.setNotificationTargetViewLocked(&v, s.Config.Target)
		v.PlatformID = s.Binding.PlatformID
		v.Baseline = s.Baseline
		v.Instance, v.RecipientID, v.Username, v.Retired = s.Binding.URL, s.UserID, s.Username, s.Retired
		v.Project = &platform.ProjectBinding{Provider: "gitlab", URL: s.Binding.URL, Project: s.Binding.Project, ProjectID: s.Binding.ProjectID, PlatformID: s.Binding.PlatformID}
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
		m.setNotificationTargetViewLocked(&v, s.Config.Target)
		v.PlatformID = s.Binding.PlatformID
		v.Baseline = s.Baseline
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
	// Only saved subscriptions belong in the summary; new settings are selected
	// explicitly by platform and target. Keep source order stable.
	entry, err := m.workspaceRoom(owner)
	if err != nil || entry.Status != "ready" {
		return nil, ErrIdentity
	}
	out := []notificationView{}
	for _, source := range []notification.SourceID{gitlab.EventsSourceID, gitlab.TodosSourceID, lark.MessagesSourceID} {
		for _, state := range m.notifications.StatesLocked(owner) {
			if state.Metadata().Key.Source != source || state.Metadata().Retired {
				continue
			}
			var v notificationView
			var err error
			switch s := state.(type) {
			case gitlab.EventsState:
				v, err = m.eventsNotificationViewLocked(owner, &s)
			case gitlab.TodosState:
				v, err = m.todosNotificationViewLocked(owner, &s)
			case lark.State:
				v, err = m.larkNotificationViewLocked(owner, &s)
			default:
				return nil, ErrInvalid
			}
			if err != nil {
				return nil, err
			}
			v.PlatformID = state.Metadata().PlatformID
			out = append(out, v)
		}
	}
	return out, nil
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
		owner := RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}
		target, err := notificationQueryTarget(q, false)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		// A saved message can locate its original (including retired) source
		// by the full physical key. Never navigate by provider alone.
		sourceSelected := q.Has("subscription_id") || q.Has("provider") || q.Has("channel")
		if sourceSelected && (!validText(q.Get("subscription_id")) || !validText(q.Get("provider")) || !validText(q.Get("channel"))) {
			writeAPIError(w, ErrRequest)
			return
		}
		id := q.Get("platform_id")
		if target != nil {
			for _, v := range append(append([]notificationView{}, views...), retired...) {
				if (id == "" || v.PlatformID == id) && v.Target.ScopeKey() == target.ScopeKey() && v.Target.Canonical() != *target {
					writeAPIError(w, ErrIdentity)
					return
				}
			}
		}
		matches := func(v notificationView) bool {
			return (id == "" || v.PlatformID == id) && (target == nil || v.Target.Canonical() == *target) &&
				(!sourceSelected || v.SubscriptionID == q.Get("subscription_id") && v.Provider == q.Get("provider") && v.Channel == q.Get("channel"))
		}
		views = filterNotificationViews(views, matches)
		retired = filterNotificationViews(retired, matches)
		if sourceSelected && len(views)+len(retired) != 1 {
			writeAPIError(w, ErrIdentity)
			return
		}
		platformViews := []gitlabNotificationView{}
		for _, scope := range m.gitlabNotificationScopesLocked(owner) {
			if sourceSelected && (len(views) != 1 || views[0].PlatformID != scope.PlatformID || views[0].Target.ScopeKey() != scope.Target) {
				continue
			}
			if id != "" && scope.PlatformID != id || target != nil && scope.Target != target.ScopeKey() {
				continue
			}
			t := m.gitlabNotificationScopeTargetLocked(owner, scope)
			if target != nil && t != *target {
				writeAPIError(w, ErrIdentity)
				return
			}
			v, err := m.gitlabNotificationViewForTargetLocked(owner, scope.PlatformID, t)
			if err != nil {
				writeAPIError(w, err)
				return
			}
			platformViews = append(platformViews, v)
		}
		var single *gitlabNotificationView
		if len(platformViews) == 1 {
			single = &platformViews[0]
		}
		_ = json.NewEncoder(w).Encode(struct {
			Subscriptions   []notificationView       `json:"subscriptions"`
			Retired         []notificationView       `json:"retired_subscriptions"`
			GitLab          *gitlabNotificationView  `json:"gitlab,omitempty"`
			GitLabPlatforms []gitlabNotificationView `json:"gitlab_platforms"`
			LarkDefaults    lark.Config              `json:"lark_defaults"`
		}{views, retired, single, platformViews, m.defaultLarkConfig("")})
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
		SubscriptionID string               `json:"subscription_id"`
		Caller         Caller               `json:"caller"`
		Version        uint64               `json:"subscription_version"`
		Target         *notification.Target `json:"target,omitempty"`
		Config         json.RawMessage      `json:"config,omitempty"`
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
		if req.Target != nil || !notificationConfigHasTarget(req.Config) {
			writeAPIError(w, ErrRequest)
			return
		}
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
		if req.Target == nil || req.Target.Mode == "" {
			writeAPIError(w, ErrRequest)
			return
		}
		err = m.syncLarkSubscription(r.Context(), key, req.Version, *req.Target)
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

// A single-settings request must explicitly select a destination. Only the
// summary endpoint permits an absent target, meaning all saved scopes.
func notificationQueryTarget(q url.Values, required bool) (*notification.Target, error) {
	mode, present := q["target_mode"]
	if !present {
		if required || q.Has("agent_id") || q.Has("membership_id") {
			return nil, ErrRequest
		}
		return nil, nil
	}
	if len(mode) != 1 || mode[0] == "" || len(q["agent_id"]) > 1 || len(q["membership_id"]) > 1 {
		return nil, ErrRequest
	}
	target, err := (notification.Target{Mode: mode[0], AgentID: q.Get("agent_id"), MembershipID: q.Get("membership_id")}).Normalize()
	if err != nil {
		return nil, notificationRoomError(err)
	}
	return &target, nil
}
func notificationConfigHasTarget(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	var target notification.Target
	data, ok := fields["target"]
	return ok && !bytes.Equal(bytes.TrimSpace(data), []byte("null")) && json.Unmarshal(data, &target) == nil && target.Mode != "" && target.Valid()
}
func filterNotificationViews(views []notificationView, matches func(notificationView) bool) []notificationView {
	out := []notificationView{}
	for _, v := range views {
		if matches(v) {
			out = append(out, v)
		}
	}
	return out
}
func (m *roomManager) setNotificationTargetViewLocked(v *notificationView, target notification.Target) {
	v.Target = target.Canonical()
	if v.Target.Mode == "" {
		v.Target.Mode = "room"
	}
	if err := m.checkNotificationTargetLocked(context.Background(), v.RoomIdentity, target); err != nil {
		v.TargetErrorCode = "target_unavailable"
		if errors.Is(err, ErrIdentity) {
			v.TargetErrorCode = "target_removed"
		}
		v.RuntimePaused = true
	}
}

func (v notificationView) MarshalJSON() ([]byte, error) {
	type wire notificationView
	switch c := v.Config.(type) {
	case gitlab.EventsConfig:
		if !c.Target.Inbox() {
			c.Target.Mode = "room"
		}
		v.Config = c
	case gitlab.TodosConfig:
		if !c.Target.Inbox() {
			c.Target.Mode = "room"
		}
		v.Config = c
	}
	return json.Marshal(wire(v))
}
func (v gitlabNotificationView) MarshalJSON() ([]byte, error) {
	type wire gitlabNotificationView
	if !v.Config.Target.Inbox() {
		v.Config.Target.Mode = "room"
	}
	return json.Marshal(wire(v))
}
