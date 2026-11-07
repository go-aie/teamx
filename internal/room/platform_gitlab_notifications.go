package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

const maxGitLabConfigBytes = 32 << 10

// Desired user settings have one durable owner. Source snapshots keep the
// configuration accepted with their progress and frozen item routes.
type gitlabNotificationRecord struct {
	RoomIdentity
	Schema  int                       `json:"schema"`
	Version uint64                    `json:"version"`
	Config  gitlab.NotificationConfig `json:"config"`
	Errors  map[string]string         `json:"errors,omitempty"`
}

type gitlabNotificationView struct {
	Version  uint64                    `json:"version"`
	Config   gitlab.NotificationConfig `json:"config"`
	Events   notificationView          `json:"events"`
	Todos    notificationView          `json:"todos"`
	Pending  map[string]bool           `json:"pending"`
	Errors   map[string]string         `json:"errors"`
	Applying bool                      `json:"applying"`
}

func (m *roomManager) gitlabNotificationPath(owner RoomIdentity) string {
	return filepath.Join(m.roomDir(owner.RoomID), "gitlab-notifications.json")
}

func gitlabRetired(state notification.State) bool {
	switch s := state.(type) {
	case gitlab.EventsState:
		return s.Retired
	case gitlab.TodosState:
		return s.Retired
	}
	return true
}

