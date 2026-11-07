package room

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/go-aie/teamx/internal/platform/connection"
)

const gitlabCallbackPath = "/v1/platform/connections/gitlab/callback"

type gitlabClientImport struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	RedirectURI  string   `json:"redirect_uri"`
	Scopes       []string `json:"scopes"`
}

func (gitlabClientImport) Format(s fmt.State, _ rune) { fmt.Fprint(s, "[REDACTED GitLab client]") }

type gitlabSetupRequest struct {
	RepositoryAccess bool                `json:"repository_access,omitempty"`
	ServiceEpoch     string              `json:"service_epoch"`
	Caller           Caller              `json:"caller"`
	ID               string              `json:"id,omitempty"`
	Origin           string              `json:"origin,omitempty"`
	Version          uint64              `json:"version,omitempty"`
	Client           *gitlabClientImport `json:"client,omitempty"`
}

func (gitlabSetupRequest) Format(s fmt.State, _ rune) {
	fmt.Fprint(s, "[REDACTED GitLab setup]")
}

type gitlabSetupView struct {
	Affected        []gitlabAuthConnection `json:"affected,omitempty"`
	Status          string                 `json:"status"`
	ApplicationID   string                 `json:"application_id,omitempty"`
	ClientImportID  string                 `json:"client_import_id,omitempty"`
	RedirectURI     string                 `json:"redirect_uri"`
	Scopes          []string               `json:"scopes"`
	ApplicationsURL string                 `json:"applications_url"`
}

func (m *roomManager) gitlabRedirectLocked() (string, error) {
	u, err := url.Parse(m.config.endpoint)
	if err != nil || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", ErrRequest
	}
	value := m.config.endpoint + gitlabCallbackPath
	if !connection.ValidOAuthRedirect(value) {
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
	if (action == "initialize") != (req.Client != nil) {
		writeAPIError(w, ErrRequest)
		return
	}
	redirect, err := m.gitlabRedirectLocked()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	file, err := m.connections.Load()
	if err != nil {
		writeAPIError(w, platformConnectionError(err))
		return
	}
	origin := req.Origin
	var target connection.Record
	if req.ID != "" {
		if origin != "" {
			writeAPIError(w, ErrRequest)
			return
		}
		for _, c := range file.Connections {
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
	_, origin, err = connection.Normalize("GitLab", origin)
	if err != nil {
		writeAPIError(w, ErrRequest)
		return
	}
	scopes := []string{"api"}
	if req.RepositoryAccess {
		scopes = append(scopes, "write_repository")
	}
	view := gitlabSetupView{Status: "configuration_required", RedirectURI: redirect, ApplicationsURL: origin + "/-/user_settings/applications", Scopes: scopes}
	if req.Client != nil {
		input := req.Client
		if target.CredentialID != "" || input.RedirectURI != redirect || !slices.Equal(input.Scopes, scopes) {
			writeAPIError(w, ErrRequest)
			return
		}
		view.ClientImportID, err = m.connections.ImportOAuthClient(connection.OAuthClientImport{Origin: origin, ClientID: input.ClientID, ClientSecret: input.ClientSecret, RedirectURI: redirect, Scopes: scopes, ActorID: req.Caller.ID})
		if err != nil {
			writeAPIError(w, platformConnectionError(err))
			return
		}
		view.Status = "owner_verification_required"
	} else {
		var apps []connection.OAuthApplication
		for _, app := range file.OAuthApplications {
			if app.Origin == origin && app.RedirectURI == redirect && (!req.RepositoryAccess || slices.Equal(app.Scopes, scopes)) && (target.CredentialID == "" || slices.ContainsFunc(file.OAuthCredentials, func(c connection.OAuthCredential) bool {
				return c.ID == target.CredentialID && c.ApplicationID == app.ID
			})) {
				apps = append(apps, app)
			}
		}
		if len(apps) == 1 {
			app := apps[0]
			view.Status = "ready"
			view.ApplicationID = app.ID
			view.Scopes = app.Scopes
			records, e := m.gitlabAuthImpactLocked(file, req.ID, app)
			if e == nil {
				view.Affected, _, e = m.gitlabAuthReferencesLocked(records)
			}
			if e != nil {
				writeAPIError(w, e)
				return
			}
		} else if target.CredentialID == "" {
			for _, v := range file.OAuthClientImports {
				if v.Origin == origin && v.ActorID == req.Caller.ID && v.RedirectURI == redirect && (!req.RepositoryAccess || slices.Equal(v.Scopes, scopes)) {
					view.Status = "owner_verification_required"
					view.ClientImportID = v.ID
					view.Scopes = v.Scopes
				}
			}
		}
	}
	// A pending existing connection still participates in the authorization fence.
	if len(view.Affected) == 0 && target.ID != "" {
		view.Affected, _, err = m.gitlabAuthReferencesLocked([]connection.Record{target})
		if err != nil {
			writeAPIError(w, err)
			return
		}
	}
	_ = json.NewEncoder(w).Encode(view)
}
