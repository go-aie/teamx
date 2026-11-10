package room

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/registry"
)

const gitlabCookiePath = "/v1/platforms/gitlab/"
const gitlabCompletePath = gitlabCookiePath + "complete"

type gitlabAuthPlatform struct {
	ID              string         `json:"id"`
	Version         uint64         `json:"version"`
	ReferencedRooms []RoomIdentity `json:"referenced_rooms"`
}
type gitlabAuthRequest struct {
	OnboardingID   string               `json:"onboarding_id,omitempty"`
	Scopes         []string             `json:"scopes,omitempty"`
	ServiceEpoch   string               `json:"service_epoch"`
	Caller         Caller               `json:"caller"`
	ID             string               `json:"id,omitempty"`
	Version        uint64               `json:"version,omitempty"`
	Name           string               `json:"name,omitempty"`
	Origin         string               `json:"origin,omitempty"`
	ApplicationID  string               `json:"application_id,omitempty"`
	ClientImportID string               `json:"client_import_id,omitempty"`
	SessionID      string               `json:"session_id,omitempty"`
	Affected       []gitlabAuthPlatform `json:"affected,omitempty"`
}
type gitlabAuthView struct {
	OnboardingID    string            `json:"onboarding_id,omitempty"`
	PlatformID      string            `json:"platform_id"`
	SessionID       string            `json:"session_id"`
	Status          string            `json:"status"`
	URL             string            `json:"authorization_url,omitempty"`
	ErrorCode       string            `json:"error_code,omitempty"`
	RequestedScopes []string          `json:"requested_scopes,omitempty"`
	MissingScopes   []string          `json:"missing_scopes,omitempty"`
	Cleanup         string            `json:"cleanup_status,omitempty"`
	ExpiresAt       time.Time         `json:"expires_at"`
	Platforms       []managedPlatform `json:"platforms,omitempty"`
}
type gitlabAuthSession struct {
	gitlabAuthView
	owner, state, browser, verifier string
	app                             registry.OAuthApplication
	previousApp                     registry.OAuthApplication
	importID, targetID, name        string
	records                         []registry.Record
	references                      map[string]string
	ctx                             context.Context
	cancel                          context.CancelFunc
	done                            chan struct{}
}

func (s *gitlabAuthSession) expectedUserID() int64 {
	if s.importID != "" {
		return s.app.OwnerID
	}
	for _, c := range s.records {
		if c.ID == s.targetID && c.CredentialID != "" {
			return c.Validation.UserID
		}
	}
	return 0
}

func oauthRandom() (string, error) {
	var b [32]byte
	_, err := rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:]), err
}
func gitlabCookieName(id string) string { return "teamx_gitlab_" + id }
func (m *roomManager) canonicalGitLabRequestLocked(r *http.Request) bool {
	redirect, err := m.gitlabRedirectLocked()
	if err != nil {
		return false
	}
	u, _ := url.Parse(redirect)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return r.Host == u.Host && scheme == u.Scheme
}
func (m *roomManager) finishGitLabAuthLocked(s *gitlabAuthSession, status, code string) {
	s.Status, s.ErrorCode, s.URL, s.state, s.verifier = status, code, "", "", ""
	if code == "invalid_scope" && len(s.RequestedScopes) == 0 {
		s.RequestedScopes = slices.Clone(s.app.Scopes)
	}
	s.cancel()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}
func (m *roomManager) cancelGitLabAuthLocked(s *gitlabAuthSession) {
	s.cancel()
	if s.Status == "waiting" {
		m.finishGitLabAuthLocked(s, "cancelled", "")
	}
}

