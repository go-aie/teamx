package room

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/go-aie/teamx/internal/platform/registry"
)

const gitlabCallbackPath = "/v1/platforms/gitlab/callback"

type gitlabClientImport struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	RedirectURI  string   `json:"redirect_uri"`
	Scopes       []string `json:"scopes"`
}

func (gitlabClientImport) Format(s fmt.State, _ rune) { fmt.Fprint(s, "[REDACTED GitLab client]") }

type gitlabSetupRequest struct {
	ApplicationID   string              `json:"application_id,omitempty"`
	EditApplication bool                `json:"edit_application,omitempty"`
	Scopes          []string            `json:"scopes,omitempty"`
	ServiceEpoch    string              `json:"service_epoch"`
	Caller          Caller              `json:"caller"`
	ID              string              `json:"id,omitempty"`
	Origin          string              `json:"origin,omitempty"`
	Version         uint64              `json:"version,omitempty"`
	Client          *gitlabClientImport `json:"client,omitempty"`
	ClientImportID  string              `json:"client_import_id,omitempty"`
}

func (gitlabSetupRequest) Format(s fmt.State, _ rune) {
	fmt.Fprint(s, "[REDACTED GitLab setup]")
}

type gitlabApplicationCandidate struct {
	ApplicationID      string   `json:"application_id"`
	ClientID           string   `json:"client_id"`
	Scopes             []string `json:"scopes"`
	MissingScopes      []string `json:"missing_scopes,omitempty"`
	CallbackCompatible bool     `json:"callback_compatible"`
}
type gitlabSetupView struct {
	Applications    []gitlabApplicationCandidate `json:"applications,omitempty"`
	ClientID        string                       `json:"client_id,omitempty"`
	Affected        []gitlabAuthPlatform         `json:"affected,omitempty"`
	Status          string                       `json:"status"`
	ApplicationID   string                       `json:"application_id,omitempty"`
	ClientImportID  string                       `json:"client_import_id,omitempty"`
	RedirectURI     string                       `json:"redirect_uri"`
	Scopes          []string                     `json:"scopes"`
	ApplicationsURL string                       `json:"applications_url"`
}

func (m *roomManager) gitlabRedirectLocked() (string, error) {
	endpoint := m.config.endpoint
	if m.config.access != nil {
		endpoint = m.config.access.public
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", ErrRequest
	}
	value := endpoint + gitlabCallbackPath
	if !registry.ValidOAuthRedirect(value) {
		return "", ErrRequest
	}
	return value, nil
}

