package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Personal admission is independent of Workspace/Git authentication. The only
// authority for the recipient is the selected connection's verified /user ID.
func (m *roomManager) resolveTodosConnectionLocked(owner RoomIdentity, id string) (gitlab.TodosQueryScope, platform.Secret, error) {
	account, secret, err := m.resolvePlatformAccountLocked(owner, id, "gitlab")
	if err != nil {
		return gitlab.TodosQueryScope{}, secret, err
	}
	return gitlab.TodosQueryScope{Binding: gitlab.TodosBinding{ConnectionID: account.ConnectionID, ConnectionGeneration: account.Generation, URL: account.URL, UserID: account.UserID, Username: account.Username, CredentialVersion: account.CredentialVersion, CredentialFingerprint: account.CredentialFingerprint}, ConnectionVersion: account.ConnectionVersion}, secret, nil
}
func (m *roomManager) todosProjectLocked(owner RoomIdentity, cfg gitlab.TodosConfig, instance string) (*platform.ProjectBinding, error) {
	if cfg.ProjectScope == "all" {
		return nil, nil
	}
	entry, err := m.workspaceRoom(owner)
	if err != nil {
		return nil, notification.ErrIdentity
	}
	saved, err := m.readWorkspaceRoomConfig(entry)
	if err != nil {
		return nil, err
	}
	if saved.Workspace == nil || saved.Workspace.Binding == nil || saved.Workspace.Binding.URL != instance || saved.Workspace.Binding.ProjectID <= 0 {
		return nil, notification.ErrConflict
	}
	project := *saved.Workspace.Binding
	return &project, nil
}

