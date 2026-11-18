package inputstream

// ExcludeCandidates commits a finite set of domain-verified skips once. It
// preserves all formal pages and returns only newly skipped pending positions.
func (r *Repository) ExcludeCandidates(provider string, candidates []Location, reason string) ([]Location, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return nil, ErrCommitUnknown
	}
	if !validKey(reason) || len(candidates) > 128 {
		return nil, ErrInvalid
	}
	p := r.parts[provider]
	if p == nil {
		return nil, ErrIdentity
	}
	if r.coordinator != nil && activeAttempt(*r.coordinator) != nil {
		return nil, ErrBusy
	}
	c, err := r.loadConsumption(provider)
	if err != nil {
		return nil, err
	}
	skipped := []Location{}
	for _, l := range candidates {
		if l.Partition != p.state.Spec.Ref {
			return nil, ErrIdentity
		}
		if _, ok := p.records[l.Seq]; !ok {
			return nil, ErrConflict
		}
		if c.Skipped[l.Seq] != "" || l.Seq <= c.Through[p.streamRefs[l.Seq]] {
			continue
		}
		c.Skipped[l.Seq] = reason
		skipped = append(skipped, l)
	}
	if len(skipped) == 0 {
		return skipped, nil
	}
	if len(c.Skipped) > MaxRecords {
		return nil, ErrCapacity
	}
	c.Revision++
	c.Checksum = ""
	c.Checksum = checksumState(c)
	if err := r.save(r.consumptionPath(provider), c); err != nil {
		return nil, err
	}
	if !p.state.Consumption {
		next := cloneRoot(p.state)
		next.Consumption, next.LocalOnly = true, r.coordinator == nil
		if err := r.saveRoot(provider, next); err != nil {
			return nil, err
		}
		p.state = next
	}
	return skipped, nil
}
