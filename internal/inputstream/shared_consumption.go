package inputstream

import (
	"path/filepath"
	"slices"
)

// SharedConsumption has one member owner. Missing initialized state is unknown,
// never an implicit cursor reset. The complete page proof precedes settlement.
type SharedConsumption struct {
	Start    uint64                            `json:"start"`
	Version  int                               `json:"version"`
	Inbox    Inbox                             `json:"inbox"`
	Member   Member                            `json:"member"`
	Grant    string                            `json:"grant"`
	Revision uint64                            `json:"revision"`
	Through  map[string]uint64                 `json:"through"`
	Skipped  map[uint64]string                 `json:"skipped"`
	Proofs   map[string]SharedConsumptionProof `json:"proofs"`
	Checksum string                            `json:"checksum"`
}
type SharedConsumptionProof struct {
	PageDigest string `json:"page_digest"`
	Proof      string `json:"proof"`
	Through    uint64 `json:"through"`
}

func SharedConsumptionPath(dir string, m Member, inbox Inbox) string {
	return filepath.Join(dir, "agents", m.MembershipID, "shared-input", inbox.ID+".json")
}
func LoadSharedConsumption(dir string, m Member, inbox Inbox, grant string) (SharedConsumption, error) {
	var s SharedConsumption
	err := readJSON(SharedConsumptionPath(dir, m, inbox), &s, MaxConsumptionBytes)
	if err != nil {
		return s, err
	}
	sum := s.Checksum
	s.Checksum = ""
	if sum != checksumState(s) || s.Version != 1 || s.Inbox != inbox || s.Member != m || s.Grant != grant || s.Revision == 0 || s.Through == nil || s.Skipped == nil || s.Proofs == nil || len(s.Proofs) > MaxReadSessions || len(s.Through) > MaxRecords || len(s.Skipped) > MaxRecords {
		return s, ErrCorrupt
	}
	s.Checksum = sum
	return s, nil
}
func SaveSharedConsumption(dir string, s SharedConsumption, p Persistence) error {
	s.Checksum = ""
	s.Checksum = checksumState(s)
	return p.SaveInputJSON(SharedConsumptionPath(dir, s.Member, s.Inbox), s)
}
func NewSharedConsumption(inbox Inbox, g SharedGrant) SharedConsumption {
	return SharedConsumption{Start: g.Start, Version: 1, Inbox: inbox, Member: g.Member, Grant: g.ID, Revision: 1, Through: map[string]uint64{}, Skipped: map[uint64]string{}, Proofs: map[string]SharedConsumptionProof{}}
}
func (s *SharedConsumption) Confirm(page SharedPage, proof string) error {
	q := page.Request
	if s.Inbox != q.Inbox || s.Member != q.Member || s.Grant != q.Grant || proof == "" {
		return ErrIdentity
	}
	if old, ok := s.Proofs[q.Page]; ok {
		if old.PageDigest != page.Digest || old.Proof != proof {
			return ErrConflict
		}
		return nil
	}
	base := q.From
	actual := max(s.Start, s.Through[q.Stream])
	if ValidateCheckpoint(actual, base, 0) != nil {
		return ErrConflict
	}
	if err := ValidateReadRange(page.Snapshot.Partition, base, page.Through, page.Snapshot.Through, page.Entries); err != nil {
		return err
	}
	if len(s.Proofs) >= MaxReadSessions {
		return ErrCapacity
	}
	s.Through[q.Stream] = page.Through
	s.Proofs[q.Page] = SharedConsumptionProof{page.Digest, proof, page.Through}
	s.Revision++
	return nil
}
func (s SharedConsumption) CheckRelease(c MemberReleaseCandidate, associationReleased bool) MemberRelease {
	out := MemberRelease{Candidate: c, State: "unknown", Reason: "member_evidence_unavailable"}
	if c.Inbox != s.Inbox || c.Member != s.Member || c.Grant != s.Grant || c.RecordDigest == "" {
		return out
	}
	out.Revision = s.Revision
	out.State = "held"
	out.Reason = "member_unread"
	if s.Skipped[c.Location.Seq] != "" {
		out.State = "released"
		out.Reason = "expired_skip"
	} else if s.Through[c.Stream] >= c.Location.Seq {
		out.Reason = "association_held"
		if associationReleased {
			out.State = "released"
			out.Reason = "consumed"
		}
	}
	if out.State == "released" {
		out.Evidence = Ref("member_release_", c, s.Revision, out.Reason)
	}
	return out
}
func (s *SharedConsumption) Skip(c MemberReleaseCandidate, reason string) error {
	if c.Inbox != s.Inbox || c.Member != s.Member || c.Grant != s.Grant || reason == "" {
		return ErrIdentity
	}
	if s.Skipped[c.Location.Seq] == reason {
		return nil
	}
	if len(s.Skipped) >= MaxRecords {
		return ErrCapacity
	}
	s.Skipped[c.Location.Seq] = reason
	s.Revision++
	return nil
}

