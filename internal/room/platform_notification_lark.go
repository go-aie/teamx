package room

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
)

func larkBinding(a registry.Account) lark.Binding {
	return lark.Binding{PlatformID: a.PlatformID, Generation: a.Generation, Account: a.Identity, CredentialVersion: a.CredentialVersion, Fingerprint: a.CredentialFingerprint}
}

// Lifecycle operations follow the registered codec. Production registers only
// search collection after the explicit offline upgrade.
func (m *roomManager) defaultLarkConfig(id string) lark.Config {
	if m.notifications.Source(lark.MessagesSourceID).Schema() == lark.SearchSchema {
		return lark.DefaultSearchConfig(id)
	}
	return lark.DefaultConfig(id)
}

func (m *roomManager) configureLarkState(owner RoomIdentity, current *lark.State, binding lark.Binding, cfg lark.Config) (lark.State, error) {
	if m.notifications.Source(lark.MessagesSourceID).Schema() == lark.SearchSchema {
		return lark.ConfigureSearch(owner, current, binding, cfg, m.notifications.Now())
	}
	return lark.Configure(owner, current, binding, cfg, m.notifications.Now())
}

// Verification restores only subscriptions paused for credentials under the
// same native account. ChangedLocked fences the old worker; it starts the new
// version after its predecessor exits, without waiting under manager.mu.
func (m *roomManager) resumeLarkSubscriptionsLocked(platformID string) error {
	for _, room := range m.index.Rooms {
		if room.Status != "ready" || m.roomBusyLocked(room.RoomIdentity) {
			continue
		}
		for _, state := range m.notifications.StatesLocked(room.RoomIdentity) {
			s, ok := state.(lark.State)
			if !ok || s.Retired || !s.Config.Enabled || s.Binding.PlatformID != platformID || s.Status != "credentials_invalid" {
				continue
			}
			a, _, err := m.resolvePlatformAccountLocked(room.RoomIdentity, platformID, "lark")
			if err != nil {
				return notificationRoomError(err)
			}
			if a.Generation != s.Binding.Generation || a.Identity != s.Binding.Account {
				continue
			}
			next, err := m.configureLarkState(room.RoomIdentity, &s, larkBinding(a), s.Config)
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
	return notificationAdapter{Source: &lark.SearchSource{Source: lark.Source{Reader: func(q lark.QueryScope) (lark.MessageReader, error) {
		return m.larkClients.New(q.Profile)
	}}}, Admit: func(_ context.Context, state notification.State) (notification.Scope, error) {
		s, ok := state.(lark.State)
		if !ok {
			return notification.Scope{}, notification.ErrInvalid
		}
		if s.Retired {
			return notification.Scope{}, notification.ErrIdentity
		}
		a, _, err := m.resolvePlatformAccountLocked(s.RoomIdentity, s.Binding.PlatformID, "lark")
		if err != nil {
			return notification.Scope{}, err
		}
		if a.Generation != s.Binding.Generation || a.Identity != s.Binding.Account {
			return notification.Scope{}, notification.ErrConflict
		}
		if larkBinding(a) != s.Binding || a.CLIProfile == nil {
			return notification.Scope{}, platform.ErrCredential
		}
		q := lark.QueryScope{Binding: s.Binding, Profile: *a.CLIProfile, PlatformVersion: a.PlatformVersion}
		data, _ := json.Marshal(q)
		return notification.Scope{Data: data, Revision: fmt.Sprintf("%d|%s", a.PlatformVersion, data), CredentialStamp: fmt.Sprintf("%d|%s", a.CredentialVersion, a.CredentialFingerprint)}, nil
	}}
}

func (m *roomManager) currentLarkForTargetLocked(owner RoomIdentity, id string, target notification.Target) (*lark.State, error) {
	for _, previous := range m.notifications.StatesLocked(owner) {
		meta := previous.Metadata()
		if meta.Key.Source == lark.MessagesSourceID && meta.PlatformID == id && meta.Target.ScopeKey() == target.ScopeKey() && meta.Retired && m.attachmentCallsBusyLocked(meta.Key) {
			return nil, ErrBusy
		}
	}
	state, err := m.notifications.CurrentLocked(owner, lark.MessagesSourceID, id, target)
	if err != nil || state == nil {
		return nil, notificationRoomError(err)
	}
	s, ok := state.(lark.State)
	if !ok {
		return nil, ErrInvalid
	}
	return &s, nil
}
func (m *roomManager) configureLarkSubscription(ctx context.Context, key notification.Key, version uint64, cfg lark.Config) (lark.State, error) {
	var normalizeErr error
	cfg, normalizeErr = lark.NormalizeConfig(cfg)
	if normalizeErr != nil {
		return lark.State{}, notificationRoomError(normalizeErr)
	}
	if cfg.Groups.Scope == "all" && m.notifications.Source(lark.MessagesSourceID).Schema() != lark.SearchSchema {
		return lark.State{}, ErrRequest
	}
	if err := ctx.Err(); err != nil {
		return lark.State{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkOnboardingContextLocked(ctx); err != nil {
		return lark.State{}, err
	}

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
	if old, exists := m.notifications.StateLocked(key); exists && (old.Metadata().Target != cfg.Target || old.Metadata().PlatformID != cfg.PlatformID) {
		return lark.State{}, ErrIdentity
	}
	current, err := m.currentLarkForTargetLocked(key.Owner, m.subscriptionPlatformLocked(key, cfg.PlatformID), cfg.Target)
	if err != nil {
		return lark.State{}, err
	}
	if key.SubscriptionID == "" && (version != 0 || current != nil) || key.SubscriptionID != "" && (current == nil || current.SubscriptionID != key.SubscriptionID || current.SubscriptionVersion != version) {
		return lark.State{}, ErrConflict
	}
	if current != nil && cfg.PlatformID != current.Binding.PlatformID {
		return lark.State{}, ErrConflict
	}
	if current == nil {
		if err := m.checkNotificationTargetLocked(ctx, key.Owner, cfg.Target); err != nil {
			return lark.State{}, err
		}
	}
	if err = m.validateNotificationTargetLocked(ctx, key.Owner, cfg.Target, cfg.Enabled); err != nil {
		return lark.State{}, err
	}
	var b lark.Binding
	if current != nil {
		b = current.Binding
	}
	if cfg.Enabled || current == nil {
		a, _, e := m.resolvePlatformAccountLocked(key.Owner, cfg.PlatformID, "lark")
		if e != nil {
			return lark.State{}, notificationRoomError(e)
		}
		b = larkBinding(a)
	}
	// A different native account is a new opt-in; old evidence remains
	// inspectable in its retired snapshot, with its original frozen destinations.
	if current != nil && cfg.Enabled && (b.PlatformID != current.Binding.PlatformID || b.Generation != current.Binding.Generation || b.Account != current.Binding.Account) {
		retired, e := lark.Retire(*current)
		if e != nil {
			return retired, notificationRoomError(e)
		}
		if e = m.notifications.SaveLocked(retired); e != nil {
			return retired, notificationRoomError(e)
		}
		done := m.notifications.ChangedLocked(retired.Metadata().Key)
		waits := m.cancelAttachmentCallsLocked(retired.Metadata().Key)
		if done != nil {
			waits = append(waits, done)
		}
		m.mu.Unlock()
		e = notification.Wait(ctx, waits)
		m.mu.Lock()
		if e != nil {
			return retired, e
		}
		active, e := m.currentLarkForTargetLocked(key.Owner, m.subscriptionPlatformLocked(key, cfg.PlatformID), cfg.Target)
		a, _, accountErr := m.resolvePlatformAccountLocked(key.Owner, cfg.PlatformID, "lark")
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
	next, err := m.configureLarkState(key.Owner, current, b, cfg)
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
func (m *roomManager) syncLarkSubscription(ctx context.Context, key notification.Key, version uint64, target notification.Target) error {
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
	target, err := target.Normalize()
	if err != nil {
		return notificationRoomError(err)
	}
	if s.Config.Target != target {
		return ErrIdentity
	}
	if err := m.validateNotificationTargetLocked(ctx, key.Owner, target, true); err != nil {
		return err
	}
	if s.Retired || !s.Config.Enabled {
		return ErrRequest
	}
	a, _, err := m.resolvePlatformAccountLocked(key.Owner, s.Binding.PlatformID, "lark")
	if err != nil {
		return notificationRoomError(err)
	}
	next, err := m.configureLarkState(key.Owner, &s, larkBinding(a), s.Config)
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
	if err != nil {
		return err
	}
	latest, ok := m.notifications.StateLocked(key)
	if !ok || latest.Metadata().Version != next.SubscriptionVersion || latest.Metadata().Target != target {
		return ErrConflict
	}
	if err := m.checkNotificationTargetLocked(ctx, key.Owner, target); err != nil {
		return err
	}
	account, _, err := m.resolvePlatformAccountLocked(key.Owner, next.Binding.PlatformID, "lark")
	if err != nil || larkBinding(account) != next.Binding {
		return ErrConflict
	}
	m.notifications.WakeLocked(key)
	return nil
}
