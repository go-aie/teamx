package inputstream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const PlatformReadLimit = 25
const MaxReadPageBytes = 1 << 20
const MaxReadSessions = 256
const MaxConsumptionBytes = 4 << 20

var ErrBusy = errors.New("input_page_pending")
var ErrBlocked = errors.New("input_source_blocked")

// Execution is supplied by the member lifecycle, never by tool arguments.
type Execution struct {
	Generation string `json:"generation"`
	Turn       uint64 `json:"turn"`
}
type ReadRequest struct {
	Turn   uint64 `json:"turn_number"`
	Stream string `json:"stream_ref,omitempty"`
	ID     string `json:"read_id,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

func DecodeRead(data []byte) (ReadRequest, error) {
	var q ReadRequest
	if err := ValidateRequest(data); err != nil {
		return q, err
	}
	if err := StrictDecode(data, &q); err != nil {
		return q, err
	}
	if q.Turn == 0 || len(q.Cursor) > 128 || len(q.Stream) > 128 || (q.ID != "" && strings.TrimSpace(q.ID) == "") {
		return q, ErrInvalid
	}
	if q.Cursor != "" {
		if q.ID != "" || q.Stream != "" || q.Limit != 0 {
			return q, ErrConflict
		}
	} else if !validKey(q.ID) || len(q.ID) > 128 || !validKey(q.Stream) || q.Limit < 0 || q.Limit > MaxRecords {
		return q, ErrInvalid
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	for k, v := range fields {
		if string(v) == `null` || (k == "limit" && q.Limit == 0) || (k == "cursor" && q.Cursor == "") {
			return q, ErrInvalid
		}
	}
	if q.Cursor != "" && len(fields) != 2 {
		return q, ErrConflict
	}
	return q, nil
}

type MaterialMemo struct {
	Status, Reason, Source string
	Positions              []int
}
type MaterialUsage struct {
	Scans, Images int
	Bytes, Nanos  int64
}
type ReadAttempt struct {
	Shared           *SharedPage   `json:"shared,omitempty"`
	ReceiptRequired  bool          `json:"receipt_required,omitempty"`
	Received         bool          `json:"received,omitempty"`
	ResultBytes      int           `json:"result_bytes,omitempty"`
	Kind             string        `json:"kind,omitempty"`
	Remaining        int           `json:"remaining,omitempty"`
	MaterialReserve  MaterialUsage `json:"material_reserve,omitzero"`
	Limit            int           `json:"page_limit"`
	ID               string        `json:"page_id"`
	Request          ReadRequest   `json:"request"`
	Execution        Execution     `json:"execution"`
	Stream           string        `json:"stream"`
	Snapshot         Snapshot      `json:"snapshot"`
	Entries          []Location    `json:"entries"`
	From             uint64        `json:"from"`
	Through          uint64        `json:"through"`
	Phase            string        `json:"phase"`
	Failure          string        `json:"failure,omitempty"`
	Next             string        `json:"next,omitempty"`
	HasMore          bool          `json:"has_more"`
	ResultDigest     string        `json:"result_digest,omitempty"`
	MaterialsClaimed bool          `json:"materials_claimed,omitempty"`
}

func (a ReadAttempt) Key() string {
	if a.Request.Cursor != "" {
		return a.Request.Cursor
	}
	return a.Request.ID
}
func (a ReadAttempt) Proof() string {
	return Ref("confirmation_", a.ID, a.Execution, a.Stream, a.Snapshot, a.From, a.Through, a.Entries)
}

type LastPage struct {
	Stream    string     `json:"stream"`
	Entries   []Location `json:"entries"`
	PageID    string     `json:"page_id"`
	Confirmed bool       `json:"confirmed"`
}
type CoordinatorState struct {
	External       map[string]ExternalObservation `json:"external,omitempty"`
	SharedIntent   *SharedReadIntent              `json:"shared_intent,omitempty"`
	Calls          map[string]CallBinding         `json:"calls,omitempty"`
	MaterialSeen   map[string]MaterialMemo        `json:"material_seen,omitempty"`
	Closed         bool                           `json:"closed,omitempty"`
	DiscoveryBytes int
	// Decoded only for existing coordinator checksums; v3 has no preview budget.
	ArchivedPreviewBytes int                 `json:"PreviewBytes"`
	Stall                *InputStall         `json:"stall,omitempty"`
	Version              int                 `json:"version"`
	Owner                Member              `json:"owner"`
	Execution            Execution           `json:"execution"`
	Bounds               map[string]Snapshot `json:"bounds"`
	Used                 int                 `json:"used"`
	Attempts             []ReadAttempt       `json:"attempts"`
	Last                 *LastPage           `json:"last_platform_page,omitempty"`
	// Conservative durable material reservations survive preparation crashes.
	MaterialScans, MaterialImages int
	MaterialBytes                 int64
	MaterialNanos                 int64
	Checksum                      string `json:"checksum"`
}
type consumptionState struct {
	Version   int               `json:"version"`
	Owner     Member            `json:"owner"`
	Partition string            `json:"partition"`
	Revision  uint64            `json:"revision"`
	Through   map[string]uint64 `json:"through"`
	Skipped   map[uint64]string `json:"skipped,omitempty"`
	Proof     string            `json:"last_confirmation,omitempty"`
	Checksum  string            `json:"checksum"`
}

func (r *Repository) coordinatorPath() string {
	return filepath.Join(r.dir, "agents", r.member.MembershipID, "input-state.json")
}
func (r *Repository) consumptionPath(provider string) string {
	return filepath.Join(r.partDir(provider), "consumption.json")
}
func checksumState(v any) string { b, _ := json.Marshal(v); return Digest(b) }
func (r *Repository) loadConsumption(provider string) (consumptionState, error) {
	p := r.parts[provider]
	if p == nil {
		return consumptionState{}, ErrIdentity
	}
	s := consumptionState{Version: 2, Owner: r.member, Partition: p.state.Spec.Ref, Through: map[string]uint64{}, Skipped: map[uint64]string{}}
	err := readJSON(r.consumptionPath(provider), &s, MaxConsumptionBytes)
	if errors.Is(err, os.ErrNotExist) {
		// The durable coordinator precedes every consumption commit. A missing
		// authority after any such commit must never reset already consumed input.
		if p.state.Consumption {
			return s, ErrCorrupt
		}
		if r.coordinator != nil {
			for _, a := range r.coordinator.Attempts {
				if a.Snapshot.Partition == s.Partition && a.Phase == "settled" && len(a.Entries) > 0 {
					return s, ErrCorrupt
				}
			}
		}
		return s, nil
	}
	if err != nil {
		return s, err
	}
	sum := s.Checksum
	s.Checksum = ""
	if sum != checksumState(s) || s.Version != 2 || s.Owner != r.member || s.Partition != p.state.Spec.Ref || s.Through == nil || len(s.Through) > MaxRecords || s.Revision == 0 {
		return s, ErrCorrupt
	}
	s.Checksum = sum
	for _, n := range s.Through {
		if n > p.state.Latest {
			return s, ErrCorrupt
		}
	}
	for n, reason := range s.Skipped {
		if n > p.state.Latest || reason == "" {
			return s, ErrCorrupt
		}
	}
	// Reclaimed checkpoints carry no retained progress. Compact them without
	// depending on the current delivery phase.
	if reclaimed := p.state.Reclaimed; reclaimed > 0 {
		for stream, through := range s.Through {
			if through <= reclaimed {
				delete(s.Through, stream)
			}
		}
		for seq := range s.Skipped {
			if seq <= reclaimed {
				delete(s.Skipped, seq)
			}
		}
	}
	return s, nil
}
func (r *Repository) loadCoordinator() error {
	var s CoordinatorState
	err := readJSON(r.coordinatorPath(), &s, MaxStateBytes)
	if errors.Is(err, os.ErrNotExist) {
		for _, p := range r.parts {
			if p.state.Consumption && !p.state.LocalOnly {
				return ErrCorrupt
			}
		}
		return nil
	}
	if err != nil {
		return err
	}
	sum := s.Checksum
	s.Checksum = ""
	if sum != checksumState(s) || s.Version != 1 || s.Owner != r.member || s.Execution.Turn == 0 || s.Execution.Generation == "" || s.Bounds == nil || s.Used < 0 || s.Used > PlatformReadLimit || len(s.Attempts) > MaxReadSessions {
		return ErrCorrupt
	}
	active := 0
	if s.SharedIntent != nil {
		q := s.SharedIntent
		if q.Page.Member != s.Owner || q.Page.Execution != s.Execution || q.Read.Turn != s.Execution.Turn || q.Page.Page != Ref("page_", s.Owner, s.Execution, q.Read.ID) || q.Page.Limit < 0 || q.Page.Limit > PlatformReadLimit-s.Used || q.Page.Grant == "" || q.Page.Stream == "" || q.Page.Partition == "" {
			return ErrCorrupt
		}
		active++
	}
	used := 0
	keys := map[string]bool{}
	for _, a := range s.Attempts {
		if a.Kind != "" && a.Kind != "room" && a.Kind != "shared" {
			return ErrCorrupt
		}
		if a.ID == "" || a.Execution != s.Execution || a.Request.Turn != s.Execution.Turn || keys[a.Key()] {
			return ErrCorrupt
		}
		keys[a.Key()] = true
		switch a.Phase {
		case "reserved", "prepared", "awaiting_confirmation":
			active++
		case "settled":
		case "failed_before_output":
		default:
			return ErrCorrupt
		}
		if a.Phase != "failed_before_output" && a.Kind != "room" {
			used += len(a.Entries)
		}
		if (a.Kind != "room" && len(a.Entries) > PlatformReadLimit || a.Kind == "room" && len(a.Entries) > 100) || a.From > a.Through || a.Through > a.Snapshot.Through {
			return ErrCorrupt
		}
		if err := ValidateReadRange(a.Snapshot.Partition, a.From, a.Through, a.Snapshot.Through, a.Entries); err != nil {
			return ErrCorrupt
		}
	}
	if active > 1 || used != s.Used || (s.Closed && active > 0) || s.DiscoveryBytes < 0 || s.DiscoveryBytes > 128<<10 {
		return ErrCorrupt
	}
	s.Checksum = sum
	r.coordinator = &s
	return nil
}
func (r *Repository) saveCoordinator(s CoordinatorState) error {
	s.Checksum = ""
	s.Checksum = checksumState(s)
	if err := r.save(r.coordinatorPath(), s); err != nil {
		return err
	}
	r.coordinator = &s
	return nil
}
func (r *Repository) stateCopy() CoordinatorState {
	b, _ := json.Marshal(r.coordinator)
	var s CoordinatorState
	_ = json.Unmarshal(b, &s)
	return s
}
func (r *Repository) execution(e Execution) error {
	if r.uncertain {
		return ErrCommitUnknown
	}
	if r.coordinator == nil || r.coordinator.Execution != e {
		return ErrIdentity
	}
	return nil
}

// Begin freezes all existing partitions. Only an authorized lifecycle may start
// a newer execution; old requests cannot interpret a new turn's bounds.
func (r *Repository) Begin(e Execution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return ErrCommitUnknown
	}
	if e.Turn == 0 || !validKey(e.Generation) {
		return ErrInvalid
	}
	if r.coordinator != nil && r.coordinator.Execution == e {
		return r.initializeConsumption()
	}
	if r.coordinator != nil && (r.coordinator.SharedIntent != nil || activeAttempt(*r.coordinator) != nil && activeAttempt(*r.coordinator).Kind == "shared") {
		return ErrBusy
	}
	if r.coordinator != nil && e.Turn <= r.coordinator.Execution.Turn {
		return ErrIdentity
	}
	s := CoordinatorState{Version: 1, Owner: r.member, Execution: e, Bounds: map[string]Snapshot{}, Attempts: []ReadAttempt{}}
	if r.coordinator != nil {
		s.Stall = r.coordinator.Stall
		s.Last = r.coordinator.Last
		if a := activeAttempt(*r.coordinator); a != nil && a.Kind == "" && len(a.Entries) > 0 {
			s.Last = &LastPage{a.Stream, append([]Location(nil), a.Entries...), a.ID, false}
		}
	}
	for provider, p := range r.parts {
		s.Bounds[provider] = snapshot(p)
	}
	if err := r.saveCoordinator(s); err != nil {
		return err
	}
	if err := r.initializeConsumption(); err != nil {
		return err
	}
	// Old execution tokens are invalid before deleting their result cache.
	entries, err := os.ReadDir(filepath.Dir(r.resultPath("unused")))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			if err := os.Remove(filepath.Join(filepath.Dir(r.resultPath("unused")), entry.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
func (r *Repository) initializeConsumption() error {
	for provider, p := range r.parts {
		if p.state.Consumption {
			if p.state.LocalOnly {
				next := cloneRoot(p.state)
				next.LocalOnly = false
				if err := r.saveRoot(provider, next); err != nil {
					return err
				}
				p.state = next
			}
			continue
		}
		c, err := r.loadConsumption(provider)
		if err != nil {
			return err
		}
		if c.Revision == 0 {
			c.Revision = 1
		}
		c.Checksum = ""
		c.Checksum = checksumState(c)
		if err := r.save(r.consumptionPath(provider), c); err != nil {
			return err
		}
		next := cloneRoot(p.state)
		next.Consumption = true
		if err := r.saveRoot(provider, next); err != nil {
			return err
		}
		p.state = next
	}
	return nil
}
func (r *Repository) State(e Execution) (CoordinatorState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return CoordinatorState{}, err
	}
	return r.stateCopy(), nil
}
func activeAttempt(s CoordinatorState) *ReadAttempt {
	for i := range s.Attempts {
		a := &s.Attempts[i]
		if a.Phase != "settled" && a.Phase != "failed_before_output" {
			return a
		}
	}
	return nil
}
func (r *Repository) Reserve(e Execution, q ReadRequest) (ReadAttempt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return ReadAttempt{}, err
	}
	if q.Turn != e.Turn {
		return ReadAttempt{}, ErrIdentity
	}
	b, _ := json.Marshal(q)
	if _, err := DecodeRead(b); err != nil {
		return ReadAttempt{}, err
	}
	s := r.stateCopy()
	key := q.ID
	if q.Cursor != "" {
		key = q.Cursor
	}
	for _, a := range s.Attempts {
		if a.Key() == key {
			if a.Request != q {
				return a, ErrConflict
			}
			return a, nil
		}
	}
	if s.Closed {
		return ReadAttempt{}, ErrIdentity
	}
	if activeAttempt(s) != nil || s.SharedIntent != nil {
		return ReadAttempt{}, ErrBusy
	}
	if len(s.Attempts) >= MaxReadSessions {
		return ReadAttempt{}, ErrCapacity
	}
	limit := q.Limit
	if limit == 0 {
		limit = PlatformReadLimit
	}
	limit = min(limit, PlatformReadLimit)
	stream := q.Stream
	if q.Cursor != "" {
		found := false
		for _, a := range s.Attempts {
			if a.Next == q.Cursor && a.Phase == "settled" {
				stream = a.Stream
				limit = a.Limit
				if limit == 0 {
					limit = PlatformReadLimit
				}
				found = true
				break
			}
		}
		if !found {
			return ReadAttempt{}, ErrExpired
		}
	}
	var p *partition
	provider := ""
	for name, part := range r.parts {
		if len(part.streamSeqs[stream]) != 0 {
			p, provider = part, name
			break
		}
	}
	if p == nil {
		return ReadAttempt{}, ErrIdentity
	}
	bound, ok := s.Bounds[provider]
	if !ok {
		return ReadAttempt{}, ErrExpired
	}
	c, err := r.loadConsumption(provider)
	if err != nil {
		return ReadAttempt{}, err
	}
	if _, known := c.Through[stream]; !known && len(c.Through) >= MaxRecords {
		return ReadAttempt{}, &CapacityError{Scope: "checkpoints"}
	}
	a := ReadAttempt{ID: Ref("page_", r.member, e, key), Request: q, Execution: e, Stream: stream, Snapshot: bound, From: max(c.Through[stream], bound.Reclaimed), Phase: "reserved", Entries: []Location{}, Limit: limit}
	a.Through = a.From
	limit = min(limit, PlatformReadLimit-s.Used)
	pageBytes := 0
	for _, seq := range p.streamSeqs[stream] {
		if seq > bound.Through {
			break
		}
		if seq <= a.From || c.Skipped[seq] != "" {
			continue
		}
		rec := p.records[seq]
		if p.projections[seq].Blocked != "" {
			return a, ErrBlocked
		}
		if a.HasMore || len(a.Entries) == limit || pageBytes+p.offsets[seq].Length > MaxReadPageBytes {
			a.HasMore = true
			a.Remaining++
			continue
		}
		a.Entries = append(a.Entries, rec.Location)
		pageBytes += p.offsets[seq].Length
		a.Through = seq
	}
	if a.HasMore && s.Used+len(a.Entries) < PlatformReadLimit {
		a.Next = Ref("read_cursor_", a.ID, a.Through)
	}
	s.Used += len(a.Entries)
	s.Attempts = append(s.Attempts, a)
	if err := r.saveCoordinator(s); err != nil {
		return a, err
	}
	return a, r.stop("page_reserved")
}
func (r *Repository) attempt(e Execution, id string) (CoordinatorState, int, error) {
	if err := r.execution(e); err != nil {
		return CoordinatorState{}, 0, err
	}
	s := r.stateCopy()
	for i := range s.Attempts {
		if s.Attempts[i].ID == id {
			return s, i, nil
		}
	}
	return s, 0, ErrExpired
}
func (r *Repository) resultPath(id string) string {
	return filepath.Join(r.dir, "agents", r.member.MembershipID, "input-results", id+".json")
}
func (r *Repository) Prepare(e Execution, id string, result json.RawMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.prepareResult(e, id, result, nil, nil)
}
func (r *Repository) PrepareMaterial(e Execution, id string, result json.RawMessage, usage *MaterialUsage, memo ...map[string]MaterialMemo) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var seen map[string]MaterialMemo
	if len(memo) > 1 {
		return ErrInvalid
	}
	if len(memo) == 1 {
		seen = memo[0]
	}
	return r.prepareResult(e, id, result, usage, seen)
}
func (r *Repository) prepareResult(e Execution, id string, result json.RawMessage, usage *MaterialUsage, memo map[string]MaterialMemo) error {
	s, i, err := r.attempt(e, id)
	if err != nil {
		return err
	}
	a := &s.Attempts[i]
	if a.Phase != "reserved" {
		return ErrConflict
	}
	if !json.Valid(result) || len(result) > MaxStateBytes-(128<<10) {
		return ErrCapacity
	}
	total := len(result)
	for _, prior := range s.Attempts {
		if prior.ID != id {
			total += prior.ResultBytes
		}
	}
	if total > 512<<20 {
		return ErrCapacity
	}
	if err := r.save(r.resultPath(id), compactJSON(result)); err != nil {
		return err
	}
	if err := r.stop("page_result_saved"); err != nil {
		return err
	}
	if usage != nil {
		reserved := a.MaterialReserve
		if !a.MaterialsClaimed || usage.Scans < 0 || usage.Scans > reserved.Scans || usage.Images < 0 || usage.Images > reserved.Images || usage.Bytes < 0 || usage.Bytes > reserved.Bytes || usage.Nanos < 0 || usage.Nanos > reserved.Nanos {
			return ErrInvalid
		}
		s.MaterialScans += usage.Scans - reserved.Scans
		s.MaterialImages += usage.Images - reserved.Images
		s.MaterialBytes += usage.Bytes - reserved.Bytes
		s.MaterialNanos += usage.Nanos - reserved.Nanos
		a.MaterialReserve = *usage
	}
	if len(memo) > 100 {
		return ErrCapacity
	}
	if s.MaterialSeen == nil {
		s.MaterialSeen = map[string]MaterialMemo{}
	}
	for key, value := range memo {
		s.MaterialSeen[key] = value
	}
	if len(s.MaterialSeen) > 100 {
		return ErrCapacity
	}
	a.ResultBytes = len(result)
	a.ResultDigest = Digest(compactJSON(result))
	a.Phase = "prepared"
	if err := r.saveCoordinator(s); err != nil {
		return err
	}
	return r.stop("page_prepared")
}
func (r *Repository) Result(e Execution, id string) (json.RawMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, i, err := r.attempt(e, id)
	if err != nil {
		return nil, err
	}
	a := s.Attempts[i]
	if a.ResultDigest == "" {
		return nil, ErrExpired
	}
	var result json.RawMessage
	if err := readJSON(r.resultPath(id), &result, MaxStateBytes); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrExpired
		}
		return nil, err
	}
	// Persistence may pretty-print the JSON; compare its canonical representation.
	result = compactJSON(result)
	if Digest(result) != a.ResultDigest {
		return nil, ErrCorrupt
	}
	return result, nil
}
func compactJSON(b json.RawMessage) json.RawMessage {
	var out bytes.Buffer
	if json.Compact(&out, b) != nil {
		return b
	}
	return out.Bytes()
}
func (r *Repository) Handoff(e Execution, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, i, err := r.attempt(e, id)
	if err != nil {
		return err
	}
	a := &s.Attempts[i]
	if a.Phase == "settled" || a.Phase == "awaiting_confirmation" {
		return nil
	}
	if a.Phase != "prepared" {
		return ErrConflict
	}
	a.Phase = "awaiting_confirmation"
	if _, bound := s.Calls[a.Request.ID]; bound {
		a.ReceiptRequired = true
	}
	if err := r.saveCoordinator(s); err != nil {
		return err
	}
	return r.stop("page_handoff")
}

// Abort is server-only and may be called solely for a proven local source
// failure. Handoff and Abort serialize under the same repository mutex.
func (r *Repository) Abort(e Execution, id, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, i, err := r.attempt(e, id)
	if err != nil {
		return err
	}
	a := &s.Attempts[i]
	if a.Phase == "failed_before_output" {
		return nil
	}
	if (a.Phase != "reserved" && a.Phase != "prepared") || !validKey(reason) {
		return ErrBusy
	}
	a.Phase = "failed_before_output"
	a.Failure = reason
	if a.Kind != "room" {
		s.Used -= len(a.Entries)
	}
	if err := r.saveCoordinator(s); err != nil {
		return err
	}
	return r.stop("page_aborted")
}

// Confirm commits native consumption after the trusted delivery boundary.
func (r *Repository) Confirm(e Execution, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, i, err := r.attempt(e, id)
	if err != nil {
		return err
	}
	a := &s.Attempts[i]
	if a.Phase == "settled" {
		return nil
	}
	if a.Phase != "awaiting_confirmation" {
		return ErrBusy
	}
	if a.Kind != "" {
		return ErrIdentity
	}
	provider := ""
	for name, p := range r.parts {
		if p.state.Spec.Ref == a.Snapshot.Partition {
			provider = name
		}
	}
	c, err := r.loadConsumption(provider)
	if err != nil {
		return err
	}
	if c.Proof != a.Proof() {
		// Missing and compacted checkpoints denote the same retired prefix.
		// Also accept old pending pages whose raw base predates compaction,
		// while preserving exact comparison above the published floor.
		floor := r.parts[provider].state.Reclaimed
		if ValidateCheckpoint(c.Through[a.Stream], a.From, floor) != nil {
			return fmt.Errorf("%w: input consumption position changed (checkpoint=%d, page_start=%d, reclaimed=%d)", ErrConflict, c.Through[a.Stream], a.From, floor)
		}
		if len(a.Entries) > 0 {
			c.Through[a.Stream] = a.Through
		}
		c.Proof = a.Proof()
		c.Revision++
		c.Checksum = ""
		c.Checksum = checksumState(c)
		b, _ := json.Marshal(c)
		if len(b) > MaxConsumptionBytes {
			return ErrCapacity
		}
		if err := r.save(r.consumptionPath(provider), c); err != nil {
			return err
		}
	}
	if err := r.stop("page_confirmed"); err != nil {
		return err
	}
	a.Phase = "settled"
	a.Received = a.ReceiptRequired
	if len(a.Entries) > 0 {
		s.Last = &LastPage{a.Stream, append([]Location(nil), a.Entries...), a.ID, true}
	}
	if err := r.saveCoordinator(s); err != nil {
		return err
	}
	return r.stop("page_settled")
}
func (r *Repository) Barrier(e Execution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return err
	}
	if activeAttempt(*r.coordinator) != nil || r.coordinator.SharedIntent != nil {
		return ErrBusy
	}
	for _, a := range r.coordinator.Attempts {
		if a.ReceiptRequired && !a.Received {
			return ErrBusy
		}
	}
	return nil
}
func (r *Repository) PendingPage(e Execution) (*LastPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return nil, err
	}
	if a := activeAttempt(*r.coordinator); a != nil && a.Kind == "" && len(a.Entries) > 0 {
		return &LastPage{a.Stream, append([]Location(nil), a.Entries...), a.ID, false}, nil
	}
	s := r.stateCopy()
	return s.Last, nil
}

// ConsumptionObservation fixes all pending facts to this turn's original bounds.
func (r *Repository) ConsumptionObservation(e Execution) (Observation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return Observation{}, err
	}
	o := Observation{Excluded: map[Location]bool{}, Bounds: cloneMap(r.coordinator.Bounds)}
	for name, p := range r.parts {
		c, err := r.loadConsumption(name)
		if err != nil {
			return o, err
		}
		bound, ok := r.coordinator.Bounds[name]
		for n, rec := range p.records {
			stream := p.streamRefs[n]
			if !ok || n > bound.Through || n <= c.Through[stream] || c.Skipped[n] != "" {
				o.Excluded[rec.Location] = true
			}
		}
	}
	return o, nil
}

// ReleaseFacts overlays consumption and protected pages on domain-owned source
// acknowledgement and business-release facts. Callers cannot fabricate progress.
func (r *Repository) ConsumptionRelease(provider string, domain ReleaseSet) (ReleaseSet, error) {
	if domain.Revision == "" || len(domain.Records) > 128 {
		return ReleaseSet{}, ErrInvalid
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return ReleaseSet{}, ErrCommitUnknown
	}
	c, err := r.loadConsumption(provider)
	if err != nil {
		return ReleaseSet{}, err
	}
	p := r.parts[provider]
	out := ReleaseSet{Records: map[uint64]ReleaseFacts{}, Retention: domain.Retention}
	protected := map[Location]bool{}
	if r.coordinator != nil && !r.coordinator.Closed {
		for _, a := range r.coordinator.Attempts {
			if a.Phase != "failed_before_output" {
				for _, l := range a.Entries {
					protected[l] = true
				}
			}
		}
	}
	if p == nil {
		return out, ErrIdentity
	}
	for n, f := range domain.Records {
		rec, ok := p.records[n]
		if !ok {
			return out, ErrConflict
		}
		f.Consumed = n <= c.Through[p.streamRefs[n]]
		f.Skipped = c.Skipped[n] != ""
		f.InFlight = f.InFlight || protected[rec.Location]
		out.Records[n] = f
	}
	locations := []Location{}
	for l := range protected {
		locations = append(locations, l)
	}
	SortedLocations(locations)
	out.Revision = Ref("release_", domain.Revision, c.Revision, locations)
	return out, nil
}

// MaterialClaim reserves worst-case allowances before any download. A crash
// cannot reset them; a reserved page resumed without a result omits images.
func (r *Repository) ClaimMaterials(e Execution, id string, scans, images int, bytes int64, nanos int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, i, err := r.attempt(e, id)
	if err != nil {
		return false, err
	}
	if s.Attempts[i].MaterialsClaimed {
		return false, nil
	}
	if s.Attempts[i].Phase != "reserved" || scans < 0 || images < 0 || bytes < 0 || nanos < 0 {
		return false, ErrConflict
	}
	s.Attempts[i].MaterialsClaimed = true
	s.Attempts[i].MaterialReserve = MaterialUsage{scans, images, bytes, nanos}
	s.MaterialScans += scans
	s.MaterialImages += images
	s.MaterialBytes += bytes
	s.MaterialNanos += nanos
	return true, r.saveCoordinator(s)
}
func (r *Repository) Locate(l Location) (string, Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return "", Snapshot{}, ErrCommitUnknown
	}
	for name, p := range r.parts {
		if p.state.Spec.Ref == l.Partition {
			return name, snapshot(p), nil
		}
	}
	return "", Snapshot{}, ErrIdentity
}
func (r *Repository) Skip(e Execution, l Location, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return err
	}
	if activeAttempt(*r.coordinator) != nil {
		return ErrBusy
	}
	if !validKey(reason) {
		return ErrInvalid
	}
	for name, p := range r.parts {
		if p.state.Spec.Ref != l.Partition {
			continue
		}
		bound, ok := r.coordinator.Bounds[name]
		if !ok || l.Seq > bound.Through {
			return ErrIdentity
		}
		if _, ok := p.records[l.Seq]; !ok {
			return ErrCorrupt
		}
		c, err := r.loadConsumption(name)
		if err != nil {
			return err
		}
		if c.Skipped[l.Seq] == reason {
			return nil
		}
		if c.Skipped[l.Seq] == "" && len(c.Skipped) >= MaxRecords {
			return &CapacityError{Scope: "skip_proofs"}
		}
		c.Skipped[l.Seq] = reason
		c.Revision++
		c.Checksum = ""
		c.Checksum = checksumState(c)
		return r.save(r.consumptionPath(name), c)
	}
	return ErrIdentity
}

// ExcludeLocal saves a verified source decision without admitting a model turn.
// It cannot consume offered records or create model budget/execution authority.
func (r *Repository) ExcludeLocal(l Location, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return ErrCommitUnknown
	}
	if !validKey(reason) {
		return ErrInvalid
	}
	if r.coordinator != nil && activeAttempt(*r.coordinator) != nil {
		return ErrBusy
	}
	for provider, p := range r.parts {
		if p.state.Spec.Ref != l.Partition {
			continue
		}
		if _, ok := p.records[l.Seq]; !ok {
			return ErrCorrupt
		}
		c, err := r.loadConsumption(provider)
		if err != nil {
			return err
		}
		if c.Skipped[l.Seq] == reason && p.state.Consumption {
			return nil
		}
		if c.Skipped[l.Seq] == "" && len(c.Skipped) >= MaxRecords {
			return ErrCapacity
		}
		c.Skipped[l.Seq] = reason
		c.Revision++
		c.Checksum = ""
		c.Checksum = checksumState(c)
		if err := r.save(r.consumptionPath(provider), c); err != nil {
			return err
		}
		if !p.state.Consumption {
			next := cloneRoot(p.state)
			next.Consumption = true
			next.LocalOnly = r.coordinator == nil
			if err := r.saveRoot(provider, next); err != nil {
				return err
			}
			p.state = next
		}
		return nil
	}
	return ErrIdentity
}

func (r *Repository) ExistingSnapshots() map[string]Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]Snapshot{}
	for name, p := range r.parts {
		out[name] = snapshot(p)
	}
	return out
}

// Keep deterministic recovery order independent of map iteration.
func SortedLocations(xs []Location) {
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].Partition != xs[j].Partition {
			return xs[i].Partition < xs[j].Partition
		}
		return xs[i].Seq < xs[j].Seq
	})
}

// Seal is the terminal output barrier. It invalidates new reads in this
// execution and releases cache/body protections only after every page settled.
func (r *Repository) Seal(e Execution) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return err
	}
	s := r.stateCopy()
	if activeAttempt(s) != nil || s.SharedIntent != nil {
		return ErrBusy
	}
	for _, a := range s.Attempts {
		if a.ReceiptRequired && !a.Received {
			return ErrBusy
		}
	}
	s.Closed = true
	return r.saveCoordinator(s)
}
func (r *Repository) ChargeDiscovery(e Execution, n int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return err
	}
	s := r.stateCopy()
	if n < 0 {
		return ErrInvalid
	}
	if s.DiscoveryBytes+n > 128<<10 {
		return ErrCapacity
	}
	s.DiscoveryBytes += n
	return r.saveCoordinator(s)
}

// PendingLocations provides exact authoritative progress, not highwater-minus-
// cursor arithmetic. Snapshot bounds are optional for maintenance/recovery.
// PendingStream is the formal read's admission set at its fixed turn bound.
func (r *Repository) PendingStream(e Execution, stream string) ([]Location, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return nil, err
	}
	for name, p := range r.parts {
		bound, ok := r.coordinator.Bounds[name]
		seqs := p.streamSeqs[stream]
		if !ok || len(seqs) == 0 {
			continue
		}
		c, err := r.loadConsumption(name)
		if err != nil {
			return nil, err
		}
		var out []Location
		for _, seq := range seqs {
			if seq > bound.Through {
				break
			}
			if seq > c.Through[stream] && c.Skipped[seq] == "" {
				out = append(out, p.records[seq].Location)
			}
		}
		return out, nil
	}
	return nil, nil
}

func (r *Repository) PendingLocations(bounds map[string]Snapshot) ([]Location, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return nil, ErrCommitUnknown
	}
	out := []Location{}
	for provider, p := range r.parts {
		c, err := r.loadConsumption(provider)
		if err != nil {
			return nil, err
		}
		bound, ok := bounds[provider]
		for n, rec := range p.records {
			if bounds != nil && (!ok || n > bound.Through) {
				continue
			}
			if n > c.Through[p.streamRefs[n]] && c.Skipped[n] == "" {
				out = append(out, rec.Location)
			}
		}
	}
	SortedLocations(out)
	return out, nil
}

func (r *Repository) RecoveryPage() (*LastPage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return nil, ErrCommitUnknown
	}
	if r.coordinator == nil {
		return nil, nil
	}
	if a := activeAttempt(*r.coordinator); a != nil && a.Kind == "" && len(a.Entries) > 0 {
		return &LastPage{a.Stream, append([]Location(nil), a.Entries...), a.ID, false}, nil
	}
	s := r.stateCopy()
	return s.Last, nil
}

// Only an exact same-page durable adapter proof permits recovery settlement.
// A large enough checkpoint alone never proves this page was delivered.
func (r *Repository) recoverConfirmation() error {
	if r.coordinator == nil {
		return nil
	}
	s := r.stateCopy()
	a := activeAttempt(s)
	if a == nil || a.Kind != "" || a.Phase != "awaiting_confirmation" {
		return nil
	}
	for provider, p := range r.parts {
		if p.state.Spec.Ref != a.Snapshot.Partition {
			continue
		}
		c, err := r.loadConsumption(provider)
		if err != nil {
			return err
		}
		if c.Proof != a.Proof() {
			return nil
		}
		a.Phase = "settled"
		a.Received = a.ReceiptRequired
		if len(a.Entries) > 0 {
			s.Last = &LastPage{a.Stream, append([]Location(nil), a.Entries...), a.ID, true}
		}
		return r.saveCoordinator(s)
	}
	return ErrCorrupt
}
