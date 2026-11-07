package room

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/scion"
	"github.com/go-aie/teamx/internal/platform/tool"
)

type scionPAT string

func (scionPAT) MarshalJSON() ([]byte, error) { return json.Marshal("[REDACTED]") }
func (scionPAT) Format(s fmt.State, _ rune)   { _, _ = io.WriteString(s, "[REDACTED]") }

type scionAuthRequest struct {
	ServiceEpoch string   `json:"service_epoch"`
	Caller       Caller   `json:"caller"`
	Executable   string   `json:"executable,omitempty"`
	URL          string   `json:"url,omitempty"`
	PAT          scionPAT `json:"pat,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
}

func (scionAuthRequest) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[REDACTED Scion authentication request]")
}

type scionAuthView struct {
	SessionID string               `json:"session_id"`
	Status    string               `json:"status"`
	Profile   *platform.CLIProfile `json:"cli_profile,omitempty"`
	Username  string               `json:"username,omitempty"`
	ErrorCode string               `json:"error_code,omitempty"`
}
type scionAuthSession struct {
	scionAuthView
	owner   string
	cancel  context.CancelFunc
	done    chan struct{}
	expires time.Time
}

func (m *roomManager) serveScionAuth(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, "/v1/platform/connections/scion/")
	if action != "discover" && action != "login" && action != "status" && action != "cancel" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req scionAuthRequest
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
	if err := m.store.check(false); err != nil {
		m.mu.Unlock()
		writeAPIError(w, err)
		return
	}
	if action == "status" || action == "cancel" {
		session := m.scionAuthSessions[req.SessionID]
		if session == nil || session.owner != req.Caller.ID || time.Now().After(session.expires) || req.PAT != "" || req.Executable != "" || req.URL != "" {
			m.mu.Unlock()
			writeAPIError(w, ErrConflict)
			return
		}
		if action == "cancel" {
			session.cancel()
			m.mu.Unlock()
			select {
			case <-session.done:
			case <-r.Context().Done():
				writeAPIError(w, ErrBusy)
				return
			case <-time.After(8 * time.Second):
				writeAPIError(w, ErrBusy)
				return
			}
			m.mu.Lock()
		}
		view := session.scionAuthView
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(view)
		return
	}
	_, origin, err := connection.Normalize("Scion", req.URL)
	if err != nil || action == "discover" && (req.PAT != "" || req.SessionID != "") || action == "login" && (!tool.OpaqueID(req.SessionID) || !platform.ValidCredential(string(req.PAT))) {
		m.mu.Unlock()
		writeAPIError(w, ErrRequest)
		return
	}
	if m.busy {
		m.mu.Unlock()
		writeAPIError(w, ErrBusy)
		return
	}
	if m.scionAuthSessions == nil {
		m.scionAuthSessions = map[string]*scionAuthSession{}
	}
	for id, session := range m.scionAuthSessions {
		select {
		case <-session.done:
			if time.Now().After(session.expires) {
				delete(m.scionAuthSessions, id)
			}
		default:
		}
	}
	if len(m.scionAuthSessions) >= 32 {
		m.mu.Unlock()
		writeAPIError(w, ErrCapacity)
		return
	}
	id := req.SessionID
	if id == "" {
		id, err = newOpaqueID()
		if err != nil {
			m.mu.Unlock()
			writeAPIError(w, err)
			return
		}
	}
	// Repeated IDs never start another CLI login. A lost HTTP response
	// is recovered through status with this client-generated ID, without the PAT.
	if old := m.scionAuthSessions[id]; old != nil {
		m.mu.Unlock()
		writeAPIError(w, ErrConflict)
		return
	}
	parent := context.Background()
	if action == "discover" {
		parent = r.Context()
	}
	ctx, cancel := context.WithTimeout(parent, 65*time.Second)
	session := &scionAuthSession{scionAuthView: scionAuthView{SessionID: id, Status: "starting"}, owner: req.Caller.ID, cancel: cancel, done: make(chan struct{}), expires: time.Now().Add(10 * time.Minute)}
	m.scionAuthSessions[id] = session
	root := m.store.dir
	if action == "discover" {
		m.mu.Unlock()
		result, err := scion.Discover(ctx, root, origin, req.Executable)
		cancel()
		m.mu.Lock()
		close(session.done)
		delete(m.scionAuthSessions, id)
		m.mu.Unlock()
		if err != nil {
			writeScionAuthError(w, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(result)
		return
	}
	profileID, err := newOpaqueID()
	if err != nil {
		cancel()
		close(session.done)
		delete(m.scionAuthSessions, id)
		m.mu.Unlock()
		writeAPIError(w, err)
		return
	}
	pat := platform.NewSecret(string(req.PAT))
	req.PAT = ""
	go func() {
		defer cancel()
		profile, validation, err := scion.Login(ctx, root, origin, req.Executable, profileID, pat)
		pat = platform.Secret{}
		m.mu.Lock()
		defer m.mu.Unlock()
		if err == nil {
			session.Status, session.Profile, session.Username = "succeeded", &profile, validation.Username
		} else {
			session.Status, session.ErrorCode = "failed", err.Error()
			if ctx.Err() == context.Canceled {
				session.Status = "cancelled"
			} else if ctx.Err() == context.DeadlineExceeded {
				session.ErrorCode = "timeout"
			}
		}
		close(session.done)
	}()
	view := session.scionAuthView
	m.mu.Unlock()
	_ = json.NewEncoder(w).Encode(view)
}

func writeScionAuthError(w http.ResponseWriter, code string) {
	switch code {
	case "cli_unavailable", "cli_version_mismatch", "credential_unavailable", "capacity", "timeout":
	default:
		code = "query_failed"
	}
	w.WriteHeader(http.StatusBadGateway)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "Scion account detection failed", "code": code, "message": "Scion account detection failed"})
}
