package inputstream

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
)

// OpenSharedRepository reuses the segment, checksum, published-range and reclaim
// implementation. The one partition root is the sole shared publication root;
// capacity is derived from it, without a separate shared reservation ledger.
func OpenSharedRepository(dir string, inbox Inbox, subscription string, codec Codec, persist Persistence) (*Repository, error) {
	if !validComponent(inbox.ID) || !validKey(inbox.RoomID) || !validKey(inbox.RoomInstance) || !validKey(subscription) || codec == nil || persist == nil {
		return nil, ErrIdentity
	}
	m := Member{inbox.RoomID, inbox.RoomInstance, inbox.ID}
	d := codec.Descriptor()
	if !validComponent(d.Provider) || !d.Policy.valid() {
		return nil, ErrInvalid
	}
	r := &Repository{sharedInbox: inbox, dir: dir, sharedDir: filepath.Join(dir, "shared-inboxes", inbox.ID), member: m, persist: persist, codecs: map[string]Codec{d.Provider: codec}, parts: map[string]*partition{}, segmentBytes: DefaultSegmentBytes, capacity: capacityState{Limits: Usage{d.Policy.MaxRecords, d.Policy.MaxBytes}, Version: FormatVersion, Owner: m, Pending: map[string]reservation{}}}
	path := filepath.Join(r.sharedDir, "partition.json")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		state := rootState{Version: FormatVersion, Spec: r.partitionSpec(d), Revision: 1, Receipts: map[string]Receipt{}, Shared: &SharedRoot{ConfigVersion: 1, Inbox: inbox, Subscription: subscription, Grants: map[string]SharedGrant{}, Pages: map[string]SharedPage{}}}
		if err := r.saveRoot(d.Provider, state); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	p, err := r.load(d.Provider)
	if err != nil {
		return nil, err
	}
	if p == nil || p.state.Shared == nil || p.state.Shared.Inbox != inbox || p.state.Shared.Subscription != subscription || p.state.Shared.Grants == nil || p.state.Shared.Pages == nil {
		return nil, ErrIdentity
	}
	if len(p.state.Shared.Grants) > MaxRecords || len(p.state.Shared.Pages) > MaxRecords {
		return nil, ErrCorrupt
	}
	if p.state.Shared.ConfigVersion == 0 || p.state.Shared.NextGrant < uint64(len(p.state.Shared.Grants)) {
		return nil, ErrCorrupt
	}
	activeMembers := map[Member]bool{}
	for key, g := range p.state.Shared.Grants {
		if key != g.ID || !g.Member.Valid() || g.Member.RoomID != inbox.RoomID || g.Member.RoomInstance != inbox.RoomInstance || g.Start > p.state.Latest || (g.Status != "active" && g.Status != "joining" && g.Status != "closing" && g.Status != "closed") {
			return nil, ErrCorrupt
		}
		if g.Status != "closed" {
			if activeMembers[g.Member] {
				return nil, ErrCorrupt
			}
			activeMembers[g.Member] = true
		}
	}
	if len(activeMembers) > 64 {
		return nil, ErrCorrupt
	}
	for id, page := range p.state.Shared.Pages {
		if id != page.Request.Page || page.Request.Inbox != inbox || page.RequestDigest != page.Request.checksum() || page.Digest != page.checksum() || page.Protection != Ref("protection_", page.RequestDigest) || (page.State != "protected" && page.State != "released" && page.State != "aborted") {
			return nil, ErrCorrupt
		}
		g, ok := p.state.Shared.Grants[page.Request.Grant]
		if !ok || g.Member != page.Request.Member || page.Request.Partition != p.state.Spec.Ref || page.Request.Execution.Generation == "" || page.Request.Execution.Turn == 0 || page.Request.From < g.Start || page.Request.Limit < 0 || page.Request.Limit > PlatformReadLimit || len(page.Entries) > page.Request.Limit || page.Request.From > page.Through || page.Through > page.Snapshot.Through || page.Scanned != page.Snapshot.Through || page.Snapshot.Partition != p.state.Spec.Ref || page.Snapshot.Through > p.state.Latest || page.Remaining < 0 || page.State != "protected" && page.Proof == "" || page.State == "protected" && g.Status == "closed" || page.State == "aborted" && page.HandedOff {
			return nil, ErrCorrupt
		}
		previous := page.Request.From
		for _, l := range page.Entries {
			if l.Seq <= previous || l.Seq > page.Through || l.Partition != p.state.Spec.Ref || l.Seq > p.state.Latest || page.State == "protected" && l.Seq <= p.state.Reclaimed {
				return nil, ErrCorrupt
			}
			if rec, exists := p.records[l.Seq]; exists && !slices.Contains(rec.Recipients, page.Request.Grant) {
				return nil, ErrCorrupt
			}
			previous = l.Seq
		}
	}
	for _, rec := range p.records {
		if len(rec.Recipients) == 0 || len(rec.Recipients) > 64 {
			return nil, ErrCorrupt
		}
		seen := map[string]bool{}
		for _, id := range rec.Recipients {
			g, ok := p.state.Shared.Grants[id]
			if !ok || seen[id] || rec.Location.Seq <= g.Start {
				return nil, ErrCorrupt
			}
			seen[id] = true
		}
	}
	if uint64(len(p.records)) > d.Policy.MaxRecords || p.bytes > d.Policy.MaxBytes {
		return nil, ErrCorrupt
	}
	r.parts[d.Provider] = p
	return r, nil
}
func (r *Repository) sharedPart() (*partition, error) {
	if r.uncertain {
		return nil, ErrCommitUnknown
	}
	if r.sharedDir == "" || len(r.parts) != 1 {
		return nil, ErrIdentity
	}
	for _, p := range r.parts {
		if p.state.Shared != nil {
			return p, nil
		}
	}
	return nil, ErrCorrupt
}
func (r *Repository) saveShared(p *partition, next rootState) error {
	next.Revision++
	if err := r.saveRoot(next.Spec.Provider, next); err != nil {
		return err
	}
	p.state = next
	return nil
}
func (r *Repository) SharedState() (SharedRoot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.sharedPart()
	if err != nil {
		return SharedRoot{}, err
	}
	return *cloneRoot(p.state).Shared, nil
}
func (r *Repository) JoinShared(m Member) (SharedGrant, error)         { return r.joinShared(m, false) }
func (r *Repository) PrepareSharedGrant(m Member) (SharedGrant, error) { return r.joinShared(m, true) }
func (r *Repository) joinShared(m Member, joining bool) (SharedGrant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.sharedPart()
	if err != nil {
		return SharedGrant{}, err
	}
	root := p.state.Shared
	if !m.Valid() || m.RoomID != root.Inbox.RoomID || m.RoomInstance != root.Inbox.RoomInstance {
		return SharedGrant{}, ErrIdentity
	}
	active := 0
	for _, g := range root.Grants {
		if g.Status != "closed" {
			active++
			if g.Member == m {
				if g.Status != "active" && !(joining && g.Status == "joining") {
					return g, ErrBusy
				}
				return g, nil
			}
		}
	}
	if active >= 64 || len(root.Grants) >= MaxRecords {
		return SharedGrant{}, ErrCapacity
	}
	next := cloneRoot(p.state)
	next.Shared.NextGrant++
	next.Shared.ConfigVersion++
	g := SharedGrant{ID: Ref("grant_", root.Inbox, m, next.Shared.NextGrant), Member: m, Start: p.state.Latest, Status: "active"}
	if joining {
		g.Status = "joining"
	}
	next.Shared.Grants[g.ID] = g
	return g, r.saveShared(p, next)
}

