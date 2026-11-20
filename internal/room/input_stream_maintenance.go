package room

import (
	"context"
	"errors"
	"os"
	"sort"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Production evidence binds a bounded target page to committed source facts.
// Pending remains available only for offline fixtures and legacy Inbox storage.
type inputSourceInventory struct {
	Candidates map[string][]inputstream.Location
	Evidence   map[inputstream.Location]notification.SourceRelease
	Current    func(notification.SourceRelease) bool
	Revision   string
	Pending    map[notification.SourceID]map[string]bool
}

func (p *platformInputStorage) maintenanceFactsLocked(provider string, inventory inputSourceInventory, now time.Time) (inputstream.ReleaseSet, error) {
	f := inputstream.ReleaseSet{Records: map[uint64]inputstream.ReleaseFacts{}}
	if inventory.Revision == "" {
		return f, ErrInvalid
	}
	locations := inventory.Candidates[provider]
	if inventory.Candidates == nil {
		// Offline fixtures may supply already-verified source facts directly.
		_, candidates, err := p.engine().Reclamation.ReclaimCandidates(provider, 128)
		if err != nil {
			return f, err
		}
		locations = candidates
	}
	if len(locations) > 128 {
		return f, ErrInvalid
	}
	policy := notification.ExpiryPolicy{AfterDays: 3, SkipExpired: false}
	if svc := p.service.Load(); svc != nil && svc.unifiedInputs {
		policy = svc.inboxExpiry.Resolved()
	}
	if err := policy.Validate(); err != nil {
		return f, err
	}
	f.Retention = time.Duration(policy.AfterDays) * 24 * time.Hour
	expired := []inputstream.Location{}
	revisions := []string{inventory.Revision}
	deadline := time.Now().Add(10 * time.Millisecond)
	for i, l := range locations {
		if i > 0 && time.Now().After(deadline) {
			break
		}
		entry, err := p.store.businessInputLocked(p.id, 0, &l)
		if err != nil {
			return f, err
		}
		source := notification.SourceID{Provider: entry.Item.Source.Provider, Channel: entry.Item.Source.Channel}
		pending, known := inventory.Pending[source]
		fact := inputstream.ReleaseFacts{Revision: inputstream.Ref("source_inventory_", inventory.Revision, l), SourceConfirmed: known && !pending[entry.Item.DeliveryID]}
		if inventory.Evidence != nil {
			proof, known := inventory.Evidence[l]
			fact.SourceConfirmed, fact.ReceiptReleased = false, false
			if known && inventory.Current != nil && inventory.Current(proof) {
				_, snap, err := p.repository.Locate(l)
				if err != nil {
					return f, err
				}
				rec, err := p.engine().Body.Read(snap, l)
				if err != nil {
					return f, err
				}
				if proof.Candidate.Digest == rec.Digest {
					fact.SourceConfirmed = proof.Status == "released"
					fact.ReceiptReleased = fact.SourceConfirmed
					fact.Revision = inputstream.Ref("source_release_", proof)
				}
			}
		}
		link, err := p.store.loadFollowupEventLocked(entry)
		if err == nil {
			fact.BusinessReleased = true
			revisions = append(revisions, inputstream.Ref("link_", link.SourceRef, link.Revision))
		} else if errors.Is(err, os.ErrNotExist) {
			ad, err := p.store.inboxAdmissionLocked(entry)
			if err != nil {
				return f, err
			}
			fact.BusinessReleased = ad.Verified && ad.Decision == notification.AdmissionExcluded
		} else {
			return f, err
		}
		if fact.SourceConfirmed && policy.SkipExpired && notification.Expired(entry.Item.CreatedAt, policy.Cutoff(now)) {
			expired = append(expired, l)
		}
		f.Records[l.Seq] = fact
	}
	f.Revision = inputstream.Ref("domain_release_", revisions)
	if len(expired) > 0 {
		skipped, err := p.engine().Consumption.ExcludeCandidates(provider, expired, "expired_input")
		if err != nil && !errors.Is(err, inputstream.ErrBusy) {
			return f, p.result(err)
		}
		for _, l := range skipped {
			fact := f.Records[l.Seq]
			fact.BusinessReleased = true
			f.Records[l.Seq] = fact
		}
		if len(skipped) > 0 {
			p.projectionDirty.Store(true)
		}
		// Previously durable expiry skips also release the association condition.
		overlay, err := p.engine().Consumption.ConsumptionRelease(provider, f)
		if err != nil {
			return f, err
		}
		for _, l := range expired {
			if overlay.Records[l.Seq].Skipped {
				fact := f.Records[l.Seq]
				fact.BusinessReleased = true
				f.Records[l.Seq] = fact
			}
		}
	}
	return p.engine().Consumption.ConsumptionRelease(provider, f)
}
func (p *platformInputStorage) maintain(provider string, inventory inputSourceInventory, now time.Time) (inputstream.ReclaimPlan, error) {
	return inputOwnerValue(context.Background(), p, "maintenance", func(ctx context.Context) (inputstream.ReclaimPlan, error) {
		return p.maintainOwned(provider, inventory, now)
	})
}
func (p *platformInputStorage) maintainOwned(provider string, inventory inputSourceInventory, now time.Time) (inputstream.ReclaimPlan, error) {
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		return inputstream.ReclaimPlan{}, err
	}
	f, err := p.maintenanceFactsLocked(provider, inventory, now)
	if err != nil {
		return inputstream.ReclaimPlan{}, err
	}
	plan, err := p.engine().Reclamation.PrepareReclaim(provider, f, now)
	if err != nil {
		return plan, p.result(err)
	}
	if plan.Usage.Records == 0 {
		return plan, nil
	}
	// Same original Store serial boundary excludes consumption/association/source
	// inventory replacement until facts are rechecked and the prefix commits.
	_, err = p.engine().Reclamation.CommitReclaim(provider, plan, f)
	return plan, p.result(err)
}

