package room

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

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
			CheckLoaded: func(owner RoomIdentity) error { _, err := m.currentEventsLocked(owner); return err },
		},
		gitlab.TodosSourceID: {
			Source: &gitlab.TodosSource{Prepare: m.preparePlatformTodos},
			Admit: func(_ context.Context, state notification.State) (notification.Scope, error) {
				s, ok := state.(gitlab.TodosState)
				if !ok {
					return notification.Scope{}, notification.ErrInvalid
				}
				if s.Config.ProjectScope == "current" && m.checkWorkspaceOperations(s.RoomIdentity) != nil {
					return notification.Scope{}, notification.ErrBusy
				}
				return m.admitTodosLocked(s)
			},
			CheckLoaded: func(owner RoomIdentity) error { _, err := m.currentTodosLocked(owner); return err },
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
	stamp := fmt.Sprintf("%s|%d|%s", b.ConnectionGeneration, b.CredentialVersion, b.CredentialFingerprint)
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
		project := platform.ProjectBinding{Provider: "gitlab", URL: b.URL, Project: b.Project, ProjectID: b.ProjectID, ConnectionID: b.ConnectionID, ConnectionGeneration: b.ConnectionGeneration}
		resolved, err := m.resolveProjectConnectionLocked(bOwner(b), project)
		if err == nil && (resolved.record.Version != b.ConnectionVersion || resolved.record.CredentialVersion != b.CredentialVersion || resolved.fingerprint() != b.CredentialFingerprint) {
			err = ErrConflict
		}
		m.mu.Unlock()
		if err != nil {
			return gitlab.APIResponse{}, err
		}
		response, err := m.projectConnectionRead(resolved)(ctx, path, q, h, limit)
		m.mu.Lock()
		currentErr := m.checkProjectConnectionLocked(resolved)
		m.mu.Unlock()
		if currentErr != nil {
			return gitlab.APIResponse{}, ErrConflict
		}
		return response, err
	}
}
