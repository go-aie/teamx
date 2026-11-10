package registry

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
)

func validScopes(scopes []string) bool {
	normalized, ok := platform.NormalizeScopes(scopes)
	return ok && slices.Equal(normalized, scopes)
}
func validRedirect(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Hostname() == "" || u.Path == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n\t ") {
		return false
	}
	return u.Scheme == "https" || u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())
}
func validateOAuth(file Snapshot) error {
	invalid := func() error { return fmt.Errorf("%w: invalid OAuth snapshot", ErrInvalid) }
	if !validRestartID(file.InstallationID) || len(file.OAuthApplications) > 100 || len(file.OAuthCredentials) > 100 {
		return invalid()
	}
	if err := validateOAuthCleanup(file); err != nil {
		return err
	}
	if err := validateOAuthClientImports(file); err != nil {
		return err
	}
	apps := map[string]OAuthApplication{}
	for _, a := range file.OAuthApplications {
		_, origin, err := Normalize("OAuth", a.Origin)
		if err != nil || origin != a.Origin || !validRestartID(a.ID) || apps[a.ID].ID != "" || a.OwnerID <= 0 || !platform.ValidCredential(a.ClientID) || !platform.ValidCredential(a.ClientSecret) || !validRedirect(a.RedirectURI) || !validScopes(a.Scopes) {
			return invalid()
		}
		for _, prior := range apps {
			if prior.Origin == a.Origin && (prior.ClientID == a.ClientID) {
				return invalid()
			}
		}
		apps[a.ID] = a
	}
	seen := map[string]bool{}
	accounts := map[struct {
		app  string
		user int64
	}]bool{}
	for _, c := range file.OAuthCredentials {
		a, ok := apps[c.ApplicationID]
		key := struct {
			app  string
			user int64
		}{c.ApplicationID, c.UserID}
		if !ok || !validRestartID(c.ID) || seen[c.ID] || accounts[key] || c.UserID <= 0 || c.GrantGeneration == 0 || c.TokenRevision == 0 || !validScopes(c.Scopes) {
			return invalid()
		}
		for _, scope := range c.Scopes {
			if !slices.Contains(a.Scopes, scope) {
				return invalid()
			}
		}
		pair := platform.ValidCredential(c.AccessToken) && platform.ValidCredential(c.RefreshToken) && !c.ExpiresAt.IsZero()
		switch c.State {
		case "active":
			if !pair || c.RefreshAttemptID != "" {
				return invalid()
			}
		case "refresh_pending", "refresh_unknown":
			if !pair || !validRestartID(c.RefreshAttemptID) {
				return invalid()
			}
		case "reauthorization_required":
			if !pair || c.RefreshAttemptID != "" && !validRestartID(c.RefreshAttemptID) {
				return invalid()
			}
		case "logged_out":
			if c.AccessToken != "" || c.RefreshToken != "" || !c.ExpiresAt.IsZero() || c.RefreshAttemptID != "" {
				return invalid()
			}
		default:
			return invalid()
		}
		seen[c.ID] = true
		accounts[key] = true
	}
	return nil
}

