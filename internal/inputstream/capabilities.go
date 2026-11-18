package inputstream

import (
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// Inbox identifies body ownership independently of the consuming member. The
// private mapping preserves existing member-derived partitions and paths.
type Inbox struct{ RoomID, RoomInstance, ID string }

func PrivateInbox(m Member) Inbox { return Inbox{m.RoomID, m.RoomInstance, m.MembershipID} }

// These are statically composed capabilities, not provider registries. Domain
// adapters validate payloads, execution authority and release facts first.
type BodyStorage interface {
	AppendOnce(string, AppendRequest) (Receipt, error)
	Snapshot(string) (Snapshot, error)
	Read(Snapshot, Location) (StoredRecord, error)
	PinSnapshots(map[string]Snapshot) (func(), error)
}
type MemberConsumption interface {
	Confirm(Execution, string) error
	Skip(Execution, Location, string) error
	ExcludeLocal(Location, string) error
	ExcludeCandidates(string, []Location, string) ([]Location, error)
	ConsumptionRelease(string, ReleaseSet) (ReleaseSet, error)
}
type ExecutionStorage interface {
	Begin(Execution) error
	Reserve(Execution, ReadRequest) (ReadAttempt, error)
	Prepare(Execution, string, json.RawMessage) error
	Handoff(Execution, string) error
	State(Execution) (CoordinatorState, error)
	Seal(Execution) error
	Abort(Execution, string, string) error
	BindCall(Execution, string, string, int) (ReadRequest, error)
	Receive(Execution, string) error
	ReserveOwned(Execution, ReadRequest, string, OwnedPage) (ReadAttempt, error)
	SettleOwned(Execution, string, string) error
	ChargeDiscovery(Execution, int) error
	ClaimMaterials(Execution, string, int, int, int64, int64) (bool, error)
	PrepareMaterial(Execution, string, json.RawMessage, *MaterialUsage, ...map[string]MaterialMemo) error
}
type ReclamationStorage interface {
	ReclaimCandidates(string, int) (Snapshot, []Location, error)
	PrepareReclaim(string, ReleaseSet, time.Time) (ReclaimPlan, error)
	CommitReclaim(string, ReclaimPlan, ReleaseSet) (string, error)
}

type Capabilities struct {
	Inbox       Inbox
	Consumer    Member
	Body        BodyStorage
	Consumption MemberConsumption
	Execution   ExecutionStorage
	Reclamation ReclamationStorage
}

func (r *Repository) Capabilities(inbox Inbox, member Member) (Capabilities, error) {
	if inbox != PrivateInbox(r.member) || member != r.member {
		return Capabilities{}, ErrIdentity
	}
	return Capabilities{inbox, member, r, r, r, r}, nil
}

// ReclaimCandidates walks a bounded prefix without sorting/scanning the rest of
// the partition. Sequence gaps are legal in imported v1 data.
func (r *Repository) ReclaimCandidates(provider string, limit int) (Snapshot, []Location, error) {
	return r.RecordCandidates(provider, 0, limit)
}

func (r *Repository) RecordCandidates(provider string, after uint64, limit int) (Snapshot, []Location, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return Snapshot{}, nil, ErrCommitUnknown
	}
	p := r.parts[provider]
	if p == nil {
		return Snapshot{}, nil, ErrIdentity
	}
	if limit < 1 || limit > 128 {
		return Snapshot{}, nil, ErrInvalid
	}
	out := make([]Location, 0, limit)
	start := sort.Search(len(p.sequence), func(i int) bool { return p.sequence[i] > after })
	for _, n := range p.sequence[start:] {
		if n > p.state.Reclaimed {
			out = append(out, Location{p.state.Spec.Ref, n})
		}
		if len(out) == limit {
			break
		}
	}
	return snapshot(p), out, nil
}

// PinSnapshots protects all referenced segments before a preview takes its
// immutable view. Release is idempotent and never changes member consumption.
func (r *Repository) PinSnapshots(bounds map[string]Snapshot) (func(), error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return nil, ErrCommitUnknown
	}
	keys := []string{}
	for provider, bound := range bounds {
		p, err := r.partFor(bound)
		if err != nil {
			return nil, err
		}
		if r.parts[provider] != p {
			return nil, ErrIdentity
		}
		for _, seg := range p.state.Segments {
			if seg.First <= bound.Through {
				keys = append(keys, provider+"/"+seg.Name)
			}
		}
	}
	if r.pins == nil {
		r.pins = map[string]int{}
	}
	for _, key := range keys {
		r.pins[key]++
	}
	return sync.OnceFunc(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, key := range keys {
			r.pins[key]--
			if r.pins[key] == 0 {
				delete(r.pins, key)
			}
		}
	}), nil
}