func (m *roomManager) loadGitLabNotificationsLocked(owner RoomIdentity) error {
	var saved gitlabNotificationRecord
	err := readSnapshot(m.gitlabNotificationPath(owner), &saved, maxGitLabConfigBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cfg, err := gitlab.NormalizeNotificationConfig(saved.Config)
	if err != nil || saved.Schema != 1 || saved.Version == 0 || saved.RoomIdentity != owner || !reflect.DeepEqual(cfg, saved.Config) {
		return ErrInvalid
	}
	m.gitlabNotifications[owner] = saved
	return nil
}

func (m *roomManager) gitlabNotificationViewLocked(owner RoomIdentity) (gitlabNotificationView, error) {
	views, err := m.notificationViewsLocked(owner)
	if err != nil {
		return gitlabNotificationView{}, err
	}
	v := gitlabNotificationView{Pending: map[string]bool{}, Errors: map[string]string{}, Applying: m.gitlabConfiguring[owner]}
	for _, view := range views {
		if view.SourceID == gitlab.EventsSourceID {
			v.Events = view
		}
		if view.SourceID == gitlab.TodosSourceID {
			v.Todos = view
		}
	}
	if saved, ok := m.gitlabNotifications[owner]; ok {
		v.Version, v.Config = saved.Version, saved.Config
		if saved.Errors != nil {
			v.Errors = maps.Clone(saved.Errors)
		}
		for _, channel := range []string{"events", "todos"} {
			v.Pending[channel] = !m.gitlabSettingsAppliedLocked(owner, saved.Config, channel)
		}
		return v, nil
	}
	// Existing subscriptions are read only until the first explicit unified save.
	// An inconsistent account requires a selection; no read changes a baseline.
	events, todos := v.Events.Config.(gitlab.EventsConfig), v.Todos.Config.(gitlab.TodosConfig)
	v.Config.Events, v.Config.Todos = gitlab.EventSettingsFrom(events), gitlab.TodoSettingsFrom(todos)
	type common struct {
		connection string
		target     notification.Target
	}
	var used []common
	if events.Enabled || !todos.Enabled && v.Events.SubscriptionID != "" {
		used = append(used, common{events.ConnectionID, events.Target})
	}
	if todos.Enabled || !events.Enabled && v.Todos.SubscriptionID != "" {
		used = append(used, common{todos.ConnectionID, todos.Target})
	}
	if len(used) > 0 {
		v.Config.ConnectionID, v.Config.Target = used[0].connection, used[0].target
		for _, c := range used[1:] {
			if c.connection != v.Config.ConnectionID || c.target != v.Config.Target {
				v.Config.ConnectionID = ""
				v.Errors["config"] = "settings_mismatch"
			}
		}
	}
	cfg, normalizeErr := gitlab.NormalizeNotificationConfig(v.Config)
	if normalizeErr == nil {
		v.Config = cfg
	}
	return v, nil
}

func (m *roomManager) gitlabSettingsAppliedLocked(owner RoomIdentity, cfg gitlab.NotificationConfig, channel string) bool {
	for _, state := range m.notifications.StatesLocked(owner) {
		if state.Metadata().Key.Source.Provider != "gitlab" || state.Metadata().Key.Source.Channel != channel || gitlabRetired(state) {
			continue
		}
		return m.gitlabStateAppliedLocked(owner, cfg, state)
	}
	return channel == "events" && !cfg.Events.Enabled || channel == "todos" && !cfg.Todos.Enabled
}

func (m *roomManager) gitlabStateAppliedLocked(owner RoomIdentity, cfg gitlab.NotificationConfig, state notification.State) bool {
	if !gitlabConfigApplied(cfg, state) {
		return false
	}
	switch s := state.(type) {
	case gitlab.EventsState:
		if !cfg.Events.Enabled {
			return !s.Config.Enabled
		}
		account, _, err := m.resolvePlatformAccountLocked(owner, cfg.ConnectionID, "gitlab")
		return err == nil && account.UserID == s.UserID && account.Generation == s.Binding.ConnectionGeneration && account.URL == s.Binding.URL
	case gitlab.TodosState:
		if !cfg.Todos.Enabled {
			return !s.Config.Enabled
		}
		account, _, err := m.resolvePlatformAccountLocked(owner, cfg.ConnectionID, "gitlab")
		if err != nil || account.UserID != s.Binding.UserID || account.Generation != s.Binding.ConnectionGeneration || account.URL != s.Binding.URL {
			return false
		}
		project, err := m.todosProjectLocked(owner, cfg.TodoConfig(), account.URL)
		return err == nil && reflect.DeepEqual(project, s.Project)
	}
	return false
}

func gitlabConfigApplied(cfg gitlab.NotificationConfig, state notification.State) bool {
	if gitlabRetired(state) {
		return false
	}
	switch s := state.(type) {
	case gitlab.EventsState:
		if !cfg.Events.Enabled {
			return !s.Config.Enabled
		}
		return reflect.DeepEqual(s.Config, cfg.EventConfig())
	case gitlab.TodosState:
		if !cfg.Todos.Enabled {
			return !s.Config.Enabled
		}
		return reflect.DeepEqual(s.Config, cfg.TodoConfig())
	}
	return false
}

func (m *roomManager) configureGitLabNotifications(ctx context.Context, owner RoomIdentity, version uint64, cfg gitlab.NotificationConfig) (gitlabNotificationView, error) {
	m.mu.Lock()
	if m.closing || m.roomBusyLocked(owner) || m.gitlabConfiguring[owner] {
		m.mu.Unlock()
		return gitlabNotificationView{}, ErrBusy
	}
	if err := m.store.check(true); err != nil {
		m.mu.Unlock()
		return gitlabNotificationView{}, err
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return gitlabNotificationView{}, err
	}
	if entry, err := m.workspaceRoom(owner); err != nil || entry.Status != "ready" {
		m.mu.Unlock()
		return gitlabNotificationView{}, ErrIdentity
	}
	if m.gitlabNotifications[owner].Version != version {
		m.mu.Unlock()
		return gitlabNotificationView{}, ErrConflict
	}
	if cfg.Events.Enabled || cfg.Todos.Enabled {
		account, _, err := m.resolvePlatformAccountLocked(owner, cfg.ConnectionID, "gitlab")
		if err != nil {
			m.mu.Unlock()
			return gitlabNotificationView{}, notificationRoomError(err)
		}
		if cfg.Events.Enabled {
			cfg.Events.Project, err = eventsProjectPath(account.URL, cfg.Events.Project)
			if err != nil {
				m.mu.Unlock()
				return gitlabNotificationView{}, err
			}
		}
		if cfg.Todos.Enabled {
			if _, err = m.todosProjectLocked(owner, cfg.TodoConfig(), account.URL); err != nil {
				m.mu.Unlock()
				return gitlabNotificationView{}, notificationRoomError(err)
			}
		}
	}
	cfg, err := gitlab.NormalizeNotificationConfig(cfg)
	if err == nil {
		err = m.validateNotificationTargetLocked(ctx, owner, cfg.Target, cfg.Events.Enabled || cfg.Todos.Enabled)
	}
	if err != nil {
		m.mu.Unlock()
		return gitlabNotificationView{}, notificationRoomError(err)
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return gitlabNotificationView{}, err
	}
	saved := m.gitlabNotifications[owner]
	if saved.Version == 0 || !reflect.DeepEqual(saved.Config, cfg) {
		if saved.Version == ^uint64(0) {
			m.mu.Unlock()
			return gitlabNotificationView{}, ErrCapacity
		}
		saved = gitlabNotificationRecord{RoomIdentity: owner, Schema: 1, Version: saved.Version + 1, Config: cfg}
		if err = m.store.save(m.gitlabNotificationPath(owner), saved, maxGitLabConfigBytes); err != nil {
			m.mu.Unlock()
			return gitlabNotificationView{}, err
		}
		m.gitlabNotifications[owner] = saved
	}
	m.gitlabConfiguring[owner] = true
	var waits []<-chan struct{}
	for _, state := range m.notifications.StatesLocked(owner) {
		if state.Metadata().Key.Source.Provider != "gitlab" {
			continue
		}
		if m.gitlabStateAppliedLocked(owner, cfg, state) {
			continue
		}
		key := state.Metadata().Key
		if done := m.notifications.CancelLocked(key); done != nil {
			waits = append(waits, done)
		}
		waits = append(waits, m.cancelAttachmentCallsLocked(key)...)
	}
	m.mu.Unlock()
	defer m.finishGitLabConfiguration(owner)
	applyErrors := map[string]string{}
	if err = notification.Wait(ctx, waits); err != nil {
		applyErrors["config"] = "apply_interrupted"
	} else {
		for _, channel := range []string{"events", "todos"} {
			m.mu.Lock()
			applied := m.gitlabSettingsAppliedLocked(owner, cfg, channel)
			var key notification.Key
			var revision uint64
			for _, state := range m.notifications.StatesLocked(owner) {
				meta := state.Metadata()
				if meta.Key.Source.Provider == "gitlab" && meta.Key.Source.Channel == channel && !gitlabRetired(state) {
					key, revision = meta.Key, meta.Version
				}
			}
			m.mu.Unlock()
			if applied {
				continue
			}
			if key.Source.Provider == "" {
				key = notification.Key{Owner: owner, Source: notification.SourceID{Provider: "gitlab", Channel: channel}}
			}
			if channel == "events" {
				_, err = m.configureEventsSubscription(ctx, key, revision, cfg.EventConfig())
			} else {
				_, err = m.configureTodosSubscription(ctx, key, revision, cfg.TodoConfig())
			}
			if err != nil {
				applyErrors[channel] = "apply_failed"
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return gitlabNotificationView{}, ErrClosing
	}
	if entry, err := m.workspaceRoom(owner); err != nil || entry.Status != "ready" || m.gitlabNotifications[owner].Version != saved.Version {
		return gitlabNotificationView{}, ErrIdentity
	}
	for _, channel := range []string{"events", "todos"} {
		if m.gitlabSettingsAppliedLocked(owner, cfg, channel) {
			delete(applyErrors, channel)
		}
	}
	saved.Errors = applyErrors
	if err := m.store.save(m.gitlabNotificationPath(owner), saved, maxGitLabConfigBytes); err != nil {
		return gitlabNotificationView{}, err
	}
	m.gitlabNotifications[owner] = saved
	v, err := m.gitlabNotificationViewLocked(owner)
	v.Applying = false
	return v, err
}

func (m *roomManager) finishGitLabConfiguration(owner RoomIdentity) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.gitlabConfiguring, owner)
	for _, state := range m.notifications.StatesLocked(owner) {
		if state.Metadata().Key.Source.Provider == "gitlab" {
			m.notifications.EnsureLocked(state.Metadata().Key)
		}
	}
}

func (m *roomManager) syncGitLabNotifications(ctx context.Context, owner RoomIdentity, version uint64) (gitlabNotificationView, error) {
	m.mu.Lock()
	v, err := m.gitlabNotificationViewLocked(owner)
	if err == nil {
		err = m.store.check(true)
	}
	if err == nil && v.Version != version {
		err = ErrConflict
	}
	if err == nil && (m.closing || m.gitlabConfiguring[owner] || m.roomBusyLocked(owner)) {
		err = ErrBusy
	}
	if err == nil && (v.Pending["events"] || v.Pending["todos"]) {
		err = ErrConflict
	}
	if err != nil {
		m.mu.Unlock()
		return v, err
	}
	m.gitlabConfiguring[owner] = true
	m.mu.Unlock()
	defer m.finishGitLabConfiguration(owner)
	v.Errors = map[string]string{}
	for _, source := range []notificationView{v.Events, v.Todos} {
		enabled := source.SourceID == gitlab.EventsSourceID && v.Config.Events.Enabled || source.SourceID == gitlab.TodosSourceID && v.Config.Todos.Enabled
		if !enabled {
			continue
		}
		key := notification.Key{Owner: owner, Source: source.SourceID, SubscriptionID: source.SubscriptionID}
		if source.SourceID == gitlab.EventsSourceID {
			err = m.syncEventsSubscription(key, source.Version)
		} else {
			err = m.syncTodosSubscription(ctx, key, source.Version)
		}
		if err != nil {
			v.Errors[source.Channel] = "sync_failed"
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return gitlabNotificationView{}, ErrClosing
	}
	if saved, ok := m.gitlabNotifications[owner]; ok {
		saved.Errors = maps.Clone(v.Errors)
		if err := m.store.save(m.gitlabNotificationPath(owner), saved, maxGitLabConfigBytes); err != nil {
			return gitlabNotificationView{}, err
		}
		m.gitlabNotifications[owner] = saved
	}
	result, readErr := m.gitlabNotificationViewLocked(owner)
	result.Errors, result.Applying = v.Errors, false
	return result, readErr
}

func (m *roomManager) serveGitLabNotifications(w http.ResponseWriter, r *http.Request) {
	var owner RoomIdentity
	var version uint64
	var cfg gitlab.NotificationConfig
	if r.URL.Path == "/v1/platform/gitlab/notifications" {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		q := r.URL.Query()
		if q.Get("service_epoch") != m.epoch || !validText(q.Get("caller_id")) {
			writeAPIError(w, ErrIdentity)
			return
		}
		owner = RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}
		m.mu.Lock()
		defer m.mu.Unlock()
		if err := m.store.check(false); err != nil {
			writeAPIError(w, err)
			return
		}
		v, err := m.gitlabNotificationViewLocked(owner)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(v)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		RoomIdentity
		ServiceEpoch string                     `json:"service_epoch"`
		Caller       Caller                     `json:"caller"`
		Version      uint64                     `json:"version"`
		Config       *gitlab.NotificationConfig `json:"config,omitempty"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGitLabConfigBytes))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	owner, version = req.RoomIdentity, req.Version
	var v gitlabNotificationView
	var err error
	if r.URL.Path == "/v1/platform/gitlab/notifications/configure" {
		if req.Config == nil {
			writeAPIError(w, ErrRequest)
			return
		}
		cfg = *req.Config
		v, err = m.configureGitLabNotifications(r.Context(), owner, version, cfg)
	} else {
		if req.Config != nil {
			writeAPIError(w, ErrRequest)
			return
		}
		v, err = m.syncGitLabNotifications(r.Context(), owner, version)
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}
