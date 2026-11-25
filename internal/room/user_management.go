package room

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/go-aie/teamx/internal/authn"
	"io"
	"maps"
	"net"
	"net/http"
	"strings"
)

// Deployment administration is deliberately separate from browser/business
// authentication. Only the local machine with its private token can enter.
func (a *serverAccess) serveUserManagement(w http.ResponseWriter, r *http.Request) {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	ip := net.ParseIP(peer)
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	digest := tokenDigest(token)
	if err != nil || ip == nil || !ip.IsLoopback() || !ok || r.Header.Get("Origin") != "" || r.Header.Get("X-Forwarded-Proto") != "" || subtle.ConstantTimeCompare(digest[:], a.admin[:]) != 1 || a.spaces == nil {
		accessError(w, 403, "forbidden")
		return
	}
	if r.URL.Path == "/v1/deployment/gitlab-applications" {
		a.serveGitLabApplications(w, r)
		return
	}
	if r.URL.Path == "/v1/deployment/status" {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// Control-plane readiness does not require a logged-in business owner
		// or a configured model. Never inspect a user's rooms with admin auth.
		a.users.mu.Lock()
		requiresRestart := a.users.failed
		a.users.mu.Unlock()
		a.spaces.mu.Lock()
		closing := a.spaces.closing
		a.spaces.mu.Unlock()
		if requiresRestart || closing {
			accessError(w, http.StatusServiceUnavailable, "deployment_restart_required")
			return
		}
		_ = writeJSON(w, map[string]any{"status": "running", "ready": true,
			"installation_id": a.installationID, "deployment_mode": "multi-user"})
		return
	}
	if r.URL.Path != "/v1/deployment/users" {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		a.users.mu.Lock()
		type userView struct {
			User
			Identity       authn.Identity `json:"external_identity"`
			DefaultSpaceID string         `json:"default_space_id"`
		}
		entries := make([]userView, 0, len(a.users.saved.Users))
		for _, entry := range a.users.saved.Users {
			entries = append(entries, userView{entry.User, entry.Identity, entry.DefaultSpace.ID})
		}
		requiresRestart := a.users.failed
		a.users.mu.Unlock()
		_ = writeJSON(w, map[string]any{"users": entries, "requires_restart": requiresRestart})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var input struct {
		UserID string `json:"user_id"`
		Status string `json:"status"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	a.spaces.adminMu.Lock()
	defer a.spaces.adminMu.Unlock()
	if input.Status == "active" && !a.spaces.userSettled(input.UserID) {
		writeAPIError(w, ErrBusy)
		return
	}
	entry, err := a.users.setStatus(input.UserID, input.Status)
	if err != nil {
		if errors.Is(err, ErrCommitUnknown) {
			a.spaces.fenceUser(input.UserID)
		}
		writeAPIError(w, err)
		return
	}
	if input.Status == "disabled" {
		a.spaces.fenceUser(entry.ID)
		if err := a.spaces.settleUser(r.Context(), entry.ID); err != nil {
			writeAPIError(w, err)
			return
		}
	}
	_ = writeJSON(w, entry.User)
}

func (s *spaceServer) fenceUser(id string) {
	a := s.access
	a.mu.Lock()
	next := maps.Clone(a.sessions)
	for key, session := range next {
		if session.Subject.UserID == id {
			delete(next, key)
		}
	}
	// Durable User status/auth version is already the authority even if this
	// session pruning fails. In-memory streams are revoked immediately.
	_ = a.replaceSessions(next)
	for lease := range a.updateLeases {
		if lease.operator == id {
			delete(a.updateLeases, lease)
			close(lease.done)
		}
	}
	a.mu.Unlock()
	s.mu.Lock()
	for _, record := range s.directory.Spaces {
		if record.OwnerUserID == id {
			if entry := s.entries[record.ID]; entry != nil {
				entry.revoke()
			}
		}
	}
	s.mu.Unlock()
}

func (s *spaceServer) settleUser(ctx context.Context, id string) error {
	s.mu.Lock()
	records := append([]SpaceRecord{}, s.directory.Spaces...)
	s.mu.Unlock()
	var result error
	for _, record := range records {
		if record.OwnerUserID != id {
			continue
		}
		if record.Status != "suspended" {
			_, err := s.changeSpaceStatus(record.ID, "archive", record.Version)
			result = errors.Join(result, err)
		}
		s.mu.Lock()
		entry := s.entries[record.ID]
		s.mu.Unlock()
		if entry != nil {
			result = errors.Join(result, s.settleSpace(ctx, entry))
		}
		if result == nil {
			s.mu.Lock()
			for i, current := range s.directory.Spaces {
				if current.ID == record.ID && current.Status == "archiving" {
					current.Status = "suspended"
					current.Version++
					result = errors.Join(result, s.saveSpaceLocked(i, current))
					break
				}
			}
			s.mu.Unlock()
		}
	}
	return result
}

func (s *spaceServer) userSettled(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, exists := s.access.users.user(id)
	if !exists {
		return false
	}
	if user.Status == "active" {
		return true
	}
	for _, record := range s.directory.Spaces {
		if record.OwnerUserID != id {
			continue
		}
		if record.Status != "suspended" {
			return false
		}
		if entry := s.entries[record.ID]; entry != nil {
			if entry.lifecycleBusy {
				return false
			}
			select {
			case <-entry.done:
			default:
				return false
			}
			entry.mu.RLock()
			runtime := entry.runtime
			entry.mu.RUnlock()
			if runtime != nil && runtime.State() != "stopped" {
				return false
			}
		}
	}
	return true
}
