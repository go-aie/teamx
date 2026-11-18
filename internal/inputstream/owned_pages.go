package inputstream

// OwnedPage is supplied only by an authoritative domain adapter. The
// coordinator owns delivery, never that adapter's cursor or publication root.
type OwnedPage struct {
	Snapshot      Snapshot
	From, Through uint64
	Entries       []Location
	Remaining     int
	HasMore       bool
}

func (r *Repository) ReserveOwned(e Execution, q ReadRequest, stream string, p OwnedPage) (ReadAttempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return ReadAttempt{}, err
	}
	s := r.stateCopy()
	if s.Closed || q.Turn != e.Turn {
		return ReadAttempt{}, ErrIdentity
	}
	key := q.ID
	if q.Cursor != "" {
		key = q.Cursor
	}
	for _, a := range s.Attempts {
		if a.Key() == key {
			if a.Request != q {
				return a, ErrConflict
			}
			return a, nil
		}
	}
	if activeAttempt(s) != nil {
		return ReadAttempt{}, ErrBusy
	}
	if len(s.Attempts) >= MaxReadSessions {
		return ReadAttempt{}, ErrCapacity
	}
	if stream == "" || p.Snapshot.Partition == "" || p.From > p.Through || p.Through > p.Snapshot.Through || len(p.Entries) > 100 || p.Remaining < 0 {
		return ReadAttempt{}, ErrInvalid
	}
	a := ReadAttempt{Kind: "room", ID: Ref("page_", r.member, e, key), Execution: e, Request: q, Stream: stream, Snapshot: p.Snapshot, From: p.From, Through: p.Through, Entries: p.Entries, Remaining: p.Remaining, HasMore: p.HasMore, Phase: "reserved", Limit: q.Limit}
	if a.HasMore && a.Remaining > 0 {
		a.Next = Ref("read_cursor_", a.ID, a.Through)
	}
	s.Attempts = append(s.Attempts, a)
	if err := r.saveCoordinator(s); err != nil {
		return a, err
	}
	return a, r.stop("page_reserved")
}

// SettleOwned accepts only a same-page native durable proof checked by the
// domain service. There is no compensating write of the domain cursor here.
func (r *Repository) SettleOwned(e Execution, id, proof string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, i, err := r.attempt(e, id)
	if err != nil {
		return err
	}
	a := &s.Attempts[i]
	if a.Kind != "room" || proof != a.Proof() {
		return ErrIdentity
	}
	if a.Phase == "settled" {
		return nil
	}
	if a.Phase != "awaiting_confirmation" {
		return ErrBusy
	}
	a.Phase = "settled"
	a.Received = a.ReceiptRequired
	if err := r.saveCoordinator(s); err != nil {
		return err
	}
	return r.stop("page_settled")
}
