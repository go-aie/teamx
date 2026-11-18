package inputstream

import "time"

// InputStall is scheduling evidence, not a second consumption authority. New
// arrivals and Session rotation cannot renew a stalled range's attempt budget.
type InputStall struct {
	Bounds   map[string]Snapshot `json:"bounds"`
	Baseline string              `json:"baseline"`
	Since    time.Time           `json:"since"`
	Attempts int                 `json:"attempts"`
	Next     time.Time           `json:"next"`
}

// ReconcileStall uses the same finite backoff schedule supplied by the member
// scheduler; it creates no timer and grants no lifecycle/unknown-action access.
func (r *Repository) ReconcileStall(e Execution, now time.Time, backoff []time.Duration) (*InputStall, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return nil, err
	}
	if now.IsZero() || len(backoff) != 3 {
		return nil, ErrInvalid
	}
	s := r.stateCopy()
	bounds := s.Bounds
	if s.Stall != nil {
		bounds = s.Stall.Bounds
	}
	progress, pending, err := r.progressLocked(bounds)
	if err != nil {
		return nil, err
	}
	if s.Stall != nil && progress != s.Stall.Baseline {
		s.Stall = nil
	}
	if s.Stall == nil {
		if !s.Closed || !pending {
			if err := r.saveCoordinator(s); err != nil {
				return nil, err
			}
			return nil, nil
		}
		bounds = s.Bounds
		progress, pending, err = r.progressLocked(bounds)
		if err != nil {
			return nil, err
		}
		if pending {
			s.Stall = &InputStall{Bounds: cloneMap(bounds), Baseline: progress, Since: now, Next: now.Add(backoff[0])}
		}
	}
	if err := r.saveCoordinator(s); err != nil {
		return nil, err
	}
	return s.Stall, nil
}
func (r *Repository) progressLocked(bounds map[string]Snapshot) (string, bool, error) {
	// Only actual consumption/exclusion within the original offer resets a stall.
	// Later records, unrelated streams and credential/Session changes do not.
	done := []Location{}
	pending := false
	for provider, bound := range bounds {
		if observation, ok := r.coordinator.External[provider]; ok {
			if observation.Snapshot.Partition != bound.Partition {
				return "", false, ErrCorrupt
			}
			for _, l := range observation.Done {
				if l.Seq <= bound.Through {
					done = append(done, l)
				}
			}
			pending = pending || observation.Pending
			continue
		}
		p := r.parts[provider]
		if p == nil {
			return "", false, ErrCorrupt
		}
		c, err := r.loadConsumption(provider)
		if err != nil {
			return "", false, err
		}
		for n, rec := range p.records {
			if n > bound.Through {
				continue
			}
			if n <= c.Through[p.streamRefs[n]] || c.Skipped[n] != "" {
				done = append(done, rec.Location)
			} else {
				pending = true
			}
		}
		if p.state.Reclaimed > bound.Reclaimed {
			done = append(done, Location{bound.Partition, p.state.Reclaimed})
		}
	}
	SortedLocations(done)
	return Ref("progress_", done), pending, nil
}

// ClaimStall records a scheduled attempt before opening a model execution.
// The caller must first pass existing pause, budget and unknown-action gates.
func (r *Repository) ClaimStall(e Execution, now time.Time, backoff []time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return err
	}
	s := r.stateCopy()
	st := s.Stall
	if !s.Closed || st == nil || len(backoff) != 3 || st.Attempts >= 3 || now.Before(st.Next) {
		return ErrBusy
	}
	st.Attempts++
	st.Next = time.Time{}
	if st.Attempts < 3 {
		st.Next = now.Add(backoff[st.Attempts])
	}
	return r.saveCoordinator(s)
}

// Explicit operator continuation does not follow from arrivals, settings or a
// new Session. The service retains lifecycle and unknown-action gates.
func (r *Repository) ContinueStall(e Execution, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return err
	}
	s := r.stateCopy()
	if s.Stall == nil || !s.Closed || now.IsZero() {
		return ErrBusy
	}
	s.Stall.Attempts = 0
	s.Stall.Next = now
	return r.saveCoordinator(s)
}

func (r *Repository) CurrentState() (CoordinatorState, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return CoordinatorState{}, false, ErrCommitUnknown
	}
	if r.coordinator == nil {
		return CoordinatorState{}, false, nil
	}
	return r.stateCopy(), true, nil
}
