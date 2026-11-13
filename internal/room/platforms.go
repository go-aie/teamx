package room

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/registry"
)

type managedPlatform struct {
	Onboarding   *platformOnboarding           `json:"onboarding,omitempty"`
	Capabilities *larkPlatformCapabilityResult `json:"capabilities,omitempty"`
	platform.Platform
	References []managedPlatformReference `json:"references,omitempty"`
}
type managedPlatformValidation = platform.Validation
type managedPlatformRecord = registry.Record
type managedPlatformsFile = registry.Snapshot

// Requests may carry secrets and must never appear in generic diagnostics.
type managedPlatformRequest struct {
	OnboardingID    string               `json:"onboarding_id,omitempty"`
	CLIProfile      *platform.CLIProfile `json:"cli_profile,omitempty"`
	ReferencedRooms []RoomIdentity       `json:"referenced_rooms,omitempty"`
	ServiceEpoch    string               `json:"service_epoch"`
	Caller          Caller               `json:"caller"`
	ID              string               `json:"id"`
	Version         uint64               `json:"version"`
	Name            string               `json:"name,omitempty"`
	Provider        string               `json:"provider,omitempty"`
	URL             string               `json:"url,omitempty"`
}

func (managedPlatformRequest) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[REDACTED platform request]")
}

func managedPlatformError(err error) error {
	for _, pair := range [][2]error{{registry.ErrRequest, ErrRequest}, {registry.ErrConflict, ErrConflict}, {registry.ErrCapacity, ErrCapacity}, {registry.ErrInvalid, ErrInvalid}} {
		if errors.Is(err, pair[0]) {
			return fmt.Errorf("%w: %s", pair[1], err)
		}
	}
	return err
}

// These adapters require manager.mu. Platform persistence shares the service
// Store's directory lock, atomic writer and commit-unknown latch.
func (m *roomManager) readPlatforms() (managedPlatformsFile, error) {
	file, err := m.platforms.Load()
	return file, managedPlatformError(err)
}
func (m *roomManager) updatePlatform(req managedPlatformRequest, remove bool) (managedPlatform, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy {
		return managedPlatform{}, ErrBusy
	}
	if m.larkAuthBusyLocked(req.CLIProfile) {
		return managedPlatform{}, ErrBusy
	}
	file, err := m.readPlatforms()
	if err != nil {
		return managedPlatform{}, err
	}
	for _, c := range file.Platforms {
		if c.ID == req.ID && m.larkAuthBusyLocked(c.CLIProfile) {
			return managedPlatform{}, ErrBusy
		}
	}
	change, err := m.platforms.Plan(registry.Request{CLIProfile: req.CLIProfile, ID: req.ID, Version: req.Version, Name: req.Name, Provider: req.Provider, URL: req.URL}, remove)
	if err != nil {
		return managedPlatform{}, managedPlatformError(err)
	}
	refs, err := m.managedPlatformReferencesLocked(req.ID)
	if err != nil {
		return managedPlatform{}, err
	}
	if change.Rotating() && !samePlatformReferences(refs, req.ReferencedRooms) {
		return managedPlatform{}, ErrConflict
	}
	if remove && len(refs) > 0 {
		return managedPlatform{}, errPlatformReferenced
	}
	if change.Rotating() {
		if err := m.invalidatePlatformQueriesLocked(req.ID); err != nil {
			return managedPlatform{}, err
		}
		if err := m.settlePlatformQueriesLocked(req.ID); err != nil {
			return managedPlatform{}, err
		}
	}
	c, err := m.platforms.Commit(change)
	if err != nil {
		return managedPlatform{}, managedPlatformError(err)
	}
	if remove {
		return managedPlatform{}, nil
	}
	return m.publicPlatformLocked(c)
}

