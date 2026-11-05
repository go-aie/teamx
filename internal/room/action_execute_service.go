package room

import (
	"encoding/json"
	"net/http"
	"time"
)

func (m *roomManager) serveActionExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Mode") != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	var req apiRequest
	if !decodeToolRequest(w, r, &req) {
		return
	}
	if req.ReferenceAction == nil || req.AgentID != "" || req.MembershipID != "" {
		writeAPIError(w, ErrRequest)
		return
	}
	result, err := m.executeReferenceAction(r.Context(), req, *req.ReferenceAction)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

const actionClientTimeout = 3 * time.Minute