// Setup only reads local configuration or explicitly imports an application.
// It never creates a remote application or accepts a personal access token.
func (m *roomManager) serveGitLabSetup(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, gitlabCookiePath)
	if action != "prepare" && action != "initialize" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req gitlabSetupRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	if m.closing || m.busy {
		writeAPIError(w, ErrBusy)
		return
	}
	if (action == "initialize") != (req.Client != nil) || action == "prepare" && req.ClientImportID != "" {
		writeAPIError(w, ErrRequest)
		return
	}
	redirect, err := m.gitlabRedirectLocked()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	file, err := m.platforms.Load()
	if err != nil {
		writeAPIError(w, managedPlatformError(err))
		return
	}
	origin := req.Origin
	var target registry.Record
	if req.ID != "" {
		if origin != "" {
			writeAPIError(w, ErrRequest)
			return
		}
		for _, c := range file.Platforms {
			if c.ID == req.ID && c.Version == req.Version && c.Provider == "gitlab" {
				target = c
				origin = c.URL
			}
		}
		if target.ID == "" {
			writeAPIError(w, ErrConflict)
			return
		}
	} else if req.Version != 0 {
		writeAPIError(w, ErrRequest)
		return
	}
	_, origin, err = registry.Normalize("GitLab", origin)
	if err != nil {
		writeAPIError(w, ErrRequest)
		return
	}
	var currentApp registry.OAuthApplication
	if target.CredentialID != "" {
		_, currentApp, err = registry.OAuthRecord(file, target.CredentialID)
		if err != nil {
			writeAPIError(w, managedPlatformError(err))
			return
		}
	}
	scopes := req.Scopes
	if scopes == nil {
		scopes = []string{"api"}
		if currentApp.ID != "" {
			scopes = currentApp.Scopes
		}
	}
	if !registry.GitLabLoginScopes(scopes) {
		writeAPIError(w, ErrRequest)
		return
	}
	if req.ApplicationID != "" && (req.Client != nil || req.EditApplication) {
		writeAPIError(w, ErrRequest)
		return
	}
	view := gitlabSetupView{Status: "configuration_required", RedirectURI: redirect, ApplicationsURL: origin + "/-/user_settings/applications", Scopes: scopes}
	if req.Client != nil {
		input := req.Client
		if input.RedirectURI != redirect || !slices.Equal(input.Scopes, scopes) {
			writeAPIError(w, ErrRequest)
			return
		}
		candidate := registry.OAuthClientImport{Origin: origin, ClientID: input.ClientID, ClientSecret: input.ClientSecret, RedirectURI: redirect, Scopes: scopes, ActorID: req.Caller.ID, ReplacesApplicationID: currentApp.ID}
		for _, session := range m.gitlabAuthSessions {
			select {
			case <-session.done:
			default:
				if session.app.Origin == origin && (session.importID == req.ClientImportID && req.ClientImportID != "" || session.app.ClientID == input.ClientID || currentApp.ID != "" && session.app.ID == currentApp.ID) {
					writeAPIError(w, ErrBusy)
					return
				}
			}
		}
		if req.ClientImportID != "" {
			view.ClientImportID, err = m.platforms.ReplaceOAuthClientImport(req.ClientImportID, candidate)
		} else {
			view.ClientImportID, err = m.platforms.ImportOAuthClient(candidate)
		}
		if err != nil {
			writeAPIError(w, managedPlatformError(err))
			return
		}
		view.Status = "owner_verification_required"
		view.ClientID = input.ClientID
	} else if req.EditApplication && currentApp.ID != "" {
		view.Scopes = currentApp.Scopes
		view.ClientID = currentApp.ClientID
		for _, v := range file.OAuthClientImports {
			if v.ReplacesApplicationID == currentApp.ID && v.ActorID == req.Caller.ID && v.RedirectURI == redirect {
				view.Status, view.ClientImportID, view.Scopes = "owner_verification_required", v.ID, v.Scopes
				view.ClientID = v.ClientID
			}
		}
	} else {
		var apps []registry.OAuthApplication
		for _, app := range file.OAuthApplications {
			if app.Origin != origin || currentApp.ID != "" && app.ID != currentApp.ID {
				continue
			}
			candidate := gitlabApplicationCandidate{ApplicationID: app.ID, ClientID: app.ClientID, Scopes: app.Scopes, CallbackCompatible: app.RedirectURI == redirect, MissingScopes: gitlabApplicationMissingScopes(app.Scopes, scopes)}
			view.Applications = append(view.Applications, candidate)
			if candidate.CallbackCompatible && len(candidate.MissingScopes) == 0 && (req.ApplicationID == "" || req.ApplicationID == app.ID) {
				apps = append(apps, app)
			}
		}
		if req.ApplicationID != "" && len(apps) != 1 {
			writeAPIError(w, ErrRequest)
			return
		}

		if len(apps) == 1 {
			app := apps[0]
			view.Status = "ready"
			view.ApplicationID = app.ID
			view.ClientID = app.ClientID
			view.Scopes = scopes
			records, e := m.gitlabAuthImpactLocked(file, req.ID, app)
			if e == nil {
				view.Affected, _, e = m.gitlabAuthReferencesLocked(records)
			}
			if e != nil {
				writeAPIError(w, e)
				return
			}
		} else if len(apps) > 1 {
			view.Status = "application_selection_required"
		} else if len(view.Applications) > 0 {
			view.Status = "application_incompatible"
		} else if target.CredentialID == "" {
			for _, v := range file.OAuthClientImports {
				if v.ReplacesApplicationID == "" && v.Origin == origin && v.ActorID == req.Caller.ID && v.RedirectURI == redirect {
					view.Status = "owner_verification_required"
					view.ClientImportID = v.ID
					view.ClientID = v.ClientID
					view.Scopes = v.Scopes
				}
			}
		}
	}
	if currentApp.ID != "" && (req.Client != nil || req.EditApplication) {
		records, e := m.gitlabAuthImpactLocked(file, target.ID, currentApp, true)
		if e == nil {
			view.Affected, _, e = m.gitlabAuthReferencesLocked(records)
		}
		if e != nil {
			writeAPIError(w, e)
			return
		}
	}
	// A pending existing connection still participates in the authorization fence.
	if len(view.Affected) == 0 && target.ID != "" {
		view.Affected, _, err = m.gitlabAuthReferencesLocked([]registry.Record{target})
		if err != nil {
			writeAPIError(w, err)
			return
		}
	}
	_ = json.NewEncoder(w).Encode(view)
}