// ValidOAuthRedirect is shared by the trusted service-address preflight.
func ValidOAuthRedirect(value string) bool { return validRedirect(value) }
func OAuthRecord(file Snapshot, id string) (OAuthCredential, OAuthApplication, error) {
	if file.Version != OAuthSnapshotVersion {
		return OAuthCredential{}, OAuthApplication{}, ErrInvalid
	}
	for _, c := range file.OAuthCredentials {
		if c.ID != id {
			continue
		}
		for _, a := range file.OAuthApplications {
			if a.ID == c.ApplicationID {
				return c, a, nil
			}
		}
	}
	return OAuthCredential{}, OAuthApplication{}, platform.ErrCredential
}
func authorization(a OAuthApplication, c OAuthCredential) platform.AuthorizationEvidence {
	scopes, _ := platform.ScopeString(c.Scopes)
	return platform.AuthorizationEvidence{Origin: a.Origin, ApplicationID: a.ID, ClientID: a.ClientID, CredentialID: c.ID, UserID: c.UserID, Generation: c.GrantGeneration, Scopes: scopes}
}
func oauthFingerprint(e platform.AuthorizationEvidence) string {
	b, _ := json.Marshal(e)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func validOAuthPlatform(file Snapshot, c Record) bool {
	if c.CredentialID == "" {
		return c.Provider == "gitlab" && c.CLIProfile == nil && !c.CredentialConfigured && c.IdentityBinding == nil && c.Validation.Status == "unverified"
	}
	cred, app, err := OAuthRecord(file, c.CredentialID)
	if err != nil || c.Provider != "gitlab" || c.URL != app.Origin || c.CLIProfile != nil || c.Validation.UserID != cred.UserID || c.Validation.Status != "valid" {
		return false
	}
	return c.CredentialConfigured == (cred.State != "logged_out") && c.IdentityBinding != nil && c.IdentityBinding.CredentialFingerprint == oauthFingerprint(authorization(app, cred))
}

// ResolveAuthorization provides stable account evidence without granting access
// to token material. A pending refresh keeps existing result evidence valid.
func ResolveAuthorization(file Snapshot, c Record) (Account, error) {
	if c.CredentialKind != "oauth" {
		a, _, err := ResolveAccount(file, c)
		return a, err
	}
	found := false
	for _, current := range file.Platforms {
		if current.ID == c.ID && current.Generation == c.Generation && current.Version == c.Version && current.CredentialVersion == c.CredentialVersion {
			c, found = current, true
			break
		}
	}
	if !found {
		return Account{}, ErrConflict
	}
	cred, app, err := OAuthRecord(file, c.CredentialID)
	if err != nil || !validOAuthPlatform(file, c) || !validIdentityBinding(c) || (cred.State != "active" && cred.State != "refresh_pending") {
		return Account{}, platform.ErrCredential
	}
	return Account{Identity: c.accountIdentity(), PlatformID: c.ID, Generation: c.Generation, URL: c.URL, UserID: c.Validation.UserID, Username: c.Validation.Username, PlatformVersion: c.Version, CredentialVersion: c.CredentialVersion, CredentialFingerprint: c.IdentityBinding.CredentialFingerprint, Authorization: authorization(app, cred)}, nil
}

// OAuthRefresh is private, immutable evidence captured under the catalog lock.
// The owner performs network work outside that lock, then rechecks it at commit.
type OAuthRefresh struct {
	Evidence     platform.AuthorizationEvidence
	Application  OAuthApplication
	RefreshToken platform.Secret
	revision     uint64
	attempt      string
}

func (OAuthRefresh) Format(s fmt.State, _ rune)   { fmt.Fprint(s, "[REDACTED OAuth refresh]") }
func (OAuthRefresh) MarshalJSON() ([]byte, error) { return json.Marshal("[REDACTED OAuth refresh]") }

func (m *Manager) BeginOAuthRefresh(expected platform.AuthorizationEvidence) (OAuthRefresh, error) {
	file, err := m.Load()
	if err != nil {
		return OAuthRefresh{}, err
	}
	cred, app, err := OAuthRecord(file, expected.CredentialID)
	if err != nil || authorization(app, cred) != expected || cred.State != "active" {
		return OAuthRefresh{}, platform.ErrCredential
	}
	if cred.TokenRevision == ^uint64(0) {
		return OAuthRefresh{}, ErrCapacity
	}
	attempt, err := newOpaqueID()
	if err != nil {
		return OAuthRefresh{}, err
	}
	cred.State, cred.RefreshAttemptID = "refresh_pending", attempt
	replaceOAuth(&file, cred)
	// Persist intent before the destructive refresh POST. A crash leaves a
	// non-admissible pending record; restarting never replays the old token.
	if err := m.save(file); err != nil {
		return OAuthRefresh{}, err
	}
	return OAuthRefresh{Evidence: expected, Application: app, RefreshToken: platform.NewSecret(cred.RefreshToken), revision: cred.TokenRevision, attempt: attempt}, nil
}

// outcome is active, refresh_unknown, or reauthorization_required. A transport
// failure must never restore active or repeat the refresh POST.
func (m *Manager) CompleteOAuthRefresh(ticket OAuthRefresh, tokens platform.OAuthTokens, outcome string) error {
	file, err := m.Load()
	if err != nil {
		return err
	}
	cred, app, err := OAuthRecord(file, ticket.Evidence.CredentialID)
	if err != nil || authorization(app, cred) != ticket.Evidence || app.ClientSecret != ticket.Application.ClientSecret || app.RedirectURI != ticket.Application.RedirectURI || cred.State != "refresh_pending" || cred.TokenRevision != ticket.revision || cred.RefreshAttemptID != ticket.attempt {
		return ErrConflict
	}
	switch outcome {
	case "active":
		scopes, ok := platform.ScopeString(tokens.Scopes)
		if !ok || scopes != ticket.Evidence.Scopes || !platform.ValidCredential(tokens.AccessToken.Reveal()) || !platform.ValidCredential(tokens.RefreshToken.Reveal()) || tokens.ExpiresAt.IsZero() {
			return ErrInvalid
		}
		cred.AccessToken, cred.RefreshToken, cred.ExpiresAt = tokens.AccessToken.Reveal(), tokens.RefreshToken.Reveal(), tokens.ExpiresAt
		cred.TokenRevision++
		cred.RefreshAttemptID = ""
	case "refresh_unknown", "reauthorization_required":
		// A complete candidate pair that failed grant/identity verification is
		// retained only in this unusable record for later explicit cleanup.
		if platform.ValidCredential(tokens.AccessToken.Reveal()) && platform.ValidCredential(tokens.RefreshToken.Reveal()) && !tokens.ExpiresAt.IsZero() {
			cred.AccessToken, cred.RefreshToken, cred.ExpiresAt = tokens.AccessToken.Reveal(), tokens.RefreshToken.Reveal(), tokens.ExpiresAt
			cred.TokenRevision++
		}
	default:
		return ErrInvalid
	}
	cred.State = outcome
	replaceOAuth(&file, cred)
	return m.save(file)
}
func replaceOAuth(file *Snapshot, c OAuthCredential) {
	for i := range file.OAuthCredentials {
		if file.OAuthCredentials[i].ID == c.ID {
			file.OAuthCredentials[i] = c
			return
		}
	}
}