// Only the explicit current-Workspace To-Do scope follows local binding
// lifecycle. Independent Events and account-wide To-Dos keep polling.
func (m *roomManager) cancelWorkspaceNotificationsLocked(owner RoomIdentity) []<-chan struct{} {
	var done []<-chan struct{}
	for _, state := range m.notifications.StatesLocked(owner) {
		if s, ok := state.(gitlab.TodosState); ok && s.Config.ProjectScope == "current" {
			done = append(done, m.cancelAttachmentCallsLocked(s.Metadata().Key)...)
			if ch := m.notifications.CancelLocked(s.Metadata().Key); ch != nil {
				done = append(done, ch)
			}
		}
	}
	return done
}
func (m *roomManager) admitTodosLocked(s gitlab.TodosState) (notification.Scope, error) {
	var result notification.Scope
	if s.Retired {
		return result, notification.ErrIdentity
	}
	if s.Status == "credentials_invalid" {
		return result, platform.ErrCredential
	}
	if s.Status == "binding_changed" {
		return result, notification.ErrConflict
	}
	current, _, err := m.resolveTodosConnectionLocked(s.RoomIdentity, s.Binding.ConnectionID)
	if err != nil {
		return result, err
	}
	if !current.Binding.SameAccount(s.Binding) || current.Binding.ConnectionGeneration != s.Binding.ConnectionGeneration {
		return result, notification.ErrConflict
	}
	if current.Binding.CredentialVersion != s.Binding.CredentialVersion || current.Binding.CredentialFingerprint != s.Binding.CredentialFingerprint {
		return result, platform.ErrCredential
	}
	project, err := m.todosProjectLocked(s.RoomIdentity, s.Config, current.Binding.URL)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(project, s.Project) {
		return result, notification.ErrConflict
	}
	result.Data, _ = json.Marshal(current)
	result.Revision = fmt.Sprintf("%d|%s|%s|%d|%d|%s", current.ConnectionVersion, current.Binding.ConnectionID, current.Binding.ConnectionGeneration, current.Binding.UserID, current.Binding.CredentialVersion, current.Binding.CredentialFingerprint)
	result.CredentialStamp = fmt.Sprintf("%s|%s|%d|%d|%s", current.Binding.ConnectionID, current.Binding.ConnectionGeneration, current.Binding.UserID, current.Binding.CredentialVersion, current.Binding.CredentialFingerprint)
	return result, nil
}
func (m *roomManager) preparePlatformTodos(_ context.Context, _ string, owner RoomIdentity, b gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
	read := func(ctx context.Context, path string, q url.Values, h http.Header, limit int64) (gitlab.APIResponse, error) {
		if err := ctx.Err(); err != nil {
			return gitlab.APIResponse{}, err
		}
		m.mu.Lock()
		current, _, err := m.resolveTodosConnectionLocked(owner, b.Binding.ConnectionID)
		account, secret, accountErr := m.resolvePlatformAccountLocked(owner, b.Binding.ConnectionID, "gitlab")
		if err == nil {
			err = accountErr
		}
		m.mu.Unlock()
		if err != nil {
			return gitlab.APIResponse{}, err
		}
		if current != b {
			return gitlab.APIResponse{}, notification.ErrBusy
		}
		response, err := m.gitlabAccountRead(owner, account, secret)(ctx, path, q, h, limit)
		m.mu.Lock()
		current, _, checkErr := m.resolveTodosConnectionLocked(owner, b.Binding.ConnectionID)
		m.mu.Unlock()
		if checkErr != nil {
			return gitlab.APIResponse{}, checkErr
		}
		if current != b {
			return gitlab.APIResponse{}, notification.ErrBusy
		}
		return response, err
	}
	return gitlab.NewTodoFetcher(b.Binding.URL, b.Binding.UserID, read)
}
func (m *roomManager) todosSubscriptionLocked(key notification.Key) (gitlab.TodosState, bool) {
	state, ok := m.notifications.StateLocked(key)
	if !ok {
		return gitlab.TodosState{}, false
	}
	s, ok := state.(gitlab.TodosState)
	return s, ok
}
func (m *roomManager) currentTodosLocked(owner RoomIdentity) (*gitlab.TodosState, error) {
	var current *gitlab.TodosState
	for _, state := range m.notifications.StatesLocked(owner) {
		if s, ok := state.(gitlab.TodosState); ok {
			if s.Retired {
				if m.notifications.DoneLocked(s.Metadata().Key) != nil || m.attachmentCallsBusyLocked(s.Metadata().Key) {
					return nil, notification.ErrBusy
				}
				continue
			}
			if current != nil {
				return nil, notification.ErrInvalid
			}
			current = &s
		}
	}
	return current, nil
}
func (m *roomManager) configureTodosSubscription(ctx context.Context, key notification.Key, version uint64, cfg gitlab.TodosConfig) (gitlab.TodosState, error) {
	if key.Source != gitlab.TodosSourceID {
		return gitlab.TodosState{}, ErrRequest
	}
	m.mu.Lock()
	var target gitlab.TodosQueryScope
	if cfg.Enabled && cfg.ConnectionID != "" {
		var targetErr error
		target, _, targetErr = m.resolveTodosConnectionLocked(key.Owner, cfg.ConnectionID)
		if targetErr != nil {
			m.mu.Unlock()
			return gitlab.TodosState{}, notificationRoomError(targetErr)
		}
	}
	previous, _ := m.currentTodosLocked(key.Owner)
	next, done, err := m.configureTodosLocked(key, version, cfg)
	var waits []<-chan struct{}
	if err == nil && previous != nil && (next.Retired || !reflect.DeepEqual(previous.Project, next.Project)) {
		waits = m.cancelAttachmentCallsLocked(previous.Metadata().Key)
	}
	if done != nil {
		waits = append(waits, done)
	}
	m.mu.Unlock()
	if err != nil {
		return next, notificationRoomError(err)
	}
	if err = notification.Wait(ctx, waits); err != nil {
		return next, err
	}
	// An account switch returns its durably retired predecessor. Only after all
	// old work settles may the replacement be saved. Failures preserve retirement.
	if next.Retired {
		m.mu.Lock()
		latest, ok := m.todosSubscriptionLocked(next.Metadata().Key)
		if !ok || latest.SubscriptionVersion != next.SubscriptionVersion {
			m.mu.Unlock()
			return next, ErrConflict
		}
		selected, _, selectErr := m.resolveTodosConnectionLocked(key.Owner, cfg.ConnectionID)
		if selectErr != nil || selected != target {
			m.mu.Unlock()
			return next, ErrConflict
		}
		created, _, createErr := m.configureTodosLocked(notification.Key{Owner: key.Owner, Source: key.Source}, 0, cfg)
		m.mu.Unlock()
		if createErr != nil {
			return next, notificationRoomError(createErr)
		}
		return created, nil
	}
	m.mu.Lock()
	m.notifications.EnsureLocked(next.Metadata().Key)
	m.mu.Unlock()
	return next, nil
}

