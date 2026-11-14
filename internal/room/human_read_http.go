package room

import (
	"encoding/json"
	"io"
	"net/http"
)

// Separate from Agent read/confirm and apiRequest. In particular execution
// credentials, agent progress and whole-state replacement are not accepted.
type humanReadRequest struct {
	ServiceInfo
	Caller      Caller    `json:"caller"`
	SnapshotSeq *uint64   `json:"snapshot_seq,omitempty"`
	SeenSeqs    *[]uint64 `json:"seen_seqs,omitempty"`
}

func (s *service) serveHumanRead(w http.ResponseWriter, r *http.Request) {
	var req humanReadRequest
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		writeAPIError(w, invalidField("", "request body exceeds limit or could not be read"))
		return
	}
	if err := decodeRequestJSON(string(data), &req); err != nil {
		writeAPIError(w, err)
		return
	}
	if req.ServiceInfo != s.info || req.Caller.Kind != "human" || req.Caller.MembershipID != "" ||
		s.humanRead == nil || req.Caller.ID != s.humanRead.operator || req.Caller.ID != requestOperator(r) {
		writeAPIError(w, ErrIdentity)
		return
	}
	confirm := r.URL.Path == "/v1/human-read/confirm"
	if confirm && (req.SnapshotSeq == nil || req.SeenSeqs == nil || len(*req.SeenSeqs) > 100) ||
		!confirm && (req.SnapshotSeq != nil || req.SeenSeqs != nil) {
		writeAPIError(w, ErrRequest)
		return
	}
	s.changes.RLock()
	defer s.changes.RUnlock()
	if s.closing.Load() {
		writeAPIError(w, ErrClosing)
		return
	}
	if s.restarting.Load() {
		writeAPIError(w, ErrBusy)
		return
	}
	var seen []uint64
	if confirm {
		seen = *req.SeenSeqs
	}
	state, err := s.humanRead.update(s.store, req.SnapshotSeq, seen, humanOperator())
	if err == errHumanReadUnavailable {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(apiError{Error: err.Error(), Message: err.Error(), Code: "human_read_unavailable"})
		return
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(state)
}
