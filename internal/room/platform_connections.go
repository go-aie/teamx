package room

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
)

type platformConnection struct {
	connection.Connection
	References []platformConnectionReference `json:"references,omitempty"`
}
type platformConnectionValidation = platform.Validation
type platformConnectionRecord = connection.Record
type platformConnectionsFile = connection.Snapshot

// Requests may carry secrets and must never appear in generic diagnostics.
type platformConnectionRequest struct {
	SnapshotVersion int                  `json:"snapshot_version,omitempty"`
	CLIProfile      *platform.CLIProfile `json:"cli_profile,omitempty"`
	ReferencedRooms []RoomIdentity       `json:"referenced_rooms,omitempty"`
	ServiceEpoch    string               `json:"service_epoch"`
	Caller          Caller               `json:"caller"`
	ID              string               `json:"id"`
	Version         uint64               `json:"version"`
	Name            string               `json:"name,omitempty"`
	Provider        string               `json:"provider,omitempty"`
	URL             string               `json:"url,omitempty"`
	PAT             string               `json:"pat,omitempty"`
	TokenEnv        string               `json:"token_env,omitempty"`
}

func (platformConnectionRequest) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[REDACTED platform request]")
}

func platformConnectionError(err error) error {
	for _, pair := range [][2]error{{connection.ErrRequest, ErrRequest}, {connection.ErrConflict, ErrConflict}, {connection.ErrCapacity, ErrCapacity}, {connection.ErrInvalid, ErrInvalid}} {
		if errors.Is(err, pair[0]) {
			return fmt.Errorf("%w: %s", pair[1], err)
		}
	}
	return err
}

// These adapters require manager.mu. Connection persistence shares the service
// Store's directory lock, atomic writer and commit-unknown latch.
func (m *roomManager) readPlatformConnections() (platformConnectionsFile, error) {
	file, err := m.connections.Load()
	return file, platformConnectionError(err)
}
func resolvePlatformCredential(file platformConnectionsFile, c platformConnectionRecord, version, credentialVersion uint64) (workspaceSecret, error) {
	secret, err := connection.ResolveCredential(file, c, version, credentialVersion)
	return workspaceSecret{value: secret.Reveal()}, platformConnectionError(err)
}
func (m *roomManager) updatePlatformConnection(req platformConnectionRequest, remove bool) (platformConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy {
		return platformConnection{}, ErrBusy
	}
	if m.larkAuthBusyLocked(req.CLIProfile) {
		return platformConnection{}, ErrBusy
	}
	file, err := m.readPlatformConnections()
	if err != nil {
		return platformConnection{}, err
	}
	for _, c := range file.Connections {
		if c.ID == req.ID && m.larkAuthBusyLocked(c.CLIProfile) {
			return platformConnection{}, ErrBusy
		}
	}
	change, err := m.connections.Plan(connection.Request{CLIProfile: req.CLIProfile, SnapshotVersion: req.SnapshotVersion, ID: req.ID, Version: req.Version, Name: req.Name, Provider: req.Provider, URL: req.URL, PAT: req.PAT, TokenEnv: req.TokenEnv}, remove)
	if err != nil {
		return platformConnection{}, platformConnectionError(err)
	}
	refs, err := m.platformConnectionReferencesLocked(req.ID)
	if err != nil {
		return platformConnection{}, err
	}
	if change.Rotating() && !samePlatformReferences(refs, req.ReferencedRooms) {
		return platformConnection{}, ErrConflict
	}
	if remove && len(refs) > 0 {
		return platformConnection{}, errPlatformReferenced
	}
	if change.Rotating() {
		if err := m.invalidatePlatformQueriesLocked(req.ID); err != nil {
			return platformConnection{}, err
		}
		if err := m.settlePlatformQueriesLocked(req.ID); err != nil {
			return platformConnection{}, err
		}
	}
	c, err := m.connections.Commit(change)
	if err != nil {
		return platformConnection{}, platformConnectionError(err)
	}
	if remove {
		return platformConnection{}, nil
	}
	return m.publicPlatformConnectionLocked(c)
}

