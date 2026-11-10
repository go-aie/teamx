package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Personal admission is independent of Workspace/Git authentication. The only
// authority for the recipient is the selected connection's verified /user ID.
func (m *roomManager) resolveTodosPlatformLocked(owner RoomIdentity, id string) (gitlab.TodosQueryScope, platform.Secret, error) {
	account, secret, err := m.resolvePlatformAccountLocked(owner, id, "gitlab")
	if err != nil {
		return gitlab.TodosQueryScope{}, secret, err
	}
	return gitlab.TodosQueryScope{Binding: gitlab.TodosBinding{PlatformID: account.PlatformID, PlatformGeneration: account.Generation, URL: account.URL, UserID: account.UserID, Username: account.Username, CredentialVersion: account.CredentialVersion, CredentialFingerprint: account.CredentialFingerprint}, PlatformVersion: account.PlatformVersion}, secret, nil
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
	current, _, err := m.resolveTodosPlatformLocked(s.RoomIdentity, s.Binding.PlatformID)
	if err != nil {
		return result, err
	}
	if !current.Binding.SameAccount(s.Binding) || current.Binding.PlatformGeneration != s.Binding.PlatformGeneration {
		return result, notification.ErrConflict
	}
	if current.Binding.CredentialVersion != s.Binding.CredentialVersion || current.Binding.CredentialFingerprint != s.Binding.CredentialFingerprint {
		return result, platform.ErrCredential
	}
	result.Data, _ = json.Marshal(current)
	result.Revision = fmt.Sprintf("%d|%s|%s|%d|%d|%s", current.PlatformVersion, current.Binding.PlatformID, current.Binding.PlatformGeneration, current.Binding.UserID, current.Binding.CredentialVersion, current.Binding.CredentialFingerprint)
	result.CredentialStamp = fmt.Sprintf("%s|%s|%d|%d|%s", current.Binding.PlatformID, current.Binding.PlatformGeneration, current.Binding.UserID, current.Binding.CredentialVersion, current.Binding.CredentialFingerprint)
	return result, nil
}
func (m *roomManager) preparePlatformTodos(_ context.Context, _ string, owner RoomIdentity, b gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
	read := func(ctx context.Context, path string, q url.Values, h http.Header, limit int64) (gitlab.APIResponse, error) {
		if err := ctx.Err(); err != nil {
			return gitlab.APIResponse{}, err
		}
		m.mu.Lock()
		current, _, err := m.resolveTodosPlatformLocked(owner, b.Binding.PlatformID)
		account, secret, accountErr := m.resolvePlatformAccountLocked(owner, b.Binding.PlatformID, "gitlab")
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
		current, _, checkErr := m.resolveTodosPlatformLocked(owner, b.Binding.PlatformID)
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
func (m *roomManager) currentTodosForTargetLocked(owner RoomIdentity, id string, target notification.Target) (*gitlab.TodosState, error) {
	for _, previous := range m.notifications.StatesLocked(owner) {
		meta := previous.Metadata()
		if meta.Key.Source == gitlab.TodosSourceID && meta.PlatformID == id && meta.Target.ScopeKey() == target.ScopeKey() && meta.Retired && m.attachmentCallsBusyLocked(meta.Key) {
			return nil, ErrBusy
		}
	}
	state, err := m.notifications.CurrentLocked(owner, gitlab.TodosSourceID, id, target)
	if err != nil || state == nil {
		return nil, notificationRoomError(err)
	}
	s, ok := state.(gitlab.TodosState)
	if !ok {
		return nil, ErrInvalid
	}
	return &s, nil
}
func (m *roomManager) configureTodosSubscription(ctx context.Context, key notification.Key, version uint64, cfg gitlab.TodosConfig) (gitlab.TodosState, error) {
	var targetErr error
	cfg.Target, targetErr = cfg.Target.Normalize()
	if targetErr != nil {
		return gitlab.TodosState{}, notificationRoomError(targetErr)
	}
	if key.Source != gitlab.TodosSourceID {
		return gitlab.TodosState{}, ErrRequest
	}
	m.mu.Lock()
	var target gitlab.TodosQueryScope
	if cfg.Enabled && cfg.PlatformID != "" {
		var targetErr error
		target, _, targetErr = m.resolveTodosPlatformLocked(key.Owner, cfg.PlatformID)
		if targetErr != nil {
			m.mu.Unlock()
			return gitlab.TodosState{}, notificationRoomError(targetErr)
		}
	}
	previous, _ := m.currentTodosForTargetLocked(key.Owner, m.subscriptionPlatformLocked(key, cfg.PlatformID), cfg.Target)
	next, done, err := m.configureTodosLocked(key, version, cfg)
	var waits []<-chan struct{}
	if err == nil && previous != nil && next.Retired {
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
		selected, _, selectErr := m.resolveTodosPlatformLocked(key.Owner, cfg.PlatformID)
		if selectErr != nil || selected != target {
			m.mu.Unlock()
			return next, ErrConflict
		}
		if saved, ok := m.gitlabNotifications[key.Owner][notificationScopeFor(cfg.PlatformID, cfg.Target)]; ok && saved.BackfillLocked && saved.Config.BackfillStart.IsZero() {
			cfg.BackfillSeconds, cfg.BackfillStart = 0, next.Baseline
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
	var targetErr error
	cfg.Target, targetErr = cfg.Target.Normalize()
	if targetErr != nil {
		return gitlab.TodosState{}, nil, targetErr
	}
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
	if old, exists := m.notifications.StateLocked(key); exists && (old.Metadata().Target != cfg.Target || old.Metadata().PlatformID != cfg.PlatformID) {
		return gitlab.TodosState{}, nil, notification.ErrIdentity
	}
	current, err := m.currentTodosForTargetLocked(key.Owner, m.subscriptionPlatformLocked(key, cfg.PlatformID), cfg.Target)
	if err != nil {
		return gitlab.TodosState{}, nil, err
	}
	if key.SubscriptionID == "" && (version != 0 || current != nil) || key.SubscriptionID != "" && (current == nil || current.SubscriptionID != key.SubscriptionID || current.SubscriptionVersion != version) {
		return gitlab.TodosState{}, nil, notification.ErrConflict
	}
	if current != nil && cfg.PlatformID == "" {
		cfg.PlatformID = current.Binding.PlatformID
	}
	if current == nil {
		if err := m.checkNotificationTargetLocked(context.Background(), key.Owner, cfg.Target); err != nil {
			return gitlab.TodosState{}, nil, roomNotificationError(err)
		}
	}
	if err := m.validateNotificationTargetLocked(context.Background(), key.Owner, cfg.Target, cfg.Enabled); err != nil {
		return gitlab.TodosState{}, nil, roomNotificationError(err)
	}
	cfg, err = gitlab.NormalizeTodosConfig(cfg)
	if err != nil {
		return gitlab.TodosState{}, nil, err
	}
	var b gitlab.TodosBinding
	if current == nil || cfg.Enabled {
		scope, _, e := m.resolveTodosPlatformLocked(key.Owner, cfg.PlatformID)
		if e != nil {
			return gitlab.TodosState{}, nil, e
		}
		b = scope.Binding
	} else {
		b = current.Binding
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
	next, err := gitlab.ConfigureTodos(key.Owner, current, b, cfg, m.notifications.Now())
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
	if err := m.checkNotificationTargetLocked(ctx, key.Owner, s.Config.Target); err != nil {
		m.mu.Unlock()
		return err
	}
	scope, _, err := m.resolveTodosPlatformLocked(key.Owner, s.Binding.PlatformID)
	if err == nil && (!scope.Binding.SameAccount(s.Binding) || scope.Binding.PlatformGeneration != s.Binding.PlatformGeneration) {
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
	next, err := gitlab.ConfigureTodos(key.Owner, &s, scope.Binding, s.Config, m.notifications.Now())
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
