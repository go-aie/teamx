package room

import (
	"context"
	"errors"
	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/notification"
	"os"
	"sort"
	"time"
)

// Pending is the actual source inventory captured by the existing source gate.
// Missing source inventories do not establish source acknowledgement.
type inputSourceInventory struct {
	Revision string
	Pending  map[notification.SourceID]map[string]bool
}

func (p *platformInputStorage) maintenanceFactsLocked(provider string, inventory inputSourceInventory) (inputstream.ReleaseSet, error) {
	f := inputstream.ReleaseSet{Records: map[uint64]inputstream.ReleaseFacts{}}
	if inventory.Revision == "" {
		return f, ErrInvalid
	}
	snap, err := p.repository.Snapshot(provider)
	if err != nil {
		return f, err
	}
	locations, err := p.repository.Scan(snap)
	if err != nil {
		return f, err
	}
	revisions := []string{inventory.Revision}
	for _, l := range locations {
		entry, err := p.store.businessInputLocked(p.id, 0, &l)
		if err != nil {
			return f, err
		}
		source := notification.SourceID{Provider: entry.Item.Source.Provider, Channel: entry.Item.Source.Channel}
		pending, known := inventory.Pending[source]
		fact := inputstream.ReleaseFacts{Revision: inputstream.Ref("source_inventory_", inventory.Revision, l), SourceConfirmed: known && !pending[entry.Item.DeliveryID]}
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
		f.Records[l.Seq] = fact
	}
	f.Revision = inputstream.Ref("domain_release_", revisions)
	return p.repository.ConsumptionRelease(provider, f)
}
func (p *platformInputStorage) maintain(provider string, inventory inputSourceInventory, now time.Time) (inputstream.ReclaimPlan, error) {
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		return inputstream.ReclaimPlan{}, err
	}
	f, err := p.maintenanceFactsLocked(provider, inventory)
	if err != nil {
		return inputstream.ReclaimPlan{}, err
	}
	plan, err := p.repository.PrepareReclaim(provider, f, now)
	if err != nil {
		return plan, p.result(err)
	}
	if plan.Usage.Records == 0 {
		return plan, nil
	}
	// Same original Store serial boundary excludes consumption/association/source
	// inventory replacement until facts are rechecked and the prefix commits.
	_, err = p.repository.CommitReclaim(provider, plan, f)
	return plan, p.result(err)
}

func (m *roomManager) inputInventoryLocked(id MemberIdentity) inputSourceInventory {
	pending := m.notifications.PendingInputDeliveriesLocked(id.RoomIdentity, platformMemberTarget(id))
	rows := []string{}
	for source, items := range pending {
		rows = append(rows, inputstream.Ref("source_", source))
		for delivery := range items {
			rows = append(rows, inputstream.Ref("pending_", source, delivery))
		}
	}
	sort.Strings(rows)
	return inputSourceInventory{Revision: inputstream.Ref("inventory_", id, rows), Pending: pending}
}
func (s *service) maintainUnifiedInputs(ctx context.Context, id MemberIdentity, inventory inputSourceInventory, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := s.inputStorage(id)
	if err != nil {
		return err
	}
	for provider := range p.repository.ExistingSnapshots() {
		plan, err := p.maintain(provider, inventory, now)
		if err != nil {
			return err
		}
		if plan.Usage.Records > 0 {
			s.store.changed("inbox", id)
		}
	}
	return nil
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
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if err := s.store.check(true); err != nil {
		return result, err
	}
	if _, _, err := s.store.memberPath(e.MemberIdentity, "agent.json", true); err != nil {
		return result, err
	}
	if err := s.store.save(path, evidence, maxPrivateFileBytes); err != nil {
		return result, err
	}
	return checked, nil
}
