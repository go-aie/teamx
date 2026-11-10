package room

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
	SessionID          string               `json:"session_id"`
	Status             string               `json:"status"`
	URL                string               `json:"verification_url,omitempty"`
	QRCode             string               `json:"-"`
	QRCodeURL          string               `json:"qrcode_url,omitempty"`
	Username           string               `json:"username,omitempty"`
	RenewalUnavailable bool                 `json:"renewal_unavailable,omitempty"`
	ExpiresAt          time.Time            `json:"expires_at,omitempty"`
	ErrorCode          string               `json:"error_code,omitempty"`
	Platforms          []managedPlatform    `json:"platforms,omitempty"`
	Stage              string               `json:"stage,omitempty"`
	Profile            *platform.CLIProfile `json:"cli_profile,omitempty"`
	AppID              string               `json:"app_id,omitempty"`
	AppName            string               `json:"app_name,omitempty"`
	ConfigurationFound bool                 `json:"configuration_found,omitempty"`
}

// Sessions are service-local, bounded and owned by the initiating operator.
// Nothing here (including links) is persisted or exposed to Agent tools.
type larkAuthSession struct {
	larkAuthView
	owner   string
	profile platform.CLIProfile
	cancel  context.CancelFunc
	done    chan struct{}
	expires time.Time
	setup   bool
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
	case "discover", "setup", "login", "authorize-sending", "authorize-targets", "sending-status", "logout", "status", "cancel", "qrcode":
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
		if s == nil || s.owner != req.Caller.ID || time.Now().After(s.expires) || req.Profile != nil || req.Executable != "" || req.Brand != "" || len(req.Affected) != 0 {
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
	if action == "sending-status" {
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
		pinned, err := m.larkClients.Pin(ctx, *req.Profile, validation.Account)
		if err != nil {
			writeLarkReadError(w, err)
			return
		}
		defer pinned.Close()
		permission, err := pinned.SendPermission(ctx)
		if err != nil {
			writeLarkReadError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(permission)
		return
	}
	for id, s := range m.larkAuthSessions {
		select {
		case <-s.done:
			if time.Now().After(s.expires) {
				delete(m.larkAuthSessions, id)
			}
		default:
		}
	}
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
	affected, err := m.invalidateLarkAuthLocked(req)
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

func (m *roomManager) invalidateLarkAuthLocked(req larkAuthRequest) ([]managedPlatform, error) {
	file, err := m.readPlatforms()
	if err != nil {
		return nil, err
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
		return nil, ErrConflict
	}
	for _, c := range matching {
		refs, err := m.managedPlatformReferencesLocked(c.ID)
		if err != nil {
			return nil, err
		}
		found := false
		for _, ack := range req.Affected {
			if ack.ID == c.ID && ack.Version == c.Version && samePlatformReferences(refs, ack.ReferencedRooms) {
				found = true
				break
			}
		}
		if !found {
			return nil, ErrConflict
		}
	}
	// Validate the complete impact snapshot before pausing anything or touching
	// CLI credentials. A failure never starts the external authentication action.
	for _, c := range matching {
		if err := m.invalidatePlatformQueriesLocked(c.ID); err != nil {
			return nil, err
		}
	}
	for _, c := range matching {
		if err := m.settlePlatformQueriesLocked(c.ID); err != nil {
			return nil, err
		}
	}
	var result []managedPlatform
	for _, c := range matching {
		ticket, err := m.platforms.BeginVerification(c.ID, c.Version)
		if err != nil {
			return nil, managedPlatformError(err)
		}
		updated, err := m.platforms.CompleteVerification(ticket, platform.Validation{Status: "failed", CheckedAt: time.Now().UTC(), ErrorCode: "credential_unavailable"})
		if err != nil {
			return nil, managedPlatformError(err)
		}
		item, err := m.publicPlatformLocked(updated)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (m *roomManager) runLarkAuth(ctx context.Context, s *larkAuthSession, action string) {
	defer s.cancel()
	var login lark.LoginResult
	var err error
	if action == "logout" {
		err = lark.Logout(ctx, s.profile)
	} else {
		loginFn := lark.Login
		if action == "authorize-sending" {
			loginFn = lark.LoginSending
		}
		if action == "authorize-targets" {
			loginFn = lark.LoginSendTargets
		}
		login, err = loginFn(ctx, s.profile, func(link, qr string) {
			m.mu.Lock()
			defer m.mu.Unlock()
			if ctx.Err() == nil {
				s.Status, s.URL, s.QRCode = "waiting", link, qr
			}
		})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s.URL, s.QRCode = "", ""
	s.Status, s.Username = "succeeded", login.Validation.Username
	s.RenewalUnavailable, s.ExpiresAt = login.RenewalUnavailable, login.ExpiresAt
	// A late success must never revive an explicitly cancelled setup session.
	if s.setup && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		s.Status, s.ErrorCode = "failed", lark.ErrorCode(err)
		s.Username = ""
		if ctx.Err() == context.Canceled {
			s.Status = "cancelled"
		}
		if s.setup && ctx.Err() == context.DeadlineExceeded {
			s.Status = "expired"
		}
	} else if s.setup {
		s.Stage = "complete"
	}
	close(s.done)
}
