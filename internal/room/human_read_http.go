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
	Caller             Caller    `json:"caller"`
	SnapshotSeq        *uint64   `json:"snapshot_seq,omitempty"`
	OpenedThrough      *uint64   `json:"opened_through,omitempty"`
	MentionSeenSeqs    *[]uint64 `json:"mention_seen_seqs,omitempty"`
	IncludeNewMessages bool      `json:"include_new_messages,omitempty"`
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
	if confirm && (req.SnapshotSeq == nil || (req.OpenedThrough == nil && req.MentionSeenSeqs == nil) ||
		(req.MentionSeenSeqs != nil && len(*req.MentionSeenSeqs) > 100) || req.IncludeNewMessages) ||
		!confirm && (req.SnapshotSeq != nil || req.OpenedThrough != nil || req.MentionSeenSeqs != nil) {
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
	delta := humanReadDelta{SnapshotSeq: req.SnapshotSeq, OpenedThrough: req.OpenedThrough, IncludeNewMessages: req.IncludeNewMessages}
	if req.MentionSeenSeqs != nil {
		delta.MentionSeenSeqs = *req.MentionSeenSeqs
	}
	state, err := s.humanRead.update(s.store, delta, humanOperator())
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