func (m *roomManager) inputInventory(ctx context.Context, svc *service, id MemberIdentity) (inputSourceInventory, error) {
	p, err := svc.inputStorage(id)
	if err != nil {
		return inputSourceInventory{}, err
	}
	candidates, err := inputOwnerValue(ctx, p, "maintenance_candidates", func(context.Context) (map[string][]inputstream.Location, error) {
		providers := []string{}
		for provider := range p.repository.ExistingSnapshots() {
			providers = append(providers, provider)
		}
		sort.Strings(providers)
		out := map[string][]inputstream.Location{}
		if len(providers) == 0 {
			return out, nil
		}
		provider := providers[p.maintenanceNext%len(providers)]
		p.maintenanceNext++
		_, out[provider], err = p.engine().Reclamation.ReclaimCandidates(provider, 128)
		return out, err
	})
	if err != nil {
		return inputSourceInventory{}, err
	}
	batch, err := inputOwnerValue(ctx, p, "maintenance_candidates", func(context.Context) ([]notification.SourceReleaseCandidate, error) {
		p.store.mu.Lock()
		defer p.store.mu.Unlock()
		out := []notification.SourceReleaseCandidate{}
		for _, locations := range candidates {
			for _, l := range locations {
				entry, err := p.store.businessInputLocked(id, 0, &l)
				if err != nil {
					return nil, err
				}
				_, snap, err := p.repository.Locate(l)
				if err != nil {
					return nil, err
				}
				rec, err := p.engine().Body.Read(snap, l)
				if err != nil {
					return nil, err
				}
				out = append(out, notification.SourceReleaseCandidate{Owner: id.RoomIdentity, InboxID: id.MembershipID, Location: l, Item: entry.Item, Digest: rec.Digest})
			}
		}
		return out, nil
	})
	if err != nil {
		return inputSourceInventory{}, err
	}
	results, err := m.notifications.CheckSourceRelease(ctx, batch)
	if err != nil {
		return inputSourceInventory{}, err
	}
	evidence := make(map[inputstream.Location]notification.SourceRelease, len(results))
	for _, r := range results {
		evidence[r.Candidate.Location] = r
	}
	return inputSourceInventory{Revision: inputstream.Ref("inventory_", id, candidates), Evidence: evidence, Current: m.notifications.ReleaseCurrent, Candidates: candidates}, nil
}
func (s *service) maintainUnifiedInputs(ctx context.Context, id MemberIdentity, inventory inputSourceInventory, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := s.inputStorage(id)
	if err != nil {
		return err
	}
	return p.run(ctx, "maintenance", func(ctx context.Context) error {
		providers := []string{}
		for provider := range inventory.Candidates {
			providers = append(providers, provider)
		}
		if inventory.Candidates == nil {
			for provider := range p.repository.ExistingSnapshots() {
				providers = append(providers, provider)
			}
		}
		sort.Strings(providers)
		if len(providers) == 0 {
			return nil
		}
		provider := providers[0]
		_, err := p.maintainOwned(provider, inventory, now)
		return err
	})
}

