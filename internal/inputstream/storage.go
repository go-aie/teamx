package inputstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Persistence is the owning Store's atomic JSON writer. Calls are serialized
// under its existing lock, then Repository.mu. It must freeze on uncertain
// replacement and return ErrCommitUnknown. No Actor or network calls occur.
type Persistence interface{ SaveInputJSON(string, any) error }
type segment struct {
	Name        string `json:"name"`
	Bytes       int64  `json:"bytes"`
	First, Last uint64
	Hash        string `json:"hash"`
}

func (s segment) bounds() SegmentRange {
	return SegmentRange{First: s.First, Last: s.Last, Bytes: s.Bytes, Digest: s.Hash}
}

type scopeMetadata struct {
	Key, Source, Label string
	First              uint64 `json:"first,omitempty"`
}
type rootState struct {
	Shared                      *SharedRoot     `json:"shared,omitempty"`
	LocalOnly                   bool            `json:"local_consumption_only,omitempty"`
	Consumption                 bool            `json:"consumption_initialized,omitempty"`
	LastReclaim                 string          `json:"last_reclaim,omitempty"`
	Checksum                    string          `json:"checksum"`
	ArchivedScopes              []scopeMetadata `json:"scopes,omitempty"`
	Version                     int             `json:"version"`
	Spec                        PartitionSpec   `json:"spec"`
	Revision, Latest, Reclaimed uint64
	Segments                    []segment
	Receipts                    map[string]Receipt
	Reclaim                     *reclaimIntent `json:"reclaim,omitempty"`
}
type reservation struct {
	ExpectedRevision uint64
	BaseThrough      uint64
	Record           StoredRecord
	Bytes            uint64
	Operation        string
	SourceRef        string `json:"source_ref,omitempty"`
}
type capacityState struct {
	Checksum string `json:"checksum"`
	Limits   Usage
	Version  int
	Owner    Member
	Pending  map[string]reservation
}
type reclaimIntent struct {
	Plan      ReclaimPlan
	Old       []segment
	Committed bool
}
type offset struct {
	Segment string
	Offset  int64
	Length  int
}
type partition struct {
	bytes    uint64
	sequence []uint64
	state    rootState
	records  map[uint64]StoredRecord
	offsets  map[uint64]offset

	projections map[uint64]Projection
	streamRefs  map[uint64]string
	streamSeqs  map[string][]uint64
}

// Repository is one member's platform storage. Provider/channel never select
// storage algorithms. Empty partitions are virtual until their first append.
type Repository struct {
	sharedInbox  Inbox
	sharedDir    string
	pins         map[string]int
	coordinator  *CoordinatorState
	mu           sync.Mutex
	dir          string
	member       Member
	persist      Persistence
	codecs       map[string]Codec
	parts        map[string]*partition
	capacity     capacityState
	uncertain    bool
	segmentBytes int64
	// Narrow fault seam: tests emulate process death after durable boundaries.
	fault func(string) error
}

const DefaultSegmentBytes int64 = 1 << 20
const MaxStateBytes = 16 << 20