func (m *roomManager) verifyPlatform(ctx context.Context, req managedPlatformRequest) (managedPlatform, error) {
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return managedPlatform{}, ErrBusy
	}
	file, loadErr := m.readPlatforms()
	if loadErr != nil {
		m.mu.Unlock()
		return managedPlatform{}, loadErr
	}
	for _, c := range file.Platforms {
		if c.ID == req.ID && m.larkAuthBusyLocked(c.CLIProfile) {
			m.mu.Unlock()
			return managedPlatform{}, ErrBusy
		}
	}
	ticket, err := m.platforms.BeginVerification(req.ID, req.Version)
	m.mu.Unlock()
	if err != nil {
		return managedPlatform{}, managedPlatformError(err)
	}
	result := m.platforms.Query(ctx, ticket)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy {
		return managedPlatform{}, ErrBusy
	}
	pending := m.cancelPlatformCallsLocked(func(c *platformCall) bool { return c.platformID == req.ID })
	if err := m.settlePlatformCallsLocked(pending); err != nil {
		return managedPlatform{}, err
	}
	c, err := m.platforms.CompleteVerification(ticket, result)
	if err != nil {
		return managedPlatform{}, managedPlatformError(err)
	}
	if c.Provider == "lark" && c.Validation.Status == "valid" {
		if err := m.applyLarkAccountCheckLocked(*c.CLIProfile, c.Validation.Account, nil); err != nil {
			return managedPlatform{}, err
		}
	}
	return m.publicPlatformLocked(c)
}

// Room ownership is checked here; connection owns the verified account and
// exact credential snapshot. No notification or Workspace is required.
func (m *roomManager) resolvePlatformAccountLocked(owner RoomIdentity, id, provider string) (registry.Account, platform.Secret, error) {
	if m.closing {
		return registry.Account{}, platform.Secret{}, ErrClosing
	}
	if err := m.store.check(true); err != nil {
		return registry.Account{}, platform.Secret{}, err
	}
	entry, err := m.workspaceRoom(owner)
	if err != nil || entry.Status != "ready" {
		return registry.Account{}, platform.Secret{}, ErrIdentity
	}
	file, err := m.platforms.Load()
	if err != nil {
		return registry.Account{}, platform.Secret{}, err
	}
	for _, c := range file.Platforms {
		if c.ID != id {
			continue
		}
		if c.Provider != provider {
			return registry.Account{}, platform.Secret{}, ErrRequest
		}
		account, secret, err := registry.ResolveAccessAccount(file, c)
		if err != nil {
			return registry.Account{}, platform.Secret{}, platform.ErrCredential
		}
		return account, secret, nil
	}
	return registry.Account{}, platform.Secret{}, platform.ErrCredential
}

// A service-local resolution, never a Room snapshot or public response.
type projectPlatformResolution struct {
	owner     RoomIdentity
	record    managedPlatformRecord
	secret    workspaceSecret
	projectID int64
	branch    string
	account   registry.Account
}

func (projectPlatformResolution) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[REDACTED project resolution]")
}

// Resolves a remote project without requiring a local repository or checkout.
func (m *roomManager) resolveProjectPlatformLocked(owner RoomIdentity, b platform.ProjectBinding) (projectPlatformResolution, error) {
	result := projectPlatformResolution{owner: owner}
	if m.closing {
		return result, ErrClosing
	}
	if err := m.store.check(true); err != nil {
		return result, err
	}
	entry, err := m.workspaceRoom(owner)
	if err != nil || entry.Status != "ready" {
		return result, ErrIdentity
	}
	if b.PlatformID == "" {
		return result, ErrRequest
	}
	file, err := m.readPlatforms()
	if err != nil {
		return result, err
	}
	for _, c := range file.Platforms {
		if c.ID != b.PlatformID {
			continue
		}
		if c.Provider != b.Provider || c.URL != b.URL || b.PlatformGeneration != "" && c.Generation != b.PlatformGeneration {
			return result, ErrConflict
		}
		account, err := registry.ResolveAuthorization(file, c)
		if err != nil {
			return result, platform.ErrCredential
		}
		result.record, result.secret, result.projectID, result.account = c, workspaceSecret{}, b.ProjectID, account
		return result, nil
	}
	return result, ErrConflict
}
func (m *roomManager) checkProjectPlatformLocked(resolved projectPlatformResolution) error {
	file, err := m.readPlatforms()
	if err != nil {
		return err
	}
	entry, err := m.workspaceRoom(resolved.owner)
	if err != nil || entry.Status != "ready" {
		return ErrIdentity
	}
	if m.closing {
		return ErrClosing
	}
	if err := m.store.check(true); err != nil {
		return err
	}
	for _, c := range file.Platforms {
		if c.ID != resolved.record.ID {
			continue
		}
		a, err := registry.ResolveAuthorization(file, c)
		if err != nil || !reflect.DeepEqual(a, resolved.account) {
			return ErrConflict
		}
		return nil
	}
	return ErrConflict
}
