package connection

import (
	"fmt"
	"slices"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Pending revocations are deliberately not business credentials. Unknown or
// manual records are never replayed automatically, including after restart.
type OAuthCleanup struct {
	UncertainGrant bool   `json:"uncertain_grant,omitempty"`
	ID             string `json:"id"`
	Origin         string `json:"origin"`
	ClientID       string `json:"client_id"`
	ClientSecret   string `json:"client_secret"`
	Token          string `json:"token,omitempty"`
	State          string `json:"state"` // pending, unknown, manual, revoked
}

func (OAuthCleanup) Format(s fmt.State, _ rune) { fmt.Fprint(s, "[REDACTED OAuth cleanup]") }
func validateOAuthCleanup(file Snapshot) error {
	if len(file.OAuthCleanup) > 200 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, c := range file.OAuthCleanup {
		_, origin, err := Normalize("OAuth", c.Origin)
		if err != nil || origin != c.Origin || !validRestartID(c.ID) || seen[c.ID] || !platform.ValidCredential(c.ClientID) || !platform.ValidCredential(c.ClientSecret) {
			return ErrInvalid
		}
		if c.State == "revoked" {
			if c.Token != "" {
				return ErrInvalid
			}
		} else if (c.State != "pending" && c.State != "unknown" && c.State != "manual") || !platform.ValidCredential(c.Token) {
			return ErrInvalid
		}
		seen[c.ID] = true
	}
	return nil
}
func addOAuthCleanup(file *Snapshot, app OAuthApplication, token, state string) (string, error) {
	if token == "" {
		return "", nil
	}
	for _, v := range file.OAuthCleanup {
		if v.Origin == app.Origin && v.ClientID == app.ClientID && v.Token == token {
			if state == "pending" && v.State == "manual" {
				for i := range file.OAuthCleanup {
					if file.OAuthCleanup[i].ID == v.ID {
						file.OAuthCleanup[i].State = "pending"
					}
				}
			}
			return v.ID, nil
		}
	}
	// Completed receipts do not grow the private snapshot indefinitely.
	file.OAuthCleanup = slices.DeleteFunc(file.OAuthCleanup, func(c OAuthCleanup) bool { return c.State == "revoked" && !c.UncertainGrant })
	if len(file.OAuthCleanup) >= 200 {
		return "", ErrCapacity
	}
	id, err := newOpaqueID()
	if err != nil {
		return "", err
	}
	file.OAuthCleanup = append(file.OAuthCleanup, OAuthCleanup{ID: id, Origin: app.Origin, ClientID: app.ClientID, ClientSecret: app.ClientSecret, Token: token, State: state})
	return id, nil
}
func (m *Manager) KeepOAuthCandidate(app OAuthApplication, tokens platform.OAuthTokens) (string, error) {
	if !platform.ValidCredential(tokens.AccessToken.Reveal()) {
		return "", nil
	}
	file, err := m.Load()
	if err != nil {
		return "", err
	}
	// Until revocation impact is accepted on the actual instance, never revoke a
	// candidate/old grant while another token for that same application is active.
	state := "pending"
	for _, c := range file.OAuthCredentials {
		if c.ApplicationID == app.ID && c.State != "logged_out" {
			state = "manual"
		}
	}
	id, err := addOAuthCleanup(&file, app, tokens.AccessToken.Reveal(), state)
	if err != nil {
		return "", err
	}
	return id, m.save(file)
}

// Mark unknown BEFORE issuing a revoke. A crash must not authorize a replay.
func (m *Manager) BeginOAuthCleanup(id string) (OAuthCleanup, error) {
	file, err := m.Load()
	if err != nil {
		return OAuthCleanup{}, err
	}
	for i, c := range file.OAuthCleanup {
		if c.ID == id && c.State == "pending" {
			file.OAuthCleanup[i].State = "unknown"
			return c, m.save(file)
		}
	}
	return OAuthCleanup{}, ErrConflict
}
func (m *Manager) FinishOAuthCleanup(id string, confirmed bool) error {
	file, err := m.Load()
	if err != nil {
		return err
	}
	for i, c := range file.OAuthCleanup {
		if c.ID == id && c.State == "unknown" {
			if !confirmed {
				return nil
			}
			file.OAuthCleanup[i].State = "revoked"
			file.OAuthCleanup[i].Token = ""
			return m.save(file)
		}
	}
	return ErrConflict
}

// CommitOAuthLogin runs after the service rechecks the frozen references and
// settles old calls. Expected records include every connection sharing a grant.
func (m *Manager) CommitOAuthLogin(expected []Record, targetID, name string, app OAuthApplication, importID, actor string, tokens platform.OAuthTokens, identity platform.Validation) ([]Connection, error) {
	file, err := m.Load()
	if err != nil {
		return nil, err
	}
	if identity.Status != "valid" || identity.UserID <= 0 || app.OwnerID != identity.UserID || !slices.Equal(tokens.Scopes, app.Scopes) || !tokens.ExpiresAt.After(time.Now()) || !platform.ValidCredential(tokens.AccessToken.Reveal()) || !platform.ValidCredential(tokens.RefreshToken.Reveal()) {
		return nil, ErrRequest
	}
	foundApp := false
	previousApp := app
	for _, a := range file.OAuthApplications {
		if a.ID == app.ID {
			previousApp = a
			foundApp = a.ClientID == app.ClientID && a.ClientSecret == app.ClientSecret && a.Origin == app.Origin && a.RedirectURI == app.RedirectURI && a.OwnerID == app.OwnerID
		}
	}
	if importID != "" {
		found := false
		replacement := false
		for _, v := range file.OAuthClientImports {
			if v.ID == importID && v.ActorID == actor && v.ClientID == app.ClientID && v.ClientSecret == app.ClientSecret && v.Origin == app.Origin && v.RedirectURI == app.RedirectURI && slices.Equal(v.Scopes, app.Scopes) {
				replacement = v.ReplacesApplicationID != ""
				found = !replacement && app.ID == v.ID || replacement && v.ReplacesApplicationID == app.ID && previousApp.ID == app.ID && previousApp.Origin == app.Origin && previousApp.OwnerID == identity.UserID
			}
		}
		if !found || !replacement && (foundApp || len(file.OAuthApplications) >= 100) {
			return nil, ErrConflict
		}
		file.OAuthClientImports = slices.DeleteFunc(file.OAuthClientImports, func(v OAuthClientImport) bool {
			return v.ID == importID || replacement && v.ReplacesApplicationID == app.ID
		})
		if replacement {
			for i, a := range file.OAuthApplications {
				if a.ID == app.ID {
					file.OAuthApplications[i] = app
				}
			}
		} else {
			file.OAuthApplications = append(file.OAuthApplications, app)
		}
		foundApp = true
	}
	if !foundApp {
		return nil, ErrConflict
	}
	for _, want := range expected {
		found := false
		for _, c := range file.Connections {
			if c.ID == want.ID && c.Generation == want.Generation && c.Version == want.Version && c.CredentialVersion == want.CredentialVersion && c.CredentialID == want.CredentialID {
				found = true
			}
		}
		if !found {
			return nil, ErrConflict
		}
	}
	targetExists := slices.ContainsFunc(file.Connections, func(c Record) bool { return c.ID == targetID })
	if !targetExists {
		if !validRestartID(targetID) || len(file.Connections) >= 100 {
			return nil, ErrRequest
		}
		for _, c := range file.Connections {
			if c.ID == targetID {
				return nil, ErrConflict
			}
		}
		n, u, err := m.normalize(name, "gitlab", app.Origin)
		if err != nil {
			return nil, err
		}
		generation, err := newOpaqueID()
		if err != nil {
			return nil, err
		}
		file.Connections = append(file.Connections, Record{Connection: Connection{ID: targetID, Name: n, URL: u, Provider: "gitlab"}, Generation: generation})
	}
	cred := OAuthCredential{}
	for _, c := range file.OAuthCredentials {
		if c.ApplicationID == app.ID && c.UserID == identity.UserID {
			if cred.ID != "" {
				return nil, ErrConflict
			}
			cred = c
		}
	}
	if cred.ID == "" {
		if len(file.OAuthCredentials) >= 100 {
			return nil, ErrCapacity
		}
		cred.ID, err = newOpaqueID()
		if err != nil {
			return nil, err
		}
		cred.ApplicationID, cred.UserID = app.ID, identity.UserID
	} else {
		// The service must have acknowledged all shared connections, even when
		// binding an additional connection to an existing OAuth account.
		for _, c := range file.Connections {
			if c.CredentialKind == "oauth" && c.CredentialID == cred.ID {
				if !slices.ContainsFunc(expected, func(v Record) bool { return v.ID == c.ID }) {
					return nil, ErrConflict
				}
			}
		}
		uncertain := cred.State == "refresh_pending" || cred.State == "refresh_unknown"
		if cred.AccessToken != tokens.AccessToken.Reveal() || uncertain {
			cleanupID, e := addOAuthCleanup(&file, previousApp, cred.AccessToken, "manual")
			if e != nil {
				return nil, e
			}
			if uncertain {
				for i, c := range file.OAuthCleanup {
					if c.ID == cleanupID {
						file.OAuthCleanup[i].UncertainGrant = true
					}
				}
			}
		}
	}
	if cred.GrantGeneration == ^uint64(0) || cred.TokenRevision == ^uint64(0) {
		return nil, ErrCapacity
	}
	cred.GrantGeneration++
	cred.TokenRevision++
	cred.State = "active"
	cred.RefreshAttemptID = ""
	cred.Scopes = slices.Clone(tokens.Scopes)
	cred.AccessToken = tokens.AccessToken.Reveal()
	cred.RefreshToken = tokens.RefreshToken.Reveal()
	cred.ExpiresAt = tokens.ExpiresAt
	if slices.ContainsFunc(file.OAuthCredentials, func(c OAuthCredential) bool { return c.ID == cred.ID }) {
		replaceOAuth(&file, cred)
	} else {
		file.OAuthCredentials = append(file.OAuthCredentials, cred)
	}
	var out []Connection
	for i, c := range file.Connections {
		if c.ID != targetID && !slices.ContainsFunc(expected, func(v Record) bool { return v.ID == c.ID }) {
			continue
		}
		if c.Version == ^uint64(0) || c.CredentialVersion == ^uint64(0) {
			return nil, ErrCapacity
		}
		c.Version++
		c.CredentialVersion++
		c.CredentialKind = "oauth"
		c.CredentialID = cred.ID
		c.CredentialConfigured = true
		c.CLIProfile = nil
		c.Validation = identity
		c.IdentityBinding = &IdentityBinding{Generation: c.Generation, CredentialVersion: c.CredentialVersion, CredentialFingerprint: oauthFingerprint(authorization(app, cred))}
		file.Connections[i] = c
		out = append(out, c.Connection)
	}
	if err = m.save(file); err != nil {
		return nil, err
	}
	return out, nil
}

// Local unavailability and revocation material commit together, before cancel
// or any remote request. Room references and connection generations survive.
func (m *Manager) LogoutOAuth(credentialID string) ([]Connection, []string, error) {
	file, err := m.Load()
	if err != nil {
		return nil, nil, err
	}
	cred, app, err := OAuthRecord(file, credentialID)
	if err != nil {
		return nil, nil, err
	}
	if cred.State == "logged_out" {
		return nil, nil, ErrConflict
	}
	if cred.GrantGeneration == ^uint64(0) {
		return nil, nil, ErrCapacity
	}
	cleanup, err := addOAuthCleanup(&file, app, cred.AccessToken, "pending")
	if err != nil {
		return nil, nil, err
	}
	// A destructive refresh may have issued inaccessible new tokens. A 200
	// for the previous token cannot prove that unknown material was revoked.
	if cred.State == "refresh_pending" || cred.State == "refresh_unknown" {
		for i, c := range file.OAuthCleanup {
			if c.ID == cleanup {
				file.OAuthCleanup[i].UncertainGrant = true
			}
		}
	}
	// Once all sharing connections are durably disconnected, explicit logout
	// also authorizes cleanup of known older tokens for this application.
	for i, c := range file.OAuthCleanup {
		if c.Origin == app.Origin && c.ClientID == app.ClientID && c.State == "manual" {
			file.OAuthCleanup[i].State = "pending"
		}
	}
	cred.State = "logged_out"
	cred.AccessToken = ""
	cred.RefreshToken = ""
	cred.ExpiresAt = time.Time{}
	cred.RefreshAttemptID = ""
	cred.GrantGeneration++
	replaceOAuth(&file, cred)
	var out []Connection
	for i, c := range file.Connections {
		if c.CredentialKind == "oauth" && c.CredentialID == credentialID {
			if c.Version == ^uint64(0) || c.CredentialVersion == ^uint64(0) {
				return nil, nil, ErrCapacity
			}
			c.Version++
			c.CredentialVersion++
			c.CredentialConfigured = false
			c.IdentityBinding = &IdentityBinding{Generation: c.Generation, CredentialVersion: c.CredentialVersion, CredentialFingerprint: oauthFingerprint(authorization(app, cred))}
			file.Connections[i] = c
			out = append(out, c.Connection)
		}
	}
	if err = m.save(file); err != nil {
		return nil, nil, err
	}
	ids := []string{}
	if cleanup != "" {
		ids = append(ids, cleanup)
	}
	return out, ids, nil
}