func (m *roomManager) verifyPlatformConnection(ctx context.Context, req platformConnectionRequest) (platformConnection, error) {
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return platformConnection{}, ErrBusy
	}
	file, loadErr := m.readPlatformConnections()
	if loadErr != nil {
		m.mu.Unlock()
		return platformConnection{}, loadErr
	}
	for _, c := range file.Connections {
		if c.ID == req.ID && m.larkAuthBusyLocked(c.CLIProfile) {
			m.mu.Unlock()
			return platformConnection{}, ErrBusy
		}
	}
	ticket, err := m.connections.BeginVerification(req.ID, req.Version)
	// Capture the test seam under the same lock as the immutable selection.
	verify := m.platformVerify
	m.mu.Unlock()
	if err != nil {
		return platformConnection{}, platformConnectionError(err)
	}
	var override connection.Verify
	if verify != nil {
		override = func(ctx context.Context, _ string, origin string, secret platform.Secret) platform.Validation {
			return verify(ctx, origin, workspaceSecret{value: secret.Reveal()})
		}
	}
	result := m.connections.Query(ctx, ticket, override)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy {
		return platformConnection{}, ErrBusy
	}
	pending := m.cancelPlatformCallsLocked(func(c *platformCall) bool { return c.connectionID == req.ID })
	if err := m.settlePlatformCallsLocked(pending); err != nil {
		return platformConnection{}, err
	}
	c, err := m.connections.CompleteVerification(ticket, result)
	if err != nil {
		return platformConnection{}, platformConnectionError(err)
	}
	return m.publicPlatformConnectionLocked(c)
}

// Room ownership is checked here; connection owns the verified account and
// exact credential snapshot. No notification or Workspace is required.
func (m *roomManager) resolvePlatformAccountLocked(owner RoomIdentity, id, provider string) (connection.Account, platform.Secret, error) {
	if m.closing {
		return connection.Account{}, platform.Secret{}, ErrClosing
	}
	if err := m.store.check(true); err != nil {
		return connection.Account{}, platform.Secret{}, err
	}
	entry, err := m.workspaceRoom(owner)
	if err != nil || entry.Status != "ready" {
		return connection.Account{}, platform.Secret{}, ErrIdentity
	}
	file, err := m.connections.Load()
	if err != nil {
		return connection.Account{}, platform.Secret{}, err
	}
	for _, c := range file.Connections {
		if c.ID != id {
			continue
		}
		if c.Provider != provider {
			return connection.Account{}, platform.Secret{}, ErrRequest
		}
		account, secret, err := connection.ResolveAccount(file, c)
		if err != nil {
			return connection.Account{}, platform.Secret{}, platform.ErrCredential
		}
		return account, secret, nil
	}
	return connection.Account{}, platform.Secret{}, platform.ErrCredential
}

// A service-local resolution, never a Room snapshot or public response.
type projectConnectionResolution struct {
	owner     RoomIdentity
	record    platformConnectionRecord
	secret    workspaceSecret
	projectID int64
}

func (projectConnectionResolution) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[REDACTED project resolution]")
}

// Resolves a remote project without requiring a local repository or checkout.
func (m *roomManager) resolveProjectConnectionLocked(owner RoomIdentity, b platform.ProjectBinding) (projectConnectionResolution, error) {
	result := projectConnectionResolution{owner: owner}
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
	if b.ConnectionID == "" {
		return result, ErrRequest
	}
	file, err := m.readPlatformConnections()
	if err != nil {
		return result, err
	}
	for _, c := range file.Connections {
		if c.ID != b.ConnectionID {
			continue
		}
		if c.Provider != b.Provider || c.URL != b.URL || b.ConnectionGeneration != "" && c.Generation != b.ConnectionGeneration {
			return result, ErrConflict
		}
		secret, err := resolvePlatformCredential(file, c, c.Version, c.CredentialVersion)
		if err != nil {
			return result, platform.ErrCredential
		}
		result.record, result.secret, result.projectID = c, secret, b.ProjectID
		return result, nil
	}
	return result, ErrConflict
}
func (m *roomManager) checkProjectConnectionLocked(resolved projectConnectionResolution) error {
	file, err := m.readPlatformConnections()
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
	for _, c := range file.Connections {
		if c.ID != resolved.record.ID {
			continue
		}
		secret, err := resolvePlatformCredential(file, c, resolved.record.Version, resolved.record.CredentialVersion)
		if err != nil || c.Generation != resolved.record.Generation || secret.value != resolved.secret.value {
			return ErrConflict
		}
		return nil
	}
	return ErrConflict
}
