package room

import (
	"context"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Discovery applies current local admission without resolving remotely or
// linking sources. An excluded record is skipped only by formal readiness, not
// by catalog/preview calls.
func (s *inputSession) catalog(ctx context.Context) (*inputstream.Catalog, error) {
	c, err := s.platformCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.addRoomCatalog(c); err != nil {
		return nil, err
	}
	return c, nil
}
func (s *inputSession) platformCatalog(ctx context.Context) (*inputstream.Catalog, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	p := s.storage
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	o, err := p.repository.ConsumptionObservation(s.execution)
	if err != nil {
		return nil, err
	}
	o.Preferences, err = p.store.inputObservationLocked(p.id)
	if err != nil {
		return nil, err
	}
	o.Blocked = map[inputstream.Location]string{}
	for _, snap := range p.repository.ExistingSnapshots() {
		locations, err := p.repository.Scan(snap)
		if err != nil {
			return nil, err
		}
		for _, l := range locations {
			if o.Excluded[l] {
				continue
			}
			e, err := p.store.businessInputLocked(p.id, 0, &l)
			if err != nil {
				return nil, err
			}
			ad, err := p.store.inboxAdmissionLocked(e)
			if err != nil {
				return nil, err
			}
			if !ad.Verified {
				o.Blocked[l] = "source_admission_unverified"
			} else if ad.Decision == notification.AdmissionExcluded {
				o.Excluded[l] = true
			}
		}
	}
	return p.repository.Catalog(s.execution.Turn, o)
}