// Reuse the source's existing admission resolver and minute retry throttle.
// The Store is never held across its bounded remote/read-only resolution.
func (s *service) resolveStoredInputAdmission(ctx context.Context, e InboxEntry) (notification.Admission, error) {
	s.inboxAdmissionMu.Lock()
	defer s.inboxAdmissionMu.Unlock()
	s.store.mu.Lock()
	result, err := s.store.inboxAdmissionLocked(e)
	s.store.mu.Unlock()
	if err != nil || result.Verified || s.resolveInboxAdmission == nil {
		return result, err
	}
	cap := s.store.notificationCapabilities(e.Item.Source.Provider).Admission
	path, err := admissionPath(s.store, cap, e.Item.Source)
	if err != nil {
		return result, err
	}
	if time.Now().Before(s.inboxAdmissionRetry[path]) {
		return result, nil
	}
	if s.inboxAdmissionRetry == nil {
		s.inboxAdmissionRetry = map[string]time.Time{}
	}
	s.inboxAdmissionRetry[path] = time.Now().Add(time.Minute)
	query, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	evidence, queryErr := s.resolveInboxAdmission(query, e.Item.Source)
	if queryErr != nil {
		return result, nil
	}
	checked, err := cap.Inspect(e.Item, evidence)
	if err != nil || !checked.Valid() {
		return result, ErrInvalid
	}
	if !checked.Verified {
		return result, nil
	}
	p, err := s.inputStorage(e.MemberIdentity)
	if err != nil {
		return result, err
	}
	err = p.run(ctx, "save_admission", func(context.Context) error {
		s.store.mu.Lock()
		defer s.store.mu.Unlock()
		if err := s.store.check(true); err != nil {
			return err
		}
		if _, _, err := s.store.memberPath(e.MemberIdentity, "agent.json", true); err != nil {
			return err
		}
		return s.store.save(path, evidence, maxPrivateFileBytes)
	})
	return checked, err
}

func (s *Store) inboxAdmission(e InboxEntry) (notification.Admission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inboxAdmissionLocked(e)
}

func (s *service) reconcileMemberInputAdmission(ctx context.Context, p *platformInputStorage) error {
	entries, err := inputOwnerValue(ctx, p, "admission_candidates", func(context.Context) ([]InboxEntry, error) {
		p.store.mu.Lock()
		defer p.store.mu.Unlock()
		if err := p.checkLocked(); err != nil {
			return nil, err
		}
		if previous := p.projection.Load(); previous != nil && previous.err != nil {
			p.projectionDirty.Store(true)
		}
		providers := []string{}
		for provider := range p.repository.ExistingSnapshots() {
			providers = append(providers, provider)
		}
		sort.Strings(providers)
		if len(providers) == 0 {
			return nil, nil
		}
		index := sort.SearchStrings(providers, p.admissionProvider)
		if index == len(providers) {
			index = 0
		}
		provider := providers[index]
		_, positions, err := p.repository.RecordCandidates(provider, p.admissionAfter, 128)
		if err != nil {
			return nil, err
		}
		out := []InboxEntry{}
		for _, l := range positions {
			e, err := p.store.businessInputLocked(p.id, 0, &l)
			if err != nil {
				return nil, err
			}
			ad, err := p.store.inboxAdmissionLocked(e)
			if err != nil {
				return nil, err
			}
			if previous, observed := p.admissions[l]; observed && previous != ad {
				p.projectionDirty.Store(true)
			}
			if !ad.Verified {
				out = append(out, e)
			}
			p.admissionAfter = l.Seq
		}
		p.admissionProvider = provider
		if len(positions) < 128 {
			p.admissionProvider = providers[(index+1)%len(providers)]
			p.admissionAfter = 0
		}
		return out, nil
	})
	if err != nil {
		return err
	}
	// One bounded remote resolution per background pass; evidence can admit
	// all records sharing that native conversation on the next projection.
	if len(entries) > 0 {
		_, err = s.resolveStoredInputAdmission(ctx, entries[0])
	}
	if err == nil && p.projectionDirty.Load() {
		err = p.run(ctx, "refresh_readiness", func(context.Context) error { return nil })
	}
	return err
}
