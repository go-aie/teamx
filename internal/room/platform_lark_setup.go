package room

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
)

// Called with mu held; all returns release it. Setup never accepts a profile
// or an impact acknowledgement: it creates a fresh CLI profile, not a connection.
func (m *roomManager) startLarkSetupLocked(w http.ResponseWriter, req larkAuthRequest) {
	if req.Profile != nil || req.SessionID != "" || len(req.Affected) != 0 || lark.Origin(req.Brand) == "" {
		m.mu.Unlock()
		writeAPIError(w, ErrRequest)
		return
	}
	if req.Brand != "feishu" {
		m.mu.Unlock()
		writeLarkReadError(w, &lark.Error{Code: "setup_brand_unverified"})
		return
	}
	if m.busy {
		m.mu.Unlock()
		writeAPIError(w, ErrBusy)
		return
	}
	for id, s := range m.larkAuthSessions {
		select {
		case <-s.done:
			if time.Now().After(s.expires) {
				delete(m.larkAuthSessions, id)
			}
		default:
			// Init and auth both rewrite the shared CLI config. Do not overlap
			// them, including when the same operator clicks setup twice.
			m.mu.Unlock()
			writeAPIError(w, ErrBusy)
			return
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
	// Each stage has its own ten-minute budget; both share cancellation and
	// ownership. A long app configuration must not consume the OAuth budget.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	s := &larkAuthSession{
		larkAuthView: larkAuthView{SessionID: id, Status: "starting", Stage: "application"},
		owner:        req.Caller.ID, cancel: cancel, done: make(chan struct{}),
		expires: time.Now().Add(21 * time.Minute), setup: true,
	}
	if m.larkAuthSessions == nil {
		m.larkAuthSessions = map[string]*larkAuthSession{}
	}
	m.larkAuthSessions[id] = s
	view := s.larkAuthView
	go m.runLarkSetup(ctx, s, req.Executable, req.Brand)
	m.mu.Unlock()
	_ = json.NewEncoder(w).Encode(view)
}

func (m *roomManager) runLarkSetup(ctx context.Context, s *larkAuthSession, executable, brand string) {
	defer s.cancel()
	result, err := lark.ConfigureApplication(ctx, executable, brand, func(progress lark.SetupProgress) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if ctx.Err() != nil || m.closing {
			return
		}
		s.profile = progress.Profile
		if progress.URL != "" {
			s.Status, s.URL, s.QRCode = "waiting", progress.URL, progress.QRCode
		}
	})
	m.mu.Lock()
	s.URL, s.QRCode = "", ""
	s.ConfigurationFound, s.AppID, s.AppName = result.Found, result.AppID, result.AppName
	if result.Profile.Profile != "" {
		s.profile = result.Profile
		p := result.Profile
		s.Profile = &p
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && m.closing {
		err = context.Canceled
	}
	if err == nil {
		// Normally there are no affected platforms for a freshly allocated
		// profile. Recheck at the handoff; never bypass existing impact guards.
		_, err = m.invalidateLarkAuthLocked(larkAuthRequest{Profile: &s.profile})
		if err != nil {
			err = &lark.Error{Code: "configuration_changed"}
		}
	}
	if err != nil {
		s.Status, s.ErrorCode = "failed", lark.ErrorCode(err)
		if ctx.Err() == context.Canceled || m.closing {
			s.Status, s.ErrorCode = "cancelled", "cancelled"
		} else if lark.ErrorCode(err) == "timeout" {
			s.Status = "expired"
		}
		close(s.done)
		m.mu.Unlock()
		return
	}
	s.Status, s.Stage = "starting", "authorization"
	m.mu.Unlock()
	loginCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	m.runLarkAuth(loginCtx, s, "login")
}
