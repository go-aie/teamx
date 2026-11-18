package inputstream

import "encoding/json"

// CallBinding belongs to the existing coordinator. It maps framework calls to
// formal pages; it owns neither a cursor nor business action receipts.
type CallBinding struct {
	Stream  string      `json:"stream"`
	Limit   int         `json:"limit"`
	Request ReadRequest `json:"request"`
}

// BindCall uses a framework identity, never an HTTP request ID or argument hash.
// An unreceived result must be replayed before another page can replace it.
func (r *Repository) BindCall(e Execution, id, stream string, limit int) (ReadRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return ReadRequest{}, err
	}
	s := r.stateCopy()
	if s.Closed {
		return ReadRequest{}, ErrIdentity
	}
	if !validKey(id) || len(id) > 128 || !validKey(stream) || len(stream) > 128 || limit < 0 || limit > 10000 {
		return ReadRequest{}, ErrInvalid
	}
	if b, ok := s.Calls[id]; ok {
		if b.Stream != stream || b.Limit != limit {
			return ReadRequest{}, ErrConflict
		}
		return b.Request, nil
	}
	if len(s.Calls) >= MaxReadSessions {
		return ReadRequest{}, ErrCapacity
	}
	q := ReadRequest{Turn: e.Turn, Stream: stream, ID: id, Limit: limit}
	for _, a := range s.Attempts {
		if a.Phase == "failed_before_output" {
			continue
		}
		if a.Phase != "settled" || a.ReceiptRequired && !a.Received {
			if a.Stream != stream {
				return ReadRequest{}, ErrBusy
			}
			q = a.Request // Even a changed limit cannot replace a failed page.
			break
		}
	}
	if s.Calls == nil {
		s.Calls = map[string]CallBinding{}
	}
	s.Calls[id] = CallBinding{stream, limit, q}
	return q, r.saveCoordinator(s)
}

// Receive is called only after the framework has persisted the complete tool
// result. A lost acknowledgment is harmless and may be repeated indefinitely.
func (r *Repository) Receive(e Execution, call string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return err
	}
	s := r.stateCopy()
	b, ok := s.Calls[call]
	if !ok {
		return ErrExpired
	}
	for i, a := range s.Attempts {
		if a.Request == b.Request {
			if a.Phase != "settled" {
				return ErrBusy
			}
			if a.Received {
				return nil
			}
			s.Attempts[i].Received = true
			return r.saveCoordinator(s)
		}
	}
	return ErrExpired
}

func (r *Repository) LookupRead(e Execution, q ReadRequest) (*ReadAttempt, *ReadAttempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return nil, nil, err
	}
	b, _ := json.Marshal(q)
	if _, err := DecodeRead(b); err != nil {
		return nil, nil, err
	}
	if q.Turn != e.Turn {
		return nil, nil, ErrIdentity
	}
	s := r.stateCopy()
	for _, a := range s.Attempts {
		if a.Key() == q.ID && q.Cursor == "" || q.Cursor != "" && a.Key() == q.Cursor {
			if a.Request != q {
				return nil, nil, ErrConflict
			}
			return &a, nil, nil
		}
	}
	if s.Closed {
		return nil, nil, ErrIdentity
	}
	if activeAttempt(s) != nil {
		return nil, nil, ErrBusy
	}
	if q.Cursor != "" {
		for _, a := range s.Attempts {
			if a.Next == q.Cursor && a.Phase == "settled" {
				return nil, &a, nil
			}
		}
		return nil, nil, ErrExpired
	}
	return nil, nil, nil
}
