package registry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/go-aie/teamx/internal/platform"
)

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
	for _, c := range file.Platforms {
		if c.ID == id && c.Version == version {
			if c.CredentialKind != "cli_profile" {
				return Verification{}, ErrRequest
			}
			secret, _, err := credentialEvidence(file, c)
			return Verification{record: c, secret: secret, credentialErr: err}, nil
		}
	}
	return Verification{}, ErrConflict
}

// Query performs the bounded verification of a CLI-owned profile.
func (m *Manager) Query(ctx context.Context, v Verification) platform.Validation {
	result := platform.Validation{Status: "failed", CheckedAt: time.Now().UTC(), ErrorCode: "credential_unavailable"}
	if v.credentialErr != nil {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if v.record.CredentialKind == "cli_profile" {
		if m.verifyProfile == nil || v.record.CLIProfile == nil {
			return result
		}
		return m.verifyProfile(ctx, v.record.Provider, v.record.URL, *v.record.CLIProfile)
	}
	return result
}
func (m *Manager) CompleteVerification(v Verification, result platform.Validation) (platform.Platform, error) {
	if !validValidation(result) || result.Status == "unverified" || result.Status == "valid" && (v.credentialErr != nil || !m.validAccountResult(v.record.Provider, v.record.URL, result)) {
		return platform.Platform{}, ErrInvalid
	}
	file, err := m.Load()
	if err != nil {
		return platform.Platform{}, err
	}
	selected := v.record
	for i, c := range file.Platforms {
		if c.ID != selected.ID {
			continue
		}
		current, _, currentErr := credentialEvidence(file, c)
		if c.Generation != selected.Generation || c.Version != selected.Version || c.CredentialVersion != selected.CredentialVersion || (currentErr == nil) != (v.credentialErr == nil) || current.Reveal() != v.secret.Reveal() || !sameProfile(c.CLIProfile, selected.CLIProfile) {
			return platform.Platform{}, ErrConflict
		}
		if c.Version == ^uint64(0) {
			return platform.Platform{}, ErrCapacity
		}
		c.Validation = result
		c.IdentityBinding = nil
		if result.Status == "valid" {
			c.identity, _ = m.resolveIdentity(c.Provider, c.URL, result)
			if c.CredentialKind == "cli_profile" && (!result.Account.Valid() || result.Account.Provider != c.Provider || result.Account.Instance != c.URL || result.UserID != 0) {
				return platform.Platform{}, ErrInvalid
			}
			_, fp, err := credentialEvidence(file, c)
			if err != nil {
				return platform.Platform{}, err
			}
			c.IdentityBinding = &IdentityBinding{Generation: c.Generation, CredentialVersion: c.CredentialVersion, CredentialFingerprint: fp}
		}
		c.Version++
		file.Platforms[i] = c
		if err := m.save(file); err != nil {
			return platform.Platform{}, err
		}
		return c.Platform, nil
	}
	return platform.Platform{}, ErrConflict
}

func validValidation(v platform.Validation) bool {
	return v.Status == "unverified" && v.CheckedAt.IsZero() && v.Username == "" && v.UserID == 0 && v.ErrorCode == "" && v.Account == (platform.AccountIdentity{}) ||
		v.Status == "valid" && !v.CheckedAt.IsZero() && (v.Account == (platform.AccountIdentity{}) || v.Account.Valid()) && len(v.Username) <= 1024 && strings.IndexFunc(v.Username, unicode.IsControl) < 0 && v.ErrorCode == "" ||
		v.Status == "failed" && !v.CheckedAt.IsZero() && v.Username == "" && v.UserID == 0 && v.Account == (platform.AccountIdentity{}) && validPlatformValidationError(v.ErrorCode)
}

func validIdentityBinding(c Record) bool {
	b := c.IdentityBinding
	if c.Validation.Status != "valid" {
		return b == nil
	}
	return c.accountIdentity().Valid() && b != nil && b.Generation == c.Generation && b.CredentialVersion == c.CredentialVersion &&
		len(b.CredentialFingerprint) == 64 && strings.Trim(b.CredentialFingerprint, "0123456789abcdef") == ""
}

// VerifiedIdentity must be checked again at query admission and commit. A saved
// username or an old validation cannot establish the current credential's user.
func VerifiedIdentity(file Snapshot, c Record) (platform.Validation, error) {
	if c.CredentialKind == "oauth" {
		_, err := ResolveAuthorization(file, c)
		return c.Validation, err
	}
	identity, _, err := ResolveVerifiedIdentity(file, c)
	return identity, err
}

// ResolveVerifiedIdentity returns the exact secret whose fingerprint establishes
// the account. Callers must inject this value, rather than resolving env again.
func ResolveVerifiedIdentity(file Snapshot, c Record) (platform.Validation, platform.Secret, error) {
	found := false
	for _, current := range file.Platforms {
		if current.ID == c.ID && current.Generation == c.Generation && current.Version == c.Version && current.CredentialVersion == c.CredentialVersion {
			c, found = current, true
			break
		}
	}
	if !found {
		return platform.Validation{}, platform.Secret{}, ErrConflict
	}
	if c.Validation.Status != "valid" || !c.accountIdentity().Valid() || !validIdentityBinding(c) {
		return platform.Validation{}, platform.Secret{}, ErrConflict
	}
	secret, fp, err := credentialEvidence(file, c)
	if err != nil || fp != c.IdentityBinding.CredentialFingerprint {
		return platform.Validation{}, platform.Secret{}, platform.ErrCredential
	}
	return c.Validation, secret, nil
}

// Public provides current connection identity display. Bound identity evidence is
// hidden when the resolved credential has changed; reading never rewrites disk.
func Public(file Snapshot, c Record) platform.Platform {
	result := c.Platform
	if c.CredentialKind == "oauth" {
		if cred, app, err := OAuthRecord(file, c.CredentialID); err == nil {
			result.AuthorizationStatus, result.TokenExpiresAt = cred.State, cred.ExpiresAt
			if cred.State == "logged_out" {
				result.RevocationStatus = "unconfirmed"
				confirmed := false
				pending := false
				for _, cleanup := range file.OAuthCleanup {
					if cleanup.Origin == app.Origin && cleanup.ClientID == app.ClientID {
						if cleanup.State == "revoked" && !cleanup.UncertainGrant {
							confirmed = true
						} else {
							pending = true
						}
					}
				}
				if confirmed && !pending {
					result.RevocationStatus = "confirmed"
				}
			}
			if cred.State == "refresh_pending" {
				result.AuthorizationStatus = "refresh_unknown"
			}
		}
	}
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
	Authorization         platform.AuthorizationEvidence
	Identity              platform.AccountIdentity
	CLIProfile            *platform.CLIProfile
	PlatformID            string
	Generation            string
	URL                   string
	UserID                int64
	Username              string
	PlatformVersion       uint64
	CredentialVersion     uint64
	CredentialFingerprint string
}

func (Account) Format(s fmt.State, _ rune) { fmt.Fprint(s, "[REDACTED platform account]") }
func ResolveAccount(file Snapshot, c Record) (Account, platform.Secret, error) {
	identity, secret, err := ResolveVerifiedIdentity(file, c)
	if err != nil {
		return Account{}, platform.Secret{}, err
	}
	return Account{Identity: c.accountIdentity(), CLIProfile: c.CLIProfile, PlatformID: c.ID, Generation: c.Generation, URL: c.URL, UserID: identity.UserID, Username: identity.Username, PlatformVersion: c.Version, CredentialVersion: c.CredentialVersion, CredentialFingerprint: c.IdentityBinding.CredentialFingerprint}, secret, nil
}

func (c Record) accountIdentity() platform.AccountIdentity {
	if c.Validation.Account.Valid() {
		return c.Validation.Account
	}
	return c.identity
}
func (m *Manager) validAccountResult(provider, origin string, v platform.Validation) bool {
	_, ok := m.resolveIdentity(provider, origin, v)
	return ok
}
func (m *Manager) resolveIdentity(provider, origin string, v platform.Validation) (platform.AccountIdentity, bool) {
	a := v.Account
	if m.identityResolver != nil {
		var ok bool
		a, ok = m.identityResolver(provider, origin, v)
		if !ok {
			return platform.AccountIdentity{}, false
		}
	}
	return a, a.Valid() && a.Provider == provider && a.Instance == origin
}
func sameProfile(a, b *platform.CLIProfile) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func credentialEvidence(file Snapshot, c Record) (platform.Secret, string, error) {
	if c.CredentialKind == "cli_profile" {
		if c.CLIProfile == nil {
			return platform.Secret{}, "", platform.ErrCredential
		}
		data, err := json.Marshal(c.CLIProfile)
		if err != nil {
			return platform.Secret{}, "", err
		}
		return platform.Secret{}, fmt.Sprintf("%x", sha256.Sum256(data)), nil
	}
	return platform.Secret{}, "", platform.ErrCredential
}

// ResolveAccessAccount separates stable authorization from short-lived OAuth
// material. OAuth callers must acquire a service token lease outside the gate.
func ResolveAccessAccount(file Snapshot, c Record) (Account, platform.Secret, error) {
	if c.CredentialKind == "oauth" {
		a, err := ResolveAuthorization(file, c)
		return a, platform.Secret{}, err
	}
	return ResolveAccount(file, c)
}