func OpenRepository(dir string, m Member, codecs []Codec, p Persistence, limits ...Usage) (*Repository, error) {
	if len(codecs) > 64 {
		return nil, ErrCapacity
	}
	if !m.Valid() || !validComponent(m.MembershipID) || p == nil {
		return nil, ErrIdentity
	}
	r := &Repository{dir: dir, member: m, persist: p, parts: map[string]*partition{}, codecs: map[string]Codec{}, segmentBytes: DefaultSegmentBytes}
	for _, c := range codecs {
		d := c.Descriptor()
		if !validComponent(d.Provider) || d.Codec == "" || d.Schema < 1 || d.GroupingVersion < 1 || !d.Policy.valid() || len(d.Channels) == 0 || r.codecs[d.Provider] != nil {
			return nil, ErrInvalid
		}
		r.codecs[d.Provider] = c
	}
	r.capacity = capacityState{Limits: Usage{MaxRecords, MaxBytes}, Version: FormatVersion, Owner: m, Pending: map[string]reservation{}}
	if len(limits) > 1 {
		return nil, ErrInvalid
	}
	if len(limits) == 1 {
		r.capacity.Limits = limits[0]
	}
	err := readJSON(r.capacityPath(), &r.capacity, 16<<20)
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return nil, err
	}
	if r.capacity.Limits.Records == 0 || r.capacity.Limits.Records > MaxRecords || r.capacity.Limits.Bytes == 0 || r.capacity.Limits.Bytes > MaxBytes {
		return nil, ErrCorrupt
	}
	if len(limits) == 1 && limits[0] != r.capacity.Limits {
		return nil, ErrConflict
	}
	if !missing && r.capacity.Checksum != capacityChecksum(r.capacity) {
		return nil, ErrCorrupt
	}
	if r.capacity.Version != FormatVersion {
		return nil, ErrVersion
	}
	if r.capacity.Owner != m || r.capacity.Pending == nil {
		return nil, ErrCorrupt
	}
	entries, err := os.ReadDir(r.inboxDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".snapshot-") {
			continue
		}
		if !e.IsDir() || !validComponent(e.Name()) {
			return nil, ErrVersion
		}
		c := r.codecs[e.Name()]
		if c == nil {
			return nil, ErrVersion
		}
		part, err := r.load(e.Name())
		if err != nil {
			return nil, err
		}
		if part != nil {
			r.parts[e.Name()] = part
		}
	}
	if missing && len(r.parts) > 0 {
		return nil, fmt.Errorf("%w: missing capacity ledger", ErrCorrupt)
	}
	// Published roots alone decide whether a reserved delivery committed. Neither
	// complete lines nor the cached capacity file are evidence of receipt.
	for provider, op := range r.capacity.Pending {
		c := r.codecs[provider]
		if c == nil {
			return nil, ErrVersion
		}
		if op.Record.Location.Partition != c.Descriptor().Spec(m).Ref || op.Record.Digest != op.Record.checksum() || op.Operation == "" || !validSourceRef(op.SourceRef) {
			return nil, ErrCorrupt
		}
		if _, err := r.validateRecord(provider, op.Record); err != nil {
			return nil, err
		}
		line, err := encodeLine(op.Record)
		if err != nil || op.Bytes != uint64(len(line)) {
			return nil, ErrCorrupt
		}
		part := r.parts[provider]
		if op.Record.Location.Seq <= op.BaseThrough {
			return nil, ErrCorrupt
		}
		if part == nil && (op.ExpectedRevision != 0 || op.BaseThrough != 0) {
			return nil, ErrCorrupt
		}
		if part != nil && part.state.Revision != op.ExpectedRevision && part.state.Revision != op.ExpectedRevision+1 {
			return nil, ErrCorrupt
		}
		if part != nil {
			if rec, ok := part.state.Receipts[deliveryKey(op.Record.Channel, op.Record.DeliveryID)]; ok {
				if rec.Digest != op.Record.DeliveryDigest || rec.Operation != op.Operation || rec.SourceRef != op.SourceRef || rec.Seq != op.Record.Location.Seq {
					return nil, ErrCorrupt
				}
				delete(r.capacity.Pending, provider)
				continue
			}
		}
		if part != nil && (part.state.Revision != op.ExpectedRevision || part.state.Latest != op.BaseThrough) {
			return nil, ErrCorrupt
		}
		// No publication: retain the reservation and replay only the SAME delivery.
	}
	total := r.usageLocked()
	for _, pending := range r.capacity.Pending {
		total.Records++
		total.Bytes += pending.Bytes
	}
	if total.Records > r.capacity.Limits.Records || total.Bytes > r.capacity.Limits.Bytes {
		return nil, ErrCorrupt
	}
	for _, p := range r.parts {
		if uint64(len(p.records)) > p.state.Spec.Policy.MaxRecords {
			return nil, ErrCorrupt
		}
		var retained uint64
		for _, o := range p.offsets {
			retained += uint64(o.Length)
		}
		if retained > p.state.Spec.Policy.MaxBytes {
			return nil, ErrCorrupt
		}
	}
	if !missing {
		if err := r.saveCapacity(); err != nil {
			return nil, err
		}
	}
	if err := r.loadCoordinator(); err != nil {
		return nil, err
	}
	for provider := range r.parts {
		if _, err := r.loadConsumption(provider); err != nil {
			return nil, err
		}
	}
	if err := r.recoverConfirmation(); err != nil {
		return nil, err
	}
	return r, nil
}
func validComponent(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func (r *Repository) inboxDir() string { return filepath.Join(r.dir, "inboxes", r.member.MembershipID) }
func (r *Repository) partDir(p string) string {
	if r.sharedDir != "" {
		return r.sharedDir
	}
	return filepath.Join(r.inboxDir(), p)
}
func (r *Repository) capacityPath() string {
	return filepath.Join(r.dir, "agents", r.member.MembershipID, "capacity.json")
}
func (r *Repository) save(path string, v any) error {
	if r.uncertain {
		return ErrCommitUnknown
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(encoded) > MaxStateBytes {
		return &CapacityError{Scope: "metadata"}
	}
	err = r.persist.SaveInputJSON(path, v)
	if errors.Is(err, ErrCommitUnknown) {
		r.uncertain = true
	}
	return err
}
func capacityChecksum(s capacityState) string {
	s.Checksum = ""
	b, _ := json.Marshal(s)
	return Digest(b)
}
func rootChecksum(s rootState) string { s.Checksum = ""; b, _ := json.Marshal(s); return Digest(b) }
func (r *Repository) saveCapacity() error {
	if r.sharedDir != "" {
		return nil
	}
	s := r.capacity
	s.Checksum = capacityChecksum(s)
	return r.save(r.capacityPath(), s)
}
func (r *Repository) saveRoot(p string, s rootState) error {
	ranges := make([]SegmentRange, len(s.Segments))
	for i, seg := range s.Segments {
		ranges[i] = seg.bounds()
	}
	if _, err := ValidatePublication(s.Reclaimed, s.Latest, ranges, false); err != nil {
		return err
	}
	s.Checksum = rootChecksum(s)
	return r.save(filepath.Join(r.partDir(p), "partition.json"), s)
}
func (r *Repository) stop(point string) error {
	if r.fault != nil {
		if err := r.fault(point); err != nil {
			r.uncertain = true
			return errors.Join(ErrCommitUnknown, err)
		}
	}
	return nil
}
func readJSON(path string, v any, limit int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return err
	}
	if int64(len(b)) > limit {
		return ErrCorrupt
	}
	if err := StrictDecode(b, v); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrCorrupt, path, err)
	}
	return nil
}
func durableDir(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if err := durableDir(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return syncDir(parent)
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func encodeLine(rec StoredRecord) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(rec); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
func (r *Repository) load(provider string) (*partition, error) {
	c := r.codecs[provider]
	dir := r.partDir(provider)
	var s rootState
	err := readJSON(filepath.Join(dir, "partition.json"), &s, 16<<20)
	if errors.Is(err, os.ErrNotExist) {
		// Only an active durable reservation permits an unpublished segment tree.
		if op, ok := r.capacity.Pending[provider]; !ok || op.ExpectedRevision != 0 || op.BaseThrough != 0 {
			return nil, fmt.Errorf("%w: missing partition root", ErrCorrupt)
		}
		if err := r.cleanSegments(provider, nil); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if s.Checksum != rootChecksum(s) {
		return nil, ErrCorrupt
	}
	if s.Version != FormatVersion {
		return nil, ErrVersion
	}
	expected := r.partitionSpec(c.Descriptor())
	oldSpec := s.Spec
	oldSpec.Channels = expected.Channels
	if !reflect.DeepEqual(oldSpec, expected) || !allChannelsAllowed(s.Spec.Channels, expected.Channels) {
		return nil, ErrVersion
	}
	if len(s.ArchivedScopes) > s.Spec.Policy.MaxReceipts+1 {
		return nil, ErrCorrupt
	}
	if s.Revision == 0 || s.Latest < s.Reclaimed || s.Receipts == nil || len(s.Receipts) > s.Spec.Policy.MaxReceipts {
		return nil, ErrCorrupt
	}
	ranges := make([]SegmentRange, len(s.Segments))
	for i, seg := range s.Segments {
		ranges[i] = seg.bounds()
	}
	if _, err := ValidatePublication(s.Reclaimed, s.Latest, ranges, false); err != nil {
		return nil, err
	}
	p := &partition{state: s, records: map[uint64]StoredRecord{}, offsets: map[uint64]offset{}, projections: map[uint64]Projection{}, streamRefs: map[uint64]string{}, streamSeqs: map[string][]uint64{}}
	var previous uint64 = s.Reclaimed
	names := map[string]bool{}
	for _, seg := range s.Segments {
		if !segmentName(seg.Name) || names[seg.Name] || seg.Bytes <= 0 || seg.First <= previous || seg.Last < seg.First || seg.Last > s.Latest || seg.Bytes > int64(s.Spec.Policy.MaxBytes) {
			return nil, ErrCorrupt
		}
		names[seg.Name] = true
		path := filepath.Join(dir, "records", seg.Name)
		b, err := ReadSegment(path, seg.bounds(), int64(s.Spec.Policy.MaxBytes))
		if err != nil {
			return nil, fmt.Errorf("%w: retained segment: %v", ErrCorrupt, err)
		}
		// Every complete unpublished record is discarded as well.
		if err := TruncateSegment(path, seg.Bytes); err != nil {
			return nil, err
		}
		first := uint64(0)
		err = WalkSegment(b, s.Spec.Policy.MaxRecordBytes, func(line []byte, at int) error {
			var rec StoredRecord
			if StrictDecode(line, &rec) != nil {
				return ErrCorrupt
			}
			projection, err := r.validateRecord(provider, rec)
			if err != nil {
				return err
			}
			if rec.Location.Seq <= previous {
				return ErrCorrupt
			}
			if first == 0 {
				first = rec.Location.Seq
			}
			previous = rec.Location.Seq
			receipt, ok := s.Receipts[deliveryKey(rec.Channel, rec.DeliveryID)]
			if !ok || receipt.Seq != rec.Location.Seq || receipt.Digest != rec.DeliveryDigest {
				return ErrCorrupt
			}
			p.records[previous] = rec
			p.offsets[previous] = offset{seg.Name, int64(at), len(line) + 1}
			p.index(r.member, rec, projection)
			return nil
		})
		if err != nil {
			return nil, err
		}
		if first != seg.First || previous != seg.Last {
			return nil, ErrCorrupt
		}
	}
	for key, receipt := range s.Receipts {
		if !validKey(key) || receipt.Channel == "" || deliveryKey(receipt.Channel, receipt.Key) != key || receipt.Partition != s.Spec.Ref || receipt.Seq == 0 || receipt.Seq > s.Latest || receipt.Digest == "" || receipt.Operation == "" || !validSourceRef(receipt.SourceRef) {
			return nil, ErrCorrupt
		}
		if receipt.Seq > s.Reclaimed {
			if rec, ok := p.records[receipt.Seq]; !ok || deliveryKey(rec.Channel, rec.DeliveryID) != key {
				return nil, ErrCorrupt
			}
		}
	}
	// The root already removed reclaimed records. Complete physical deletion
	// before dropping the intent or exposing freed capacity.
	if err := r.cleanSegments(provider, s.Segments); err != nil {
		return nil, err
	}
	if s.Reclaim != nil && s.Reclaim.Committed {
		if s.Reclaim.Plan.Through != s.Reclaimed {
			return nil, ErrCorrupt
		}
		s.Reclaim = nil
		if err := r.saveRoot(provider, s); err != nil {
			return nil, err
		}
		p.state = s
	}
	return p, nil
}
func segmentName(n string) bool {
	return strings.HasSuffix(n, ".jsonl") && len(n) <= 100 && filepath.Base(n) == n && n != ".jsonl" && !strings.HasPrefix(n, ".")
}
func (r *Repository) cleanSegments(provider string, keep []segment) error {
	dir := filepath.Join(r.partDir(provider), "records")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		if len(keep) > 0 {
			return ErrCorrupt
		}
		return nil
	}
	if err != nil {
		return err
	}
	names := map[string]bool{}
	for _, s := range keep {
		names[s.Name] = true
	}
	for _, e := range entries {
		if e.IsDir() || (!segmentName(e.Name()) && !strings.HasPrefix(e.Name(), ".rewrite-")) {
			return ErrCorrupt
		}
		if !names[e.Name()] {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	return syncDir(dir)
}
func (r *Repository) validateRecord(provider string, rec StoredRecord) (Projection, error) {
	d := r.codecs[provider].Descriptor()
	spec := r.partitionSpec(d)
	if rec.Codec != d.Codec || rec.Schema != d.Schema || rec.Group.Version != d.GroupingVersion {
		return Projection{}, ErrVersion
	}
	if rec.Location.Partition != spec.Ref || rec.Location.Seq == 0 || !validKey(rec.DeliveryID) || !slices.Contains(d.Channels, rec.Channel) || rec.CreatedAt.IsZero() || rec.ReceivedAt.IsZero() || rec.DeliveryDigest == "" || rec.Digest != rec.checksum() {
		return Projection{}, ErrCorrupt
	}
	projection, err := decodeProjection(r.codecs[provider], rec.Schema, rec.Payload)
	if err != nil {
		return Projection{}, err
	}
	if projection.Validate() != nil || !reflect.DeepEqual(projection.Group, rec.Group) {
		return Projection{}, ErrCorrupt
	}
	if validator, ok := r.codecs[provider].(StoredRecordValidator); ok {
		if err := validator.ValidateStoredRecord(rec); err != nil {
			return Projection{}, err
		}
	}
	return projection, nil
}
func (p *partition) index(owner Member, rec StoredRecord, projection Projection) {
	seq := rec.Location.Seq
	p.sequence = append(p.sequence, seq)
	p.bytes += uint64(p.offsets[seq].Length)
	p.projections[seq] = projection
	stream := rec.Group.StreamRef(owner, p.state.Spec.Provider, rec.Channel)
	p.streamRefs[seq] = stream
	p.streamSeqs[stream] = append(p.streamSeqs[stream], seq)
}
func cloneRoot(s rootState) rootState {
	s.Segments = slices.Clone(s.Segments)
	s.ArchivedScopes = slices.Clone(s.ArchivedScopes)
	s.Receipts = cloneMap(s.Receipts)
	if s.Shared != nil {
		b, _ := json.Marshal(s.Shared)
		s.Shared = new(SharedRoot)
		_ = json.Unmarshal(b, s.Shared)
	}
	return s
}
func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
func (r *Repository) usageLocked() Usage {
	var u Usage
	for _, p := range r.parts {
		u.Records += uint64(len(p.records))
		u.Bytes += p.bytes
	}
	return u
}
func (r *Repository) InspectUsage() (Usage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return Usage{}, ErrCommitUnknown
	}
	return r.usageLocked(), nil
}

type CapacityError struct {
	Scope, Partition string
	FirstProtected   uint64
}

func (e *CapacityError) Error() string {
	return fmt.Sprintf("%s: %s %s first_protected=%d", ErrCapacity, e.Scope, e.Partition, e.FirstProtected)
}
func (*CapacityError) Unwrap() error { return ErrCapacity }
func (r *Repository) AppendOnce(provider string, q AppendRequest) (Receipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sharedDir != "" {
		defer delete(r.capacity.Pending, provider)
	}
	if r.uncertain {
		return Receipt{}, ErrCommitUnknown
	}
	c := r.codecs[provider]
	if c == nil {
		return Receipt{}, ErrUnsupported
	}
	d := c.Descriptor()
	spec := r.partitionSpec(d)
	if len(q.Payload) > spec.Policy.MaxRecordBytes {
		return Receipt{}, &CapacityError{Scope: "record", Partition: spec.Ref}
	}
	if !validKey(q.DeliveryID) || !validSourceRef(q.SourceRef) || !slices.Contains(d.Channels, q.Channel) || q.CreatedAt.IsZero() || q.ReceivedAt.IsZero() || !json.Valid(q.Payload) {
		return Receipt{}, ErrInvalid
	}
	projection, err := decodeProjection(c, d.Schema, q.Payload)
	if err != nil {
		return Receipt{}, err
	}
	if err := projection.Validate(); err != nil {
		return Receipt{}, err
	}
	// The adapter supplies the immutable delivery digest; presentation snapshots
	// may vary without changing it. The payload's own checksum is always checked.
	if q.DeliveryDigest == "" {
		q.DeliveryDigest = Digest(q.Payload)
	}
	p := r.parts[provider]
	if p != nil {
		if previous, ok := p.state.Receipts[deliveryKey(q.Channel, q.DeliveryID)]; ok {
			if previous.Digest != q.DeliveryDigest || q.SourceRef != "" && previous.SourceRef != q.SourceRef {
				return Receipt{}, ErrConflict
			}
			return previous, nil
		}
	}
	if p != nil && p.state.Reclaim != nil {
		return Receipt{}, ErrConflict
	}
	seq := q.Seq
	if seq == 0 {
		seq = 1
		if p != nil {
			seq = p.state.Latest + 1
		}
	}
	rec := StoredRecord{Location: Location{spec.Ref, seq}, DeliveryID: q.DeliveryID, Channel: q.Channel, Codec: d.Codec, Schema: d.Schema, Group: projection.Group, CreatedAt: q.CreatedAt.UTC(), ReceivedAt: q.ReceivedAt.UTC(), Payload: bytes.Clone(q.Payload), DeliveryDigest: q.DeliveryDigest}
	if r.sharedDir != "" {
		if p == nil || p.state.Shared == nil {
			return Receipt{}, ErrIdentity
		}
		for _, g := range p.state.Shared.Grants {
			if g.Status == "active" {
				rec.Recipients = append(rec.Recipients, g.ID)
			}
		}
		sort.Strings(rec.Recipients)
		if len(rec.Recipients) == 0 {
			return Receipt{}, ErrBlocked
		}
	}
	rec.Digest = rec.checksum()
	if validator, ok := c.(StoredRecordValidator); ok {
		if err := validator.ValidateStoredRecord(rec); err != nil {
			return Receipt{}, err
		}
	}
	line, err := encodeLine(rec)
	if err != nil {
		return Receipt{}, err
	}
	if len(line) > spec.Policy.MaxRecordBytes {
		return Receipt{}, &CapacityError{Scope: "record", Partition: spec.Ref}
	}
	op, exists := r.capacity.Pending[provider]
	if exists {
		if op.Record.DeliveryID != rec.DeliveryID || op.Record.Channel != rec.Channel || op.Record.DeliveryDigest != rec.DeliveryDigest || q.SourceRef != "" && q.SourceRef != op.SourceRef {
			return Receipt{}, ErrConflict
		}
		// Preserve the originally reserved sequence, payload and receive time.
		rec = op.Record
		projection, err = decodeProjection(c, rec.Schema, rec.Payload)
		if err != nil {
			return Receipt{}, err
		}
		line, _ = encodeLine(rec)
		seq = rec.Location.Seq
	} else {
		// Bound independent dedup/scope evidence before reserving or writing body.
		// Leave room for the new segment and reclaim intent in the 16 MiB root.
		probe := rootState{Spec: spec, Receipts: map[string]Receipt{}}
		if p != nil {
			probe = cloneRoot(p.state)
			probe.Spec = spec
		}
		probe.Receipts[deliveryKey(rec.Channel, rec.DeliveryID)] = Receipt{Partition: spec.Ref, Seq: seq, Key: rec.DeliveryID, Digest: rec.DeliveryDigest, Operation: Ref("append_", spec.Ref, rec.DeliveryID, rec.DeliveryDigest, seq), Channel: rec.Channel, SourceRef: q.SourceRef}
		probe.ArchivedScopes = nil
		encoded, err := json.Marshal(probe)
		if err != nil {
			return Receipt{}, err
		}
		if len(encoded) > MaxStateBytes-(1<<20) {
			return Receipt{}, &CapacityError{Scope: "metadata", Partition: spec.Ref}
		}
		if seq == 0 || p != nil && seq <= p.state.Latest {
			return Receipt{}, ErrConflict
		}
		var partUsage Usage
		first := seq
		if p != nil {
			partUsage.Records = uint64(len(p.records))
			partUsage.Bytes = p.bytes
			if len(p.sequence) > 0 {
				first = p.sequence[0]
			}
			if len(p.state.Receipts) >= spec.Policy.MaxReceipts {
				return Receipt{}, &CapacityError{Scope: "receipts", Partition: spec.Ref, FirstProtected: first}
			}
		}
		if partUsage.Records+1 > spec.Policy.MaxRecords || partUsage.Bytes+uint64(len(line)) > spec.Policy.MaxBytes {
			return Receipt{}, &CapacityError{"partition", spec.Ref, first}
		}
		total := r.usageLocked()
		for _, pending := range r.capacity.Pending {
			total.Records++
			total.Bytes += pending.Bytes
		}
		if total.Records+1 > r.capacity.Limits.Records || total.Bytes+uint64(len(line)) > r.capacity.Limits.Bytes {
			return Receipt{}, &CapacityError{"member", spec.Ref, first}
		}
		op = reservation{Record: rec, Bytes: uint64(len(line)), SourceRef: q.SourceRef, Operation: Ref("append_", spec.Ref, rec.DeliveryID, rec.DeliveryDigest, seq)}
		if p != nil {
			op.ExpectedRevision = p.state.Revision
			op.BaseThrough = p.state.Latest
		}
		r.capacity.Pending[provider] = op
		if err := r.saveCapacity(); err != nil {
			if !r.uncertain {
				delete(r.capacity.Pending, provider)
			}
			return Receipt{}, err
		}
		if err := r.stop("reserved"); err != nil {
			return Receipt{}, err
		}
	}
	if p == nil {
		p = &partition{state: rootState{Version: FormatVersion, Spec: spec, Receipts: map[string]Receipt{}}, records: map[uint64]StoredRecord{}, offsets: map[uint64]offset{}, projections: map[uint64]Projection{}, streamRefs: map[uint64]string{}, streamSeqs: map[string][]uint64{}}
	}
	next := cloneRoot(p.state)
	next.Spec = spec
	dir := filepath.Join(r.partDir(provider), "records")
	if err := durableDir(dir); err != nil {
		return Receipt{}, err
	}
	var seg segment
	if n := len(next.Segments); n > 0 && next.Segments[n-1].Bytes+int64(len(line)) <= r.segmentBytes {
		seg = next.Segments[n-1]
		next.Segments = next.Segments[:n-1]
	} else {
		seg = segment{Name: fmt.Sprintf("%020d.jsonl", seq), First: seq}
	}
	path := filepath.Join(dir, seg.Name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Receipt{}, err
	}
	// A previous failed append may have left a short or complete unpublished tail.
	if err = f.Truncate(seg.Bytes); err == nil {
		_, err = f.Seek(seg.Bytes, io.SeekStart)
	}
	if err != nil {
		return Receipt{}, errors.Join(err, f.Close())
	}
	if err := WriteSegment(f, line); err != nil {
		return Receipt{}, err
	}
	if err = syncDir(dir); err != nil {
		return Receipt{}, err
	}
	if err = r.stop("body_synced"); err != nil {
		return Receipt{}, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Receipt{}, err
	}
	start := seg.Bytes
	seg.Bytes += int64(len(line))
	seg.Last = seq
	seg.Hash = Digest(b)
	next.Segments = append(next.Segments, seg)
	next.Latest = seq
	next.Revision++
	receipt := Receipt{Partition: spec.Ref, Seq: seq, Key: rec.DeliveryID, Digest: rec.DeliveryDigest, Operation: op.Operation, Channel: rec.Channel, SourceRef: op.SourceRef}
	next.Receipts[deliveryKey(rec.Channel, rec.DeliveryID)] = receipt
	// Scope metadata was derived by the historical query contract. Existing
	// v1 roots include it in their checksum; new publications omit it.
	next.ArchivedScopes = nil
	if err := r.saveRoot(provider, next); err != nil {
		return Receipt{}, err
	}
	if err := r.stop("published"); err != nil {
		return Receipt{}, err
	}
	p.state = next
	p.records[seq] = rec
	p.offsets[seq] = offset{seg.Name, start, len(line)}
	p.index(r.member, rec, projection)
	r.parts[provider] = p
	delete(r.capacity.Pending, provider)
	if err := r.saveCapacity(); err != nil {
		r.uncertain = true
		return Receipt{}, fmt.Errorf("%w: capacity settlement: %v", ErrCommitUnknown, err)
	}
	if err := r.stop("settled"); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}
func (r *Repository) Snapshot(provider string) (Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return Snapshot{}, ErrCommitUnknown
	}
	c := r.codecs[provider]
	if c == nil {
		return Snapshot{}, ErrUnsupported
	}
	if p := r.parts[provider]; p != nil {
		return snapshot(p), nil
	}
	return Snapshot{Partition: r.partitionSpec(c.Descriptor()).Ref}, nil
}
func snapshot(p *partition) Snapshot {
	return Snapshot{p.state.Spec.Ref, p.state.Revision, p.state.Latest, p.state.Reclaimed}
}
func (r *Repository) partFor(s Snapshot) (*partition, error) {
	for _, p := range r.parts {
		if p.state.Spec.Ref == s.Partition {
			if s.Revision > p.state.Revision || s.Through > p.state.Latest || s.Reclaimed != p.state.Reclaimed {
				return nil, ErrExpired
			}
			return p, nil
		}
	}
	return nil, ErrIdentity
}
func (r *Repository) Scan(s Snapshot) ([]Location, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return nil, ErrCommitUnknown
	}
	p, err := r.partFor(s)
	if err != nil && s.Revision == 0 && s.Through == 0 && s.Reclaimed == 0 {
		for _, c := range r.codecs {
			if r.partitionSpec(c.Descriptor()).Ref == s.Partition {
				return []Location{}, nil
			}
		}
	}
	if err != nil {
		return nil, err
	}
	out := []Location{}
	for n := range p.records {
		if n <= s.Through {
			out = append(out, Location{s.Partition, n})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}
func (r *Repository) Read(s Snapshot, l Location) (StoredRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return StoredRecord{}, ErrCommitUnknown
	}
	p, err := r.partFor(s)
	if err != nil {
		return StoredRecord{}, err
	}
	if l.Partition != s.Partition || l.Seq > s.Through {
		return StoredRecord{}, ErrIdentity
	}
	if l.Seq <= p.state.Reclaimed {
		return StoredRecord{}, ErrExpired
	}
	o, ok := p.offsets[l.Seq]
	if !ok {
		return StoredRecord{}, ErrCorrupt
	}
	f, err := os.Open(filepath.Join(r.partDir(p.state.Spec.Provider), "records", o.Segment))
	if err != nil {
		return StoredRecord{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	defer f.Close()
	b := make([]byte, o.Length)
	if _, err := f.ReadAt(b, o.Offset); err != nil {
		return StoredRecord{}, ErrCorrupt
	}
	var rec StoredRecord
	if StrictDecode(b, &rec) != nil || rec.Location != l {
		return StoredRecord{}, ErrCorrupt
	}
	if rec.Digest != p.records[l.Seq].Digest || rec.Digest != rec.checksum() {
		return StoredRecord{}, ErrCorrupt
	}
	return rec, nil
}

func deliveryKey(channel, id string) string { return Ref("delivery_", channel, id) }

func allChannelsAllowed(saved, current []string) bool {
	for _, c := range saved {
		if !slices.Contains(current, c) {
			return false
		}
	}
	return true
}

// Only derived display metadata is bounded here; immutable payload is retained.
func decodeProjection(c Codec, schema int, b json.RawMessage) (Projection, error) {
	p, err := c.Decode(schema, b)
	if err != nil {
		return Projection{}, err
	}
	var cut bool
	p.Label, cut = boundedText(p.Label, 1024)
	p.Incomplete = p.Incomplete || cut
	p.SourceLabel, cut = boundedText(p.SourceLabel, 1024)
	p.Incomplete = p.Incomplete || cut

	return p, nil
}

// Shared body namespaces are independent of member names, including a private
// Membership whose text happens to equal the shared Inbox ID.
func (r *Repository) partitionSpec(d Descriptor) PartitionSpec {
	spec := d.Spec(r.member)
	if r.sharedDir != "" {
		spec.Ref = Ref("partition_", "inbox", r.sharedInbox, d.Provider)
		spec.QuotaScope = Ref("quota_", "inbox", r.sharedInbox)
	}
	return spec
}
