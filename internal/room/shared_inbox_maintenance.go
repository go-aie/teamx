package room

import (
	"context"
	"errors"
	"os"
	"slices"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func (m *roomManager) maintainSharedInbox(ctx context.Context, s *service) error {
	p := s.store.sharedHandle.Load()
	if p == nil {
		return nil
	}
	p.dirty.Store(true)
	if !p.maintenance.TryLock() {
		return ErrBusy
	}
	defer p.maintenance.Unlock()
	for batch := 0; batch < 2; batch++ {
		p.dirty.Store(false)
		if err := m.maintainSharedBatch(ctx, s, p); err != nil {
			return err
		}
		if !p.dirty.Load() {
			break
		}
	}
	return nil
}
func (m *roomManager) maintainSharedBatch(ctx context.Context, s *service, p *sharedInputStorage) error {
	if s.closing.Load() || s.restarting.Load() {
		return ErrClosing
	}
	if err := m.notifications.ExpireTargetLocked(s.info.RoomIdentity, notification.Target{Mode: "shared_inbox", InboxID: sharedProjectInbox}); err != nil {
		return notificationRoomError(err)
	}
	type batch struct {
		snapshot  inputstream.Snapshot
		locations []inputstream.Location
	}
	b, err := sharedOwnerValue(ctx, p, func() (batch, error) {
		snap, locs, err := p.repository.ReclaimCandidates("gitlab", 128)
		return batch{snap, locs}, err
	})
	if err != nil || len(b.locations) == 0 {
		return err
	}
	v := p.view.Load()
	candidates := make([]notification.SourceReleaseCandidate, 0, len(b.locations))
	for _, l := range b.locations {
		rec, ok := v.records[l.Seq]
		if !ok {
			return ErrConflict
		}
		item, err := s.store.notificationCapabilities("gitlab").Input.DecodeStoredInput(rec)
		if err != nil {
			return err
		}
		candidates = append(candidates, notification.SourceReleaseCandidate{Owner: s.info.RoomIdentity, InboxID: v.root.Inbox.ID, Location: l, Item: item, Digest: rec.Digest})
	}
	proofs, err := m.notifications.CheckSourceRelease(ctx, candidates)
	if err != nil {
		return err
	}
	now := m.notifications.Now()
	policy := s.inboxExpiry.Resolved()
	retention := time.Duration(policy.AfterDays) * 24 * time.Hour
	if len(proofs) > 0 {
		p.maintenanceStatus.Store(&sharedMaintenanceStatus{Partition: b.snapshot.Partition, First: b.locations[0].Seq, Source: proofs[0].Status})
	}
	source := map[uint64]notification.SourceRelease{}
	for _, proof := range proofs {
		source[proof.Candidate.Location.Seq] = proof
	}
	requests := map[string][]inputstream.MemberReleaseCandidate{}
	for _, l := range b.locations {
		rec := v.records[l.Seq]
		for _, gid := range rec.Recipients {
			g := v.root.Grants[gid]
			if g.Status == "closed" {
				continue
			}
			requests[gid] = append(requests[gid], inputstream.MemberReleaseCandidate{Inbox: v.root.Inbox, Member: g.Member, Grant: gid, Stream: v.streams[l.Seq], Location: l, RecordDigest: rec.Digest})
		}
	}
	evidence := []inputstream.MemberRelease{}
	for gid, requests := range requests {
		g := v.root.Grants[gid]
		check := func(member *platformInputStorage) ([]inputstream.MemberRelease, error) {
			out := []inputstream.MemberRelease{}
			c, err := inputstream.LoadSharedConsumption(s.store.dir, g.Member, v.root.Inbox, g.ID)
			if err != nil {
				for _, q := range requests {
					out = append(out, inputstream.MemberRelease{Candidate: q, State: "unknown", Reason: "member_evidence_unavailable"})
				}
				return out, nil
			}
			changed := false
			if member != nil {
				for seq := range c.Skipped {
					if seq <= v.snapshot.Reclaimed {
						delete(c.Skipped, seq)
						changed = true
					}
				}
				active := false
				for _, page := range v.root.Pages {
					if page.Request.Grant == gid && page.State == "protected" {
						active = true
					}
				}
				state, exists, stateErr := member.repository.CurrentState()
				if stateErr != nil {
					return out, stateErr
				}
				if exists && state.SharedIntent != nil && state.SharedIntent.Page.Grant == gid {
					active = true
				}
				if !active {
					for stream, through := range c.Through {
						if through <= v.snapshot.Reclaimed {
							delete(c.Through, stream)
							changed = true
						}
					}
				}
				if changed {
					c.Revision++
				}
			}
			for _, q := range requests {
				protected := false
				if member != nil {
					state, exists, err := member.repository.CurrentState()
					if err != nil {
						return out, err
					}
					if exists && state.SharedIntent != nil && state.SharedIntent.Page.Grant == gid && state.SharedIntent.Page.Stream == q.Stream {
						protected = true
					}
				}
				for _, page := range v.root.Pages {
					if page.State == "protected" && page.Request.Grant == gid && slices.Contains(page.Entries, q.Location) {
						protected = true
					}
				}
				proof := source[q.Location.Seq]
				if member != nil && policy.SkipExpired && !protected && proof.Status == "released" && m.notifications.ReleaseCurrent(proof) && notification.Expired(v.records[q.Location.Seq].CreatedAt, policy.Cutoff(now)) && c.Skipped[q.Location.Seq] == "" && c.Through[q.Stream] < q.Location.Seq {
					if err := c.Skip(q, "expired_input"); err != nil {
						return out, err
					}
					changed = true
				}
				business := false
				if c.Through[q.Stream] >= q.Location.Seq && member != nil {
					s.store.mu.Lock()
					entry, e := v.entry(member.id, q.Location)
					if e == nil {
						_, e = s.store.loadFollowupEventLocked(entry)
					}
					s.store.mu.Unlock()
					business = e == nil
				} else if c.Through[q.Stream] >= q.Location.Seq {
					// Offline evidence is loaded under the same Store association checks;
					// unread or missing data remains protected even without an active owner.
					s.store.mu.Lock()
					roster, loadErr := s.store.loadRoom()
					if loadErr == nil {
						for _, member := range roster.Members {
							if member.MembershipID == g.Member.MembershipID {
								id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
								entry, e := v.entry(id, q.Location)
								if e == nil {
									_, e = s.store.loadFollowupEventLocked(entry)
								}
								business = e == nil
								break
							}
						}
					}

					s.store.mu.Unlock()
				}
				out = append(out, c.CheckRelease(q, business))
			}
			if changed {
				if err := inputstream.SaveSharedConsumption(s.store.dir, c, inputPersistence{s.store}); err != nil {
					return out, err
				}
			}
			return out, nil
		}
		var released []inputstream.MemberRelease
		if handle, ok := s.store.inputHandles.Load(g.Member.MembershipID); ok {
			member := handle.(*platformInputStorage)
			released, err = inputOwnerValue(ctx, member, "shared_member_release", func(context.Context) ([]inputstream.MemberRelease, error) { return check(member) })
		} else {
			released, err = check(nil)
		}
		if err != nil {
			return err
		}
		evidence = append(evidence, released...)
	}
	_, err = sharedOwnerValue(ctx, p, func() (inputstream.ReclaimPlan, error) {
		current := p.view.Load()
		if current.snapshot.Through != b.snapshot.Through || current.snapshot.Reclaimed != b.snapshot.Reclaimed {
			return inputstream.ReclaimPlan{}, ErrConflict
		}
		facts := inputstream.ReleaseSet{Revision: inputstream.Ref("shared_release_", proofs, evidence), Retention: retention, Records: map[uint64]inputstream.ReleaseFacts{}}
		for _, l := range b.locations {
			proof := source[l.Seq]
			confirmed := proof.Status == "released" && m.notifications.ReleaseCurrent(proof)
			facts.Records[l.Seq] = inputstream.ReleaseFacts{Revision: inputstream.Ref("source_release_", proof), SourceConfirmed: confirmed, ReceiptReleased: confirmed}
		}
		facts, err := p.repository.OverlaySharedProtection(facts, evidence)
		if err != nil {
			return inputstream.ReclaimPlan{}, err
		}
		plan, err := p.repository.PrepareReclaim("gitlab", facts, now)
		if err != nil || plan.Through == plan.Snapshot.Reclaimed {
			return plan, err
		}
		_, err = p.repository.CommitReclaim("gitlab", plan, facts)
		if err == nil {
			err = p.repository.CompactSharedPages(nil)
		}
		return plan, err
	})
	if errors.Is(err, ErrConflict) || errors.Is(err, inputstream.ErrConflict) {
		return nil
	}
	if err == nil {
		s.sharedInputsChanged(p)
	}
	return err
}

// A member restart reconciles a committed checkpoint proof before handing out
// another page. Protection release can be repeated after either response loss.
func (p *platformInputStorage) reconcileSharedPages() error {
	shared := p.store.sharedHandle.Load()
	if shared == nil {
		return nil
	}
	state, exists, err := p.repository.CurrentState()
	if err != nil || !exists {
		return err
	}
	for _, a := range state.Attempts {
		if a.Kind == "shared" && a.Shared != nil && a.Phase == "failed_before_output" {
			proof := inputstream.Ref("shared_abort_", a.Shared.Digest, a.Failure)
			if _, err := sharedOwnerValue(context.Background(), shared, func() (struct{}, error) {
				return struct{}{}, shared.repository.ReleaseSharedPage(*a.Shared, proof, true)
			}); err != nil {
				return err
			}
			continue
		}
		if a.Shared == nil || a.Kind != "shared" || (a.Phase != "awaiting_confirmation" && a.Phase != "settled") {
			continue
		}
		c, err := inputstream.LoadSharedConsumption(p.store.dir, inputMember(p.id), a.Shared.Request.Inbox, a.Shared.Request.Grant)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		proof, ok := c.Proofs[a.ID]
		if !ok || proof.PageDigest != a.Shared.Digest || proof.Proof != a.Proof() {
			continue
		}
		if err := p.repository.SettleOwned(state.Execution, a.ID, a.Proof()); err != nil {
			return err
		}
		if _, err := sharedOwnerValue(context.Background(), shared, func() (struct{}, error) {
			return struct{}{}, shared.repository.ReleaseSharedPage(*a.Shared, a.Proof(), false)
		}); err != nil {
			return err
		}
	}
	return p.compactSharedProofs(state)
}

func (p *platformInputStorage) compactSharedProofs(state inputstream.CoordinatorState) error {
	shared := p.store.sharedHandle.Load()
	if shared == nil {
		return nil
	}
	root := shared.view.Load().root
	keep := map[string]bool{}
	for _, a := range state.Attempts {
		keep[a.ID] = true
	}
	if state.SharedIntent != nil {
		keep[state.SharedIntent.Page.Page] = true
	}
	for _, g := range root.Grants {
		if g.Member != inputMember(p.id) {
			continue
		}
		c, err := inputstream.LoadSharedConsumption(p.store.dir, inputMember(p.id), root.Inbox, g.ID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			if g.Status == "closed" {
				continue
			}
			return err
		}
		retired := []inputstream.SharedPage{}
		for id, page := range root.Pages {
			if len(retired) >= 128 {
				break
			}
			if page.Request.Grant != g.ID || page.State == "protected" || keep[id] {
				continue
			}
			proof, ok := c.Proofs[id]
			if ok && (proof.PageDigest != page.Digest || proof.Proof != page.Proof) {
				return inputstream.ErrCorrupt
			}
			if ok {
				delete(c.Proofs, id)
			}
			retired = append(retired, page)
		}
		if len(retired) == 0 {
			continue
		}
		c.Revision++
		if err := inputstream.SaveSharedConsumption(p.store.dir, c, inputPersistence{p.store}); err != nil {
			return err
		}
		if _, err := sharedOwnerValue(context.Background(), shared, func() (struct{}, error) { return struct{}{}, shared.repository.CompactSharedPages(retired) }); err != nil {
			return err
		}
	}
	return nil
}