// Closing is durable before any handoff can be admitted. Existing handed-off
// pages can settle by exact Session proof, without reauthorizing body access.
func (r *Repository) CloseSharedGrant(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.sharedPart()
	if err != nil {
		return err
	}
	g, ok := p.state.Shared.Grants[id]
	if !ok {
		return ErrIdentity
	}
	if g.Status == "closed" {
		return nil
	}
	next := cloneRoot(p.state)
	g.Status = "closing"
	next.Shared.ConfigVersion++
	pending := false
	for _, page := range next.Shared.Pages {
		if page.Request.Grant == id && page.State == "protected" {
			pending = true
		}
	}
	if !pending {
		g.Status = "closed"
	}
	next.Shared.Grants[id] = g
	return r.saveShared(p, next)
}
func sharedAuthorized(p *partition, m Member, grant string) (SharedGrant, error) {
	g, ok := p.state.Shared.Grants[grant]
	if !ok || g.Member != m || g.Status != "active" {
		return g, ErrIdentity
	}
	return g, nil
}
func (r *Repository) SharedRecords(m Member, grant string, through uint64) ([]StoredRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.sharedPart()
	if err != nil {
		return nil, err
	}
	if _, err := sharedAuthorized(p, m, grant); err != nil {
		return nil, err
	}
	out := []StoredRecord{}
	for _, seq := range p.sequence {
		rec := p.records[seq]
		if seq > through || !slices.Contains(rec.Recipients, grant) {
			continue
		}
		// Do not share mutable payload/slice ownership with a task caller.
		rec.Payload = slices.Clone(rec.Payload)
		rec.Recipients = slices.Clone(rec.Recipients)
		rec.Group.Context = slices.Clone(rec.Group.Context)
		out = append(out, rec)
	}
	return out, nil
}
func (r *Repository) FixSharedPage(q SharedPageRequest) (SharedPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.sharedPart()
	if err != nil {
		return SharedPage{}, err
	}
	if old, ok := p.state.Shared.Pages[q.Page]; ok && old.RequestDigest != q.checksum() {
		return cloneSharedPage(old), ErrConflict
	}
	g, err := sharedAuthorized(p, q.Member, q.Grant)
	if err != nil {
		return SharedPage{}, err
	}
	if q.Inbox != p.state.Shared.Inbox || q.Partition != p.state.Spec.Ref || q.From < g.Start || q.From > p.state.Latest || q.Page == "" || q.Stream == "" || q.Execution.Generation == "" || q.Execution.Turn == 0 || q.Limit < 0 || q.Limit > PlatformReadLimit {
		return SharedPage{}, ErrInvalid
	}
	if old, ok := p.state.Shared.Pages[q.Page]; ok {
		if old.RequestDigest != q.checksum() {
			return old, ErrConflict
		}
		if len(old.Entries) > 0 && old.Entries[0].Seq <= p.state.Reclaimed {
			return old, ErrExpired
		}
		return cloneSharedPage(old), nil
	}
	if len(p.state.Shared.Pages) >= MaxRecords {
		return SharedPage{}, ErrCapacity
	}
	bound := p.state.Latest
	if q.Bound != nil {
		bound = *q.Bound
	}
	if bound > p.state.Latest || q.From > bound {
		return SharedPage{}, ErrConflict
	}
	page := SharedPage{Request: q, RequestDigest: q.checksum(), Snapshot: snapshot(p), Through: q.From, Entries: []Location{}, State: "protected"}
	page.Snapshot.Through = bound
	page.Scanned = bound
	pageBytes := 0
	for _, seq := range p.streamSeqs[q.Stream] {
		if seq <= q.From || seq > bound || slices.Contains(q.Excluded, seq) || !slices.Contains(p.records[seq].Recipients, q.Grant) {
			continue
		}
		if page.Remaining > 0 || len(page.Entries) >= q.Limit || pageBytes+p.offsets[seq].Length > MaxReadPageBytes {
			page.Remaining++
			continue
		}
		page.Entries = append(page.Entries, Location{q.Partition, seq})
		pageBytes += p.offsets[seq].Length
		page.Through = seq
	}
	// With an empty selected stream the checkpoint can advance to its bound.
	if page.Remaining == 0 {
		page.Through = bound
	}
	page.Protection = Ref("protection_", page.RequestDigest)
	page.Digest = page.checksum()
	next := cloneRoot(p.state)
	next.Shared.Pages[q.Page] = page
	if err := r.saveShared(p, next); err != nil {
		return page, err
	}
	if err := r.stop("shared_protected"); err != nil {
		return page, err
	}
	return cloneSharedPage(page), nil
}
func cloneSharedPage(p SharedPage) SharedPage {
	b, _ := json.Marshal(p)
	var v SharedPage
	_ = json.Unmarshal(b, &v)
	return v
}

