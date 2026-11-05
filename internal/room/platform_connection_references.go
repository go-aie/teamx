package room

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/notification"
)

type platformConnectionReference struct {
	RoomIdentity
	Name    string `json:"name"`
	Pending bool   `json:"pending,omitempty"`
}

// Includes unfinished targets so deletion cannot invalidate recovery material.
// Unknown/corrupt Room data fails closed, before any connection mutation.
func (m *roomManager) platformConnectionReferencesLocked(id string) ([]platformConnectionReference, error) {
	refs := []platformConnectionReference{}
	subscribed := map[RoomIdentity]bool{}
	for _, owner := range m.notifications.ReferencesLocked(id) {
		subscribed[owner] = true
	}
	for _, entry := range m.index.Rooms {
		if entry.Status == "deleting" && entry.DeletionPhase == "removing" {
			continue
		}
		saved, err := m.readWorkspaceRoomConfig(entry)
		if err != nil {
			return nil, err
		}
		used := saved.Workspace != nil && saved.Workspace.Binding != nil && saved.Workspace.Binding.ConnectionID == id
		pending := false
		for _, op := range m.index.WorkspaceOperations {
			if op.RoomIdentity == entry.RoomIdentity && !op.finished() && op.Target.Binding != nil && op.Target.Binding.ConnectionID == id {
				pending = true
			}
		}
		toolsUsed := false
		if entry.Status == "ready" || entry.Status == "deleting" {
			bindings, err := m.readToolBindingsLocked(entry.RoomIdentity)
			if err != nil {
				return nil, err
			}
			for _, b := range bindings.Bindings {
				if b.ConnectionID == id {
					toolsUsed = true
				}
			}
		}
		if used || pending || subscribed[entry.RoomIdentity] || toolsUsed {
			refs = append(refs, platformConnectionReference{entry.RoomIdentity, entry.Name, pending})
		}
	}
	return refs, nil
}
func (m *roomManager) publicPlatformConnectionLocked(c connection.Connection) (platformConnection, error) {
	file, err := m.connections.Load()
	if err != nil {
		return platformConnection{}, platformConnectionError(err)
	}
	for _, record := range file.Connections {
		if record.ID == c.ID {
			c = connection.Public(file, record)
			break
		}
	}
	refs, err := m.platformConnectionReferencesLocked(c.ID)
	return platformConnection{Connection: c, References: refs}, err
}
func samePlatformReferences(refs []platformConnectionReference, acknowledged []RoomIdentity) bool {
	want := make([]RoomIdentity, 0, len(refs))
	for _, ref := range refs {
		want = append(want, ref.RoomIdentity)
	}
	return len(want) == 0 && len(acknowledged) == 0 || reflect.DeepEqual(want, acknowledged)
}

// Pause BEFORE committing the replacement. A partial failure preserves the
// previous connection, with already-paused subscriptions requiring explicit
// sync. Cancellation plus subscription revisions reject all late results.
func (m *roomManager) invalidatePlatformQueriesLocked(id string) error {
	m.cancelPlatformCallsLocked(func(c *platformCall) bool { return c.connectionID == id })
	return notificationRoomError(m.notifications.InvalidateConnectionLocked(id))
}

var errPlatformReferenced = fmt.Errorf("%w: connection is referenced; explicitly replace or unbind it first", ErrConflict)

// Requires manager.mu on entry AND return. A rotation owns the catalog while
// joining old query processes, but never closes the active model service.
func (m *roomManager) settlePlatformQueriesLocked(id string) error {
	pending := m.notifications.ConnectionDoneLocked(id)
	for c := range m.platformCalls {
		if c.connectionID == id {
			pending = append(pending, c.done)
		}
	}
	return m.settlePlatformCallsLocked(pending)
}

// Joins canceled work without holding the catalog gate. Requires mu on entry and return.
func (m *roomManager) settlePlatformCallsLocked(pending []<-chan struct{}) error {
	if len(pending) == 0 {
		return nil
	}
	m.busy = true
	done := make(chan struct{})
	m.operationDone = done
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	err := notification.Wait(ctx, pending)
	cancel()
	if err != nil {
		err = ErrBusy
	}
	m.mu.Lock()
	m.busy = false
	close(done)
	return err
}
