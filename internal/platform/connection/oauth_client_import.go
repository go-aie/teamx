package connection

import (
	"fmt"
	"slices"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Imported configuration cannot supply business credentials until login verifies
// its native owner. A replacement leaves the current application and grant intact
// until the verified candidate is committed.
type OAuthClientImport struct {
	ReplacesApplicationID string    `json:"replaces_application_id,omitempty"`
	ID                    string    `json:"id"`
	Origin                string    `json:"origin"`
	ClientID              string    `json:"client_id"`
	ClientSecret          string    `json:"client_secret"`
	RedirectURI           string    `json:"redirect_uri"`
	Scopes                []string  `json:"scopes"`
	ActorID               string    `json:"actor_id"`
	State                 string    `json:"state"`
	CreatedAt             time.Time `json:"created_at"`
}

func (OAuthClientImport) Format(s fmt.State, _ rune) { fmt.Fprint(s, "[REDACTED OAuth client import]") }
func validOAuthClientImport(v OAuthClientImport) bool {
	_, origin, err := Normalize("OAuth", v.Origin)
	return err == nil && origin == v.Origin && validRestartID(v.ID) && platform.ValidCredential(v.ClientID) && platform.ValidCredential(v.ClientSecret) && validRedirect(v.RedirectURI) && GitLabLoginScopes(v.Scopes) && v.ActorID != "" && len(v.ActorID) <= 1024 && v.State == "owner_verification_required" && !v.CreatedAt.IsZero()
}
func validateOAuthClientImports(file Snapshot) error {
	if len(file.OAuthClientImports) > 100 {
		return ErrInvalid
	}
	ids := map[string]bool{}
	for i, v := range file.OAuthClientImports {
		if !validOAuthClientImport(v) || ids[v.ID] {
			return ErrInvalid
		}
		ids[v.ID] = true
		if v.ReplacesApplicationID != "" && !slices.ContainsFunc(file.OAuthApplications, func(a OAuthApplication) bool { return a.ID == v.ReplacesApplicationID && a.Origin == v.Origin }) {
			return ErrInvalid
		}
		for _, prior := range file.OAuthClientImports[:i] {
			if prior.Origin == v.Origin && (prior.ClientID == v.ClientID) {
				return ErrInvalid
			}
		}
		for _, app := range file.OAuthApplications {
			if app.ID == v.ID || app.Origin == v.Origin && app.ClientID == v.ClientID && app.ID != v.ReplacesApplicationID {
				return ErrInvalid
			}
		}
	}
	return nil
}
func (m *Manager) ImportOAuthClient(input OAuthClientImport) (string, error) {
	return m.importOAuthClient(input, "")
}

// ReplaceOAuthClientImport replaces only an unverified import owned by the
// operator. A fresh ID makes stale edits and old authorization sessions fail.
func (m *Manager) ReplaceOAuthClientImport(id string, input OAuthClientImport) (string, error) {
	if id == "" {
		return "", ErrRequest
	}
	return m.importOAuthClient(input, id)
}

func (m *Manager) importOAuthClient(input OAuthClientImport, replaceID string) (string, error) {
	if err := m.store.Check(true); err != nil {
		return "", err
	}
	file, err := m.Load()
	if err != nil {
		return "", err
	}
	input.ID, err = newOpaqueID()
	if err != nil {
		return "", err
	}
	input.State, input.CreatedAt = "owner_verification_required", time.Now().UTC()
	if !validOAuthClientImport(input) {
		return "", ErrRequest
	}
	if replaceID != "" {
		index := slices.IndexFunc(file.OAuthClientImports, func(v OAuthClientImport) bool {
			return v.ID == replaceID && v.ActorID == input.ActorID && v.Origin == input.Origin && v.ReplacesApplicationID == input.ReplacesApplicationID
		})
		if index < 0 {
			return "", ErrConflict
		}
		file.OAuthClientImports = slices.Delete(file.OAuthClientImports, index, index+1)
	}
	for _, prior := range file.OAuthClientImports {
		if prior.Origin == input.Origin && (prior.ClientID == input.ClientID || input.ReplacesApplicationID != "" && prior.ReplacesApplicationID == input.ReplacesApplicationID && prior.ActorID == input.ActorID) {
			if replaceID == "" && prior.ReplacesApplicationID == input.ReplacesApplicationID && prior.ActorID == input.ActorID && prior.ClientID == input.ClientID && prior.ClientSecret == input.ClientSecret && prior.RedirectURI == input.RedirectURI && slices.Equal(prior.Scopes, input.Scopes) {
				return prior.ID, nil
			}
			return "", ErrConflict
		}
	}
	foundReplacement := input.ReplacesApplicationID == ""
	for _, app := range file.OAuthApplications {
		if app.ID == input.ReplacesApplicationID && app.Origin == input.Origin {
			foundReplacement = true
		}
		if app.Origin == input.Origin && app.ClientID == input.ClientID && app.ID != input.ReplacesApplicationID {
			return "", ErrConflict
		}
	}
	if !foundReplacement {
		return "", ErrConflict
	}
	if len(file.OAuthClientImports) >= 100 {
		return "", ErrCapacity
	}
	if file.InstallationID == "" {
		file.InstallationID, err = newOpaqueID()
		if err != nil {
			return "", err
		}
	}
	file.Version = OAuthSnapshotVersion
	file.OAuthClientImports = append(file.OAuthClientImports, input)
	if err = m.save(file); err != nil {
		return "", err
	}
	return input.ID, nil
}

// GitLabLoginScopes limits imported grants to the implemented API and optional
// Git-over-HTTP capabilities; never silently expands an existing grant.
func GitLabLoginScopes(scopes []string) bool {
	return slices.Equal(scopes, []string{"api"}) || slices.Equal(scopes, []string{"api", "write_repository"})
}
