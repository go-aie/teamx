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
	// Admit the binding before reading any member-scoped target evidence.
	m.mu.Lock()
	err := m.checkToolMemberLocked(r.Context(), toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller})
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	m.mu.Unlock()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	id := MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
	action, err := svc.store.agentAction(id, *req.ReferenceAction)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	result, err := m.executeReferenceAction(r.Context(), req, action)
	if err != nil {
		writeAPIError(w, agentActionDiagnostic(err))
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

const actionClientTimeout = 3 * time.Minute
