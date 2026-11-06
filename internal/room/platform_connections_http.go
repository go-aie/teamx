package room

import (
	"encoding/json"
	"io"
	"net/http"
)

func (m *roomManager) servePlatformConnections(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/v1/platform/connections", "/v1/platform/connections/save", "/v1/platform/connections/delete", "/v1/platform/connections/verify":
	default:
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/v1/platform/connections" {
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
		file, err := m.readPlatformConnections()
		if err != nil {
			writeAPIError(w, err)
			return
		}
		connections := make([]platformConnection, 0, len(file.Connections))
		for _, c := range file.Connections {
			item, err := m.publicPlatformConnectionLocked(c.Connection)
			if err != nil {
				writeAPIError(w, err)
				return
			}
			connections = append(connections, item)
		}
		_ = json.NewEncoder(w).Encode(struct {
			Connections []platformConnection `json:"connections"`
		}{connections})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req platformConnectionRequest
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
	var result platformConnection
	var err error
	switch r.URL.Path {
	case "/v1/platform/connections/save":
		result, err = m.updatePlatformConnection(req, false)
	default:
		if req.CLIProfile != nil || req.SnapshotVersion != 0 || req.Name != "" || req.Provider != "" || req.URL != "" || req.PAT != "" || req.TokenEnv != "" {
			writeAPIError(w, ErrRequest)
			return
		}
		if r.URL.Path == "/v1/platform/connections/verify" {
			result, err = m.verifyPlatformConnection(r.Context(), req)
		} else {
			result, err = m.updatePlatformConnection(req, true)
		}
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if r.URL.Path == "/v1/platform/connections/delete" {
		_ = json.NewEncoder(w).Encode(struct {
			Deleted bool `json:"deleted"`
		}{true})
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}
