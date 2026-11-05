package connection

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

type Verify func(context.Context, string, string, platform.Secret) platform.Validation

// Verification captures immutable scope; it must not be serialized or logged.
type Verification struct {
	record        Record
	secret        platform.Secret
	credentialErr error
}

func (m *Manager) BeginVerification(id string, version uint64) (Verification, error) {
	file, err := m.Load()
	if err != nil {
		return Verification{}, err
	}
	for _, c := range file.Connections {
		if c.ID == id && c.Version == version {
			secret, err := ResolveCredential(file, c, c.Version, c.CredentialVersion)
			return Verification{record: c, secret: secret, credentialErr: err}, nil
		}
	}
	return Verification{}, ErrConflict
}

// Query uses the injected provider verifier, with an optional per-call override.
func (m *Manager) Query(ctx context.Context, v Verification, override Verify) platform.Validation {
	result := platform.Validation{Status: "failed", CheckedAt: time.Now().UTC(), ErrorCode: "credential_unavailable"}
	if v.credentialErr != nil {
		return result
	}
	verify := m.verify
	if override != nil {
		verify = override
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return verify(ctx, v.record.Provider, v.record.URL, v.secret)
}
func (m *Manager) CompleteVerification(v Verification, result platform.Validation) (Connection, error) {
	if !validValidation(result) || result.Status == "unverified" || result.Status == "valid" && (v.credentialErr != nil || result.UserID <= 0) {
		return Connection{}, ErrInvalid
	}
	file, err := m.Load()
	if err != nil {
		return Connection{}, err
	}
	selected := v.record
	for i, c := range file.Connections {
		if c.ID != selected.ID {
			continue
		}
		current, currentErr := ResolveCredential(file, c, selected.Version, selected.CredentialVersion)
		if c.Generation != selected.Generation || c.Version != selected.Version || c.CredentialVersion != selected.CredentialVersion || (currentErr == nil) != (v.credentialErr == nil) || current.Reveal() != v.secret.Reveal() {
			return Connection{}, ErrConflict
		}
		if c.Version == ^uint64(0) {
			return Connection{}, ErrCapacity
		}
		c.Validation = result
		c.IdentityBinding = nil
		if result.UserID > 0 {
			c.IdentityBinding = &IdentityBinding{Generation: c.Generation, CredentialVersion: c.CredentialVersion, CredentialFingerprint: fingerprint(v.secret)}
		}
		c.Version++
		file.Connections[i] = c
		if err := m.save(file); err != nil {
			return Connection{}, err
		}
		return c.Connection, nil
	}
	return Connection{}, ErrConflict
}

func fingerprint(secret platform.Secret) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(secret.Reveal())))
}

func validValidation(v platform.Validation) bool {
	return v.Status == "unverified" && v.CheckedAt.IsZero() && v.Username == "" && v.UserID == 0 && v.ErrorCode == "" ||
		v.Status == "valid" && !v.CheckedAt.IsZero() && platform.ValidUsername(v.Username) && v.UserID > 0 && v.ErrorCode == "" ||
		v.Status == "failed" && !v.CheckedAt.IsZero() && v.Username == "" && v.UserID == 0 && validPlatformValidationError(v.ErrorCode)
}

func validIdentityBinding(c Record) bool {
	b := c.IdentityBinding
	if c.Validation.Status != "valid" {
		return b == nil
	}
	return c.Validation.UserID > 0 && b != nil && b.Generation == c.Generation && b.CredentialVersion == c.CredentialVersion &&
		len(b.CredentialFingerprint) == 64 && strings.Trim(b.CredentialFingerprint, "0123456789abcdef") == ""
}

// VerifiedIdentity must be checked again at query admission and commit. A saved
// username or an old validation cannot establish the current credential's user.
func VerifiedIdentity(file Snapshot, c Record) (platform.Validation, error) {
	identity, _, err := ResolveVerifiedIdentity(file, c)
	return identity, err
}

// ResolveVerifiedIdentity returns the exact secret whose fingerprint establishes
// the account. Callers must inject this value, rather than resolving env again.
func ResolveVerifiedIdentity(file Snapshot, c Record) (platform.Validation, platform.Secret, error) {
	found := false
	for _, current := range file.Connections {
		if current.ID == c.ID && current.Generation == c.Generation && current.Version == c.Version && current.CredentialVersion == c.CredentialVersion {
			c, found = current, true
			break
		}
	}
	if !found {
		return platform.Validation{}, platform.Secret{}, ErrConflict
	}
	if c.Validation.Status != "valid" || c.Validation.UserID <= 0 || !validIdentityBinding(c) {
		return platform.Validation{}, platform.Secret{}, ErrConflict
	}
	secret, err := ResolveCredential(file, c, c.Version, c.CredentialVersion)
	if err != nil || fingerprint(secret) != c.IdentityBinding.CredentialFingerprint {
		return platform.Validation{}, platform.Secret{}, platform.ErrCredential
	}
	return c.Validation, secret, nil
}

// Public provides current connection identity display. Bound identity evidence is
// hidden when the resolved credential has changed; reading never rewrites disk.
func Public(file Snapshot, c Record) Connection {
	result := c.Connection
	if c.Validation.Status == "valid" {
		if _, err := VerifiedIdentity(file, c); err != nil {
			result.Validation = platform.Validation{Status: "unverified"}
		}
	}
	return result
}

// Account is immutable service-local execution evidence shared by notifications
// and platform tools. It does not depend on a subscription or local repository.
type Account struct {
	ConnectionID          string
	Generation            string
	URL                   string
	UserID                int64
	Username              string
	ConnectionVersion     uint64
	CredentialVersion     uint64
	CredentialFingerprint string
}

func (Account) Format(s fmt.State, _ rune) { fmt.Fprint(s, "[REDACTED platform account]") }
func ResolveAccount(file Snapshot, c Record) (Account, platform.Secret, error) {
	identity, secret, err := ResolveVerifiedIdentity(file, c)
	if err != nil {
		return Account{}, platform.Secret{}, err
	}
	return Account{c.ID, c.Generation, c.URL, identity.UserID, identity.Username, c.Version, c.CredentialVersion, c.IdentityBinding.CredentialFingerprint}, secret, nil
}
