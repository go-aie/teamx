package room

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"time"
)

type roomAttachmentStateRequest struct {
	ServiceInfo
	Caller Caller   `json:"caller"`
	IDs    []string `json:"ids"`
}
type roomAttachmentState struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	Attachment *RoomAttachment `json:"attachment,omitempty"`
	ExpiresAt  *time.Time      `json:"expires_at,omitempty"`
}

func (s *service) attachmentHTTPIdentity(r *http.Request, info ServiceInfo, caller Caller) bool {
	return info == s.info && caller.Kind == "human" && caller.MembershipID == "" && validText(caller.ID) && caller.ID == requestOperator(r) && s.humanRead != nil && caller.ID == s.humanRead.operator
}

// Validate the complete multipart shape before upload publication can succeed.
type singleImagePart struct {
	part   *multipart.Part
	reader *multipart.Reader
}

func (p *singleImagePart) Read(b []byte) (int, error) {
	n, err := p.part.Read(b)
	if err == io.EOF {
		if _, next := p.reader.NextPart(); next != io.EOF {
			return n, attachmentError("upload requires exactly one file field")
		}
	}
	return n, err
}
func (s *service) serveRoomAttachments(w http.ResponseWriter, r *http.Request) {
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
	var info ServiceInfo
	var caller Caller
	var id string
	var seq uint64
	if r.URL.Path == "/v1/attachments/state" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req roomAttachmentStateRequest
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil {
			writeAPIError(w, ErrRequest)
			return
		}
		if err = decodeRequestJSON(string(b), &req); err != nil {
			writeAPIError(w, err)
			return
		}
		if !s.attachmentHTTPIdentity(r, req.ServiceInfo, req.Caller) {
			writeAPIError(w, ErrIdentity)
			return
		}
		if len(req.IDs) > roomAttachmentCount {
			writeAPIError(w, ErrRequest)
			return
		}
		states := []roomAttachmentState{}
		for _, id := range req.IDs {
			if !validRoomAttachmentID(id) {
				writeAPIError(w, ErrRequest)
				return
			}
			state, err := s.attachmentState(req.Caller.ID, id)
			if err != nil {
				writeAPIError(w, err)
				return
			}
			states = append(states, state)
		}
		_ = json.NewEncoder(w).Encode(struct {
			Attachments []roomAttachmentState `json:"attachments"`
		}{states})
		return
	}
	q := r.URL.Query()
	for _, key := range []string{"room_id", "room_instance", "service_epoch", "caller_id", "attachment_id"} {
		if len(q[key]) != 1 {
			writeAPIError(w, ErrRequest)
			return
		}
	}
	info = ServiceInfo{RoomIdentity: RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}, ServiceEpoch: q.Get("service_epoch")}
	caller = Caller{Author: Author{"human", q.Get("caller_id")}}
	id = q.Get("attachment_id")
	if !s.attachmentHTTPIdentity(r, info, caller) {
		writeAPIError(w, ErrIdentity)
		return
	}
	if !validRoomAttachmentID(id) {
		writeAPIError(w, ErrRequest)
		return
	}
	switch r.URL.Path {
	case "/v1/attachments/upload":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		limits := attachmentLimits(s.imageMaterials)
		s.store.mu.Lock()
		old, lookupErr := s.store.attachmentMeta(id)
		if lookupErr == nil && old.Operator == caller.ID {
			limits.ImageBytes = roomAttachmentBytes
		}
		s.store.mu.Unlock()
		if r.ContentLength > limits.ImageBytes+(64<<10) {
			writeAPIError(w, attachmentError("upload body exceeds limit"))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limits.ImageBytes+(64<<10))
		reader, err := r.MultipartReader()
		if err != nil {
			writeAPIError(w, ErrRequest)
			return
		}
		part, err := reader.NextPart()
		if err != nil {
			writeAPIError(w, ErrRequest)
			return
		}
		defer part.Close()
		if part.FormName() != "file" || part.FileName() == "" || len(part.Header) > 4 {
			writeAPIError(w, ErrRequest)
			return
		}
		_, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		if err != nil || len(params["filename"]) > 4096 {
			writeAPIError(w, ErrRequest)
			return
		}
		m, err := s.store.uploadAttachment(r.Context(), caller.ID, id, params["filename"], &singleImagePart{part, reader})
		if err != nil {
			writeAPIError(w, err)
			return
		}
		expires := m.CreatedAt.Add(roomAttachmentTTL)
		_ = json.NewEncoder(w).Encode(struct {
			Attachment RoomAttachment `json:"attachment"`
			ExpiresAt  time.Time      `json:"expires_at"`
		}{m.Attachment, expires})
	case "/v1/attachments/content":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if q.Has("seq") {
			var err error
			seq, err = strconv.ParseUint(q.Get("seq"), 10, 64)
			if err != nil || seq == 0 || len(q["seq"]) != 1 {
				writeAPIError(w, ErrRequest)
				return
			}
		}
		s.store.attachmentLife.RLock()
		defer s.store.attachmentLife.RUnlock()
		m, release, err := s.store.pinAttachment(caller.ID, id, seq)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		defer release()
		b, _, err := s.store.readAttachment(m)
		if err != nil {
			writeAPIError(w, attachmentError("image is unavailable"))
			return
		}
		// Clear can commit during the read; recheck native visibility before delivery.
		current, releaseCurrent, err := s.store.pinAttachment(caller.ID, id, seq)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		defer releaseCurrent()
		if current != m || r.Context().Err() != nil {
			writeAPIError(w, ErrIdentity)
			return
		}
		w.Header().Set("Content-Type", m.Attachment.MIMEType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": m.Attachment.Name}))
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}
func (s *service) attachmentState(operator, id string) (roomAttachmentState, error) {
	result := roomAttachmentState{ID: id, Status: "unavailable"}
	s.store.attachmentLife.RLock()
	defer s.store.attachmentLife.RUnlock()
	s.store.mu.Lock()
	m, err := s.store.attachmentMeta(id)
	if errors.Is(err, os.ErrNotExist) {
		result.Status = "missing"
		s.store.mu.Unlock()
		return result, nil
	}
	if err != nil {
		s.store.mu.Unlock()
		return result, nil
	}
	if m.Operator != operator {
		s.store.mu.Unlock()
		return result, ErrIdentity
	}
	v, err := s.store.loadRoom()
	if err == nil && !s.store.attachmentAvailable(m, attachmentReferences(v)) {
		result.Status = "expired"
		if s.store.attachmentStorage().unconfirmed[id] {
			result.Status = "unavailable"
		}
		s.store.mu.Unlock()
		return result, nil
	}
	s.store.mu.Unlock()
	if err != nil {
		return result, err
	}
	m, release, err := s.store.pinAttachment(operator, id, 0)
	if err != nil {
		return result, nil
	}
	defer release()
	if _, _, err = s.store.readAttachment(m); err != nil {
		return result, nil
	}
	result.Status = "available"
	result.Attachment = &m.Attachment
	expires := m.CreatedAt.Add(roomAttachmentTTL)
	result.ExpiresAt = &expires
	return result, nil
}
