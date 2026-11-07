package room

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

type actionQueryRecord struct {
	agentActionRecord
	Context *actionBusinessContext `json:"context,omitempty"`
	Detail  *referenceAction       `json:"detail,omitempty"`
}

func actionQueryView(a referenceAction, detail bool) actionQueryRecord {
	v := actionQueryRecord{agentActionRecord: projectActionRecords([]referenceAction{a})[0]}
	if a.Request.UnitRef != "" {
		v.Context = &actionBusinessContext{a.Request.ObjectRef, a.Request.UnitRef, a.Request.Revision}
	}
	if detail {
		v.Detail = &a
	}
	return v
}
func (m *roomManager) serveActions(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/actions/execute" {
		m.serveActionExecute(w, r)
		return
	}
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
	if req.AgentID != "" || req.MembershipID != "" || req.Caller.Kind != "agent" {
		writeAPIError(w, ErrIdentity)
		return
	}
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	err := m.checkToolMemberLocked(r.Context(), toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller})
	m.mu.Unlock()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if svc == nil {
		writeAPIError(w, ErrIdentity)
		return
	}
	if r.URL.Path == "/v1/actions/resolve" {
		if req.ActionResolve == nil {
			writeAPIError(w, ErrRequest)
			return
		}
		view, err := m.resolveActionTarget(r.Context(), req, *req.ActionResolve)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		_ = json.NewEncoder(w).Encode(view)
		return
	}
	if r.URL.Path != "/v1/actions/read" && r.URL.Path != "/v1/actions/list" {
		w.WriteHeader(404)
		return
	}
	if req.Followup == nil {
		writeAPIError(w, ErrRequest)
		return
	}
	id := MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
	q := *req.Followup
	if r.URL.Path == "/v1/actions/read" && !safeMembershipID(q.ID) {
		writeAPIError(w, invalidField("id", "required stable action ID"))
		return
	}
	if err := svc.store.initReferenceFollowups(id); err != nil {
		writeAPIError(w, err)
		return
	}
	actions, err := svc.store.queryActions(id, q.ID, q.ObjectRef, q.UnitRef)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if r.URL.Path == "/v1/actions/read" {
		if len(actions) != 1 {
			writeAPIError(w, ErrRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(actionQueryView(actions[0], true))
		return
	}
	limit := req.Limit
	if limit == 0 {
		limit = 25
	}
	if limit < 1 || limit > 25 {
		writeAPIError(w, ErrRequest)
		return
	}
	start := 0
	key := referenceDigest("", id, q.ObjectRef, q.UnitRef)
	if q.Cursor != "" {
		decoded, e := base64.RawURLEncoding.DecodeString(q.Cursor)
		parts := strings.Split(string(decoded), ":")
		if e != nil || len(parts) != 2 || parts[0] != key {
			writeAPIError(w, ErrRequest)
			return
		}
		start, e = strconv.Atoi(parts[1])
		if e != nil || start < 0 || start > len(actions) {
			writeAPIError(w, ErrRequest)
			return
		}
	}
	items := []actionQueryRecord{}
	size := 0
	end := start
	for end < len(actions) && len(items) < limit {
		v := actionQueryView(actions[end], false)
		b, _ := json.Marshal(v)
		if size+len(b) > followupPageBytes {
			break
		}
		items = append(items, v)
		size += len(b)
		end++
	}
	if end == start && start < len(actions) {
		writeAPIError(w, ErrCapacity)
		return
	}
	next := ""
	if end < len(actions) {
		next = base64.RawURLEncoding.EncodeToString([]byte(key + ":" + strconv.Itoa(end)))
	}
	_ = json.NewEncoder(w).Encode(struct {
		Items    []actionQueryRecord `json:"items"`
		Next     string              `json:"next,omitempty"`
		Complete bool                `json:"complete"`
	}{items, next, next == ""})
}
