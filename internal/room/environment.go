package room

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
)

var environmentStreams = make(chan struct{}, 8)

func environmentID(epoch string, id MemberIdentity) string {
	b, _ := json.Marshal(struct {
		Epoch string
		MemberIdentity
	}{epoch, id})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (m *roomManager) environmentMemberCurrent(id MemberIdentity) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, err := m.workspaceRoom(id.RoomIdentity)
	if m.closing || err != nil || entry.Status != "ready" {
		return false
	}
	config, err := m.readWorkspaceRoomConfig(entry)
	if err != nil {
		return false
	}
	for _, member := range config.Members {
		if member.AgentID == id.AgentID && member.MembershipID == id.MembershipID {
			return true
		}
	}
	return false
}

// Recheck existing browser credentials too: logging out or replacing a member
// must close a long-lived viewer, not just deny its next connection.
func environmentAccessCurrent(r *http.Request) bool {
	a, ok := r.Context().Value(accessContextKey{}).(*serverAccess)
	if !ok {
		return true
	} // In-process handlers; production is access-wrapped.
	a.mu.Lock()
	defer a.mu.Unlock()
	if header := r.Header.Get("Authorization"); header != "" {
		token, found := strings.CutPrefix(header, "Bearer ")
		hash := sha256.Sum256([]byte(token))
		return found && !a.disabled && subtle.ConstantTimeCompare(hash[:], a.admin[:]) == 1
	}
	if a.disabled {
		return true
	}
	cookie, err := r.Cookie(a.cookieName())
	if err != nil {
		return false
	}
	s, exists := a.sessions[sha256.Sum256([]byte(cookie.Value))]
	return exists && a.now().Before(s.expires)
}

func environmentError(w http.ResponseWriter, code int) {
	w.Header().Set("Cache-Control", "private, no-store")
	http.Error(w, "环境画面暂不可用，请重新连接。", code)
}

func (m *roomManager) serveEnvironment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && !(r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/control")) {
		w.Header().Set("Allow", "GET")
		environmentError(w, 405)
		return
	}
	if _, execution := r.Context().Value(executionContextKey{}).(executionBinding); execution {
		environmentError(w, 403)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/v1/environments" {
		q := r.URL.Query()
		id := RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}
		if q.Get("service_epoch") != m.epoch || q.Get("caller_id") != requestOperator(r) {
			writeAPIError(w, ErrIdentity)
			return
		}
		type view struct {
			ID        string `json:"id"`
			Label     string `json:"label"`
			ViewerURL string `json:"viewer_url"`
		}
		views := []view{}
		shared, _ := m.computerEnvironments(id)
		for _, item := range shared {
			views = append(views, view{item.ID, item.Label, item.ViewerURL})
		}
		slices.SortFunc(views, func(a, b view) int { return strings.Compare(a.ID, b.ID) })
		_ = json.NewEncoder(w).Encode(struct {
			Environments []view `json:"environments"`
		}{views})
		return
	}
	key, asset, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/environment/"), "/")
	if !ok {
		environmentError(w, 404)
		return
	}
	if id, found := m.computerEnvironmentMember(key); found {
		m.serveComputerEnvironment(w, r, id, asset)
		return
	}
	environmentError(w, 404)
}
