package room

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

type larkAuthPlatform struct {
	ID              string         `json:"id"`
	Version         uint64         `json:"version"`
	ReferencedRooms []RoomIdentity `json:"referenced_rooms"`
}

type larkAuthRequest struct {
	ServiceEpoch string               `json:"service_epoch"`
	Caller       Caller               `json:"caller"`
	Executable   string               `json:"executable,omitempty"`
	Brand        string               `json:"brand,omitempty"`
	Profile      *platform.CLIProfile `json:"cli_profile,omitempty"`
	SessionID    string               `json:"session_id,omitempty"`
	Affected     []larkAuthPlatform   `json:"affected,omitempty"`
}

type larkAuthView struct {
	SessionID          string                `json:"session_id"`
	Status             string                `json:"status"`
	URL                string                `json:"verification_url,omitempty"`
	QRCode             string                `json:"-"`
	QRCodeURL          string                `json:"qrcode_url,omitempty"`
	Username           string                `json:"username,omitempty"`
	RenewalUnavailable bool                  `json:"renewal_unavailable,omitempty"`
	ExpiresAt          time.Time             `json:"expires_at,omitempty"`
	ErrorCode          string                `json:"error_code,omitempty"`
	Platforms          []managedPlatform     `json:"platforms,omitempty"`
	Stage              string                `json:"stage,omitempty"`
	Profile            *platform.CLIProfile  `json:"cli_profile,omitempty"`
	AppID              string                `json:"app_id,omitempty"`
	AppName            string                `json:"app_name,omitempty"`
	Permissions        *lark.PermissionCheck `json:"permissions,omitempty"`
	ConfigurationFound bool                  `json:"configuration_found,omitempty"`
}

// Sessions are service-local, bounded and owned by the initiating operator.
// Authorization links remain service-local; connection status can expose them
// to the initiating member without persisting temporary authorization material.
type larkAuthSession struct {
	larkAuthView
	owner       string
	profile     platform.CLIProfile
	cancel      context.CancelFunc
	done        chan struct{}
	expires     time.Time
	setup       bool
	operationID string
	validation  platform.Validation
	connectErr  error
	targets     []larkAuthorizationTarget
}

func (m *roomManager) larkAuthBusyLocked(p *platform.CLIProfile) bool {
	if p == nil {
		return false
	}
	for _, s := range m.larkAuthSessions {
		if s.setup || s.profile.Profile == p.Profile {
			select {
			case <-s.done:
			default:
				return true
			}
		}
	}
	return false
}

