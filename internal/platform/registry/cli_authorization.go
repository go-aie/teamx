package registry

import (
	"reflect"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// InvalidateCLIProfile fences the entire shared credential before native login.
// The caller has already settled readers and frozen the complete impact set.
// Rotating the credential revision makes old binding evidence unusable even
// when the CLI's profile name and native account remain unchanged.
func (m *Manager) InvalidateCLIProfile(expected []Record) ([]Record, error) {
	file, err := m.Load()
	if err != nil {
		return nil, err
	}
	if len(expected) == 0 {
		return nil, ErrRequest
	}
	profile := expected[0].CLIProfile
	if profile == nil {
		return nil, ErrRequest
	}
	matched := 0
	result := []Record{}
	for i, c := range file.Platforms {
		if c.Provider != "lark" || c.CLIProfile == nil || c.CLIProfile.Profile != profile.Profile {
			continue
		}
		var before *Record
		for j := range expected {
			if expected[j].ID == c.ID {
				before = &expected[j]
				break
			}
		}
		if before == nil || !reflect.DeepEqual(c, *before) {
			return nil, ErrConflict
		}
		if c.Version == ^uint64(0) || c.CredentialVersion == ^uint64(0) {
			return nil, ErrCapacity
		}
		matched++
		c.Version++
		c.CredentialVersion++
		c.IdentityBinding = nil
		c.Validation = platform.Validation{Status: "failed", CheckedAt: time.Now().UTC(), ErrorCode: "credential_unavailable"}
		file.Platforms[i] = c
		result = append(result, c)
	}
	if matched != len(expected) {
		return nil, ErrConflict
	}
	return result, m.save(file)
}
