package connection

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/go-aie/teamx/internal/platform"
)

const snapshotVersion = 1
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
		return file, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return file, fmt.Errorf("%w: platform connections must be a private regular file", ErrInvalid)
	}
	if readSnapshot(name, &file) != nil || file.Version != snapshotVersion || len(file.Connections) > 100 || len(file.Credentials) > 100 {
		return Snapshot{}, fmt.Errorf("%w: malformed platform connections", ErrInvalid)
	}
	seen := map[string]bool{}
	credentials := map[string]Credential{}
	for _, cred := range file.Credentials {
		if !validRestartID(cred.ID) || !validRestartID(cred.ConnectionID) || !platform.ValidPAT(cred.PAT) || credentials[cred.ID].ID != "" {
			return Snapshot{}, fmt.Errorf("%w: invalid platform credential", ErrInvalid)
		}
		credentials[cred.ID] = cred
	}
	used := map[string]bool{}
	for _, c := range file.Connections {
		n, u, err := m.normalize(c.Name, c.Provider, c.URL)
		sourceOK := c.CredentialKind == "pat" && credentials[c.CredentialID].ConnectionID == c.ID && !used[c.CredentialID] && c.TokenEnv == "" ||
			c.CredentialKind == "env" && c.CredentialID == "" && envName.MatchString(c.TokenEnv) && len(c.TokenEnv) <= 128
		v := c.Validation
		validationOK := validValidation(v) && validIdentityBinding(c)
		if err != nil || n != c.Name || u != c.URL || !validRestartID(c.ID) || !validRestartID(c.Generation) || seen[c.ID] || c.Version == 0 ||
			c.CredentialVersion == 0 || c.CredentialVersion > c.Version || !c.CredentialConfigured || !sourceOK || !validationOK {
			return Snapshot{}, fmt.Errorf("%w: invalid platform connection", ErrInvalid)
		}
		seen[c.ID] = true
		if c.CredentialID != "" {
			used[c.CredentialID] = true
		}
	}
	if len(used) != len(credentials) {
		return Snapshot{}, fmt.Errorf("%w: unreferenced platform credential", ErrInvalid)
	}
	return file, nil
}
func validPlatformValidationError(s string) bool {
	switch s {
	case "credential_unavailable", "credential_rejected", "query_failed", "invalid_response":
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