// Account operations follow the selected authorization. Application management
// explicitly fences every authorization, including logged-out accounts.
func (m *roomManager) gitlabAuthImpactLocked(file registry.Snapshot, targetID string, app registry.OAuthApplication, allAccounts ...bool) ([]registry.Record, error) {
	credentialID := ""
	for _, c := range file.Platforms {
		if c.ID == targetID {
			credentialID = c.CredentialID
		}
	}
	var records []registry.Record
	for _, c := range file.Platforms {
		shared := credentialID != "" && c.CredentialKind == "oauth" && c.CredentialID == credentialID
		if len(allAccounts) > 0 && allAccounts[0] && c.CredentialKind == "oauth" {
			cred, _, err := registry.OAuthRecord(file, c.CredentialID)
			if err != nil {
				return nil, err
			}
			shared = cred.ApplicationID == app.ID
		}
		if c.ID == targetID || shared {
			records = append(records, c)
		}
	}
	return records, nil
}
func (m *roomManager) gitlabAuthReferencesLocked(records []registry.Record) ([]gitlabAuthPlatform, map[string]string, error) {
	affected := []gitlabAuthPlatform{}
	fingerprints := map[string]string{}
	for _, c := range records {
		refs, err := m.managedPlatformReferencesLocked(c.ID)
		if err != nil {
			return nil, nil, err
		}
		rooms := []RoomIdentity{}
		for _, r := range refs {
			rooms = append(rooms, r.RoomIdentity)
		}
		affected = append(affected, gitlabAuthPlatform{ID: c.ID, Version: c.Version, ReferencedRooms: rooms})
		fingerprints[c.ID] = gitlabAuthReferencesFingerprint(refs)
	}
	return affected, fingerprints, nil
}
func (m *roomManager) checkGitLabAuthLocked(s *gitlabAuthSession) error {
	if m.closing {
		return ErrClosing
	}
	if m.busy {
		return ErrBusy
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if err := m.store.check(true); err != nil {
		return err
	}
	redirect, err := m.gitlabRedirectLocked()
	if err != nil || redirect != s.app.RedirectURI {
		return ErrConflict
	}
	file, err := m.platforms.Load()
	if err != nil {
		return err
	}
	if s.importID == "" {
		if !slices.ContainsFunc(file.OAuthApplications, func(a registry.OAuthApplication) bool {
			return a.ID == s.app.ID && a.ClientID == s.app.ClientID && a.ClientSecret == s.app.ClientSecret && a.RedirectURI == s.app.RedirectURI && a.OwnerID == s.app.OwnerID && a.Origin == s.app.Origin && slices.Equal(a.Scopes, s.app.Scopes)
		}) {
			return ErrConflict
		}
	} else if !slices.ContainsFunc(file.OAuthClientImports, func(a registry.OAuthClientImport) bool {
		return a.ID == s.importID && a.ActorID == s.owner && a.ClientID == s.app.ClientID && a.ClientSecret == s.app.ClientSecret && a.RedirectURI == s.app.RedirectURI && slices.Equal(a.Scopes, s.app.Scopes) && a.ReplacesApplicationID == s.previousApp.ID
	}) {
		return ErrConflict
	}
	if s.previousApp.ID != "" && !slices.ContainsFunc(file.OAuthApplications, func(a registry.OAuthApplication) bool {
		return a.ID == s.previousApp.ID && a.ClientID == s.previousApp.ClientID && a.ClientSecret == s.previousApp.ClientSecret && a.OwnerID == s.previousApp.OwnerID && a.RedirectURI == s.previousApp.RedirectURI && slices.Equal(a.Scopes, s.previousApp.Scopes)
	}) {
		return ErrConflict
	}
	current, err := m.gitlabAuthImpactLocked(file, s.targetID, s.app, s.previousApp.ID != "")
	if s.name != "" && len(s.records) > 0 && s.records[0].CredentialID != "" && s.previousApp.ID == "" {
		credentialID := s.records[0].CredentialID
		current = nil
		for _, c := range file.Platforms {
			if c.CredentialID == credentialID {
				current = append(current, c)
			}
		}
	}
	if err != nil || len(current) != len(s.records) {
		return ErrConflict
	}
	for _, want := range s.records {
		if !slices.ContainsFunc(current, func(c registry.Record) bool {
			return c.ID == want.ID && c.Generation == want.Generation && c.Version == want.Version && c.CredentialVersion == want.CredentialVersion && c.CredentialID == want.CredentialID
		}) {
			return ErrConflict
		}
		refs, err := m.managedPlatformReferencesLocked(want.ID)
		if err != nil {
			return err
		}
		if gitlabAuthReferencesFingerprint(refs) != s.references[want.ID] {
			return ErrConflict
		}
	}
	return nil
}
func (m *roomManager) serveGitLabAuth(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, gitlabCookiePath)
	if action == "callback" {
		m.serveGitLabCallback(w, r)
		return
	}
	if action == "complete" {
		if r.Method != http.MethodGet {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		_, _ = io.WriteString(w, "<!doctype html><html lang=zh-CN><meta charset=utf-8><title>GitLab 授权已处理</title><p>授权回调已处理。请返回 TeamX 的平台连接页面核对结果，此窗口可以关闭。</p></html>")
		return
	}
	if action != "login" && action != "logout" && action != "status" && action != "cancel" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var req gitlabAuthRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	m.mu.Lock()
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		m.mu.Unlock()
		writeAPIError(w, ErrIdentity)
		return
	}
	if !m.canonicalGitLabRequestLocked(r) {
		m.mu.Unlock()
		writeAPIError(w, ErrIdentity)
		return
	}
	if m.closing {
		m.mu.Unlock()
		writeAPIError(w, ErrClosing)
		return
	}
	if action == "status" || action == "cancel" {
		s := m.gitlabAuthSessions[req.SessionID]
		cookie, err := r.Cookie(gitlabCookieName(req.SessionID))
		if s == nil || s.owner != req.Caller.ID || time.Now().After(s.ExpiresAt.Add(time.Minute)) || err != nil || cookie.Value != s.browser || req.ID != "" || req.Version != 0 || req.Name != "" || req.Origin != "" || req.ApplicationID != "" || req.ClientImportID != "" || len(req.Affected) != 0 || len(req.Scopes) != 0 || req.OnboardingID != "" {
			m.mu.Unlock()
			writeAPIError(w, ErrIdentity)
			return
		}
		if action == "cancel" {
			m.cancelGitLabAuthLocked(s)
		}
		view := s.gitlabAuthView
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(view)
		return
	}
	if m.busy || req.SessionID != "" {
		m.mu.Unlock()
		writeAPIError(w, ErrBusy)
		return
	}
	if err := m.store.check(true); err != nil {
		m.mu.Unlock()
		writeAPIError(w, err)
		return
	}
	s, err := m.newGitLabAuthLocked(req, action)
	if err != nil {
		m.mu.Unlock()
		writeAPIError(w, managedPlatformError(err))
		return
	}
	secure := strings.HasPrefix(s.app.RedirectURI, "https:")
	http.SetCookie(w, &http.Cookie{Name: gitlabCookieName(s.SessionID), Value: s.browser, Path: gitlabCookiePath, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: 660})
	if action == "logout" {
		go m.runGitLabLogout(s)
	}
	view := s.gitlabAuthView
	m.mu.Unlock()
	_ = json.NewEncoder(w).Encode(view)
}
func (m *roomManager) newGitLabAuthLocked(req gitlabAuthRequest, action string) (*gitlabAuthSession, error) {
	var onboarding platformOnboarding
	if req.OnboardingID != "" {
		if action != "login" {
			return nil, ErrRequest
		}
		var e error
		onboarding, e = m.readOnboardingLocked(req.OnboardingID, req.Caller.ID)
		if e != nil {
			return nil, e
		}
		if onboarding.Cancelled || onboarding.Input.Provider != "gitlab" || req.ID != "" && req.ID != onboarding.PlatformID {
			return nil, ErrConflict
		}
	}
	file, err := m.platforms.Load()
	if err != nil {
		return nil, err
	}
	var target registry.Record
	for _, c := range file.Platforms {
		if c.ID == req.ID && c.Version == req.Version {
			target = c
		}
	}
	if onboarding.ID != "" {
		if target.ID != "" {
			if target.URL != onboarding.Input.Origin || onboarding.Generation != "" && target.Generation != onboarding.Generation {
				return nil, ErrConflict
			}
		} else {
			_, origin, e := registry.Normalize(req.Name, req.Origin)
			if e != nil || origin != onboarding.Input.Origin {
				return nil, ErrConflict
			}
			if req.ID == onboarding.PlatformID {
				req.ID = ""
			}
		}
	}
	if req.ID != "" && (target.ID == "" || target.Provider != "gitlab") {
		return nil, ErrConflict
	}
	var app registry.OAuthApplication
	var previousApp registry.OAuthApplication
	importID := ""
	if target.ID != "" {
		if req.Name != "" || req.Origin != "" {
			return nil, ErrRequest
		}
		if target.CredentialID != "" {
			_, app, err = registry.OAuthRecord(file, target.CredentialID)
		}
	} else {
		if action == "logout" || req.Version != 0 {
			return nil, ErrRequest
		}
		name, origin, e := registry.Normalize(req.Name, req.Origin)
		if e != nil {
			return nil, e
		}
		req.Name = name
		for _, v := range file.OAuthApplications {
			if v.ID == req.ApplicationID && v.Origin == origin {
				app = v
			}
		}
		if req.ClientImportID != "" && req.ApplicationID == "" {
			for _, v := range file.OAuthClientImports {
				if v.ReplacesApplicationID == "" && v.ID == req.ClientImportID && v.ActorID == req.Caller.ID && v.Origin == origin {
					app = registry.OAuthApplication{ID: v.ID, Origin: v.Origin, ClientID: v.ClientID, ClientSecret: v.ClientSecret, RedirectURI: v.RedirectURI, Scopes: v.Scopes}
					importID = v.ID
				}
			}
		}
		req.ID, err = newOpaqueID()
		if onboarding.ID != "" {
			req.ID = onboarding.PlatformID
		}
		if err != nil {
			return nil, err
		}
	}
	if target.ID != "" && target.CredentialID == "" && action == "login" {
		for _, v := range file.OAuthApplications {
			if v.ID == req.ApplicationID && v.Origin == target.URL {
				app = v
			}
		}
		for _, v := range file.OAuthClientImports {
			if v.ReplacesApplicationID == "" && req.ApplicationID == "" && v.ID == req.ClientImportID && v.ActorID == req.Caller.ID && v.Origin == target.URL {
				app = registry.OAuthApplication{ID: v.ID, Origin: v.Origin, ClientID: v.ClientID, ClientSecret: v.ClientSecret, RedirectURI: v.RedirectURI, Scopes: v.Scopes}
				importID = v.ID
			}
		}
	}
	if target.CredentialID != "" && req.ClientImportID != "" {
		if action != "login" || req.ApplicationID != "" {
			return nil, ErrRequest
		}
		previousApp = app
		found := false
		for _, v := range file.OAuthClientImports {
			if v.ID == req.ClientImportID && v.ActorID == req.Caller.ID && v.Origin == target.URL && v.ReplacesApplicationID == app.ID {
				app = registry.OAuthApplication{ID: app.ID, OwnerID: app.OwnerID, Origin: v.Origin, ClientID: v.ClientID, ClientSecret: v.ClientSecret, RedirectURI: v.RedirectURI, Scopes: v.Scopes}
				importID, found = v.ID, true
				break
			}
		}
		if !found {
			return nil, ErrConflict
		}
	}
	if err != nil {
		return nil, err
	}
	redirect, err := m.gitlabRedirectLocked()
	if err != nil {
		return nil, err
	}
	if app.ID == "" || app.RedirectURI != redirect || !registry.GitLabLoginScopes(app.Scopes) || req.ApplicationID != "" && req.ApplicationID != app.ID {
		return nil, ErrRequest
	}
	scopes := req.Scopes
	if scopes == nil {
		scopes = app.Scopes
	}
	if !registry.GitLabLoginScopes(scopes) || len(gitlabApplicationMissingScopes(app.Scopes, scopes)) != 0 {
		return nil, ErrRequest
	}
	if action == "logout" && target.CredentialKind != "oauth" {
		return nil, ErrRequest
	}
	records, err := m.gitlabAuthImpactLocked(file, req.ID, app, previousApp.ID != "")
	if target.ID == "" && previousApp.ID == "" && len(req.Affected) > 0 {
		for _, want := range req.Affected {
			for _, c := range file.Platforms {
				if c.ID == want.ID && c.CredentialKind == "oauth" {
					cred, _, e := registry.OAuthRecord(file, c.CredentialID)
					if e != nil {
						return nil, e
					}
					if cred.ApplicationID == app.ID {
						records = append(records, c)
					}
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	affected, refs, err := m.gitlabAuthReferencesLocked(records)
	if err != nil {
		return nil, err
	}
	if len(affected) != len(req.Affected) {
		return nil, ErrConflict
	}
	for _, v := range affected {
		if !slices.ContainsFunc(req.Affected, func(a gitlabAuthPlatform) bool {
			return a.ID == v.ID && a.Version == v.Version && slices.Equal(a.ReferencedRooms, v.ReferencedRooms)
		}) {
			return nil, ErrConflict
		}
	}
	for id, s := range m.gitlabAuthSessions {
		select {
		case <-s.done:
			if time.Now().After(s.ExpiresAt.Add(time.Minute)) {
				delete(m.gitlabAuthSessions, id)
			}
		default:
			if s.app.Origin == app.Origin && ((s.app.ClientID == app.ClientID && (previousApp.ID != "" || s.previousApp.ID != "" || target.CredentialID == "" || s.expectedUserID() == 0 || s.expectedUserID() == target.Validation.UserID)) || slices.ContainsFunc(s.records, func(c registry.Record) bool { return c.ID == req.ID })) {
				return nil, ErrBusy
			}
		}
	}
	if len(m.gitlabAuthSessions) >= 32 {
		return nil, ErrCapacity
	}
	id, err := newOpaqueID()
	if err != nil {
		return nil, err
	}
	state, err := oauthRandom()
	if err != nil {
		return nil, err
	}
	browser, err := oauthRandom()
	if err != nil {
		return nil, err
	}
	verifier, err := oauthRandom()
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(10 * time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	s := &gitlabAuthSession{gitlabAuthView: gitlabAuthView{SessionID: id, PlatformID: req.ID, OnboardingID: req.OnboardingID, Status: "waiting", ExpiresAt: expires, RequestedScopes: slices.Clone(scopes)}, owner: req.Caller.ID, state: state, browser: browser, verifier: verifier, app: app, previousApp: previousApp, importID: importID, targetID: req.ID, name: req.Name, records: records, references: refs, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {app.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {strings.Join(scopes, " ")}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	s.URL = app.Origin + "/oauth/authorize?" + q.Encode()
	if action == "logout" {
		s.Status = "disconnecting"
		s.URL = ""
		s.state = ""
		s.verifier = ""
	}
	if m.gitlabAuthSessions == nil {
		m.gitlabAuthSessions = map[string]*gitlabAuthSession{}
	}
	m.gitlabAuthSessions[id] = s
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		defer m.mu.Unlock()
		if s.Status == "waiting" {
			status := "cancelled"
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				status = "expired"
			}
			m.finishGitLabAuthLocked(s, status, "")
		}
	}()
	return s, nil
}
func (m *roomManager) serveGitLabCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(r.URL.RawQuery) > 8192 || len(q["state"]) != 1 || len(q["code"]) > 1 || len(q["error"]) > 1 || q.Get("code") != "" && q.Get("error") != "" {
		writeAPIError(w, ErrRequest)
		return
	}
	m.mu.Lock()
	var s *gitlabAuthSession
	for _, v := range m.gitlabAuthSessions {
		if v.Status == "waiting" && v.state != "" && v.state == q.Get("state") {
			s = v
			break
		}
	}
	if s == nil || !m.canonicalGitLabRequestLocked(r) {
		m.mu.Unlock()
		writeAPIError(w, ErrIdentity)
		return
	}
	cookie, e := r.Cookie(gitlabCookieName(s.SessionID))
	if e != nil || cookie.Value != s.browser {
		m.mu.Unlock()
		writeAPIError(w, ErrIdentity)
		return
	}
	if err = m.checkGitLabAuthLocked(s); err != nil {
		m.finishGitLabAuthLocked(s, "failed", "source_changed")
		m.mu.Unlock()
		http.Redirect(w, r, gitlabCompletePath, 303)
		return
	}
	s.state = ""
	s.URL = "" // consume before any network work
	if q.Get("error") != "" || !platform.ValidCredential(q.Get("code")) {
		status, code := "cancelled", ""
		switch q.Get("error") {
		case "invalid_scope", "invalid_client":
			status, code = "failed", q.Get("error")
		case "", "access_denied":
		default:
			status, code = "failed", "authorization_failed"
		}
		m.finishGitLabAuthLocked(s, status, code)
		m.mu.Unlock()
		http.Redirect(w, r, gitlabCompletePath, 303)
		return
	}
	s.Status = "exchanging"
	verifier := s.verifier
	s.verifier = ""
	transport := m.platformHTTP
	m.mu.Unlock()
	tokens, identity, exchangeErr := gitlab.ExchangeOAuthCode(s.ctx, gitlab.OAuthLoginRequest{Origin: s.app.Origin, ClientID: s.app.ClientID, ClientSecret: platform.NewSecret(s.app.ClientSecret), RedirectURI: s.app.RedirectURI, Code: platform.NewSecret(q.Get("code")), Verifier: platform.NewSecret(verifier), UserID: s.expectedUserID(), Scopes: strings.Join(s.RequestedScopes, " ")}, transport)
	code := "authorization_failed"
	var missingScopes []string
	// Imported applications need proof of native ownership, not merely a user
	// authorized by somebody else's application. OAuth user-app listing is read only.
	if exchangeErr == nil && s.importID != "" {
		remote, e := gitlab.OwnedApplications(s.ctx, s.app.Origin, tokens.AccessToken, transport)
		ownerApp := s.app
		ownerApp.OwnerID = identity.UserID
		index := slices.IndexFunc(remote, func(a gitlab.UserApplication) bool { return applicationMatches(a, ownerApp) })
		if e != nil || index < 0 {
			exchangeErr = ErrIdentity
		} else if missingScopes = gitlabApplicationMissingScopes(remote[index].Scopes, ownerApp.Scopes); len(missingScopes) > 0 {
			exchangeErr, code = ErrRequest, "invalid_scope"
		}
	}
	m.mu.Lock()
	s.MissingScopes = missingScopes
	var protocolErr *gitlab.OAuthRefreshError
	if errors.As(exchangeErr, &protocolErr) {
		code = protocolErr.Code
	}
	if exchangeErr == nil {
		exchangeErr = m.checkGitLabAuthLocked(s)
		code = "source_changed"
	}
	if exchangeErr == nil {
		s.Status = "settling"
		for _, c := range s.records {
			if exchangeErr = m.invalidatePlatformQueriesLocked(c.ID); exchangeErr != nil {
				break
			}
		}
		if exchangeErr == nil {
			exchangeErr = m.settleGitLabAuthLocked(s)
		}
		if exchangeErr == nil {
			exchangeErr = m.checkGitLabAuthLocked(s)
		}
		code = "switch_unconfirmed"
		if exchangeErr == nil {
			s.Status = "committing"
			app := s.app
			if s.importID != "" {
				app.OwnerID = identity.UserID
			}
			var saved []platform.Platform
			saved, exchangeErr = m.platforms.CommitOAuthLogin(s.records, s.targetID, s.name, app, s.importID, s.owner, tokens, identity)
			if exchangeErr == nil {
				for _, c := range saved {
					v, e := m.publicPlatformLocked(c)
					if e != nil {
						exchangeErr = e
						break
					}
					s.Platforms = append(s.Platforms, v)
				}
			}
		}
	}
	if exchangeErr == nil {
		m.finishGitLabAuthLocked(s, "succeeded", "")
		m.mu.Unlock()
		if s.OnboardingID != "" {
			_, _ = m.applyOnboarding(r.Context(), s.OnboardingID, s.owner)
		}
	} else {
		// A commit-unknown save must never lead to revoking a possibly adopted token.
		s.Cleanup = "not_required"
		if tokens.AccessToken.Reveal() != "" || protocolErr != nil && protocolErr.State == "refresh_unknown" {
			s.Cleanup = "unconfirmed"
		}
		cleanupID, e := m.platforms.KeepOAuthCandidate(s.app, tokens)
		if e == nil && cleanupID != "" {
			s.Cleanup = "pending"
		}
		if s.Status == "settling" || s.Status == "committing" {
			code = "switch_unconfirmed"
		}
		s.Status = "cleaning"
		m.mu.Unlock()
		if e == nil && cleanupID != "" {
			m.cleanupGitLabCandidate(s, cleanupID)
		}
		m.mu.Lock()
		m.finishGitLabAuthLocked(s, "failed", code)
		m.mu.Unlock()
	}
	http.Redirect(w, r, gitlabCompletePath, 303)
}
func (m *roomManager) settleGitLabAuthLocked(s *gitlabAuthSession) error {
	var pending []<-chan struct{}
	for _, c := range s.records {
		pending = append(pending, m.notifications.PlatformDoneLocked(c.ID)...)
		for call := range m.platformCalls {
			if call.platformID == c.ID {
				pending = append(pending, call.done)
			}
		}
	}
	// Refresh belongs to the shared credential, not just the selected registry.
	seen := map[string]bool{}
	for _, c := range s.records {
		if !seen[c.CredentialID] {
			seen[c.CredentialID] = true
			if f := m.oauthFlights[c.CredentialID]; f != nil {
				f.cancel()
				pending = append(pending, f.done)
			}
		}
	}
	return m.settlePlatformCallsLocked(pending)
}
func (m *roomManager) cleanupGitLabCandidate(s *gitlabAuthSession, id string) {
	m.mu.Lock()
	file, err := m.platforms.Load()
	if err == nil {
		for _, c := range file.OAuthCleanup {
			if c.ID == id && c.State == "manual" {
				s.Cleanup = "manual_required"
				m.mu.Unlock()
				return
			}
		}
	}
	if m.closing {
		m.mu.Unlock()
		return
	}
	for _, app := range file.OAuthApplications {
		if app.Origin == s.app.Origin && app.ClientID == s.app.ClientID {
			for _, cred := range file.OAuthCredentials {
				if cred.ApplicationID == app.ID && cred.State != "logged_out" {
					s.Cleanup = "manual_required"
					m.mu.Unlock()
					return
				}
			}
		}
	}
	item, err := m.platforms.BeginOAuthCleanup(id)
	transport := m.platformHTTP
	if err != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	call := &platformCall{tool: "gitlab-revoke", cancel: cancel, done: make(chan struct{})}
	if m.platformCalls == nil {
		m.platformCalls = map[*platformCall]bool{}
	}
	m.platformCalls[call] = true
	m.mu.Unlock()
	err = gitlab.RevokeOAuth(ctx, item.Origin, item.ClientID, platform.NewSecret(item.ClientSecret), platform.NewSecret(item.Token), transport)
	m.mu.Lock()
	defer m.mu.Unlock()
	saved := m.platforms.FinishOAuthCleanup(id, err == nil)
	s.Cleanup = "unconfirmed"
	if err == nil && saved == nil {
		s.Cleanup = "revoked"
	}
	m.finishPlatformCallLocked(call)
}
func (m *roomManager) runGitLabLogout(s *gitlabAuthSession) {
	m.mu.Lock()
	err := m.checkGitLabAuthLocked(s)
	var saved []platform.Platform
	var ids []string
	if err == nil {
		saved, ids, err = m.platforms.LogoutOAuth(s.records[0].CredentialID)
	}
	if err != nil {
		m.finishGitLabAuthLocked(s, "failed", "logout_unconfirmed")
		m.mu.Unlock()
		return
	}
	s.Cleanup = "pending"
	for _, c := range saved {
		v, e := m.publicPlatformLocked(c)
		if e != nil {
			err = e
			break
		}
		s.Platforms = append(s.Platforms, v)
	}
	for _, c := range s.records {
		if e := m.invalidatePlatformQueriesLocked(c.ID); e != nil {
			err = e
		}
	}
	if e := m.settleGitLabAuthLocked(s); e != nil {
		err = e
	}
	if err != nil {
		m.finishGitLabAuthLocked(s, "disconnected", "settlement_unconfirmed")
		m.mu.Unlock()
		return
	}
	// Include a token returned by a refresh that was already in flight at logout.
	file, e := m.platforms.Load()
	if e == nil {
		for _, c := range file.OAuthCleanup {
			if c.Origin == s.app.Origin && c.ClientID == s.app.ClientID && c.State == "pending" && !slices.Contains(ids, c.ID) {
				ids = append(ids, c.ID)
			}
		}
	}
	if e != nil {
		m.finishGitLabAuthLocked(s, "disconnected", "revocation_unconfirmed")
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	allConfirmed := true
	for _, id := range ids {
		m.cleanupGitLabCandidate(s, id)
		m.mu.Lock()
		allConfirmed = allConfirmed && s.Cleanup == "revoked"
		m.mu.Unlock()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s.Platforms = nil
	for _, c := range saved {
		if v, e := m.publicPlatformLocked(c); e == nil {
			s.Platforms = append(s.Platforms, v)
			if v.RevocationStatus != "confirmed" {
				allConfirmed = false
			}
		} else {
			allConfirmed = false
		}
	}
	if !allConfirmed {
		s.Cleanup = "unconfirmed"
	}
	if s.Cleanup != "revoked" {
		m.finishGitLabAuthLocked(s, "disconnected", "revocation_unconfirmed")
	} else {
		m.finishGitLabAuthLocked(s, "logged_out", "")
	}
}

func applicationMatches(remote gitlab.UserApplication, local registry.OAuthApplication) bool {
	return remote.ClientID == local.ClientID && remote.RedirectURI == local.RedirectURI && remote.Confidential
}

func gitlabApplicationMissingScopes(available, requested []string) []string {
	// Some GitLab versions omit application scopes. The token's actual granted
	// scopes are always checked independently during exchange.
	if len(available) == 0 {
		return nil
	}
	var missing []string
	for _, scope := range requested {
		if !slices.Contains(available, scope) {
			missing = append(missing, scope)
		}
	}
	return missing
}
func gitlabAuthReferencesFingerprint(refs []managedPlatformReference) string {
	data, _ := json.Marshal(refs)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
