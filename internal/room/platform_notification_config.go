package room

import (
	"context"
	"errors"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
)

func (m *roomManager) eventsSubscriptionLocked(key notification.Key) (gitlab.EventsState, bool) {
	state, ok := m.notifications.StateLocked(key)
	if !ok {
		return gitlab.EventsState{}, false
	}
	s, ok := state.(gitlab.EventsState)
	return s, ok
}

// Exactly one non-retired Events subscription is allowed per Room/platform. A retired
// worker must settle before a replacement can be admitted, including on retry.
func (m *roomManager) currentEventsLocked(owner RoomIdentity, platformIDs ...string) (*gitlab.EventsState, error) {
	id, err := m.notificationPlatformLocked(owner, gitlab.EventsSourceID, platformIDs...)
	if err != nil {
		return nil, err
	}
	for _, previous := range m.notifications.StatesLocked(owner) {
		meta := previous.Metadata()
		if meta.Key.Source == gitlab.EventsSourceID && meta.PlatformID == id && meta.Retired && m.attachmentCallsBusyLocked(meta.Key) {
			return nil, ErrBusy
		}
	}
	state, err := m.notifications.CurrentLocked(owner, gitlab.EventsSourceID, id)
	if err != nil || state == nil {
		return nil, notificationRoomError(err)
	}
	s, ok := state.(gitlab.EventsState)
	if !ok {
		return nil, ErrInvalid
	}
	return &s, nil
}

// Project URLs and namespace paths are accepted only against the selected
// connection's HTTPS origin. This parses configuration; it performs no Git work.
func eventsProjectPath(origin, input string) (string, error) {
	project := strings.TrimSpace(input)
	if strings.Contains(project, "://") {
		u, err := workspaceHTTPSURL(project)
		if err != nil || u.Scheme+"://"+u.Host != origin {
			return "", ErrRequest
		}
		project = strings.TrimPrefix(u.Path, "/")
	}
	project = strings.TrimSuffix(project, ".git")
	if !workspaceProjectPath(project) || !strings.Contains(project, "/") {
		return "", ErrRequest
	}
	return project, nil
}