// ReleaseSharedPage accepts a member proof already verified against its durable
// ledger by the service. It compares the entire page, not a highwater alone.
func (r *Repository) ReleaseSharedPage(page SharedPage, proof string, abort bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.sharedPart()
	if err != nil {
		return err
	}
	old, ok := p.state.Shared.Pages[page.Request.Page]
	if !ok || old.Digest != page.Digest || old.RequestDigest != page.RequestDigest || proof == "" {
		return ErrIdentity
	}
	if old.State != "protected" {
		if old.Proof != proof {
			return ErrConflict
		}
		return nil
	}
	next := cloneRoot(p.state)

	old.State = "released"
	if abort {
		old.State = "aborted"
		old.HandedOff = false
	}
	old.Proof = proof
	next.Shared.Pages[page.Request.Page] = old
	g := next.Shared.Grants[page.Request.Grant]
	if g.Status == "closing" {
		pending := false
		for _, other := range next.Shared.Pages {
			if other.Request.Grant == g.ID && other.State == "protected" {
				pending = true
			}
		}
		if !pending {
			g.Status = "closed"
			next.Shared.Grants[g.ID] = g
		}
	}
	return r.saveShared(p, next)
}

// OverlaySharedProtection is applied immediately before reclaim planning and
// commit. Closed grants are proved locally. Missing member facts stay held.
func (r *Repository) OverlaySharedProtection(facts ReleaseSet, evidence []MemberRelease) (ReleaseSet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.sharedPart()
	if err != nil {
		return facts, err
	}
	out := ReleaseSet{Revision: facts.Revision, Retention: facts.Retention, Records: cloneMap(facts.Records)}
	for seq, f := range out.Records {
		rec, ok := p.records[seq]
		if !ok {
			return out, ErrExpired
		}
		f.Consumed = true
		f.BusinessReleased = true
		for _, id := range rec.Recipients {
			g := p.state.Shared.Grants[id]
			if g.Status == "closed" {
				continue
			}
			released := false
			for _, e := range evidence {
				if e.Candidate == (MemberReleaseCandidate{p.state.Shared.Inbox, g.Member, id, p.streamRefs[seq], rec.Location, rec.Digest}) && e.State == "released" && e.Evidence != "" {
					released = true
					break
				}
			}
			if !released {
				f.Consumed = false
				f.BusinessReleased = false
			}
		}
		for _, page := range p.state.Shared.Pages {
			if page.State == "protected" && slices.Contains(page.Entries, rec.Location) {
				f.InFlight = true
			}
		}
		out.Records[seq] = f
	}
	return out, nil
}

