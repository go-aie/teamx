package room

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform/lark"
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

func defaultNotificationAdapters(m *roomManager) map[notification.SourceID]notificationAdapter {
	catalog := defaultPlatformCatalog()
	if m != nil && m.platformCatalog != nil {
		catalog = m.platformCatalog
	}
	bindings := platforms.SourceBindings{GitLabEvents: m.preparePlatformEvents, GitLabTodos: m.preparePlatformTodos, LarkReader: func(q lark.QueryScope) (lark.MessageReader, error) {
		return m.larkClients.New(q.Profile)
	}}
	adapters := make(map[notification.SourceID]notificationAdapter)
	for _, definition := range catalog.Definitions() {
		for _, source := range definition.Sources {
			var adapter notificationAdapter
			switch source.ID.Provider {
			case "gitlab":
				adapter = m.gitlabNotificationLifecycle(source.ID)
			case "lark":
				adapter = m.larkNotificationLifecycle()
			default:
				panic("notification lifecycle adapter missing: " + source.ID.Provider)
			}
			adapter.Source = source.Bind(bindings)
			if adapter.Source == nil || adapter.Source.ID() != source.ID {
				panic("notification source binding mismatch")
			}
			adapters[source.ID] = adapter
		}
	}
	return adapters
}

func (m *roomManager) initPlatformNotifications() {
	m.gitlabNotifications = make(map[RoomIdentity]map[notificationScope]gitlabNotificationRecord)
	m.gitlabConfiguring = make(map[platformNotificationKey]bool)
	if m.notificationAdapters == nil {
		m.notificationAdapters = defaultNotificationAdapters(m)
	}
	sources := make([]notification.Source, 0, len(m.notificationAdapters))
	for _, adapter := range m.notificationAdapters {
		sources = append(sources, adapter.Source)
	}
	m.notifications = notification.New(notification.Host{
		Changed: m.observeNotificationChange,
		Check:   m.store.check,
		Directory: func(owner RoomIdentity) (string, error) {
			return filepath.Join(m.roomDir(owner.RoomID), "notifications"), nil
		},
		EnsureDirectory: makeDurableDir,
		Save:            m.store.save,
		Admit:           m.admitPlatformNotification,
		Deliver:         m.commitPlatformNotification,
		Expiry:          func() notification.ExpiryPolicy { return m.config.inboxExpiry.Resolved() },
		Maintain:        m.requestNotificationMaintenance,
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
		if err := m.loadGitLabNotificationsLocked(entry.RoomIdentity); err != nil {
			return err
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

type notificationRosterKey struct{}
type notificationRoster struct {
	owner   RoomIdentity
	members []MemberState
}

// Obtain the Room-native roster outside the directory lock. Admission then
// rechecks runtime identity and member-change fences against this fixed view.
func (m *roomManager) admitPlatformNotification(ctx context.Context, state notification.State) (notification.Scope, error) {
	owner := state.Metadata().Key.Owner
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(owner)
	m.mu.Unlock()
	var members []MemberState
	if svc != nil && !svc.closing.Load() {
		list, err := svc.room.Call[MemberList](ctx, ListMembers{owner})
		if err != nil {
			return notification.Scope{}, roomNotificationError(err)
		}
		for _, member := range list.Members {
			members = append(members, member.MemberState)
		}
	} else {
		snapshot, err := readPublishedRoom(m.roomDir(owner.RoomID), owner)
		if err != nil {
			return notification.Scope{}, roomNotificationError(err)
		}
		members = snapshot.Members
	}
	ctx = context.WithValue(ctx, notificationRosterKey{}, notificationRoster{owner, members})
	m.mu.Lock()
	defer m.mu.Unlock()
	current, _ := m.runtimeServiceLocked(owner)
	if current != svc {
		return notification.Scope{}, notification.ErrBusy
	}
	return m.admitPlatformNotificationLocked(ctx, state)
}

// Query admission captures the connection and configuration under the short
// manager lock. Source publication independently checks the captured data revision.
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
	if meta.Key.Source.Provider == "gitlab" {
		if m.gitlabConfiguring[notificationApplyKey(meta.Key.Owner, meta.PlatformID, meta.Target)] {
			return scope, notification.ErrBusy
		}
		if saved, ok := m.gitlabNotifications[meta.Key.Owner][notificationScopeFor(meta.PlatformID, meta.Target)]; ok && !gitlabConfigApplied(saved.Config, state) {
			return scope, notification.ErrBusy
		}
	}
	entry, err := m.workspaceRoom(meta.Key.Owner)
	if err != nil || entry.Status != "ready" {
		return scope, notification.ErrIdentity
	}
	if err := m.checkNotificationTargetLocked(ctx, meta.Key.Owner, meta.Target); err != nil {
		return scope, roomNotificationError(err)
	}
	if meta.Target.Mode == "shared_inbox" {
		svc, _ := m.runtimeServiceLocked(meta.Key.Owner)
		if svc == nil {
			return scope, notification.ErrBusy
		}
		p := svc.store.sharedHandle.Load()
		active := false
		if p != nil {
			for _, g := range p.view.Load().root.Grants {
				if g.Status == "active" {
					active = true
				}
			}
		}
		if !active && !notification.DeliveryAdmission(ctx) {
			return scope, notification.ErrBusy
		}
	}
	adapter, ok := m.notificationAdapters[meta.Key.Source]
	if !ok || adapter.Admit == nil {
		return scope, notification.ErrRequest
	}
	scope, err = adapter.Admit(ctx, state)
	if err != nil {
		return scope, err
	}
	if saved, ok := m.gitlabNotifications[meta.Key.Owner][notificationScopeFor(meta.PlatformID, meta.Target)]; ok && meta.Key.Source.Provider == "gitlab" {
		scope.Revision += fmt.Sprintf("|config:%d", saved.Version)
	}
	scope.Directory = filepath.Join(m.roomDir(meta.Key.Owner.RoomID), "notification-queries", meta.Key.Source.Provider+"."+meta.Key.Source.Channel+"."+meta.Key.SubscriptionID)
	svc, _ := m.runtimeServiceLocked(meta.Key.Owner)
	scope.Current = !m.roomBusyLocked(meta.Key.Owner) && svc != nil && !svc.closing.Load() && !svc.restarting.Load()
	return scope, nil
}

// Reserve the exact runtime under the directory lock, then let the target own
// the accepted write. The runtime change lease survives through its receipt.
func (m *roomManager) commitPlatformNotification(ctx context.Context, meta notification.Metadata, item notification.Item) error {
	owner := meta.Key.Owner
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(owner)
	if m.closing || m.roomBusyLocked(owner) || svc == nil || !svc.changes.TryRLock() {
		m.mu.Unlock()
		return notification.ErrBusy
	}
	if svc.closing.Load() || svc.restarting.Load() {
		svc.changes.RUnlock()
		m.mu.Unlock()
		return notification.ErrBusy
	}
	m.mu.Unlock()
	defer svc.changes.RUnlock()
	return roomNotificationError(svc.receiveInbox(ctx, item.Route.Target, item))
}

// Callers hold manager.mu for domain checks only. Persistence and actor waits
// occur outside it; the source owner rejects a changed data revision.
func (m *roomManager) saveNotificationLocked(state, previous notification.State) error {
	m.mu.Unlock()
	err := m.notifications.SaveFrom(state, previous)
	m.mu.Lock()
	return err
}
