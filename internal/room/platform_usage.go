package room

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
)

type managedPlatformReference struct {
	RoomIdentity
	Name    string `json:"name"`
	Pending bool   `json:"pending,omitempty"`
}

// Includes unfinished targets so deletion cannot invalidate recovery material.
// Unknown/corrupt Room data fails closed, before any connection mutation.
func (m *roomManager) managedPlatformReferencesLocked(id string) ([]managedPlatformReference, error) {
	ops, err := m.onboardingOperationsLocked()
	if err != nil {
		return nil, err
	}
	refs := []managedPlatformReference{}
	subscribed := map[RoomIdentity]bool{}
	for _, owner := range m.notifications.ReferencesLocked(id) {
		subscribed[owner] = true
	}
	for owner, saved := range m.gitlabNotifications {
		for key := range saved {
			if key.PlatformID == id {
				subscribed[owner] = true
			}
		}
	}
	for _, entry := range m.index.Rooms {
		if entry.Status == "deleting" && entry.DeletionPhase == "removing" {
			continue
		}
		saved, err := m.readWorkspaceRoomConfig(entry)
		if err != nil {
			return nil, err
		}
		used := saved.Workspace != nil && saved.Workspace.Binding != nil && saved.Workspace.Binding.PlatformID == id
		pending := false
		for _, op := range ops {
			if op.PlatformID == id && op.Input.RoomIdentity == entry.RoomIdentity && !op.Cancelled && (!onboardingFinished(op.Notifications) || !onboardingFinished(op.Tool)) {
				pending = true
			}
		}
		for _, op := range m.index.WorkspaceOperations {
			if op.RoomIdentity == entry.RoomIdentity && !op.finished() && op.Target.Binding != nil && op.Target.Binding.PlatformID == id {
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
				if b.PlatformID == id {
					toolsUsed = true
				}
			}
		}
		if used || pending || subscribed[entry.RoomIdentity] || toolsUsed {
			refs = append(refs, managedPlatformReference{entry.RoomIdentity, entry.Name, pending})
		}
	}
	return refs, nil
}
func (m *roomManager) publicPlatformLocked(c platform.Platform) (managedPlatform, error) {
	file, err := m.platforms.Load()
	if err != nil {
		return managedPlatform{}, managedPlatformError(err)
	}
	var capabilities *larkPlatformCapabilityResult
	for _, record := range file.Platforms {
		if record.ID == c.ID {
			if record.Provider == "lark" {
				p := m.larkPlatformCapabilitiesLocked(record)
				capabilities = &p
			}
			c = registry.Public(file, record)
			if record.Provider == "gitlab" && record.CredentialID != "" && c.AuthorizationStatus != "logged_out" {
				_, app, e := registry.OAuthRecord(file, record.CredentialID)
				if e == nil && app.Source != "" {
					if _, e = m.currentGitLabApplicationLocked(app); e != nil {
						c.AuthorizationStatus = "application_unavailable"
					}
				}
			}
			break
		}
	}
	if c.TokenExpiresAt.After(time.Now()) {
		m.updates.atEarliest(uiUpdate{Topic: "platforms"}, c.TokenExpiresAt)
	}
	refs, err := m.managedPlatformReferencesLocked(c.ID)
	return managedPlatform{SupportsNotifications: m.supportsNotifications(c.Provider), Platform: c, References: refs, Capabilities: capabilities}, err
}
func samePlatformReferences(refs []managedPlatformReference, acknowledged []RoomIdentity) bool {
	want := make([]RoomIdentity, 0, len(refs))
	for _, ref := range refs {
		want = append(want, ref.RoomIdentity)
	}
	return len(want) == 0 && len(acknowledged) == 0 || reflect.DeepEqual(want, acknowledged)
}

// Pause BEFORE committing the replacement. A partial failure preserves the
// previous connection. Paused subscriptions require verified same-account
// recovery or explicit sync. Revisions reject all late results.
type platformQueryPause struct {
	notifications *notification.PlatformPause
	pending       []<-chan struct{}
}

func (p *platformQueryPause) release() {
	if p != nil {
		p.notifications.Release()
	}
}

// Requires manager.mu on entry and return. Freeze all shared connections
// before publishing invalidated subscriptions, and retain the exact receipts
// captured at cancellation rather than querying replacement workers later.
func (m *roomManager) invalidatePlatformQueriesLocked(ids ...string) (*platformQueryPause, error) {
	p := &platformQueryPause{notifications: m.notifications.PausePlatforms(ids...)}
	p.pending = p.notifications.Done()
	for _, id := range ids {
		p.pending = append(p.pending, m.cancelPlatformCallsLocked(func(c *platformCall) bool { return c.platformID == id })...)
	}
	m.mu.Unlock()
	var err error
	for _, id := range ids {
		if err = m.notifications.InvalidatePlatformLocked(id); err != nil {
			break
		}
	}
	m.mu.Lock()
	return p, notificationRoomError(err)
}

var errPlatformReferenced = fmt.Errorf("%w: connection is referenced; explicitly replace or unbind it first", ErrConflict)
var errPlatformSettlement = fmt.Errorf("%w: previous platform work has not settled", ErrBusy)

// Requires manager.mu on entry AND return. A rotation owns the catalog while
// joining old query processes, but never closes the active model service.
func (m *roomManager) settlePlatformQueriesLocked(p *platformQueryPause) error {
	return m.settlePlatformQueryReceiptsLocked(p.pending)
}

func (m *roomManager) settlePlatformQueryReceiptsLocked(pending []<-chan struct{}) error {
	if err := m.settlePlatformCallsLocked(pending); err != nil {
		if errors.Is(err, ErrBusy) {
			return errPlatformSettlement
		}
		return err
	}
	return nil
}

// Joins canceled work without holding the catalog gate. Requires mu on entry and return.
func (m *roomManager) settlePlatformCallsLocked(pending []<-chan struct{}) error {
	if len(pending) == 0 {
		return nil
	}
	m.busy = true
	m.catalogChangedLocked()
	done := make(chan struct{})
	m.operationDone = done
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	err := platforms.WaitCalls(ctx, pending)
	cancel()
	if err != nil {
		err = ErrBusy
	}
	m.mu.Lock()
	m.busy = false
	m.catalogChangedLocked()
	close(done)
	return err
}
