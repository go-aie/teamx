package connection

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const snapshotVersion = 4
const OAuthSnapshotVersion = snapshotVersion

const maxPlatformConnectionsBytes int64 = 2 << 20

// Store delegates all writes to the service's existing atomic snapshot owner.
// Check must enforce both closure and the shared commit-unknown write latch.
// Save must preserve that latch after an uncertain replacement. The module
// never creates a second writer or takes a Room lock from its own lock.
type Store struct {
	Path  string
	Check func(writing bool) error
	Save  func(path string, value any, limit int64) error
}

// Load validates private disk data without reflecting malformed credentials.
func (m *Manager) Load() (Snapshot, error) {
	file := Snapshot{Version: snapshotVersion, Connections: []Record{}}
	if err := m.store.Check(false); err != nil {
		return file, err
	}
	name := m.store.Path
	info, err := os.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		file.InstallationID, err = newOpaqueID()
		return file, err
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxPlatformConnectionsBytes {
		return file, fmt.Errorf("%w: platform connections must be a private regular file", ErrInvalid)
	}
	var header struct {
		Version int `json:"version"`
	}
	data, readErr := os.ReadFile(name)
	if readErr != nil || int64(len(data)) > maxPlatformConnectionsBytes || json.Unmarshal(data, &header) != nil {
		return Snapshot{}, ErrInvalid
	}
	if header.Version != snapshotVersion {
		return Snapshot{}, fmt.Errorf("%w: OAuth-only connections require version 4; stop the service and run teamx connections upgrade --rooms-dir ROOT", ErrInvalid)
	}
	var saved Snapshot
	if readSnapshot(name, &saved) != nil {
		return Snapshot{}, fmt.Errorf("%w: malformed platform connections", ErrInvalid)
	}
	file = saved
	return m.validateSnapshot(file)
}

func (m *Manager) validateSnapshot(file Snapshot) (Snapshot, error) {
	if file.Version != snapshotVersion {
		return Snapshot{}, fmt.Errorf("%w: unsupported platform connections version; supported version is 4; newer snapshots require a compatible binary", ErrInvalid)
	}
	if len(file.Connections) > 100 {
		return Snapshot{}, fmt.Errorf("%w: malformed platform connections", ErrInvalid)
	}
	if err := validateOAuth(file); err != nil {
		return Snapshot{}, err
	}
	seen := map[string]bool{}
	for index, c := range file.Connections {
		n, u, err := m.normalize(c.Name, c.Provider, c.URL)
		sourceOK := c.CredentialKind == "cli_profile" && c.CLIProfile != nil && c.CredentialID == "" && m.validProfile != nil && m.validProfile(c.Provider, c.URL, *c.CLIProfile)
		if m.profileOnly != nil && m.profileOnly(c.Provider) {
			sourceOK = sourceOK && c.CredentialKind == "cli_profile"
		}
		if c.CredentialKind == "oauth" {
			sourceOK = validOAuthConnection(file, c)
		}
		v := c.Validation
		identityOK := true
		if v.Status == "valid" {
			c.identity, identityOK = m.resolveIdentity(c.Provider, c.URL, v)
		}
		validationOK := identityOK && validValidation(v) && validIdentityBinding(c)
		if c.CredentialKind == "cli_profile" && v.Status == "valid" {
			validationOK = validationOK && v.Account.Valid() && v.Account.Provider == c.Provider && v.Account.Instance == c.URL && v.UserID == 0
		}
		if err != nil || n != c.Name || u != c.URL || !validRestartID(c.ID) || !validRestartID(c.Generation) || seen[c.ID] || c.Version == 0 ||
			c.RevocationStatus != "" || c.AuthorizationStatus != "" || !c.TokenExpiresAt.IsZero() ||
			c.CredentialVersion == 0 || c.CredentialVersion > c.Version || (!c.CredentialConfigured && c.CredentialKind != "oauth") || !sourceOK || !validationOK {
			return Snapshot{}, fmt.Errorf("%w: invalid platform connection", ErrInvalid)
		}
		file.Connections[index] = c
		seen[c.ID] = true
	}
	return file, nil
}
func validPlatformValidationError(s string) bool {
	switch s {
	case "credential_unavailable", "credential_rejected", "query_failed", "invalid_response", "invalid_profile", "identity_mismatch", "cli_unavailable", "cli_version_mismatch", "permission_denied", "cursor_expired", "rate_limited", "network_error", "timeout", "cancelled", "output_limit":
		return true
	}
	return false
}

func readSnapshot(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPlatformConnectionsBytes {
		return ErrInvalid
	}
	dec := json.NewDecoder(io.LimitReader(f, maxPlatformConnectionsBytes+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return ErrInvalid
	}
	if dec.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func (m *Manager) save(file Snapshot) error {
	if err := m.store.Check(true); err != nil {
		return err
	}
	return m.store.Save(m.store.Path, file, maxPlatformConnectionsBytes)
}