func (r *Repository) ActivateSharedGrant(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.sharedPart()
	if err != nil {
		return err
	}
	g, ok := p.state.Shared.Grants[id]
	if !ok {
		return ErrIdentity
	}
	if g.Status == "active" {
		return nil
	}
	if g.Status != "joining" {
		return ErrConflict
	}
	next := cloneRoot(p.state)
	g.Status = "active"
	next.Shared.Grants[id] = g
	next.Shared.ConfigVersion++
	return r.saveShared(p, next)
}

// CompactSharedPages receives only pages for which the member has durably
// dropped replay authority after advancing to another execution. It never
// removes a protection or relies on an absent member file.
func (r *Repository) CompactSharedPages(pages []SharedPage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.sharedPart()
	if err != nil {
		return err
	}
	if len(pages) > 128 {
		return ErrCapacity
	}
	next := cloneRoot(p.state)
	for _, page := range pages {
		old, ok := next.Shared.Pages[page.Request.Page]
		if !ok {
			continue
		}
		if old.State == "protected" || old.Proof != page.Proof || old.Digest != page.Digest {
			return ErrConflict
		}
		delete(next.Shared.Pages, old.Request.Page)
	}
	// Closed Grant evidence remains until every frozen receiver and protection
	// referencing it has disappeared from the authoritative publication root.
	for id, g := range next.Shared.Grants {
		if g.Status != "closed" {
			continue
		}
		retained := false
		for _, rec := range p.records {
			if slices.Contains(rec.Recipients, id) {
				retained = true
				break
			}
		}
		if retained {
			continue
		}
		for pid, page := range next.Shared.Pages {
			if page.Request.Grant == id {
				if page.State == "protected" {
					retained = true
				} else {
					delete(next.Shared.Pages, pid)
				}
			}
		}
		if !retained {
			delete(next.Shared.Grants, id)
		}
	}
	if checksumState(next.Shared) == checksumState(p.state.Shared) {
		return nil
	}
	return r.saveShared(p, next)
}
