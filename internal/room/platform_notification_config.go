package room

import (
	"context"
	"errors"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func (m *roomManager) eventsSubscriptionLocked(key notification.Key) (gitlab.EventsState, bool) {
	state, ok := m.notifications.StateLocked(key)
	if !ok {
		return gitlab.EventsState{}, false
	}
	s, ok := state.(gitlab.EventsState)
	return s, ok
}

// Exactly one non-retired Events subscription is allowed per room. A retired
// worker must settle before a replacement can be admitted, including on retry.
func (m *roomManager) currentEventsLocked(owner RoomIdentity) (*gitlab.EventsState, error) {
	var current *gitlab.EventsState
	for _, state := range m.notifications.StatesLocked(owner) {
		if s, ok := state.(gitlab.EventsState); ok {
			if s.Retired {
				if m.notifications.DoneLocked(s.Metadata().Key) != nil {
					return nil, ErrBusy
				}
				continue
			}
			if current != nil {
				return nil, ErrInvalid
			}
			current = &s
		}
	}
	return current, nil
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
	current, err := m.currentEventsLocked(key.Owner)
	if err != nil {
		return gitlab.EventsState{}, err
	}
	if key.SubscriptionID == "" && (version != 0 || current != nil) || key.SubscriptionID != "" && (current == nil || current.SubscriptionID != key.SubscriptionID || current.SubscriptionVersion != version) {
		return gitlab.EventsState{}, ErrConflict
	}
	var binding platform.ProjectBinding
	var account connection.Account
	if current != nil {
		binding = current.Binding
		if cfg.ConnectionID == "" {
			cfg.ConnectionID = current.Config.ConnectionID
		}
		if cfg.Project == "" {
			cfg.Project = current.Config.Project
		}
	}
	if cfg.Enabled {
		if cfg.ConnectionID != "" {
			var secret platform.Secret
			account, secret, err = m.resolvePlatformAccountLocked(key.Owner, cfg.ConnectionID, "gitlab")
			if err != nil {
				return gitlab.EventsState{}, notificationRoomError(err)
			}
			cfg.Project, err = eventsProjectPath(account.URL, cfg.Project)
			if err != nil {
				return gitlab.EventsState{}, err
			}
			binding = platform.ProjectBinding{Provider: "gitlab", URL: account.URL, Project: cfg.Project, ConnectionID: account.ConnectionID, ConnectionGeneration: account.Generation}
			// Only a new target needs remote verification. Existing snapshots have
			// already verified the stable project ID; credential resolution stays live.
			if current != nil && current.Binding == (platform.ProjectBinding{Provider: "gitlab", URL: binding.URL, Project: binding.Project, ConnectionID: binding.ConnectionID, ConnectionGeneration: binding.ConnectionGeneration, ProjectID: current.Binding.ProjectID}) && current.UserID == account.UserID {
				binding.ProjectID = current.Binding.ProjectID
			} else {
				verify := m.platformProject
				if verify == nil {
					verify = verifyGitLabProject
				}
				m.mu.Unlock()
				projectID, verifyErr := verify(ctx, account.URL, cfg.Project, workspaceSecret{value: secret.Reveal()})
				m.mu.Lock()
				if verifyErr != nil || projectID <= 0 {
					return gitlab.EventsState{}, ErrRequest
				}
				selected, _, selectErr := m.resolvePlatformAccountLocked(key.Owner, cfg.ConnectionID, "gitlab")
				latest, readErr := m.currentEventsLocked(key.Owner)
				if m.roomBusyLocked(key.Owner) || selectErr != nil || selected != account || readErr != nil || (current == nil) != (latest == nil) || current != nil && (latest.SubscriptionID != current.SubscriptionID || latest.SubscriptionVersion != current.SubscriptionVersion) {
					return gitlab.EventsState{}, ErrConflict
				}
				binding.ProjectID = projectID
			}
		} else if current == nil || current.Config.ConnectionID != "" {
			return gitlab.EventsState{}, ErrRequest
		}
	} else if current != nil {
		cfg.ConnectionID, cfg.Project = current.Config.ConnectionID, current.Config.Project
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
		m.mu.Unlock()
		if done != nil {
			err = notification.Wait(ctx, []<-chan struct{}{done})
		}
		m.mu.Lock()
		if err != nil {
			return retired, err
		}
		latest, ok := m.eventsSubscriptionLocked(retired.Metadata().Key)
		active, readErr := m.currentEventsLocked(key.Owner)
		selected, _, selectErr := m.resolvePlatformAccountLocked(key.Owner, cfg.ConnectionID, "gitlab")
		if m.roomBusyLocked(key.Owner) || !ok || latest.SubscriptionVersion != retired.SubscriptionVersion || readErr != nil || active != nil || selectErr != nil || selected != account {
			return retired, ErrConflict
		}
		current = nil
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
		account, _, err := m.resolvePlatformAccountLocked(s.RoomIdentity, s.Binding.ConnectionID, "gitlab")
		if err != nil {
			return gitlab.Binding{}, err
		}
		if account.URL != s.Binding.URL || account.Generation != s.Binding.ConnectionGeneration || account.UserID != s.UserID {
			return gitlab.Binding{}, notification.ErrConflict
		}
		// Use the exact verified credential evidence, rather than resolving the
		// environment a second time and potentially adopting an unverified PAT.
		return gitlab.Binding{Version: 1, RoomID: s.RoomID, RoomInstance: s.RoomInstance,
			URL: s.Binding.URL, Project: s.Binding.Project, ProjectID: s.Binding.ProjectID,
			ConnectionID: account.ConnectionID, ConnectionGeneration: account.Generation,
			ConnectionVersion: account.ConnectionVersion, CredentialVersion: account.CredentialVersion,
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
