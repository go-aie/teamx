package inputstream

// ExternalObservation feeds the existing finite recovery gate with the member
// owner's current durable progress inside one frozen, body-owned offer.
type ExternalObservation struct {
	Snapshot Snapshot   `json:"snapshot"`
	Grant    string     `json:"grant"`
	Done     []Location `json:"done"`
	Pending  bool       `json:"pending"`
}

func (r *Repository) ObserveExternal(e Execution, key string, o ExternalObservation) (ExternalObservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return o, err
	}
	s := r.stateCopy()
	if key == "" || o.Snapshot.Partition == "" || o.Grant == "" {
		return o, ErrInvalid
	}
	if old, ok := s.External[key]; ok {
		if old.Grant != o.Grant || old.Snapshot.Through != o.Snapshot.Through || old.Snapshot.Partition != o.Snapshot.Partition {
			return old, ErrConflict
		}
		if checksumState(old) == checksumState(o) {
			return old, nil
		}
	}
	if s.External == nil {
		s.External = map[string]ExternalObservation{}
	}
	if len(s.External) > 64 {
		return o, ErrCapacity
	}
	s.External[key] = o
	s.Bounds[key] = o.Snapshot
	if err := r.saveCoordinator(s); err != nil {
		return o, err
	}
	return o, nil
}
