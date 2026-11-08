package room

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func larkBinding(a connection.Account) lark.Binding {
	return lark.Binding{ConnectionID: a.ConnectionID, Generation: a.Generation, Account: a.Identity, CredentialVersion: a.CredentialVersion, Fingerprint: a.CredentialFingerprint}
}

// Verification restores only subscriptions paused for credentials under the
// same native account. ChangedLocked fences the old worker; it starts the new
// version after its predecessor exits, without waiting under manager.mu.
func (m *roomManager) resumeLarkSubscriptionsLocked(connectionID string) error {
	for _, room := range m.index.Rooms {
		if room.Status != "ready" || m.roomBusyLocked(room.RoomIdentity) {
			continue
		}
		for _, state := range m.notifications.StatesLocked(room.RoomIdentity) {
			s, ok := state.(lark.State)
			if !ok || s.Retired || !s.Config.Enabled || s.Binding.ConnectionID != connectionID || s.Status != "credentials_invalid" {
				continue
			}
			a, _, err := m.resolvePlatformAccountLocked(room.RoomIdentity, connectionID, "lark")
			if err != nil {
				return notificationRoomError(err)
			}
			if a.Generation != s.Binding.Generation || a.Identity != s.Binding.Account {
				continue
			}
			next, err := lark.Configure(room.RoomIdentity, &s, larkBinding(a), s.Config, m.notifications.Now())
			if err != nil {
				return notificationRoomError(err)
			}
			if err := m.notifications.SaveLocked(next); err != nil {
				return notificationRoomError(err)
			}
			if m.notifications.ChangedLocked(next.Metadata().Key) == nil {
				m.notifications.WakeLocked(next.Metadata().Key)
			}
		}
	}
	return nil
}

