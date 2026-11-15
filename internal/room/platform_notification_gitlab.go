package room

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
)

// Recovery preserves opt-in, account, target and progress. Only a fresh account
// check may clear an explicit credential rejection; startup can repair an old
// local credential pause using the currently verified connection.
func (m *roomManager) resumeGitLabSubscriptionsLocked(platformID string, verified bool) error {
	for _, room := range m.index.Rooms {
		if room.Status != "ready" || m.roomBusyLocked(room.RoomIdentity) {
			continue
		}
		for _, state := range m.notifications.StatesLocked(room.RoomIdentity) {
			meta := state.Metadata()
			if meta.Key.Source.Provider != "gitlab" || meta.PlatformID != platformID || !meta.Enabled || meta.Retired {
				continue
			}
			var runtime notification.RuntimeState
			switch s := state.(type) {
			case gitlab.EventsState:
				runtime = s.RuntimeState
			case gitlab.TodosState:
				runtime = s.RuntimeState
			default:
				continue
			}
			if runtime.Status != "credentials_invalid" || !verified && runtime.ErrorCode != "credential_unavailable" {
				continue
			}
			a, _, err := m.resolvePlatformAccountLocked(room.RoomIdentity, platformID, "gitlab")
			if errors.Is(err, platform.ErrCredential) {
				continue
			}
			if err != nil {
				return err
			}
			var next notification.State
			switch s := state.(type) {
			case gitlab.EventsState:
				if s.UserID <= 0 || s.UserID != a.UserID || s.Binding.URL != a.URL || s.Binding.PlatformGeneration != a.Generation {
					continue
				}
				next, err = gitlab.ConfigureEvents(room.RoomIdentity, &s, &s.Binding, s.Config, m.notifications.Now())
			case gitlab.TodosState:
				if s.Binding.UserID != a.UserID || s.Binding.URL != a.URL || s.Binding.PlatformGeneration != a.Generation {
					continue
				}
				b := gitlab.TodosBinding{PlatformID: a.PlatformID, PlatformGeneration: a.Generation, URL: a.URL, UserID: a.UserID,
					Username: a.Username, CredentialVersion: a.CredentialVersion, CredentialFingerprint: a.CredentialFingerprint}
				next, err = gitlab.ConfigureTodos(room.RoomIdentity, &s, b, s.Config, m.notifications.Now())
			}
			if err != nil {
				return notificationRoomError(err)
			}
			// Reuse the normal admission boundary, including exact membership and
			// unapplied user settings, before saving a recoverable candidate.
			if _, err := m.admitPlatformNotificationLocked(context.Background(), next); err != nil {
				if errors.Is(err, notification.ErrBusy) || errors.Is(err, notification.ErrIdentity) || errors.Is(err, notification.ErrConflict) || errors.Is(err, platform.ErrCredential) {
					continue
				}
				return notificationRoomError(err)
			}
			if err := m.notifications.SaveLocked(next); err != nil {
				return notificationRoomError(err)
			}
			if m.notifications.ChangedLocked(meta.Key) == nil {
				m.notifications.WakeLocked(meta.Key)
			}
		}
	}
	return nil
}

