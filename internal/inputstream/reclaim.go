package inputstream

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// PlanReclaim is a pure prefix planner, also usable by a Room adapter before
// its native Actor candidate commit. It owns no Room state or commit callbacks.
func PlanReclaim(s Snapshot, policy Policy, records []StoredRecord, sizes map[uint64]int, facts ReleaseSet, now time.Time) (ReclaimPlan, error) {
	if !policy.valid() || facts.Revision == "" || now.IsZero() {
		return ReclaimPlan{}, ErrInvalid
	}
	if facts.Retention < 0 {
		return ReclaimPlan{}, ErrInvalid
	}
	retention := policy.Retention
	if facts.Retention > 0 {
		retention = facts.Retention
	}
	plan := ReclaimPlan{Snapshot: s, Policy: policy.Version, FactsRevision: facts.Revision, Through: s.Reclaimed, Now: now}
	for _, rec := range records {
		if rec.Location.Partition != s.Partition || rec.Location.Seq <= plan.Through || rec.Location.Seq > s.Through || sizes[rec.Location.Seq] <= 0 {
			return ReclaimPlan{}, ErrCorrupt
		}
		f, ok := facts.Records[rec.Location.Seq]
		if !ok || f.Revision == "" || !f.SourceConfirmed || !f.BusinessReleased || (!f.Consumed && !f.Skipped) || f.InFlight {
			break
		}
		at := rec.CreatedAt
		if policy.TimeBasis == "received" {
			at = rec.ReceivedAt
		}
		if policy.TimeBasis != "explicit" && now.Before(at.Add(retention)) {
			break
		}
		plan.Through = rec.Location.Seq
		plan.Usage.Records++
		plan.Usage.Bytes += uint64(sizes[rec.Location.Seq])
	}
	plan.ID = Ref("reclaim_", s, policy.Version, facts, now, plan.Through)
	return plan, nil
}
func (r *Repository) planLocked(p *partition, f ReleaseSet, now time.Time) (ReclaimPlan, error) {
	records := make([]StoredRecord, 0, min(len(f.Records), 128))
	sizes := map[uint64]int{}
	// A batch is always a contiguous retained prefix, never a caller-selected
	// collection that could jump an unverified record.
	for _, n := range p.sequence {
		if _, ok := f.Records[n]; !ok {
			break
		}
		if len(records) == 128 {
			break
		}
		records = append(records, p.records[n])
		sizes[n] = p.offsets[n].Length
	}
	f.Records = cloneMap(f.Records)
	protected := map[string]bool{}
	for n, fact := range f.Records {
		if fact.InFlight {
			protected[p.offsets[n].Segment] = true
		}
	}
	if p.state.Shared != nil {
		for _, page := range p.state.Shared.Pages {
			if page.State != "protected" {
				continue
			}
			for _, l := range page.Entries {
				if off, ok := p.offsets[l.Seq]; ok {
					protected[off.Segment] = true
				}
				if fact, ok := f.Records[l.Seq]; ok {
					fact.InFlight = true
					f.Records[l.Seq] = fact
				}
			}
		}
	}
	if r.coordinator != nil && !r.coordinator.Closed {
		for _, a := range r.coordinator.Attempts {
			if a.Phase == "failed_before_output" {
				continue
			}
			for _, l := range a.Entries {
				if l.Partition == p.state.Spec.Ref {
					protected[p.offsets[l.Seq].Segment] = true
				}
			}
		}
	}
	for _, seg := range p.state.Segments {
		if len(records) == 0 || seg.First > records[len(records)-1].Location.Seq {
			break
		}
		if protected[seg.Name] || r.pins[p.state.Spec.Provider+"/"+seg.Name] > 0 {
			if _, ok := f.Records[seg.First]; ok {
				fact := f.Records[seg.First]
				fact.InFlight = true
				f.Records[seg.First] = fact
			}
		}
	}
	return PlanReclaim(snapshot(p), p.state.Spec.Policy, records, sizes, f, now)
}
func (r *Repository) PrepareReclaim(provider string, f ReleaseSet, now time.Time) (ReclaimPlan, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return ReclaimPlan{}, ErrCommitUnknown
	}
	p := r.parts[provider]
	if p == nil {
		return ReclaimPlan{}, ErrIdentity
	}
	if _, ok := r.capacity.Pending[provider]; ok {
		return ReclaimPlan{}, ErrConflict
	}
	plan, err := r.planLocked(p, f, now)
	if err != nil {
		return ReclaimPlan{}, err
	}
	if p.state.Reclaim != nil && p.state.Reclaim.Plan.ID == plan.ID {
		return plan, nil
	}
	if plan.Through == p.state.Reclaimed {
		if p.state.Reclaim != nil {
			next := cloneRoot(p.state)
			next.Reclaim = nil
			if err := r.saveRoot(provider, next); err != nil {
				return ReclaimPlan{}, err
			}
			p.state = next
		}
		return plan, nil
	}
	next := cloneRoot(p.state)
	next.Reclaim = &reclaimIntent{Plan: plan, Old: append([]segment(nil), next.Segments...)}
	if err := r.saveRoot(provider, next); err != nil {
		return ReclaimPlan{}, err
	}
	p.state = next
	if err := r.stop("reclaim_prepared"); err != nil {
		return ReclaimPlan{}, err
	}
	return plan, nil
}
func (r *Repository) CommitReclaim(provider string, plan ReclaimPlan, f ReleaseSet) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return "", ErrCommitUnknown
	}
	p := r.parts[provider]
	if p == nil {
		return "", ErrIdentity
	}
	if p.state.LastReclaim == plan.ID {
		return plan.ID, nil
	}
	if plan.Through == p.state.Reclaimed && p.state.Reclaim == nil {
		return "", ErrExpired
	}
	if p.state.Reclaim == nil || p.state.Reclaim.Plan.ID != plan.ID || snapshot(p) != plan.Snapshot || p.state.Spec.Policy.Version != plan.Policy {
		return "", ErrConflict
	}
	current, err := r.planLocked(p, f, plan.Now)
	if err != nil {
		return "", err
	}
	if current.ID != plan.ID {
		return "", ErrConflict
	}
	next := cloneRoot(p.state)
	next.Reclaim = &reclaimIntent{Plan: plan, Old: append([]segment(nil), p.state.Segments...), Committed: true}
	next.LastReclaim = plan.ID
	next.Reclaimed = plan.Through
	next.Revision++
	for _, n := range p.sequence {
		if n > plan.Through {
			break
		}
		if f.Records[n].ReceiptReleased {
			rec := p.records[n]
			delete(next.Receipts, deliveryKey(rec.Channel, rec.DeliveryID))
		}
	}
	next.Segments = nil
	dir := filepath.Join(r.partDir(provider), "records")
	for _, seg := range p.state.Segments {
		if seg.Last <= plan.Through {
			continue
		}
		if seg.First > plan.Through {
			next.Segments = append(next.Segments, seg)
			continue
		}
		// Rewrite only the retained suffix, preserving sequence and business IDs.
		var body []byte
		first := uint64(0)
		for _, n := range p.sequence[sort.Search(len(p.sequence), func(i int) bool { return p.sequence[i] > plan.Through }):] {
			if n > seg.Last {
				break
			}
			if p.offsets[n].Segment == seg.Name {
				if first == 0 {
					first = n
				}
				line, _ := encodeLine(p.records[n])
				body = append(body, line...)
			}
		}
		name := "reclaim-" + plan.ID[len("reclaim_"):] + ".jsonl"
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		if err := WriteSegment(file, body); err != nil {
			return "", err
		}
		if err = syncDir(dir); err != nil {
			return "", err
		}
		next.Segments = append(next.Segments, segment{Name: name, Bytes: int64(len(body)), First: first, Last: seg.Last, Hash: Digest(body)})
	}
	if err := r.stop("reclaim_rewritten"); err != nil {
		return "", err
	}
	if err := r.saveRoot(provider, next); err != nil {
		return "", err
	}
	if err := r.stop("reclaim_boundary"); err != nil {
		return "", err
	}
	// Freeze on every failure after logical publication. Usage cannot become
	// available while old physical bytes or an uncertain intent remain.
	if err := r.cleanReclaimedSegments(provider, p.state.Segments, plan.Through); err != nil {
		r.uncertain = true
		return "", errors.Join(ErrCommitUnknown, err)
	}
	if err := r.stop("reclaim_deleted"); err != nil {
		return "", err
	}
	next.Reclaim = nil
	if err := r.saveRoot(provider, next); err != nil {
		r.uncertain = true
		return "", errors.Join(ErrCommitUnknown, err)
	}
	cut := sort.Search(len(p.sequence), func(i int) bool { return p.sequence[i] > plan.Through })
	for _, n := range p.sequence[:cut] {
		stream := p.streamRefs[n]
		seqs := p.streamSeqs[stream]
		start := sort.Search(len(seqs), func(i int) bool { return seqs[i] > plan.Through })
		if start == len(seqs) {
			delete(p.streamSeqs, stream)
		} else {
			p.streamSeqs[stream] = seqs[start:]
		}
		delete(p.records, n)
		delete(p.offsets, n)
		delete(p.projections, n)
		delete(p.streamRefs, n)
	}
	p.sequence = p.sequence[cut:]
	p.bytes -= plan.Usage.Bytes
	for _, seg := range next.Segments {
		if seg.Name != "reclaim-"+plan.ID[len("reclaim_"):]+".jsonl" {
			continue
		}
		var at int64
		for _, n := range p.sequence {
			if n > seg.Last {
				break
			}
			if n >= seg.First {
				o := p.offsets[n]
				o.Segment, o.Offset = seg.Name, at
				p.offsets[n] = o
				at += int64(o.Length)
			}
		}
	}
	p.state = next
	if err := r.stop("reclaim_settled"); err != nil {
		return "", err
	}
	return plan.ID, nil
}
func sortedSeqs(p *partition) []uint64 {
	ns := make([]uint64, 0, len(p.records))
	for n := range p.records {
		ns = append(ns, n)
	}
	sort.Slice(ns, func(i, j int) bool { return ns[i] < ns[j] })
	return ns
}

// Normal maintenance deletes only this batch's original segments. Full orphan
// enumeration belongs to exclusive reopen/recovery, not each running batch.
func (r *Repository) cleanReclaimedSegments(provider string, old []segment, through uint64) error {
	dir := filepath.Join(r.partDir(provider), "records")
	for _, seg := range old {
		if seg.First > through {
			break
		}
		if err := os.Remove(filepath.Join(dir, seg.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncDir(dir)
}
