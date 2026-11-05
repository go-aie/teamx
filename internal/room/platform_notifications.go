package room

import (
	"context"
	"path/filepath"

	"github.com/go-aie/teamx/internal/platform/notification"
)

// Built once at service composition. Each source owns its codec, query scope
// and optional cross-subscription recovery check; no dynamic plugin loader.
// Admit and CheckLoaded run with manager.mu held.
type notificationAdapter struct {
	Source      notification.Source
	Admit       func(context.Context, notification.State) (notification.Scope, error)
	CheckLoaded func(RoomIdentity) error
}

func (m *roomManager) initPlatformNotifications() {
	if m.notificationAdapters == nil {
		m.notificationAdapters = m.gitlabNotificationAdapters()
	}
	sources := make([]notification.Source, 0, len(m.notificationAdapters))
	for _, adapter := range m.notificationAdapters {
		sources = append(sources, adapter.Source)
	}
	m.notifications = notification.New(notification.Host{
		Gate: &m.mu,
		Check: func(writing bool) error {
			if writing && m.closing {
				return notification.ErrClosing
			}
			return m.store.check(writing)
		},
		Directory: func(owner RoomIdentity) (string, error) {
			entry, err := m.workspaceRoom(owner)
			if err != nil || entry.Status != "ready" {
				return "", notification.ErrIdentity
			}
			return filepath.Join(m.roomDir(owner.RoomID), "notifications"), nil
		},
		EnsureDirectory: makeDurableDir,
		Save:            m.store.save,
		Admit:           m.admitPlatformNotificationLocked,
		Deliver:         m.commitPlatformNotificationLocked,
	}, sources...)
}
func (m *roomManager) loadPlatformNotificationsLocked() error {
	for _, entry := range m.index.Rooms {
		if entry.Status != "ready" {
			continue
		}
		if err := m.notifications.LoadLocked(entry.RoomIdentity); err != nil {
			return notificationRoomError(err)
		}
		for _, adapter := range m.notificationAdapters {
			if adapter.CheckLoaded != nil {
				if err := adapter.CheckLoaded(entry.RoomIdentity); err != nil {
					return notificationRoomError(err)
				}
			}
		}
	}
	return nil
}

// This is the sole query admission boundary. The service holds manager.mu while
// invoking it and while committing any accepted candidate. Network work happens
// after the immutable scope has been returned and the gate released.
func (m *roomManager) admitPlatformNotificationLocked(ctx context.Context, state notification.State) (notification.Scope, error) {
	var scope notification.Scope
	if err := ctx.Err(); err != nil {
		return scope, err
	}
	if m.closing {
		return scope, notification.ErrClosing
	}
	if err := m.store.check(true); err != nil {
		return scope, err
	}
	meta := state.Metadata()
	entry, err := m.workspaceRoom(meta.Key.Owner)
	if err != nil || entry.Status != "ready" {
		return scope, notification.ErrIdentity
	}
	adapter, ok := m.notificationAdapters[meta.Key.Source]
	if !ok || adapter.Admit == nil {
		return scope, notification.ErrRequest
	}
	scope, err = adapter.Admit(ctx, state)
	if err != nil {
		return scope, err
	}
	scope.Directory = filepath.Join(m.roomDir(meta.Key.Owner.RoomID), "notification-queries", meta.Key.Source.Provider+"."+meta.Key.Source.Channel+"."+meta.Key.SubscriptionID)
	svc, _ := m.runtimeServiceLocked(meta.Key.Owner)
	scope.Current = !m.roomBusyLocked(meta.Key.Owner) && svc != nil && !svc.closing.Load() && !svc.restarting.Load()
	return scope, nil
}

// Gate -> service changes read lock -> target actor. Holding the catalog gate
// through the reply prevents activation/deletion from crossing this commit.
func (m *roomManager) commitPlatformNotificationLocked(ctx context.Context, meta notification.Metadata, item notification.Item) error {
	owner := meta.Key.Owner
	svc, _ := m.runtimeServiceLocked(owner)
	if m.closing || m.roomBusyLocked(owner) || svc == nil {
		return notification.ErrBusy
	}
	if !svc.changes.TryRLock() {
		return notification.ErrBusy
	}
	defer svc.changes.RUnlock()
	if svc.closing.Load() || svc.restarting.Load() {
		return notification.ErrBusy
	}
	if item.Route.Target.Inbox() {
		return roomNotificationError(svc.receiveInbox(ctx, item.Route.Target, item))
	}
	_, err := svc.room.Call[PostReceipt](ctx, postIntegrationMessage{RoomIdentity: owner, DeliveryID: item.DeliveryID, Text: item.Text, Source: item.Source})
	return roomNotificationError(err)
}