func (m *roomManager) configureEventsSubscription(ctx context.Context, key notification.Key, version uint64, cfg gitlab.EventsConfig) (gitlab.EventsState, error) {
	if key.Source != gitlab.EventsSourceID {
		return gitlab.EventsState{}, ErrRequest
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing || m.roomBusyLocked(key.Owner) {
		return gitlab.EventsState{}, ErrBusy
	}
	entry, err := m.workspaceRoom(key.Owner)
	if err != nil || entry.Status != "ready" {
		return gitlab.EventsState{}, ErrIdentity
	}
	current, err := m.currentEventsLocked(key.Owner, m.subscriptionPlatformLocked(key, cfg.PlatformID))
	if err != nil {
		return gitlab.EventsState{}, err
	}
	if key.SubscriptionID == "" && (version != 0 || current != nil) || key.SubscriptionID != "" && (current == nil || current.SubscriptionID != key.SubscriptionID || current.SubscriptionVersion != version) {
		return gitlab.EventsState{}, ErrConflict
	}
	if err := m.validateNotificationTargetLocked(ctx, key.Owner, cfg.Target, cfg.Enabled); err != nil {
		return gitlab.EventsState{}, err
	}
	var binding platform.ProjectBinding
	var account registry.Account
	if current != nil {
		binding = current.Binding
		if cfg.PlatformID == "" {
			cfg.PlatformID = current.Config.PlatformID
		}
		if cfg.Project == "" {
			cfg.Project = current.Config.Project
		}
	}
	if cfg.Enabled {
		if cfg.PlatformID != "" {
			var secret platform.Secret
			account, secret, err = m.resolvePlatformAccountLocked(key.Owner, cfg.PlatformID, "gitlab")
			if err != nil {
				return gitlab.EventsState{}, notificationRoomError(err)
			}
			cfg.Project, err = eventsProjectPath(account.URL, cfg.Project)
			if err != nil {
				return gitlab.EventsState{}, err
			}
			binding = platform.ProjectBinding{Provider: "gitlab", URL: account.URL, Project: cfg.Project, PlatformID: account.PlatformID, PlatformGeneration: account.Generation}
			// Only a new target needs remote verification. Existing snapshots have
			// already verified the stable project ID; credential resolution stays live.
			if current != nil && current.Binding == (platform.ProjectBinding{Provider: "gitlab", URL: binding.URL, Project: binding.Project, PlatformID: binding.PlatformID, PlatformGeneration: binding.PlatformGeneration, ProjectID: current.Binding.ProjectID}) && current.UserID == account.UserID {
				binding.ProjectID = current.Binding.ProjectID
			} else {
				verify := m.platformProject

				m.mu.Unlock()
				projectID, verifyErr := m.verifyGitlabAccountProject(ctx, key.Owner, account, secret, cfg.Project, verify)
				m.mu.Lock()
				if verifyErr != nil || projectID <= 0 {
					return gitlab.EventsState{}, ErrRequest
				}
				selected, _, selectErr := m.resolvePlatformAccountLocked(key.Owner, cfg.PlatformID, "gitlab")
				latest, readErr := m.currentEventsLocked(key.Owner, m.subscriptionPlatformLocked(key, cfg.PlatformID))
				if m.roomBusyLocked(key.Owner) || selectErr != nil || selected != account || readErr != nil || (current == nil) != (latest == nil) || current != nil && (latest.SubscriptionID != current.SubscriptionID || latest.SubscriptionVersion != current.SubscriptionVersion) {
					return gitlab.EventsState{}, ErrConflict
				}
				binding.ProjectID = projectID
			}
		} else if current == nil || current.Config.PlatformID != "" {
			return gitlab.EventsState{}, ErrRequest
		}
	} else if current != nil {
		cfg.PlatformID, cfg.Project = current.Config.PlatformID, current.Config.Project
	}
	cfg, err = gitlab.NormalizeEventsConfig(cfg)
	if err != nil {
		return gitlab.EventsState{}, notificationRoomError(err)
	}
	if err := ctx.Err(); err != nil {
		return gitlab.EventsState{}, err
	}
	// Changing project, instance, connection or account starts a fresh identity.
	replacement := current != nil && cfg.Enabled && (binding != current.Binding || account.UserID != current.UserID)
	if replacement {
		retired, retireErr := gitlab.RetireEvents(*current)
		if retireErr != nil {
			return retired, notificationRoomError(retireErr)
		}
		if err = m.notifications.SaveLocked(retired); err != nil {
			return retired, err
		}
		done := m.notifications.ChangedLocked(retired.Metadata().Key)
		waits := m.cancelAttachmentCallsLocked(retired.Metadata().Key)
		if done != nil {
			waits = append(waits, done)
		}
		m.mu.Unlock()
		err = notification.Wait(ctx, waits)
		m.mu.Lock()
		if err != nil {
			return retired, err
		}
		latest, ok := m.eventsSubscriptionLocked(retired.Metadata().Key)
		active, readErr := m.currentEventsLocked(key.Owner, m.subscriptionPlatformLocked(key, cfg.PlatformID))
		selected, _, selectErr := m.resolvePlatformAccountLocked(key.Owner, cfg.PlatformID, "gitlab")
		if m.roomBusyLocked(key.Owner) || !ok || latest.SubscriptionVersion != retired.SubscriptionVersion || readErr != nil || active != nil || selectErr != nil || selected != account {
			return retired, ErrConflict
		}
		if saved, ok := m.gitlabNotifications[key.Owner][cfg.PlatformID]; ok && saved.BackfillLocked && saved.Config.BackfillStart.IsZero() {
			cfg.BackfillSeconds, cfg.BackfillStart = 0, current.Baseline
		}
		current = nil
	}
	if err := m.validateNotificationTargetLocked(ctx, key.Owner, cfg.Target, cfg.Enabled); err != nil {
		return gitlab.EventsState{}, err
	}
	next, err := gitlab.ConfigureEvents(key.Owner, current, &binding, cfg, m.notifications.Now())
	if err != nil {
		return next, notificationRoomError(err)
	}
	if cfg.Enabled && account.UserID > 0 {
		next.UserID, next.Username = account.UserID, account.Username
	}
	if err = m.notifications.SaveLocked(next); err != nil {
		return next, err
	}
	done := m.notifications.ChangedLocked(next.Metadata().Key)
	m.mu.Unlock()
	if done != nil {
		err = notification.Wait(ctx, []<-chan struct{}{done})
	}
	m.mu.Lock()
	if err != nil {
		return next, err
	}
	m.notifications.EnsureLocked(next.Metadata().Key)
	return next.Clone(), nil
}

func (m *roomManager) syncEventsSubscription(key notification.Key, version uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing {
		return ErrClosing
	}
	if m.roomBusyLocked(key.Owner) {
		return ErrBusy
	}
	s, ok := m.eventsSubscriptionLocked(key)
	if !ok || s.Retired || s.SubscriptionVersion != version {
		return ErrIdentity
	}
	if _, err := m.eventsBindingLocked(s); err != nil {
		return notificationRoomError(err)
	}
	next, changed, err := gitlab.SyncEvents(s, s.Binding)
	if err != nil {
		return notificationRoomError(err)
	}
	if err = m.notifications.SaveLocked(next); err != nil {
		return notificationRoomError(err)
	}
	if changed {
		m.notifications.ChangedLocked(next.Metadata().Key)
	}
	m.notifications.WakeLocked(next.Metadata().Key)
	return nil
}

// Credential identity is checked against the subscription, never Workspace.
func (m *roomManager) eventsBindingLocked(s gitlab.EventsState) (gitlab.Binding, error) {
	if s.Retired {
		return gitlab.Binding{}, notification.ErrIdentity
	}
	if s.UserID > 0 {
		account, _, err := m.resolvePlatformAccountLocked(s.RoomIdentity, s.Binding.PlatformID, "gitlab")
		if err != nil {
			return gitlab.Binding{}, err
		}
		if account.URL != s.Binding.URL || account.Generation != s.Binding.PlatformGeneration || account.UserID != s.UserID {
			return gitlab.Binding{}, notification.ErrConflict
		}
		// Use the exact verified credential evidence, rather than resolving the
		// mutable configuration a second time during an admitted operation.
		return gitlab.Binding{Version: 1, RoomID: s.RoomID, RoomInstance: s.RoomInstance,
			URL: s.Binding.URL, Project: s.Binding.Project, ProjectID: s.Binding.ProjectID,
			PlatformID: account.PlatformID, PlatformGeneration: account.Generation,
			PlatformVersion: account.PlatformVersion, CredentialVersion: account.CredentialVersion,
			CredentialFingerprint: account.CredentialFingerprint}, nil
	}
	b, err := m.gitLabProjectBindingLocked(s.RoomIdentity, s.Binding)
	if errors.Is(err, ErrIdentity) {
		return b, platform.ErrCredential
	}
	return b, roomNotificationError(err)
}

// Keep HTTP error mapping at the Room boundary. Unknown storage errors retain
// their identity (including the shared commit-unknown latch).
func notificationRoomError(err error) error {
	for _, pair := range [][2]error{{platform.ErrCredential, ErrRequest}, {notification.ErrRequest, ErrRequest}, {notification.ErrInvalid, ErrInvalid}, {notification.ErrIdentity, ErrIdentity}, {notification.ErrConflict, ErrConflict}, {notification.ErrBusy, ErrBusy}, {notification.ErrClosing, ErrClosing}, {notification.ErrCapacity, ErrCapacity}} {
		if errors.Is(err, pair[0]) {
			return pair[1]
		}
	}
	return err
}
func roomNotificationError(err error) error {
	for _, pair := range [][2]error{{ErrRequest, notification.ErrRequest}, {ErrInvalid, notification.ErrInvalid}, {ErrIdentity, notification.ErrIdentity}, {ErrConflict, notification.ErrConflict}, {ErrBusy, notification.ErrBusy}, {ErrClosing, notification.ErrClosing}, {ErrCapacity, notification.ErrCapacity}} {
		if errors.Is(err, pair[0]) {
			return pair[1]
		}
	}
	return err
}