// Requires the shared gate. A retired predecessor with a live worker prevents
// concurrent replacement creation, including retries of a failed HTTP response.
func (m *roomManager) configureTodosLocked(key notification.Key, version uint64, cfg gitlab.TodosConfig) (gitlab.TodosState, <-chan struct{}, error) {
	if m.closing {
		return gitlab.TodosState{}, nil, notification.ErrClosing
	}
	if m.roomBusyLocked(key.Owner) {
		return gitlab.TodosState{}, nil, notification.ErrBusy
	}
	entry, err := m.workspaceRoom(key.Owner)
	if err != nil || entry.Status != "ready" {
		return gitlab.TodosState{}, nil, notification.ErrIdentity
	}
	current, err := m.currentTodosLocked(key.Owner)
	if err != nil {
		return gitlab.TodosState{}, nil, err
	}
	if key.SubscriptionID == "" && (version != 0 || current != nil) || key.SubscriptionID != "" && (current == nil || current.SubscriptionID != key.SubscriptionID || current.SubscriptionVersion != version) {
		return gitlab.TodosState{}, nil, notification.ErrConflict
	}
	if current != nil && cfg.ConnectionID == "" {
		cfg.ConnectionID = current.Binding.ConnectionID
	}
	if err := m.validateNotificationTargetLocked(context.Background(), key.Owner, cfg.Target, cfg.Enabled); err != nil {
		return gitlab.TodosState{}, nil, roomNotificationError(err)
	}
	cfg, err = gitlab.NormalizeTodosConfig(cfg)
	if err != nil {
		return gitlab.TodosState{}, nil, err
	}
	var b gitlab.TodosBinding
	var project *platform.ProjectBinding
	if current == nil || cfg.Enabled {
		if cfg.ProjectScope == "current" {
			if err = m.checkWorkspaceOperations(key.Owner); err != nil {
				return gitlab.TodosState{}, nil, err
			}
		}
		scope, _, e := m.resolveTodosConnectionLocked(key.Owner, cfg.ConnectionID)
		if e != nil {
			return gitlab.TodosState{}, nil, e
		}
		b = scope.Binding
		project, err = m.todosProjectLocked(key.Owner, cfg, b.URL)
		if err != nil {
			return gitlab.TodosState{}, nil, err
		}
	} else {
		b = current.Binding
		project = current.Project
	}
	if current != nil && cfg.Enabled && !current.Binding.SameAccount(b) {
		retired, e := gitlab.RetireTodos(*current)
		if e != nil {
			return retired, nil, e
		}
		if e = m.notifications.SaveLocked(retired); e != nil {
			return retired, nil, e
		}
		return retired, m.notifications.ChangedLocked(retired.Metadata().Key), nil
	}
	next, err := gitlab.ConfigureTodos(key.Owner, current, b, project, cfg, m.notifications.Now())
	if err != nil {
		return next, nil, err
	}
	if err = m.notifications.SaveLocked(next); err != nil {
		return next, nil, err
	}
	return next, m.notifications.ChangedLocked(next.Metadata().Key), nil
}
func (m *roomManager) syncTodosSubscription(ctx context.Context, key notification.Key, version uint64) error {
	m.mu.Lock()
	if m.closing || m.roomBusyLocked(key.Owner) {
		m.mu.Unlock()
		return ErrBusy
	}
	s, ok := m.todosSubscriptionLocked(key)
	if !ok || s.Retired || !s.Config.Enabled || s.SubscriptionVersion != version {
		m.mu.Unlock()
		return ErrIdentity
	}
	if s.Config.ProjectScope == "current" {
		if err := m.checkWorkspaceOperations(key.Owner); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	scope, _, err := m.resolveTodosConnectionLocked(key.Owner, s.Binding.ConnectionID)
	if err == nil && (!scope.Binding.SameAccount(s.Binding) || scope.Binding.ConnectionGeneration != s.Binding.ConnectionGeneration) {
		err = notification.ErrConflict
	}
	var project *platform.ProjectBinding
	if err == nil {
		project, err = m.todosProjectLocked(key.Owner, s.Config, scope.Binding.URL)
	}
	if err == nil && !reflect.DeepEqual(project, s.Project) {
		err = notification.ErrConflict
	}
	if err != nil {
		if errors.Is(err, notification.ErrConflict) || errors.Is(err, platform.ErrCredential) {
			if saveErr := m.notifications.SaveLocked(s.ScopeFailed(err)); saveErr != nil {
				m.mu.Unlock()
				return notificationRoomError(saveErr)
			}
		}
		m.mu.Unlock()
		return notificationRoomError(err)
	}
	next, err := gitlab.ConfigureTodos(key.Owner, &s, scope.Binding, project, s.Config, m.notifications.Now())
	if err == nil {
		err = m.notifications.SaveLocked(next)
	}
	var done <-chan struct{}
	if err == nil {
		done = m.notifications.ChangedLocked(key)
	}
	m.mu.Unlock()
	if err != nil {
		return notificationRoomError(err)
	}
	if done != nil {
		if err = notification.Wait(ctx, []<-chan struct{}{done}); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.notifications.WakeLocked(key)
	m.mu.Unlock()
	return nil
}