func (m *roomManager) serveLarkAuth(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, "/v1/platforms/lark/")
	switch action {
	case "discover", "setup", "login", "permissions", "logout", "status", "cancel", "qrcode":
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if action == "qrcode" {
		m.serveLarkAuthQRCode(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req larkAuthRequest
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
	if m.closing {
		m.mu.Unlock()
		writeAPIError(w, ErrClosing)
		return
	}
	if action == "status" || action == "cancel" {
		s := m.larkAuthSessions[req.SessionID]
		if s == nil || s.operationID != "" || s.owner != req.Caller.ID || time.Now().After(s.expires) || req.Profile != nil || req.Executable != "" || req.Brand != "" || len(req.Affected) != 0 {
			m.mu.Unlock()
			writeAPIError(w, ErrConflict)
			return
		}
		if action == "cancel" {
			s.cancel()
			m.mu.Unlock()
			select {
			case <-s.done:
			case <-r.Context().Done():
				writeAPIError(w, ErrBusy)
				return
			case <-time.After(8 * time.Second):
				writeAPIError(w, ErrBusy)
				return
			}
			m.mu.Lock()
		}
		view := m.publicLarkAuthView(s)
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(view)
		return
	}
	if action == "discover" {
		m.mu.Unlock()
		if req.Profile != nil || req.SessionID != "" || len(req.Affected) != 0 {
			writeAPIError(w, ErrRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
		defer cancel()
		result, err := lark.Discover(ctx, req.Executable, req.Brand)
		if err != nil {
			writeLarkReadError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
		return
	}
	if action == "setup" {
		m.startLarkSetupLocked(w, req)
		return
	}
	if req.Profile == nil || !lark.ValidProfile(*req.Profile) || req.SessionID != "" || req.Executable != "" || req.Brand != "" || len(req.Affected) > 128 {
		m.mu.Unlock()
		writeAPIError(w, ErrRequest)
		return
	}
	if m.busy || m.larkAuthBusyLocked(req.Profile) {
		m.mu.Unlock()
		writeAPIError(w, ErrBusy)
		return
	}
	if action == "permissions" {
		if len(req.Affected) != 0 {
			m.mu.Unlock()
			writeAPIError(w, ErrRequest)
			return
		}
		before, loadErr := m.platforms.Load()
		if loadErr != nil {
			m.mu.Unlock()
			writeAPIError(w, loadErr)
			return
		}
		m.mu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		client, err := m.larkClients.New(*req.Profile)
		if err != nil {
			writeLarkReadError(w, err)
			return
		}
		validation, err := client.Verify(ctx)
		if err != nil {
			writeLarkReadError(w, err)
			return
		}
		for _, c := range before.Platforms {
			if c.Provider == "lark" && c.CLIProfile != nil && c.CLIProfile.Profile == req.Profile.Profile && c.Validation.Account.Valid() && c.Validation.Account != validation.Account {
				writeLarkReadError(w, &lark.Error{Code: "identity_mismatch"})
				return
			}
		}
		check, err := m.larkClients.CheckPermissions(ctx, *req.Profile, validation.Account)
		if err != nil {
			writeLarkReadError(w, err)
			return
		}
		m.mu.Lock()
		current, loadErr := m.platforms.Load()
		if loadErr != nil || !sameLarkProfileRevisions(before, current, *req.Profile) || m.larkAuthBusyLocked(req.Profile) {
			m.mu.Unlock()
			writeAPIError(w, ErrConflict)
			return
		}
		m.cacheLarkProfilePermissionsLocked(*req.Profile, check)
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(check)
		return
	}

	m.pruneLarkAuthSessionsLocked()
	if len(m.larkAuthSessions) >= 32 {
		m.mu.Unlock()
		writeAPIError(w, ErrCapacity)
		return
	}
	id, err := newOpaqueID()
	if err != nil {
		m.mu.Unlock()
		writeAPIError(w, err)
		return
	}
	// Reserve before settling old reads, since settlement temporarily releases mu.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	s := &larkAuthSession{larkAuthView: larkAuthView{SessionID: id, Status: "starting"}, owner: req.Caller.ID, profile: *req.Profile, cancel: cancel, done: make(chan struct{}), expires: time.Now().Add(11 * time.Minute)}
	if m.larkAuthSessions == nil {
		m.larkAuthSessions = map[string]*larkAuthSession{}
	}
	m.larkAuthSessions[id] = s
	affected, targets, err := m.invalidateLarkAuthLocked(req)
	s.targets = targets
	if err != nil || m.closing {
		cancel()
		close(s.done)
		delete(m.larkAuthSessions, id)
		m.mu.Unlock()
		if err == nil {
			err = ErrClosing
		}
		writeAPIError(w, err)
		return
	}
	view := s.larkAuthView
	view.Platforms = affected
	go m.runLarkAuth(ctx, s, action)
	m.mu.Unlock()
	_ = json.NewEncoder(w).Encode(view)
}

func (m *roomManager) pruneLarkAuthSessionsLocked() {
	for id, s := range m.larkAuthSessions {
		select {
		case <-s.done:
			if time.Now().After(s.expires) {
				delete(m.larkAuthSessions, id)
			}
		default:
		}
	}
}

func (m *roomManager) publicLarkAuthView(s *larkAuthSession) larkAuthView {
	view := s.larkAuthView
	if s.Status == "waiting" && s.QRCode != "" {
		q := url.Values{"service_epoch": {m.epoch}, "caller_id": {s.owner}, "session_id": {s.SessionID}}
		view.QRCodeURL = "/v1/platforms/lark/qrcode?" + q.Encode()
	}
	return view
}

// Serve the generated image from this origin, so the existing img-src 'self'
// policy remains intact. The opaque flow is checked on every image request.
func (m *roomManager) serveLarkAuthQRCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	m.mu.Lock()
	s := m.larkAuthSessions[r.URL.Query().Get("session_id")]
	if m.closing || r.URL.Query().Get("service_epoch") != m.epoch || s == nil || r.URL.Query().Get("caller_id") != s.owner || s.Status != "waiting" || time.Now().After(s.expires) {
		m.mu.Unlock()
		writeAPIError(w, ErrIdentity)
		return
	}
	if s.operationID != "" {
		op, err := m.readPlatformConnectLocked(s.operationID)
		if err != nil || connectTerminal(op.Status) || op.CancelRequested || m.checkOnboardingTargetLocked(r.Context(), platformOnboardingSpec{MemberIdentity: op.MemberIdentity}) != nil {
			m.mu.Unlock()
			writeAPIError(w, ErrIdentity)
			return
		}
	}
	qr := s.QRCode
	m.mu.Unlock()
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(qr, "data:image/png;base64,"))
	if err != nil || len(data) < 8 || string(data[:8]) != "\x89PNG\r\n\x1a\n" {
		writeAPIError(w, ErrInvalid)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

func (m *roomManager) invalidateLarkAuthLocked(req larkAuthRequest) ([]managedPlatform, []larkAuthorizationTarget, error) {
	file, err := m.readPlatforms()
	if err != nil {
		return nil, nil, err
	}
	var matching []managedPlatformRecord
	for _, c := range file.Platforms {
		// A profile name identifies the shared CLI account even when platforms
		// use different paths to the same CLI executable.
		if c.Provider == "lark" && c.CLIProfile != nil && c.CLIProfile.Profile == req.Profile.Profile {
			matching = append(matching, c)
		}
	}
	if len(matching) != len(req.Affected) {
		return nil, nil, ErrConflict
	}
	for _, c := range matching {
		refs, err := m.managedPlatformReferencesLocked(c.ID)
		if err != nil {
			return nil, nil, err
		}
		found := false
		for _, ack := range req.Affected {
			if ack.ID == c.ID && ack.Version == c.Version && samePlatformReferences(refs, ack.ReferencedRooms) {
				found = true
				break
			}
		}
		if !found {
			return nil, nil, ErrConflict
		}
	}
	// Validate the complete impact snapshot before pausing anything or touching
	// CLI credentials. A failure never starts the external authentication action.
	for _, c := range matching {
		if err := m.invalidatePlatformQueriesLocked(c.ID); err != nil {
			return nil, nil, err
		}
	}
	for _, c := range matching {
		if err := m.settlePlatformQueriesLocked(c.ID); err != nil {
			return nil, nil, err
		}
	}
	// Settlement yields the manager gate. Recheck the acknowledged room impact
	// before native credential changes, alongside registry revision preflight.
	for _, c := range matching {
		refs, err := m.managedPlatformReferencesLocked(c.ID)
		if err != nil {
			return nil, nil, err
		}
		for _, ack := range req.Affected {
			if ack.ID == c.ID && !samePlatformReferences(refs, ack.ReferencedRooms) {
				return nil, nil, ErrConflict
			}
		}
	}
	var result []managedPlatform
	targets := []larkAuthorizationTarget{}
	if len(matching) == 0 {
		return result, targets, nil
	}
	updated, err := m.platforms.InvalidateCLIProfile(matching)
	if err != nil {
		return nil, nil, managedPlatformError(err)
	}
	for _, c := range updated {
		for _, before := range matching {
			if before.ID == c.ID {
				targets = append(targets, larkAuthorizationTarget{ID: c.ID, Version: c.Version, Generation: c.Generation, CredentialVersion: c.CredentialVersion, Profile: *c.CLIProfile, Account: before.Validation.Account})
			}
		}
		delete(m.larkPermissionChecks, c.ID)
		item, err := m.publicPlatformLocked(c.Platform)
		if err != nil {
			return nil, nil, err
		}
		result = append(result, item)
	}
	return result, targets, nil
}

func (m *roomManager) runLarkAuth(ctx context.Context, s *larkAuthSession, action string) {
	defer s.cancel()
	var login lark.LoginResult
	var err error
	if action == "logout" {
		err = lark.Logout(ctx, s.profile)
	} else {
		loginFn := lark.Login
		login, err = loginFn(ctx, s.profile, func(link, qr string) {
			m.mu.Lock()
			defer m.mu.Unlock()
			if ctx.Err() == nil {
				s.Status, s.URL, s.QRCode = "waiting", link, qr
				m.checkpointPlatformConnectLocked(s)
			}
		})
	}
	if err == nil && action != "logout" {
		check, checkErr := m.larkClients.CheckPermissions(ctx, s.profile, login.Validation.Account)
		if checkErr == nil {
			login.Permissions = &check
		} else if lark.ErrorCode(checkErr) == "identity_mismatch" {
			err = checkErr
		}
	}
	if err != nil && action != "logout" {
		// The native split flow may save a token before returning cancellation or
		// a failure. Observe the settled profile; never assume rollback or turn
		// this observation into a successful operation or a member mutation.
		observeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		if client, e := m.larkClients.New(s.profile); e == nil {
			if v, e := client.Verify(observeCtx); e == nil {
				if check, e := m.larkClients.CheckPermissions(observeCtx, s.profile, v.Account); e == nil {
					login.Permissions = &check
				}
			}
		}
		stop()
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	s.URL, s.QRCode = "", ""
	s.Status, s.Username = "succeeded", login.Validation.Username
	s.RenewalUnavailable, s.ExpiresAt = login.RenewalUnavailable, login.ExpiresAt
	s.Permissions = login.Permissions
	// A late success must never revive an explicitly cancelled setup session.
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		s.Status, s.ErrorCode = "failed", lark.ErrorCode(err)
		s.Username = ""
		if ctx.Err() == context.Canceled {
			s.Status = "cancelled"
		}
		if s.setup && lark.ErrorCode(err) == "timeout" {
			s.Status = "expired"
		}
	} else if s.setup {
		s.Stage = "complete"
	}
	s.validation = login.Validation
	if err == nil && action != "logout" && len(s.targets) > 0 {
		if commitErr := m.completeLarkAuthorizationLocked(s); commitErr != nil {
			s.Status, s.ErrorCode = "failed", "identity_or_version_changed"
			if errors.Is(commitErr, ErrIdentity) {
				s.ErrorCode = "identity_mismatch"
			}
			if errors.Is(commitErr, ErrCommitUnknown) {
				s.Status, s.Stage, s.ErrorCode = "unknown", "connection", "connection_unconfirmed"
			}
		}
	}
	for _, target := range s.targets {
		file, loadErr := m.platforms.Load()
		if loadErr != nil {
			break
		}
		for _, c := range file.Platforms {
			if c.ID == target.ID {
				if item, e := m.publicPlatformLocked(c.Platform); e == nil {
					s.Platforms = append(s.Platforms, item)
				}
			}
		}
	}
	close(s.done)
	m.finishPlatformConnectLocked(s)
}
