package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

const maxGitLabConfigBytes = 1 << 20
const gitlabNotificationSchema = 4

type platformNotificationKey struct {
	Owner      RoomIdentity
	PlatformID string
	Target     string
}

type gitlabNotificationFile struct {
	RoomIdentity
	Schema    int                                   `json:"schema"`
	Platforms map[string][]gitlabNotificationRecord `json:"platforms"`
}

// Desired user settings have one durable owner. Source snapshots keep the
// configuration accepted with their progress and frozen item routes.
type gitlabNotificationRecord struct {
	RoomIdentity
	Schema         int                       `json:"schema"`
	Version        uint64                    `json:"version"`
	Config         gitlab.NotificationConfig `json:"config"`
	Errors         map[string]string         `json:"errors,omitempty"`
	BackfillLocked bool                      `json:"backfill_locked,omitempty"`
}

type gitlabNotificationView struct {
	PlatformID     string                    `json:"platform_id"`
	Version        uint64                    `json:"version"`
	Config         gitlab.NotificationConfig `json:"config"`
	Events         notificationView          `json:"events"`
	Todos          notificationView          `json:"todos"`
	Pending        map[string]bool           `json:"pending"`
	Errors         map[string]string         `json:"errors"`
	Applying       bool                      `json:"applying"`
	BackfillLocked bool                      `json:"backfill_locked"`
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
	var raw struct {
		Schema int `json:"schema"`
	}
	path := m.gitlabNotificationPath(owner)
	data, err := readPrivateNotificationJSON(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if json.Unmarshal(data, &raw) != nil {
		return ErrInvalid
	}
	if raw.Schema != gitlabNotificationSchema {
		return fmt.Errorf("%w: GitLab notifications require offline upgrade: teamx notifications upgrade --rooms-dir ROOT", ErrInvalid)
	}
	var saved gitlabNotificationFile
	if err := readSnapshot(path, &saved, maxGitLabConfigBytes); err != nil {
		return err
	}
	records, err := indexGitLabNotifications(owner, saved)
	if err != nil {
		return err
	}
	if err := m.checkNotificationMembershipsLocked(owner, records); err != nil {
		return err
	}
	m.gitlabNotifications[owner] = records
	return nil
}

type notificationScope struct{ PlatformID, Target string }

func notificationScopeFor(id string, target notification.Target) notificationScope {
	return notificationScope{id, target.ScopeKey()}
}
func notificationApplyKey(owner RoomIdentity, id string, target notification.Target) platformNotificationKey {
	return platformNotificationKey{owner, id, target.ScopeKey()}
}
func indexGitLabNotifications(owner RoomIdentity, saved gitlabNotificationFile) (map[notificationScope]gitlabNotificationRecord, error) {
	if saved.Schema != gitlabNotificationSchema || saved.RoomIdentity != owner || saved.Platforms == nil {
		return nil, ErrInvalid
	}
	records := map[notificationScope]gitlabNotificationRecord{}
	agents := map[string]string{}
	for id, entries := range saved.Platforms {
		if len(entries) == 0 {
			return nil, ErrInvalid
		}
		for _, record := range entries {
			cfg, err := gitlab.NormalizeNotificationConfig(record.Config)
			if err != nil || cfg.PlatformID != id || record.Schema != gitlabNotificationSchema || record.Version == 0 || record.RoomIdentity != owner || !reflect.DeepEqual(cfg, record.Config) {
				return nil, ErrInvalid
			}
			if cfg.Target.Inbox() {
				if agent, ok := agents[cfg.Target.MembershipID]; ok && agent != cfg.Target.AgentID {
					return nil, ErrIdentity
				}
				agents[cfg.Target.MembershipID] = cfg.Target.AgentID
			}
			key := notificationScopeFor(id, cfg.Target)
			if prior, exists := records[key]; exists {
				if prior.Config.Target != cfg.Target {
					return nil, ErrIdentity
				}
				return nil, ErrConflict
			}
			records[key] = record
		}
	}
	return records, nil
}
func validateGitLabNotificationFile(owner RoomIdentity, saved gitlabNotificationFile) error {
	_, err := indexGitLabNotifications(owner, saved)
	return err
}
func gitlabNotificationFileFor(owner RoomIdentity, records map[notificationScope]gitlabNotificationRecord) gitlabNotificationFile {
	saved := gitlabNotificationFile{RoomIdentity: owner, Schema: gitlabNotificationSchema, Platforms: map[string][]gitlabNotificationRecord{}}
	for key, record := range records {
		saved.Platforms[key.PlatformID] = append(saved.Platforms[key.PlatformID], record)
	}
	for id := range saved.Platforms {
		sort.Slice(saved.Platforms[id], func(i, j int) bool {
			return saved.Platforms[id][i].Config.Target.ScopeKey() < saved.Platforms[id][j].Config.Target.ScopeKey()
		})
	}
	return saved
}
func (m *roomManager) saveGitLabNotificationLocked(owner RoomIdentity, record gitlabNotificationRecord) error {
	records := maps.Clone(m.gitlabNotifications[owner])
	if records == nil {
		records = map[notificationScope]gitlabNotificationRecord{}
	}
	key := notificationScopeFor(record.Config.PlatformID, record.Config.Target)
	if prior, ok := records[key]; ok && prior.Config.Target != record.Config.Target {
		return ErrIdentity
	}
	records[key] = record
	if err := m.checkNotificationMembershipsLocked(owner, records); err != nil {
		return err
	}
	saved := gitlabNotificationFileFor(owner, records)
	if err := validateGitLabNotificationFile(owner, saved); err != nil {
		return err
	}
	if err := m.store.save(m.gitlabNotificationPath(owner), saved, maxGitLabConfigBytes); err != nil {
		return err
	}
	m.gitlabNotifications[owner] = records
	return nil
}

func (m *roomManager) checkNotificationMembershipsLocked(owner RoomIdentity, records map[notificationScope]gitlabNotificationRecord) error {
	agents := map[string]string{}
	check := func(target notification.Target) error {
		if !target.Inbox() {
			return nil
		}
		if agent, exists := agents[target.MembershipID]; exists && agent != target.AgentID {
			return ErrIdentity
		}
		agents[target.MembershipID] = target.AgentID
		return nil
	}
	for _, record := range records {
		if err := check(record.Config.Target); err != nil {
			return err
		}
	}
	for _, state := range m.notifications.StatesLocked(owner) {
		if err := check(state.Metadata().Target); err != nil {
			return err
		}
	}
	return nil
}

func (m *roomManager) subscriptionPlatformLocked(key notification.Key, fallback string) string {
	if state, ok := m.notifications.StateLocked(key); ok {
		return state.Metadata().PlatformID
	}
	return fallback
}
func (m *roomManager) gitlabNotificationViewForTargetLocked(owner RoomIdentity, id string, target notification.Target) (gitlabNotificationView, error) {
	target, err := target.Normalize()
	if err != nil {
		return gitlabNotificationView{}, notificationRoomError(err)
	}
	events, err := m.currentEventsForTargetLocked(owner, id, target)
	if err != nil {
		return gitlabNotificationView{}, err
	}
	todos, err := m.currentTodosForTargetLocked(owner, id, target)
	if err != nil {
		return gitlabNotificationView{}, err
	}
	ev, err := m.eventsNotificationViewLocked(owner, events)
	if err != nil {
		return gitlabNotificationView{}, err
	}
	tv, err := m.todosNotificationViewLocked(owner, todos)
	if err != nil {
		return gitlabNotificationView{}, err
	}
	ev.PlatformID, tv.PlatformID = id, id
	m.setNotificationTargetViewLocked(&ev, target)
	m.setNotificationTargetViewLocked(&tv, target)
	v := gitlabNotificationView{PlatformID: id, Events: ev, Todos: tv, Pending: map[string]bool{}, Errors: map[string]string{}, Applying: m.gitlabConfiguring[notificationApplyKey(owner, id, target)]}
	if saved, ok := m.gitlabNotifications[owner][notificationScopeFor(id, target)]; ok {
		if saved.Config.Target != target {
			return gitlabNotificationView{}, ErrIdentity
		}
		v.Version, v.Config, v.BackfillLocked = saved.Version, saved.Config, saved.BackfillLocked || events != nil || todos != nil
		if saved.Errors != nil {
			v.Errors = maps.Clone(saved.Errors)
		}
		for _, channel := range []string{"events", "todos"} {
			v.Pending[channel] = !m.gitlabSettingsAppliedLocked(owner, saved.Config, channel)
		}
		return v, nil
	}
	// Legacy per-channel snapshots remain the source of truth until explicitly
	// saved or upgraded. No first-use default is applied to existing progress.
	if events == nil && todos == nil {
		v.Config = gitlab.DefaultNotificationConfig(id)
		v.Config.Target = target
		return v, nil
	}
	v.BackfillLocked = true
	v.Config.Enabled = true
	v.Config.PlatformID = id
	v.Config.IntervalSeconds = gitlab.DefaultIntervalSeconds
	if events != nil {
		c := events.Config
		v.Config.Events = gitlab.EventSettingsFrom(c)
		v.Config.Target, v.Config.IntervalSeconds = c.Target, c.IntervalSeconds
		v.Config.BackfillSeconds, v.Config.BackfillStart = c.BackfillSeconds, c.BackfillStart
	}
	if todos != nil {
		c := todos.Config
		v.Config.Todos = gitlab.TodoSettingsFrom(c)
		if events == nil {
			v.Config.Target, v.Config.IntervalSeconds = c.Target, c.IntervalSeconds
			v.Config.BackfillSeconds, v.Config.BackfillStart = c.BackfillSeconds, c.BackfillStart
		} else if c.Target != v.Config.Target || c.IntervalSeconds != v.Config.IntervalSeconds || c.BackfillSeconds != v.Config.BackfillSeconds || !c.BackfillStart.Equal(v.Config.BackfillStart) {
			v.Errors["config"] = "settings_mismatch"
		}
	}
	if cfg, err := gitlab.NormalizeNotificationConfig(v.Config); err == nil {
		v.Config = cfg
	}
	return v, nil
}

func (m *roomManager) gitlabSettingsAppliedLocked(owner RoomIdentity, cfg gitlab.NotificationConfig, channel string) bool {
	for _, state := range m.notifications.StatesLocked(owner) {
		if state.Metadata().Key.Source.Provider != "gitlab" || state.Metadata().Key.Source.Channel != channel || state.Metadata().PlatformID != cfg.PlatformID || state.Metadata().Target != cfg.Target || gitlabRetired(state) {
			continue
		}
		return m.gitlabStateAppliedLocked(owner, cfg, state)
	}
	return channel == "events" && !cfg.EventConfig().Enabled || channel == "todos" && !cfg.TodoConfig().Enabled
}

func (m *roomManager) gitlabStateAppliedLocked(owner RoomIdentity, cfg gitlab.NotificationConfig, state notification.State) bool {
	if !gitlabConfigApplied(cfg, state) {
		return false
	}
	switch s := state.(type) {
	case gitlab.EventsState:
		if !cfg.EventConfig().Enabled {
			return !s.Config.Enabled
		}
		account, _, err := m.resolvePlatformAccountLocked(owner, cfg.PlatformID, "gitlab")
		return err == nil && account.UserID == s.UserID && account.Generation == s.Binding.PlatformGeneration && account.URL == s.Binding.URL
	case gitlab.TodosState:
		if !cfg.TodoConfig().Enabled {
			return !s.Config.Enabled
		}
		account, _, err := m.resolvePlatformAccountLocked(owner, cfg.PlatformID, "gitlab")
		if err != nil || account.UserID != s.Binding.UserID || account.Generation != s.Binding.PlatformGeneration || account.URL != s.Binding.URL {
			return false
		}
		return true
	}
	return false
}

func gitlabConfigApplied(cfg gitlab.NotificationConfig, state notification.State) bool {
	if gitlabRetired(state) || state.Metadata().PlatformID != cfg.PlatformID || state.Metadata().Target != cfg.Target {
		return false
	}
	switch s := state.(type) {
	case gitlab.EventsState:
		if !cfg.EventConfig().Enabled {
			return !s.Config.Enabled
		}
		expected := cfg.EventConfig()
		if cfg.BackfillStart.IsZero() {
			expected.BackfillSeconds, expected.BackfillStart = s.Config.BackfillSeconds, s.Config.BackfillStart
		}
		actual := s.Config
		actual.Target = actual.Target.Canonical()
		return reflect.DeepEqual(actual, expected)
	case gitlab.TodosState:
		if !cfg.TodoConfig().Enabled {
			return !s.Config.Enabled
		}
		expected := cfg.TodoConfig()
		if cfg.BackfillStart.IsZero() {
			expected.BackfillSeconds, expected.BackfillStart = s.Config.BackfillSeconds, s.Config.BackfillStart
		}
		actual := s.Config
		actual.Target = actual.Target.Canonical()
		return reflect.DeepEqual(actual, expected)
	}
	return false
}

func (m *roomManager) configureGitLabNotifications(ctx context.Context, owner RoomIdentity, version uint64, cfg gitlab.NotificationConfig) (gitlabNotificationView, error) {
	var normalizeErr error
	cfg.Target, normalizeErr = cfg.Target.Normalize()
	if normalizeErr != nil {
		return gitlabNotificationView{}, notificationRoomError(normalizeErr)
	}
	m.mu.Lock()
	prior, exists := m.gitlabNotifications[owner][notificationScopeFor(cfg.PlatformID, cfg.Target)]
	if !exists {
		if err := m.checkNotificationTargetLocked(ctx, owner, cfg.Target); err != nil {
			m.mu.Unlock()
			return gitlabNotificationView{}, err
		}
	}
	if exists && prior.Config.Target != cfg.Target {
		m.mu.Unlock()
		return gitlabNotificationView{}, ErrIdentity
	}
	if err := m.checkOnboardingContextLocked(ctx); err != nil {
		m.mu.Unlock()
		return gitlabNotificationView{}, err
	}

	if m.closing || m.roomBusyLocked(owner) || m.gitlabConfiguring[notificationApplyKey(owner, cfg.PlatformID, cfg.Target)] {
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
	if m.gitlabNotifications[owner][notificationScopeFor(cfg.PlatformID, cfg.Target)].Version != version {
		m.mu.Unlock()
		return gitlabNotificationView{}, ErrConflict
	}
	file, loadErr := m.platforms.Load()
	validPlatform := false
	if loadErr == nil {
		for _, c := range file.Platforms {
			if c.ID == cfg.PlatformID && c.Provider == "gitlab" {
				validPlatform = true
			}
		}
	}
	if loadErr != nil || !validPlatform {
		m.mu.Unlock()
		if loadErr != nil {
			return gitlabNotificationView{}, loadErr
		}
		return gitlabNotificationView{}, ErrRequest
	}
	if cfg.Enabled && (cfg.Events.Enabled || cfg.Todos.Enabled) {
		account, _, err := m.resolvePlatformAccountLocked(owner, cfg.PlatformID, "gitlab")
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
	}
	cfg, err := gitlab.NormalizeNotificationConfig(cfg)
	previous, viewErr := m.gitlabNotificationViewForTargetLocked(owner, cfg.PlatformID, cfg.Target)
	if err == nil {
		err = viewErr
	}
	locked := previous.BackfillLocked
	if err == nil && locked && (cfg.BackfillSeconds != previous.Config.BackfillSeconds || !cfg.BackfillStart.Equal(previous.Config.BackfillStart)) {
		err = ErrConflict
	}
	if err == nil && !locked {
		// Freeze a relative choice before either collector starts, so retries and
		// enabling the second channel later use exactly the same reception start.
		start, startErr := notification.BackfillBaseline(cfg.BackfillSeconds, cfg.BackfillStart, m.notifications.Now())
		err = startErr
		if err == nil && (cfg.Enabled && (cfg.Events.Enabled || cfg.Todos.Enabled)) {
			cfg.BackfillSeconds, cfg.BackfillStart = 0, start.UTC().Truncate(time.Second)
			locked = true
		}
	}
	if err == nil {
		err = m.validateNotificationTargetLocked(ctx, owner, cfg.Target, cfg.Enabled && (cfg.Events.Enabled || cfg.Todos.Enabled))
	}
	if err != nil {
		m.mu.Unlock()
		return gitlabNotificationView{}, notificationRoomError(err)
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return gitlabNotificationView{}, err
	}
	saved := m.gitlabNotifications[owner][notificationScopeFor(cfg.PlatformID, cfg.Target)]
	if saved.Version == 0 || !reflect.DeepEqual(saved.Config, cfg) || saved.BackfillLocked != locked {
		if saved.Version == ^uint64(0) {
			m.mu.Unlock()
			return gitlabNotificationView{}, ErrCapacity
		}
		saved = gitlabNotificationRecord{RoomIdentity: owner, Schema: gitlabNotificationSchema, Version: saved.Version + 1, Config: cfg, BackfillLocked: locked}
		if err = m.saveGitLabNotificationLocked(owner, saved); err != nil {
			m.mu.Unlock()
			return gitlabNotificationView{}, err
		}
	}
	m.gitlabConfiguring[notificationApplyKey(owner, cfg.PlatformID, cfg.Target)] = true
	var waits []<-chan struct{}
	for _, state := range m.notifications.StatesLocked(owner) {
		if state.Metadata().Key.Source.Provider != "gitlab" || state.Metadata().PlatformID != cfg.PlatformID || state.Metadata().Target != cfg.Target {
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
	defer m.finishGitLabConfiguration(owner, cfg.PlatformID, cfg.Target)
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
				if meta.Key.Source.Provider == "gitlab" && meta.Key.Source.Channel == channel && meta.PlatformID == cfg.PlatformID && meta.Target == cfg.Target && !gitlabRetired(state) {
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
				runtime := cfg.EventConfig()
				runtime.BackfillSeconds, runtime.BackfillStart = m.gitlabChannelBackfill(owner, cfg, key)
				_, err = m.configureEventsSubscription(ctx, key, revision, runtime)
			} else {
				runtime := cfg.TodoConfig()
				runtime.BackfillSeconds, runtime.BackfillStart = m.gitlabChannelBackfill(owner, cfg, key)
				_, err = m.configureTodosSubscription(ctx, key, revision, runtime)
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
	if entry, err := m.workspaceRoom(owner); err != nil || entry.Status != "ready" || m.gitlabNotifications[owner][notificationScopeFor(cfg.PlatformID, cfg.Target)].Version != saved.Version {
		return gitlabNotificationView{}, ErrIdentity
	}
	for _, channel := range []string{"events", "todos"} {
		if m.gitlabSettingsAppliedLocked(owner, cfg, channel) {
			delete(applyErrors, channel)
		}
	}
	saved.Errors = applyErrors
	if err := m.saveGitLabNotificationLocked(owner, saved); err != nil {
		return gitlabNotificationView{}, err
	}
	v, err := m.gitlabNotificationViewForTargetLocked(owner, cfg.PlatformID, cfg.Target)
	v.Applying = false
	return v, err
}

func (m *roomManager) finishGitLabConfiguration(owner RoomIdentity, platformID string, target notification.Target) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.gitlabConfiguring, notificationApplyKey(owner, platformID, target))
	for _, state := range m.notifications.StatesLocked(owner) {
		if state.Metadata().Key.Source.Provider == "gitlab" && state.Metadata().PlatformID == platformID && state.Metadata().Target == target {
			m.notifications.EnsureLocked(state.Metadata().Key)
		}
	}
}

func (m *roomManager) syncGitLabNotifications(ctx context.Context, owner RoomIdentity, version uint64, platformID string, target notification.Target) (gitlabNotificationView, error) {
	m.mu.Lock()
	v, err := m.gitlabNotificationViewForTargetLocked(owner, platformID, target)
	cfg := v.Config
	if err == nil {
		err = m.store.check(true)
	}
	if err == nil {
		err = m.validateNotificationTargetLocked(ctx, owner, target, true)
	}
	if err == nil && v.Version == 0 && v.Events.SubscriptionID == "" && v.Todos.SubscriptionID == "" {
		err = ErrIdentity
	}
	if err == nil && v.Version != version {
		err = ErrConflict
	}
	if err == nil && (m.closing || m.gitlabConfiguring[notificationApplyKey(owner, cfg.PlatformID, cfg.Target)] || m.roomBusyLocked(owner)) {
		err = ErrBusy
	}
	if err == nil && (v.Pending["events"] || v.Pending["todos"]) {
		err = ErrConflict
	}
	if err != nil {
		m.mu.Unlock()
		return v, err
	}
	m.gitlabConfiguring[notificationApplyKey(owner, cfg.PlatformID, cfg.Target)] = true
	m.mu.Unlock()
	defer m.finishGitLabConfiguration(owner, cfg.PlatformID, cfg.Target)
	v.Errors = map[string]string{}
	for _, source := range []notificationView{v.Events, v.Todos} {
		enabled := v.Config.Enabled && (source.SourceID == gitlab.EventsSourceID && v.Config.Events.Enabled || source.SourceID == gitlab.TodosSourceID && v.Config.Todos.Enabled)
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
	if entry, err := m.workspaceRoom(owner); err != nil || entry.Status != "ready" {
		return gitlabNotificationView{}, ErrIdentity
	}
	if saved, ok := m.gitlabNotifications[owner][notificationScopeFor(cfg.PlatformID, cfg.Target)]; ok {
		if saved.Version != version || saved.Config.Target != cfg.Target {
			return gitlabNotificationView{}, ErrConflict
		}
		saved.Errors = maps.Clone(v.Errors)
		if err := m.saveGitLabNotificationLocked(owner, saved); err != nil {
			return gitlabNotificationView{}, err
		}
	}
	result, readErr := m.gitlabNotificationViewForTargetLocked(owner, cfg.PlatformID, cfg.Target)
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
		target, err := notificationQueryTarget(q, true)
		if err != nil || !validText(q.Get("platform_id")) {
			if err == nil {
				err = ErrRequest
			}
			writeAPIError(w, err)
			return
		}
		file, err := m.platforms.Load()
		if err != nil {
			writeAPIError(w, err)
			return
		}
		found := false
		for _, p := range file.Platforms {
			if p.ID == q.Get("platform_id") && p.Provider == "gitlab" {
				found = true
				break
			}
		}
		if !found {
			writeAPIError(w, ErrRequest)
			return
		}
		v, err := m.gitlabNotificationViewForTargetLocked(owner, q.Get("platform_id"), *target)
		if err == nil && v.Version == 0 && v.Events.SubscriptionID == "" && v.Todos.SubscriptionID == "" {
			err = m.checkNotificationTargetLocked(r.Context(), owner, *target)
		}
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
		ServiceEpoch string               `json:"service_epoch"`
		PlatformID   string               `json:"platform_id,omitempty"`
		Caller       Caller               `json:"caller"`
		Version      uint64               `json:"version"`
		Config       json.RawMessage      `json:"config,omitempty"`
		Target       *notification.Target `json:"target,omitempty"`
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
		if req.Target != nil || !notificationConfigHasTarget(req.Config) {
			writeAPIError(w, ErrRequest)
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(req.Config))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&cfg) != nil || decoder.Decode(new(any)) != io.EOF {
			writeAPIError(w, ErrRequest)
			return
		}
		if req.PlatformID != "" && req.PlatformID != cfg.PlatformID {
			writeAPIError(w, ErrConflict)
			return
		}
		v, err = m.configureGitLabNotifications(r.Context(), owner, version, cfg)
	} else {
		if len(req.Config) != 0 || req.Target == nil || req.Target.Mode == "" || !validText(req.PlatformID) {
			writeAPIError(w, ErrRequest)
			return
		}
		v, err = m.syncGitLabNotifications(r.Context(), owner, version, req.PlatformID, *req.Target)
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// Legacy channel snapshots may store a relative history choice. Preserve their
// exact config and baseline; a channel enabled later starts from the original
// reception baseline instead of interpreting that relative choice again.
func (m *roomManager) gitlabChannelBackfill(owner RoomIdentity, cfg gitlab.NotificationConfig, key notification.Key) (int, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !cfg.BackfillStart.IsZero() {
		return cfg.BackfillSeconds, cfg.BackfillStart
	}
	if state, ok := m.notifications.StateLocked(key); ok {
		switch s := state.(type) {
		case gitlab.EventsState:
			return s.Config.BackfillSeconds, s.Config.BackfillStart
		case gitlab.TodosState:
			return s.Config.BackfillSeconds, s.Config.BackfillStart
		}
	}
	start := time.Time{}
	for _, state := range m.notifications.StatesLocked(owner) {
		if state.Metadata().PlatformID != cfg.PlatformID || state.Metadata().Target != cfg.Target {
			continue
		}
		baseline := time.Time{}
		switch s := state.(type) {
		case gitlab.EventsState:
			baseline = s.Baseline
		case gitlab.TodosState:
			baseline = s.Baseline
		}
		if !baseline.IsZero() && (start.IsZero() || baseline.Before(start)) {
			start = baseline
		}
	}
	if !start.IsZero() {
		return 0, start
	}
	return cfg.BackfillSeconds, cfg.BackfillStart
}

func (m *roomManager) gitlabNotificationScopesLocked(owner RoomIdentity) []notificationScope {
	scopes := map[notificationScope]bool{}
	for key := range m.gitlabNotifications[owner] {
		scopes[key] = true
	}
	for _, state := range m.notifications.StatesLocked(owner) {
		meta := state.Metadata()
		if meta.Key.Source.Provider == "gitlab" && !meta.Retired {
			scopes[notificationScopeFor(meta.PlatformID, meta.Target)] = true
		}
	}
	out := make([]notificationScope, 0, len(scopes))
	for scope := range scopes {
		out = append(out, scope)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PlatformID != out[j].PlatformID {
			return out[i].PlatformID < out[j].PlatformID
		}
		return out[i].Target < out[j].Target
	})
	return out
}
func (m *roomManager) gitlabNotificationScopeTargetLocked(owner RoomIdentity, scope notificationScope) notification.Target {
	if record, ok := m.gitlabNotifications[owner][scope]; ok {
		return record.Config.Target
	}
	for _, state := range m.notifications.StatesLocked(owner) {
		meta := state.Metadata()
		if !meta.Retired && meta.Key.Source.Provider == "gitlab" && notificationScopeFor(meta.PlatformID, meta.Target) == scope {
			return meta.Target
		}
	}
	return notification.Target{}
}
