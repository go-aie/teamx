package room

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func connectOrigin(in platformConnectInput) string {
	if in.Provider == "lark" {
		return lark.Origin("feishu")
	}
	return in.Origin
}
func validConnectInput(in platformConnectInput) bool {
	if connectIntent(in) != "connect" && connectIntent(in) != "authorize" {
		return false
	}
	if connectIntent(in) == "authorize" && (in.Provider != "lark" || !tool.OpaqueID(in.PlatformID) || in.PlatformVersion == 0) {
		return false
	}
	switch in.Provider {
	case "lark":
		return in.Origin == "" && in.ApplicationID == "" && in.AccountUsername == ""
	case "gitlab":
		if in.Intent != "" {
			return false
		}
		_, origin, err := registry.Normalize(in.Name, in.Origin)
		return err == nil && origin == in.Origin && (in.ApplicationID == "" || tool.OpaqueID(in.ApplicationID)) &&
			(in.AccountUsername == "" || validText(in.AccountUsername) && len(in.AccountUsername) <= 255 && !strings.ContainsAny(in.AccountUsername, " /\\\t\n"))
	}
	return false
}
func validConnectProvider(op platformConnectOperation) bool {
	if !validConnectInput(op.Input) {
		return false
	}
	switch op.Input.Provider {
	case "lark":
		return op.Interaction.Kind == "lark_authorization" && op.ApplicationFingerprint == "" && op.BoundApplicationID == "" && !op.ApplicationImport
	case "gitlab":
		if op.Interaction.Kind != "gitlab_authorization" || op.Profile != nil {
			return false
		}
		if op.BoundApplicationID != "" && (!tool.OpaqueID(op.BoundApplicationID) || op.Input.ApplicationID != "" || op.Input.PlatformID != "") || op.ApplicationImport && op.BoundApplicationID == "" {
			return false
		}
		if op.ApplicationFingerprint == "" {
			return op.Input.ApplicationID == "" && op.Input.PlatformID == "" && op.BoundApplicationID == "" && op.Stage == "application" && (op.Status == "waiting" || op.CancelRequested)
		}
		return len(op.ApplicationFingerprint) == 64 && (op.Input.ApplicationID != "" || op.Input.PlatformID != "" || op.BoundApplicationID != "")
	}
	return false
}

type gitlabConnectApplication struct {
	ID         string `json:"id"`
	Origin     string `json:"origin"`
	Compatible bool   `json:"compatible"`
	Reason     string `json:"reason,omitempty"`
}

