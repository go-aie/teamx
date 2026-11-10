package room

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

func (m *roomManager) servePlatforms(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/platforms/onboarding/") {
		m.servePlatformOnboarding(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/platforms/gitlab/") {
		if r.URL.Path == gitlabCookiePath+"prepare" || r.URL.Path == gitlabCookiePath+"initialize" {
			m.serveGitLabSetup(w, r)
		} else {
			m.serveGitLabAuth(w, r)
		}
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/platforms/lark/") {
		m.serveLarkAuth(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/platforms/scion/") {
		m.serveScionAuth(w, r)
		return
	}

	switch r.URL.Path {
	case "/v1/platforms", "/v1/platforms/save", "/v1/platforms/delete", "/v1/platforms/verify":
	default:
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/v1/platforms" {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closing {
			writeAPIError(w, ErrClosing)
			return
		}
		if r.URL.Query().Get("service_epoch") != m.epoch || !validText(r.URL.Query().Get("caller_id")) {
			writeAPIError(w, ErrIdentity)
			return
		}
		file, err := m.readPlatforms()
		if err != nil {
			writeAPIError(w, err)
			return
		}
		platforms := make([]managedPlatform, 0, len(file.Platforms))
		for _, c := range file.Platforms {
			item, err := m.publicPlatformLocked(c.Platform)
			if err != nil {
				writeAPIError(w, err)
				return
			}
			platforms = append(platforms, item)
		}
		_ = json.NewEncoder(w).Encode(struct {
			Platforms []managedPlatform `json:"platforms"`
		}{platforms})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req managedPlatformRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	if req.OnboardingID != "" {
		m.mu.Lock()
		op, e := m.readOnboardingLocked(req.OnboardingID, req.Caller.ID)
		m.mu.Unlock()
		if e != nil {
			writeAPIError(w, e)
			return
		}
		if op.Cancelled || op.PlatformID != req.ID || r.URL.Path == "/v1/platforms/delete" || r.URL.Path == "/v1/platforms/save" && (op.Input.Provider != req.Provider || op.Input.Origin != req.URL) {
			writeAPIError(w, ErrConflict)
			return
		}
	}
	var result managedPlatform
	var err error
	switch r.URL.Path {
	case "/v1/platforms/save":
		result, err = m.updatePlatform(req, false)
	default:
		if req.CLIProfile != nil || req.Name != "" || req.Provider != "" || req.URL != "" {
			writeAPIError(w, ErrRequest)
			return
		}
		if r.URL.Path == "/v1/platforms/verify" {
			result, err = m.verifyPlatform(r.Context(), req)
		} else {
			result, err = m.updatePlatform(req, true)
		}
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if r.URL.Path == "/v1/platforms/delete" {
		_ = json.NewEncoder(w).Encode(struct {
			Deleted bool `json:"deleted"`
		}{true})
		return
	}
	if req.OnboardingID != "" {
		op, e := m.applyOnboarding(r.Context(), req.OnboardingID, req.Caller.ID)
		if e != nil {
			writeAPIError(w, e)
			return
		}
		m.mu.Lock()
		// Verification may advance the platform version after its profile was saved.
		refreshed, e := m.publicPlatformLocked(result.Platform)
		m.mu.Unlock()
		if e != nil {
			writeAPIError(w, e)
			return
		}
		result = refreshed
		result.Onboarding = &op
	}
	_ = json.NewEncoder(w).Encode(result)
}
