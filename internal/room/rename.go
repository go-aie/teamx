package room

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type roomRenameRequest struct {
	ServiceEpoch string `json:"service_epoch"`
	Caller       Caller `json:"caller"`
	RoomIdentity
	Name         string `json:"name"`
	ExpectedName string `json:"expected_name"`
}

func (m *roomManager) serveRoomRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req roomRenameRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, ErrRequest)
		return
	}
	if dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	if !m.mu.TryLock() {
		writeAPIError(w, ErrBusy)
		return
	}
	defer m.mu.Unlock()
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	if m.closing {
		writeAPIError(w, ErrClosing)
		return
	}
	if err := m.store.check(true); err != nil {
		writeAPIError(w, err)
		return
	}
	if m.roomBusyLocked(req.RoomIdentity) {
		writeAPIError(w, ErrBusy)
		return
	}
	name := strings.TrimSpace(req.Name)
	if !validText(name) || len(name) > 256 || !validText(req.ExpectedName) || len(req.ExpectedName) > 256 {
		writeAPIError(w, ErrRequest)
		return
	}
	next := m.copyIndex()
	for i := range next.Rooms {
		e := &next.Rooms[i]
		if e.RoomIdentity != req.RoomIdentity {
			continue
		}
		if e.Status != "ready" {
			writeAPIError(w, ErrBusy)
			return
		}
		if e.Name != req.ExpectedName {
			writeAPIError(w, ErrConflict)
			return
		}
		if e.Name != name {
			e.Name, e.UpdatedAt = name, time.Now().UTC()
			if err := m.saveIndex(next); err != nil {
				writeAPIError(w, err)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(e.roomSummary)
		return
	}
	writeAPIError(w, ErrIdentity)
}