// Only the live service repairs old pauses. Offline loading and list/get remain
// observations; a worker is started only after all recovery saves succeed.
func (m *roomManager) startPlatformNotifications() error {
	m.mu.Lock()
	file, err := m.platforms.Load()
	if err == nil {
		for _, c := range file.Platforms {
			if c.Provider == "gitlab" && c.Validation.Status == "valid" {
				if c.CredentialKind == "oauth" {
					cred, _, credentialErr := registry.OAuthRecord(file, c.CredentialID)
					if credentialErr != nil || cred.State != "active" {
						continue
					}
				}
				if err = m.resumeGitLabSubscriptionsLocked(c.ID, false); err != nil {
					break
				}
			}
		}
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return m.notifications.Start()
}

// Provider scope assembly belongs beside its configuration and queries. The
// common admission/delivery path does not inspect concrete provider states.
func (m *roomManager) gitlabNotificationAdapters() map[notification.SourceID]notificationAdapter {
	return map[notification.SourceID]notificationAdapter{
		gitlab.EventsSourceID: {
			Source: &gitlab.EventsSource{Prepare: m.preparePlatformEvents},
			Admit: func(_ context.Context, state notification.State) (notification.Scope, error) {
				s, ok := state.(gitlab.EventsState)
				if !ok {
					return notification.Scope{}, notification.ErrInvalid
				}
				return m.admitEventsLocked(s)
			},
			CheckLoaded: func(owner RoomIdentity) error { return m.checkGitLabSubscriptionsLocked(owner, gitlab.EventsSourceID) },
		},
		gitlab.TodosSourceID: {
			Source: &gitlab.TodosSource{Prepare: m.preparePlatformTodos},
			Admit: func(_ context.Context, state notification.State) (notification.Scope, error) {
				s, ok := state.(gitlab.TodosState)
				if !ok {
					return notification.Scope{}, notification.ErrInvalid
				}
				return m.admitTodosLocked(s)
			},
			CheckLoaded: func(owner RoomIdentity) error { return m.checkGitLabSubscriptionsLocked(owner, gitlab.TodosSourceID) },
		},
	}
}

func (m *roomManager) admitEventsLocked(s gitlab.EventsState) (notification.Scope, error) {
	var scope notification.Scope
	b, err := m.eventsBindingLocked(s)
	if err != nil {
		return scope, err
	}
	scope.Data, _ = json.Marshal(b)
	stamp := fmt.Sprintf("%s|%d|%s", b.PlatformGeneration, b.CredentialVersion, b.CredentialFingerprint)
	scope.CredentialStamp = stamp
	scope.Revision = fmt.Sprintf("%x", sha256.Sum256(append(append([]byte{}, scope.Data...), []byte(stamp)...)))
	return scope, nil
}

func (m *roomManager) preparePlatformEvents(ctx context.Context, _ string, b gitlab.Binding) (gitlab.EventFetcher, int64, error) {
	f, err := gitlab.NewEventFetcher(ctx, b, m.eventsRead(b))
	if err != nil {
		return nil, 0, err
	}
	return f, f.ProjectID(), nil
}

// Resolve the admitted credential before each read and reject a scope change
// after the response body settles. No member files or subprocess are involved.
func (m *roomManager) eventsRead(b gitlab.Binding) gitlab.ReadAPI {
	return func(ctx context.Context, path string, q url.Values, h http.Header, limit int64) (gitlab.APIResponse, error) {
		m.mu.Lock()
		project := platform.ProjectBinding{Provider: "gitlab", URL: b.URL, Project: b.Project, ProjectID: b.ProjectID, PlatformID: b.PlatformID, PlatformGeneration: b.PlatformGeneration}
		resolved, err := m.resolveProjectPlatformLocked(bOwner(b), project)
		if err == nil && (resolved.record.Version != b.PlatformVersion || resolved.record.CredentialVersion != b.CredentialVersion || resolved.fingerprint() != b.CredentialFingerprint) {
			err = ErrConflict
		}
		m.mu.Unlock()
		if err != nil {
			return gitlab.APIResponse{}, err
		}
		response, err := m.projectPlatformRead(resolved)(ctx, path, q, h, limit)
		m.mu.Lock()
		currentErr := m.checkProjectPlatformLocked(resolved)
		m.mu.Unlock()
		if currentErr != nil {
			return gitlab.APIResponse{}, ErrConflict
		}
		return response, err
	}
}

func (m *roomManager) checkGitLabSubscriptionsLocked(owner RoomIdentity, source notification.SourceID) error {
	for _, state := range m.notifications.StatesLocked(owner) {
		meta := state.Metadata()
		if meta.Key.Source != source {
			continue
		}
		if _, err := m.notifications.CurrentLocked(owner, source, meta.PlatformID, meta.Target); err != nil {
			return err
		}
	}
	return nil
}
