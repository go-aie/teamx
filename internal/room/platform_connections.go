package room

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"

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
	change, err := m.connections.Plan(connection.Request{CLIProfile: req.CLIProfile, ID: req.ID, Version: req.Version, Name: req.Name, Provider: req.Provider, URL: req.URL}, remove)
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
	m.mu.Unlock()
	if err != nil {
		return platformConnection{}, platformConnectionError(err)
	}
	result := m.connections.Query(ctx, ticket)
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
	if c.Provider == "lark" && c.Validation.Status == "valid" {
		if err := m.resumeLarkSubscriptionsLocked(c.ID); err != nil {
			return platformConnection{}, err
		}
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
		account, secret, err := connection.ResolveAccessAccount(file, c)
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
	branch    string
	account   connection.Account
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
		account, err := connection.ResolveAuthorization(file, c)
		if err != nil {
			return result, platform.ErrCredential
		}
		result.record, result.secret, result.projectID, result.account = c, workspaceSecret{}, b.ProjectID, account
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
		a, err := connection.ResolveAuthorization(file, c)
		if err != nil || !reflect.DeepEqual(a, resolved.account) {
			return ErrConflict
		}
		return nil
	}
	return ErrConflict
}