func (m *roomManager) gitlabConnectApplicationsLocked(file registry.Snapshot) []gitlabConnectApplication {
	apps := []gitlabConnectApplication{}
	redirect, err := m.gitlabRedirectLocked()
	for _, a := range file.OAuthApplications {
		v := gitlabConnectApplication{ID: a.ID, Origin: a.Origin, Compatible: true}
		switch {
		case err != nil:
			v.Compatible, v.Reason = false, "service_address_unavailable"
		case a.RedirectURI != redirect:
			v.Compatible, v.Reason = false, "redirect_mismatch"
		case !registry.GitLabLoginScopes(a.Scopes):
			v.Compatible, v.Reason = false, "invalid_scope"
		}
		apps = append(apps, v)
	}
	return apps
}
func gitlabConnectFingerprint(app registry.OAuthApplication) string {
	data, _ := json.Marshal(app)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
func gitlabConnectRecordMatches(file registry.Snapshot, c registry.Record, op platformConnectOperation) bool {
	if c.Provider != "gitlab" || c.URL != op.Input.Origin || c.Name != op.Input.Name || c.ID != op.PlatformID {
		return false
	}
	cred, app, err := registry.OAuthRecord(file, c.CredentialID)
	return err == nil && cred.State == "active" && app.ID == connectApplicationID(op) && connectApplicationFingerprint(app, op.ApplicationImport) == op.ApplicationFingerprint
}
func (m *roomManager) startGitLabConnectLocked(op platformConnectOperation) (platformConnectOperation, error) {
	file, err := m.platforms.Load()
	if err != nil {
		return op, err
	}
	op.Interaction.Kind = "gitlab_authorization"
	if op.Input.PlatformID == "" && op.Input.ApplicationID == "" {
		op.Status, op.Stage = "waiting", "application"
		return op, m.savePlatformConnectLocked(&op)
	}
	if op.Input.PlatformID != "" {
		for _, c := range file.Platforms {
			if c.ID != op.Input.PlatformID || c.Version != op.Input.PlatformVersion || c.Provider != "gitlab" || c.URL != op.Input.Origin || c.Name != op.Input.Name {
				continue
			}
			a, _, e := registry.ResolveAccessAccount(file, c)
			cred, app, ce := registry.OAuthRecord(file, c.CredentialID)
			if e != nil || ce != nil || cred.State != "active" || op.Input.ApplicationID != "" && op.Input.ApplicationID != app.ID || op.Input.AccountUsername != "" && !strings.EqualFold(c.Validation.Username, op.Input.AccountUsername) {
				return op, ErrConflict
			}
			// Keep fixed input intact. Reuse does not need a new compatible callback.
			op.PlatformID, op.Account, op.ApplicationFingerprint = c.ID, a.Identity, gitlabConnectFingerprint(app)
			op.Status, op.Stage, op.ResultVersion = "completed", "complete", 1
			op.Application.Status, op.Authorization.Status = "reused", "reused"
			op.Connection = onboardingStep{Status: "connected", Version: c.Version}
			return op, m.savePlatformConnectLocked(&op)
		}
		return op, ErrConflict
	}
	if op.Input.PlatformVersion != 0 {
		return op, ErrRequest
	}
	for _, a := range file.OAuthApplications {
		if a.ID != op.Input.ApplicationID || a.Origin != op.Input.Origin {
			continue
		}
		redirect, e := m.gitlabRedirectLocked()
		if e != nil || a.RedirectURI != redirect || !registry.GitLabLoginScopes(a.Scopes) {
			return op, conflictReason("application_id", "GitLab OAuth application is incompatible", "Open Settings → Platforms to check the service callback and required scopes.")
		}
		op.ApplicationFingerprint = gitlabConnectFingerprint(a)
		op.Application.Status = "reused"
		op.Status, op.Stage = "waiting", "authorization"
		return op, m.savePlatformConnectLocked(&op)
	}
	return op, conflictReason("application_id", "No compatible verified GitLab OAuth application", "Configure and verify the application in Settings → Platforms, then use its exact application ID. Never submit credentials through chat.")
}
func (m *roomManager) gitlabConnectSessionLocked(id string) *gitlabAuthSession {
	for _, s := range m.gitlabAuthSessions {
		if s.operationID == id {
			return s
		}
	}
	return nil
}

// Only the original human's canonical browser can establish the temporary OAuth
// session. Agent start/status never creates or receives browser authorization data.
func (m *roomManager) authorizeGitLabConnectLocked(w http.ResponseWriter, r *http.Request, op platformConnectOperation, req platformConnectInteractionRequest) error {
	if op.Input.Provider != "gitlab" || connectTerminal(op.Status) || op.CancelRequested {
		return ErrConflict
	}
	if !m.canonicalGitLabRequestLocked(r) {
		return &requestDiagnostic{cause: ErrIdentity, Field: "service_origin", Message: "GitLab authorization requires the configured service origin", Hint: "Open the service at its configured address and refresh before retrying."}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	err := m.checkOnboardingTargetLocked(ctx, platformOnboardingSpec{MemberIdentity: op.MemberIdentity})
	cancel()
	if err != nil {
		if errors.Is(err, ErrIdentity) {
			return &requestDiagnostic{cause: ErrIdentity, Field: "membership_id", Message: "Original GitLab connection member is no longer available", Hint: "Refresh the conversation and inspect its original member before starting another operation."}
		}
		return err
	}
	if req.ApplicationID != "" && req.ClientImportID != "" {
		return ErrRequest
	}
	reference := req.ApplicationID
	if req.ClientImportID != "" {
		reference = req.ClientImportID
	}
	if op.ApplicationFingerprint != "" && reference != "" && (reference != connectApplicationID(op) || (req.ClientImportID != "") != op.ApplicationImport) {
		return ErrConflict
	}
	s := m.gitlabConnectSessionLocked(op.ID)
	if s != nil {
		if s.connectErr != nil {
			return s.connectErr
		}
		if s.Status != "waiting" || s.ctx.Err() != nil {
			return ErrConflict
		}
		cookie, err := r.Cookie(gitlabCookieName(s.SessionID))
		// A second browser cannot steal an already established session. Tabs in the
		// original browser share the HttpOnly cookie and reuse the exact session.
		if err != nil || cookie.Value != s.browser {
			return &requestDiagnostic{cause: ErrIdentity, Field: "browser_session", Message: "GitLab authorization is already bound to another browser session", Hint: "Continue in the browser that first opened authorization. Do not transfer cookies or session URLs."}
		}
	} else {
		file, err := m.platforms.Load()
		if err != nil {
			return err
		}
		app, importID, err := connectApplication(file, op, req)
		if err != nil {
			return err
		}
		redirect, err := m.gitlabRedirectLocked()
		if err != nil || app.RedirectURI != redirect || !registry.GitLabLoginScopes(app.Scopes) {
			return ErrConflict
		}
		if op.ApplicationFingerprint == "" {
			op.BoundApplicationID, op.ApplicationImport = app.ID, importID != ""
			op.ApplicationFingerprint = connectApplicationFingerprint(app, op.ApplicationImport)
			op.Stage, op.Status = "authorization", "waiting"
			if importID == "" {
				op.Application.Status = "reused"
			}
			if err := m.savePlatformConnectLocked(&op); err != nil {
				return err
			}
		}
		authReq := gitlabAuthRequest{Caller: Caller{Kind: "human", ID: op.Source.Author.ID}, Name: op.Input.Name, Origin: op.Input.Origin, ClientImportID: importID}
		if importID == "" {
			authReq.ApplicationID = app.ID
		}
		s, err = m.newGitLabAuthLocked(authReq, "login")
		if err != nil {
			return managedPlatformError(err)
		}
		s.operationID, s.targetID, s.PlatformID = op.ID, op.PlatformID, op.PlatformID
		http.SetCookie(w, &http.Cookie{Name: gitlabCookieName(s.SessionID), Value: s.browser, Path: gitlabCookiePath, HttpOnly: true, Secure: strings.HasPrefix(s.app.RedirectURI, "https:"), SameSite: http.SameSiteLaxMode, MaxAge: 660})
	}
	return json.NewEncoder(w).Encode(struct {
		URL string `json:"authorization_url"`
	}{s.URL})
}

// Fence callback settlement with the original operation and membership. This
// runs before the one existing OAuth commit path, never through onboarding.
func (m *roomManager) checkGitLabConnectLocked(s *gitlabAuthSession) error {
	if s.operationID == "" {
		return nil
	}
	op, err := m.readPlatformConnectLocked(s.operationID)
	if err != nil {
		return err
	}
	if op.CancelRequested || connectTerminal(op.Status) || op.PlatformID != s.targetID || connectApplicationID(op) != s.app.ID || op.Input.Origin != s.app.Origin || op.ApplicationFingerprint != connectApplicationFingerprint(s.app, op.ApplicationImport) {
		return ErrConflict
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.checkOnboardingTargetLocked(ctx, platformOnboardingSpec{MemberIdentity: op.MemberIdentity})
}
func (m *roomManager) checkpointGitLabConnectLocked(s *gitlabAuthSession, identity platform.Validation) error {
	if s.operationID == "" {
		return nil
	}
	op, err := m.readPlatformConnectLocked(s.operationID)
	if err != nil {
		return err
	}
	if op.Input.AccountUsername != "" && !strings.EqualFold(op.Input.AccountUsername, identity.Username) {
		return ErrIdentity
	}
	account, valid := gitlab.LegacyAccountIdentity(op.Input.Origin, identity)
	if !valid {
		return ErrIdentity
	}
	op.Account = account
	if op.ApplicationImport {
		op.Application.Status = "applied"
	}
	op.Authorization.Status = "applied"
	op.Stage, op.Status = "connection", "running"
	return m.savePlatformConnectLocked(&op)
}
func (m *roomManager) finishGitLabConnectLocked(s *gitlabAuthSession) {
	if s.operationID == "" {
		return
	}
	op, err := m.readPlatformConnectLocked(s.operationID)
	if err != nil {
		s.connectErr = err
		return
	}
	if connectTerminal(op.Status) {
		return
	}
	op.Status, op.ErrorCode = s.Status, s.ErrorCode
	if op.CancelRequested {
		op.Status, op.ErrorCode = "cancelled", "cancelled"
	}
	if !op.CancelRequested && (s.Status == "succeeded" || s.ErrorCode == "switch_unconfirmed") {
		// Read the exact callback target, application and verified account. A saved
		// connection is an applied effect even when later public projection failed.
		file, e := m.platforms.Load()
		if e == nil {
			for _, c := range file.Platforms {
				if !gitlabConnectRecordMatches(file, c, op) {
					continue
				}
				a, _, ae := registry.ResolveAccessAccount(file, c)
				if ae == nil && a.Identity == op.Account {
					op.Status, op.Stage, op.ErrorCode = "completed", "complete", ""
					op.Connection = onboardingStep{Status: "connected", Version: c.Version}
				}
			}
		}
		if op.Status != "completed" {
			op.Status, op.ErrorCode = "unknown", "connection_unconfirmed"
			op.Connection = onboardingStep{Status: "unknown", ErrorCode: op.ErrorCode}
		}
	}
	switch op.Status {
	case "completed", "cancelled", "expired", "failed", "unknown":
	default:
		op.Status, op.ErrorCode = "unknown", "connection_unconfirmed"
	}
	if op.Authorization.Status == "pending" && (op.Status == "failed" || op.Status == "expired") {
		op.Authorization = onboardingStep{Status: "failed", ErrorCode: op.ErrorCode}
	}
	op.ResultVersion++
	s.connectErr = m.savePlatformConnectLocked(&op)
	m.wakeConnectNotificationsLocked()
}

// The human may fill an initially absent application exactly once. Fixed Agent
// input remains unchanged so lost start responses still recover the same request.
func connectApplicationID(op platformConnectOperation) string {
	if op.BoundApplicationID != "" {
		return op.BoundApplicationID
	}
	return op.Input.ApplicationID
}
func connectApplicationFingerprint(app registry.OAuthApplication, imported bool) string {
	if imported {
		// Ownership is verified by the native callback after the import is bound.
		app.OwnerID = 0
	}
	return gitlabConnectFingerprint(app)
}
func connectApplication(file registry.Snapshot, op platformConnectOperation, req platformConnectInteractionRequest) (registry.OAuthApplication, string, error) {
	id, imported := connectApplicationID(op), op.ApplicationImport
	if op.ApplicationFingerprint == "" {
		id, imported = req.ApplicationID, req.ClientImportID != ""
		if imported {
			id = req.ClientImportID
		}
	}
	var app registry.OAuthApplication
	if imported {
		for _, v := range file.OAuthClientImports {
			if v.ID == id && v.ActorID == op.Source.Author.ID && v.Origin == op.Input.Origin && v.ReplacesApplicationID == "" {
				app = registry.OAuthApplication{ID: v.ID, Origin: v.Origin, ClientID: v.ClientID, ClientSecret: v.ClientSecret, RedirectURI: v.RedirectURI, Scopes: v.Scopes}
			}
		}
	} else {
		for _, v := range file.OAuthApplications {
			if v.ID == id && v.Origin == op.Input.Origin {
				app = v
			}
		}
	}
	if app.ID == "" || op.ApplicationFingerprint != "" && connectApplicationFingerprint(app, imported) != op.ApplicationFingerprint {
		return app, "", ErrConflict
	}
	if imported {
		return app, id, nil
	}
	return app, "", nil
}

type gitlabConnectSetup struct {
	Origin          string                     `json:"origin"`
	ApplicationsURL string                     `json:"applications_url"`
	RedirectURI     string                     `json:"redirect_uri,omitempty"`
	Scopes          []string                   `json:"scopes"`
	Applications    []gitlabConnectApplication `json:"applications"`
	ErrorCode       string                     `json:"error_code,omitempty"`
}

func (m *roomManager) gitlabConnectSetupLocked(op platformConnectOperation) (*gitlabConnectSetup, error) {
	file, err := m.platforms.Load()
	if err != nil {
		return nil, err
	}
	setup := &gitlabConnectSetup{Origin: op.Input.Origin, ApplicationsURL: op.Input.Origin + "/-/user_settings/applications", Scopes: []string{"api"}, Applications: []gitlabConnectApplication{}}
	setup.RedirectURI, err = m.gitlabRedirectLocked()
	if err != nil {
		setup.ErrorCode = "service_address_unavailable"
	}
	for _, a := range m.gitlabConnectApplicationsLocked(file) {
		if a.Origin == op.Input.Origin {
			setup.Applications = append(setup.Applications, a)
		}
	}
	return setup, nil
}