type SharedReadIntent struct {
	Read ReadRequest       `json:"read"`
	Page SharedPageRequest `json:"page"`
}

// ReserveSharedIntent runs in the member owner before asking the body owner
// for a protection. Its page identity and normalized budget survive restart.
func (r *Repository) ReserveSharedIntent(e Execution, q ReadRequest, request SharedPageRequest) (SharedPageRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return request, err
	}
	s := r.stateCopy()
	if s.Closed || q.Turn != e.Turn || request.Member != r.member || request.Execution != e {
		return request, ErrIdentity
	}
	if old := s.SharedIntent; old != nil {
		if old.Read != q {
			return request, ErrConflict
		}
		return old.Page, nil
	}
	if activeAttempt(s) != nil {
		return request, ErrBusy
	}
	if !validKey(q.ID) || !validKey(q.Stream) || q.Cursor != "" || q.Limit < 0 || q.Limit > MaxRecords || request.Grant == "" || request.Partition == "" || request.Stream == "" {
		return request, ErrInvalid
	}
	limit := q.Limit
	if limit == 0 {
		limit = PlatformReadLimit
	}
	request.Limit = min(limit, PlatformReadLimit-s.Used)
	request.Page = Ref("page_", r.member, e, q.ID)
	s.SharedIntent = &SharedReadIntent{q, request}
	if err := r.saveCoordinator(s); err != nil {
		return request, err
	}
	return request, r.stop("shared_intent")
}
func (r *Repository) ReserveShared(e Execution, q ReadRequest, page SharedPage) (ReadAttempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return ReadAttempt{}, err
	}
	s := r.stateCopy()
	for _, old := range s.Attempts {
		if old.Request.ID == q.ID {
			if old.Request != q || old.Shared == nil || old.Shared.Digest != page.Digest {
				return old, ErrConflict
			}
			return old, nil
		}
	}
	intent := s.SharedIntent
	if intent == nil || intent.Read != q || intent.Page.checksum() != page.RequestDigest || page.Digest != page.checksum() || page.State != "protected" || len(page.Entries) > intent.Page.Limit || page.Request.Member != r.member {
		return ReadAttempt{}, ErrConflict
	}
	if len(s.Attempts) >= MaxReadSessions || activeAttempt(s) != nil {
		return ReadAttempt{}, ErrBusy
	}
	a := ReadAttempt{Kind: "shared", ID: page.Request.Page, Request: q, Execution: e, Stream: q.Stream, Snapshot: page.Snapshot, From: page.Request.From, Through: page.Through, Entries: slices.Clone(page.Entries), Remaining: page.Remaining, HasMore: page.Remaining > 0, Phase: "reserved", Limit: page.Request.Limit, Shared: &page}
	if a.HasMore && s.Used+len(a.Entries) < PlatformReadLimit {
		a.Next = Ref("read_cursor_", a.ID, a.Through)
	}
	s.Used += len(a.Entries)
	s.SharedIntent = nil
	s.Attempts = append(s.Attempts, a)
	if err := r.saveCoordinator(s); err != nil {
		return a, err
	}
	return a, r.stop("page_reserved")
}

// AuthorizeSharedHandoff and Grant closing serialize in the body owner. Once
// this receipt exists, only exact Session confirmation can release the page.
func (r *Repository) AuthorizeSharedHandoff(page SharedPage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, err := r.sharedPart()
	if err != nil {
		return err
	}
	if _, err = sharedAuthorized(p, page.Request.Member, page.Request.Grant); err != nil {
		return err
	}
	old, ok := p.state.Shared.Pages[page.Request.Page]
	if !ok || old.Digest != page.Digest || old.State == "aborted" {
		return ErrIdentity
	}
	if old.HandedOff || old.State == "released" {
		return nil
	}
	next := cloneRoot(p.state)
	old.HandedOff = true
	next.Shared.Pages[old.Request.Page] = old
	return r.saveShared(p, next)
}
func (r *Repository) AbortSharedIntent(e Execution, page string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return err
	}
	s := r.stateCopy()
	if s.SharedIntent == nil {
		return nil
	}
	if s.SharedIntent.Page.Page != page {
		return ErrIdentity
	}
	s.SharedIntent = nil
	return r.saveCoordinator(s)
}
