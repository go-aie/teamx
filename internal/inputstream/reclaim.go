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
		if policy.TimeBasis != "explicit" && now.Before(at.Add(policy.Retention)) {
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
	records := make([]StoredRecord, 0, len(p.records))
	sizes := map[uint64]int{}
	for n, rec := range p.records {
		records = append(records, rec)
		sizes[n] = p.offsets[n].Length
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Location.Seq < records[j].Location.Seq })
	// A retained in-flight page can still reference its segment. Conservatively
	// protect the entire boundary segment until the domain releases that page.
	// This also applies when suffix protection facts are missing.
	f.Records = cloneMap(f.Records)
	protected := map[string]bool{}
	for seq, off := range p.offsets {
		fact, ok := f.Records[seq]
		if !ok || fact.Revision == "" || fact.InFlight {
			protected[off.Segment] = true
		}
	}
	for _, seg := range p.state.Segments {
		if protected[seg.Name] {
			fact := f.Records[seg.First]
			fact.InFlight = true
			f.Records[seg.First] = fact
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
		for _, n := range sortedSeqs(p) {
			if n > plan.Through && p.offsets[n].Segment == seg.Name {
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
		n, err := file.Write(body)
		if err == nil && n != len(body) {
			err = ErrCorrupt
		}
		if err == nil {
			err = file.Sync()
		}
		err = errors.Join(err, file.Close())
		if err != nil {
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
	if err := r.cleanSegments(provider, next.Segments); err != nil {
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
	loaded, err := r.load(provider)
	if err != nil {
		r.uncertain = true
		return "", errors.Join(ErrCommitUnknown, err)
	}
	r.parts[provider] = loaded
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
