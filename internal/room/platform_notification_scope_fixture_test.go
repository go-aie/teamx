package room

import (
	"sort"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Historical one-receiver fixture selection rejects ambiguous data.
func (m *roomManager) notificationTargetLocked(owner RoomIdentity, source notification.SourceID, id string) (notification.Target, error) {
	var target notification.Target
	found := false
	for _, state := range m.notifications.StatesLocked(owner) {
		meta := state.Metadata()
		if meta.Key.Source != source || meta.PlatformID != id || meta.Retired {
			continue
		}
		if found && target != meta.Target {
			return target, ErrConflict
		}
		target, found = meta.Target, true
	}
	return target, nil
}
func (m *roomManager) gitlabTargetLocked(owner RoomIdentity, id string) (notification.Target, error) {
	var target notification.Target
	found := false
	accept := func(t notification.Target) error {
		if found && target != t {
			return ErrConflict
		}
		target, found = t, true
		return nil
	}
	for key, record := range m.gitlabNotifications[owner] {
		if key.PlatformID == id {
			if err := accept(record.Config.Target); err != nil {
				return target, err
			}
		}
	}
	for _, state := range m.notifications.StatesLocked(owner) {
		meta := state.Metadata()
		if meta.Key.Source.Provider == "gitlab" && meta.PlatformID == id && !meta.Retired {
			if err := accept(meta.Target); err != nil {
				return target, err
			}
		}
	}
	return target, nil
}

// Optional selection is used by single-platform callers only. Ambiguity never
// chooses the first platform. Subscription identities remain authoritative.
func (m *roomManager) notificationPlatformLocked(owner RoomIdentity, source notification.SourceID, ids ...string) (string, error) {
	if len(ids) > 0 {
		return ids[0], nil
	}
	id := ""
	found := false
	for _, state := range m.notifications.StatesLocked(owner) {
		meta := state.Metadata()
		if meta.Key.Source != source || meta.Retired {
			continue
		}
		if found && id != meta.PlatformID {
			return "", ErrConflict
		}
		id, found = meta.PlatformID, true
	}
	return id, nil
}
func (m *roomManager) gitlabPlatformIDsLocked(owner RoomIdentity) []string {
	ids := map[string]bool{}
	for key := range m.gitlabNotifications[owner] {
		ids[key.PlatformID] = true
	}
	for _, state := range m.notifications.StatesLocked(owner) {
		meta := state.Metadata()
		if meta.Key.Source.Provider == "gitlab" && !meta.Retired {
			ids[meta.PlatformID] = true
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}
func (m *roomManager) gitlabPlatformLocked(owner RoomIdentity, ids ...string) (string, error) {
	if len(ids) > 0 && ids[0] != "" {
		return ids[0], nil
	}
	all := m.gitlabPlatformIDsLocked(owner)
	if len(all) > 1 {
		return "", ErrConflict
	}
	if len(all) == 1 {
		return all[0], nil
	}
	return "", nil
}

func (m *roomManager) fixtureGitLabNotificationViewLocked(owner RoomIdentity, platformIDs ...string) (gitlabNotificationView, error) {
	id, err := m.gitlabPlatformLocked(owner, platformIDs...)
	if err != nil {
		return gitlabNotificationView{}, err
	}
	target, err := m.gitlabTargetLocked(owner, id)
	if err != nil {
		return gitlabNotificationView{}, err
	}
	return m.gitlabNotificationViewForTargetLocked(owner, id, target)
}

// The shared notification manager enforces one active subscription per Room/platform/target.
func (m *roomManager) fixtureCurrentLarkLocked(owner RoomIdentity, platformIDs ...string) (*lark.State, error) {
	id, err := m.notificationPlatformLocked(owner, lark.MessagesSourceID, platformIDs...)
	if err != nil {
		return nil, err
	}
	target, err := m.notificationTargetLocked(owner, lark.MessagesSourceID, id)
	if err != nil {
		return nil, err
	}
	return m.currentLarkForTargetLocked(owner, id, target)
}

// Fixture readers for historical one-receiver tests. New multi-receiver tests
// use the explicit target methods and HTTP contract.
func (m *roomManager) gitlabNotificationViewLocked(owner RoomIdentity, ids ...string) (gitlabNotificationView, error) {
	return m.fixtureGitLabNotificationViewLocked(owner, ids...)
}
func (m *roomManager) currentLarkLocked(owner RoomIdentity, ids ...string) (*lark.State, error) {
	return m.fixtureCurrentLarkLocked(owner, ids...)
}
func (m *roomManager) currentEventsLocked(owner RoomIdentity, platformIDs ...string) (*gitlab.EventsState, error) {
	id, err := m.notificationPlatformLocked(owner, gitlab.EventsSourceID, platformIDs...)
	if err != nil {
		return nil, err
	}
	target, err := m.notificationTargetLocked(owner, gitlab.EventsSourceID, id)
	if err != nil {
		return nil, err
	}
	return m.currentEventsForTargetLocked(owner, id, target)
}
func (m *roomManager) currentTodosLocked(owner RoomIdentity, platformIDs ...string) (*gitlab.TodosState, error) {
	id, err := m.notificationPlatformLocked(owner, gitlab.TodosSourceID, platformIDs...)
	if err != nil {
		return nil, err
	}
	target, err := m.notificationTargetLocked(owner, gitlab.TodosSourceID, id)
	if err != nil {
		return nil, err
	}
	return m.currentTodosForTargetLocked(owner, id, target)
}

func explicitRoomNotificationConfig(cfg gitlab.NotificationConfig) gitlab.NotificationConfig {
	if !cfg.Target.Inbox() {
		cfg.Target.Mode = "room"
	}
	return cfg
}