func (m *roomManager) larkNotificationAdapter() notificationAdapter {
	return notificationAdapter{Source: &lark.Source{Reader: func(q lark.QueryScope) (lark.MessageReader, error) {
		return m.larkClients.New(q.Profile)
	}}, Admit: func(_ context.Context, state notification.State) (notification.Scope, error) {
		s, ok := state.(lark.State)
		if !ok {
			return notification.Scope{}, notification.ErrInvalid
		}
		if s.Retired {
			return notification.Scope{}, notification.ErrIdentity
		}
		a, _, err := m.resolvePlatformAccountLocked(s.RoomIdentity, s.Binding.ConnectionID, "lark")
		if err != nil {
			return notification.Scope{}, err
		}
		if a.Generation != s.Binding.Generation || a.Identity != s.Binding.Account {
			return notification.Scope{}, notification.ErrConflict
		}
		if larkBinding(a) != s.Binding || a.CLIProfile == nil {
			return notification.Scope{}, platform.ErrCredential
		}
		q := lark.QueryScope{Binding: s.Binding, Profile: *a.CLIProfile, ConnectionVersion: a.ConnectionVersion}
		data, _ := json.Marshal(q)
		return notification.Scope{Data: data, Revision: fmt.Sprintf("%d|%s", a.ConnectionVersion, data), CredentialStamp: fmt.Sprintf("%d|%s", a.CredentialVersion, a.CredentialFingerprint)}, nil
	}, CheckLoaded: func(owner RoomIdentity) error { _, err := m.currentLarkLocked(owner); return err }}
}
func (m *roomManager) currentLarkLocked(owner RoomIdentity) (*lark.State, error) {
	var current *lark.State
	for _, state := range m.notifications.StatesLocked(owner) {
		if s, ok := state.(lark.State); ok {
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
func (m *roomManager) configureLarkSubscription(ctx context.Context, key notification.Key, version uint64, cfg lark.Config) (lark.State, error) {
	var normalizeErr error
	cfg, normalizeErr = lark.NormalizeConfig(cfg)
	if normalizeErr != nil {
		return lark.State{}, notificationRoomError(normalizeErr)
	}
	if err := ctx.Err(); err != nil {
		return lark.State{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if key.Source != lark.MessagesSourceID {
		return lark.State{}, ErrRequest
	}
	if m.closing || m.roomBusyLocked(key.Owner) {
		return lark.State{}, ErrBusy
	}
	entry, err := m.workspaceRoom(key.Owner)
	if err != nil || entry.Status != "ready" {
		return lark.State{}, ErrIdentity
	}
	current, err := m.currentLarkLocked(key.Owner)
	if err != nil {
		return lark.State{}, err
	}
	if key.SubscriptionID == "" && (version != 0 || current != nil) || key.SubscriptionID != "" && (current == nil || current.SubscriptionID != key.SubscriptionID || current.SubscriptionVersion != version) {
		return lark.State{}, ErrConflict
	}
	if err = m.validateNotificationTargetLocked(ctx, key.Owner, cfg.Target, cfg.Enabled); err != nil {
		return lark.State{}, err
	}
	var b lark.Binding
	if current != nil {
		b = current.Binding
	}
	if cfg.Enabled {
		a, _, e := m.resolvePlatformAccountLocked(key.Owner, cfg.ConnectionID, "lark")
		if e != nil {
			return lark.State{}, notificationRoomError(e)
		}
		b = larkBinding(a)
	}
	// A different native account/connection is a new opt-in; old evidence remains
	// inspectable in its retired snapshot, with its original frozen destinations.
	if current != nil && cfg.Enabled && (b.ConnectionID != current.Binding.ConnectionID || b.Generation != current.Binding.Generation || b.Account != current.Binding.Account) {
		retired, e := lark.Retire(*current)
		if e != nil {
			return retired, notificationRoomError(e)
		}
		if e = m.notifications.SaveLocked(retired); e != nil {
			return retired, notificationRoomError(e)
		}
		done := m.notifications.ChangedLocked(retired.Metadata().Key)
		m.mu.Unlock()
		if done != nil {
			e = notification.Wait(ctx, []<-chan struct{}{done})
		}
		m.mu.Lock()
		if e != nil {
			return retired, e
		}
		active, e := m.currentLarkLocked(key.Owner)
		a, _, accountErr := m.resolvePlatformAccountLocked(key.Owner, cfg.ConnectionID, "lark")
		if m.closing || m.roomBusyLocked(key.Owner) || e != nil || active != nil || accountErr != nil || larkBinding(a) != b {
			return retired, ErrConflict
		}
		current = nil
	}
	if err := ctx.Err(); err != nil {
		return lark.State{}, err
	}
	if err := m.validateNotificationTargetLocked(ctx, key.Owner, cfg.Target, cfg.Enabled); err != nil {
		return lark.State{}, err
	}
	next, err := lark.Configure(key.Owner, current, b, cfg, m.notifications.Now())
	if err != nil {
		return next, notificationRoomError(err)
	}
	if err = m.notifications.SaveLocked(next); err != nil {
		return next, notificationRoomError(err)
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
	return next, nil
}
func (m *roomManager) syncLarkSubscription(ctx context.Context, key notification.Key, version uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closing || m.roomBusyLocked(key.Owner) {
		return ErrBusy
	}
	state, ok := m.notifications.StateLocked(key)
	if !ok {
		return ErrIdentity
	}
	s, ok := state.(lark.State)
	if !ok || s.SubscriptionVersion != version {
		return ErrConflict
	}
	if s.Retired || !s.Config.Enabled {
		return ErrRequest
	}
	a, _, err := m.resolvePlatformAccountLocked(key.Owner, s.Binding.ConnectionID, "lark")
	if err != nil {
		return notificationRoomError(err)
	}
	next, err := lark.Configure(key.Owner, &s, larkBinding(a), s.Config, m.notifications.Now())
	if err != nil {
		return notificationRoomError(err)
	}
	if err = m.notifications.SaveLocked(next); err != nil {
		return notificationRoomError(err)
	}
	done := m.notifications.ChangedLocked(key)
	m.mu.Unlock()
	if done != nil {
		err = notification.Wait(ctx, []<-chan struct{}{done})
	}
	m.mu.Lock()
	if err == nil {
		m.notifications.WakeLocked(key)
	}
	return err
}
