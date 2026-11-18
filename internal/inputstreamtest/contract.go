// Package inputstream defines the local, member-scoped input contract. It has
// no scheduler, remote client or authority to change consumption state.
package inputstreamtest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const Contract = "inbox_streams/v1"
const GroupingVersion = 1
const MaxRecords = 10000
const DirectoryBytes = 8 << 10
const PageBytes = 16 << 10
const OverviewBytes = 8 << 10
const PreviewBytes = 8 << 10
const DiscoveryTurnBytes = 128 << 10
const PreviewTurnBytes = 32 << 10

type Member struct{ RoomID, RoomInstance, MembershipID string }

func (m Member) Valid() bool { return m.RoomID != "" && m.RoomInstance != "" && m.MembershipID != "" }

// Group is adapter output from saved native evidence, never an Agent locator.
// Namespace-bearing IDs are encoded as separate strings by native adapters.
type Group struct {
	Version                                            int
	Kind, Provider, Channel, Instance, Tenant, Account string
	Context                                            []string
	Subscription, Precision, Missing                   string
	Facts                                              Facts
}
type Facts struct {
	Project, ProjectID, ObjectType, IID, ObjectRef            string
	Chat, ChatID, ThreadID, EntryKind, EventType, OperationID string
	Label, SourceLabel, AccountLabel, Summary                 string
	Incomplete                                                bool
}

func key(prefix string, values ...any) string {
	b, _ := json.Marshal(values)
	h := sha256.Sum256(b)
	return prefix + hex.EncodeToString(h[:])
}
func (g Group) Validate() error {
	if g.Version != GroupingVersion || len(g.Context) == 0 {
		return ErrInvalid
	}
	if g.Kind == "room" {
		if g.Provider != "" || len(g.Context) != 2 {
			return ErrInvalid
		}
		return nil
	}
	if g.Kind != "notification" || g.Provider == "" || g.Channel == "" || g.Instance == "" {
		return ErrInvalid
	}
	if g.Precision == "source" {
		if g.Subscription == "" || g.Missing == "" {
			return ErrInvalid
		}
	} else if g.Account == "" || g.Precision != "context" {
		return ErrInvalid
	}
	for _, s := range g.Context {
		if strings.TrimSpace(s) == "" {
			return ErrInvalid
		}
	}
	return nil
}
func (g Group) StreamRef(m Member) (string, error) {
	if !m.Valid() || g.Validate() != nil {
		return "", ErrInvalid
	}
	if g.Kind == "room" && (g.Context[0] != m.RoomID || g.Context[1] != m.RoomInstance) {
		return "", ErrIdentity
	}
	// Channel is intentionally absent for native objects: Events/Todos co-group.
	var fallback any
	if g.Precision == "source" {
		fallback = []string{g.Channel, g.Subscription}
	}
	return key("stream_", GroupingVersion, m, g.Kind, g.Provider, g.Instance, g.Tenant, g.Account, g.Context, fallback), nil
}
func (g Group) AccountRef(m Member) string {
	if g.Kind != "notification" || g.Account == "" {
		return ""
	}
	return key("account_", m, g.Provider, g.Instance, g.Tenant, g.Account)
}
func (g Group) SourceKey(m Member) string {
	if r := g.AccountRef(m); r != "" {
		return r
	}
	return key("source_", m, g.Provider, g.Instance, g.Tenant, g.Channel, g.Subscription)
}
func RoomGroup(m Member, label string) Group {
	return Group{Version: GroupingVersion, Kind: "room", Context: []string{m.RoomID, m.RoomInstance}, Facts: Facts{Label: label, EntryKind: "message"}}
}

var (
	ErrInvalid     = errors.New("input_invalid")
	ErrIdentity    = errors.New("input_identity_mismatch")
	ErrExpired     = errors.New("input_catalog_expired: repeat the original query")
	ErrConflict    = errors.New("input_filter_conflict")
	ErrUnsupported = errors.New("input_filter_unsupported")
	ErrAmbiguous   = errors.New("input_scope_ambiguous")
	ErrCapacity    = errors.New("input_capacity_exceeded")
)

type StreamSummary struct {
	Ref         string `json:"stream_ref"`
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	SourceLabel string `json:"source_label,omitempty"`
	Pending     int    `json:"pending_entries"`
	Hint        string `json:"hint,omitempty"`
	Attention   string `json:"attention,omitempty"`
	Blocked     string `json:"blocked_reason,omitempty"`
}
type InputDirectory struct {
	Pending   int             `json:"pending_streams"`
	Blocked   int             `json:"blocked_streams,omitempty"`
	Streams   []StreamSummary `json:"streams"`
	Next      string          `json:"next_cursor,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
}
type SourceSummary struct {
	Kind       string `json:"kind"`
	Provider   string `json:"provider,omitempty"`
	AccountRef string `json:"account_ref,omitempty"`
	Label      string `json:"source_label"`
	Pending    int    `json:"pending_streams"`
	Entries    int    `json:"pending_entries"`
	Blocked    int    `json:"blocked_streams,omitempty"`
	Attention  string `json:"attention,omitempty"`
}
type Overview struct {
	ScopeRef   string          `json:"scope_ref,omitempty"`
	ObservedAt time.Time       `json:"observed_at"`
	Pending    int             `json:"pending_streams"`
	Entries    int             `json:"pending_entries"`
	Blocked    int             `json:"blocked_streams,omitempty"`
	Sources    []SourceSummary `json:"sources"`
	Next       string          `json:"next_cursor,omitempty"`
	Notice     string          `json:"notice,omitempty"`
	Stop       string          `json:"stop_reason,omitempty"`
}
type List struct {
	ScopeRef string          `json:"scope_ref,omitempty"`
	Matched  int             `json:"matched_streams"`
	Streams  []StreamSummary `json:"streams"`
	Next     string          `json:"next_cursor,omitempty"`
	Notice   string          `json:"notice,omitempty"`
	Stop     string          `json:"stop_reason,omitempty"`
}
type Excerpt struct {
	Text      string `json:"excerpt"`
	Truncated bool   `json:"truncated,omitempty"`
}
type Preview struct {
	Ref     string    `json:"stream_ref"`
	Entries []Excerpt `json:"entries"`
	Notice  string    `json:"notice,omitempty"`
	Stop    string    `json:"stop_reason,omitempty"`
}
type Stream struct {
	Ref    string `json:"stream_ref"`
	Kind   string `json:"kind"`
	Label  string `json:"label"`
	Source any    `json:"source,omitempty"`
}

// Entries and Followups contain existing business projections, not raw records.
type Read struct {
	Stream    Stream            `json:"stream"`
	Entries   []json.RawMessage `json:"entries"`
	Followups []json.RawMessage `json:"followups,omitempty"`
	HasMore   bool              `json:"has_more"`
	Next      *string           `json:"next_cursor,omitempty"`
	Remaining *int              `json:"inbox_entries_remaining,omitempty"`
	Stop      string            `json:"stop_reason,omitempty"`
}

func (r Read) MarshalJSON() ([]byte, error) {
	type projection Read
	if r.Stop == "turn_budget_exhausted" {
		return json.Marshal(struct {
			projection
			Next *string `json:"next_cursor"`
		}{projection(r), nil})
	}
	return json.Marshal(projection(r))
}

// Query is a flat transport contract. A cursor inherits all original fields.
type Query struct {
	within      string
	views       bool
	Turn        uint64      `json:"turn_number"`
	Cursor      string      `json:"cursor,omitempty"`
	Kind        string      `json:"kind,omitempty"`
	State       string      `json:"state,omitempty"`
	Provider    string      `json:"provider,omitempty"`
	AccountRef  string      `json:"account_ref,omitempty"`
	Project     string      `json:"project,omitempty"`
	ProjectID   json.Number `json:"project_id,omitempty"`
	ObjectType  string      `json:"object_type,omitempty"`
	IID         json.Number `json:"iid,omitempty"`
	ObjectRef   string      `json:"object_ref,omitempty"`
	Chat        string      `json:"chat,omitempty"`
	ChatID      string      `json:"chat_id,omitempty"`
	ThreadID    string      `json:"thread_id,omitempty"`
	EntryKind   string      `json:"entry_kind,omitempty"`
	EventType   string      `json:"event_type,omitempty"`
	OperationID string      `json:"operation_id,omitempty"`
	Channel     string      `json:"channel,omitempty"`
	Text        string      `json:"query,omitempty"`
	Attention   string      `json:"attention,omitempty"`
	Sort        string      `json:"sort,omitempty"`
	Limit       int         `json:"limit,omitempty"`
}
type PreviewRequest struct {
	Turn  uint64 `json:"turn_number"`
	Ref   string `json:"stream_ref"`
	Limit int    `json:"limit,omitempty"`
}
type ReadRequest struct {
	Turn   uint64 `json:"turn_number"`
	Ref    string `json:"stream_ref,omitempty"`
	ReadID string `json:"read_id,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

func (q ReadRequest) Validate() error {
	if q.Turn == 0 || q.Limit < 0 || q.Limit > 10000 || len(q.ReadID) > 128 || len(q.Cursor) > 128 {
		return ErrInvalid
	}
	if q.Cursor != "" {
		if q.Ref != "" || q.ReadID != "" || q.Limit != 0 {
			return ErrConflict
		}
		return nil
	}
	if q.Ref == "" || strings.TrimSpace(q.ReadID) == "" {
		return ErrInvalid
	}
	return nil
}

// PreparedPage is internal. Exact locations and native proof never enter DTOs.
type Location struct {
	Partition string
	Seq       uint64
}
type Page struct {
	Member                  Member
	ID, StreamRef, Snapshot string
	Entries                 []Location
	From, Through           uint64
}
type PreparedPage struct {
	Page          Page
	Result        Read
	NativeReceipt string
}
type Confirmation struct {
	Page          Page
	NativeReceipt string
}
type InputAdapter interface {
	Pending(context.Context, Member) ([]Record, error)
	Preview(context.Context, Member, PreviewRequest) (Preview, error)
	Prepare(context.Context, Member, ReadRequest) (PreparedPage, error)
	Confirm(context.Context, Member, PreparedPage) (Confirmation, error)
}

// StorageBackend is a narrow contract only in this stage. Room commits these
// effects in its Actor candidate; no callbacks hand its state to common code.
type PartitionSpec struct {
	Owner, Ref, Domain, Provider, Backend string
	Codecs                                map[string]int
	QuotaScope, Policy                    string
}
type StoredRecord struct {
	Location          Location
	DeliveryID, Codec string
	Schema            int
	Group             Group
	Payload           json.RawMessage
	Digest            string
}
type Snapshot struct {
	Partition, Revision string
	Through             uint64
}
type Usage struct{ Records, Bytes uint64 }
type ReleaseFacts struct {
	Revision                                              string
	Consumed, SourceConfirmed, BusinessReleased, InFlight bool
}
type ReclaimPlan struct {
	Snapshot Snapshot
	Policy   string
	Through  uint64
	Facts    ReleaseFacts
}
type StorageBackend interface {
	AppendOnce(context.Context, PartitionSpec, StoredRecord) (string, error)
	Snapshot(context.Context) (Snapshot, error)
	Scan(context.Context, Snapshot) ([]Location, error)
	Read(context.Context, Snapshot, Location) (StoredRecord, error)
	InspectUsage(context.Context) (Usage, error)
	PrepareReclaim(context.Context, ReleaseFacts) (ReclaimPlan, error)
	CommitReclaim(context.Context, ReclaimPlan) (string, error)
}

// Decode is shared by transports: reject unknown fields and explicit nulls.
func Decode(data []byte, target any) error {
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		return ErrInvalid
	}
	for _, v := range raw {
		if string(v) == "null" {
			return ErrInvalid
		}
	}
	for _, field := range []string{"limit", "preview_limit"} {
		if value, ok := raw[field]; ok {
			var limit int
			if json.Unmarshal(value, &limit) != nil || limit < 1 {
				return ErrInvalid
			}
		}
	}
	if value, ok := raw["cursor"]; ok {
		var cursor string
		if json.Unmarshal(value, &cursor) != nil {
			return ErrInvalid
		}
		if cursor != "" && len(raw) != 2 {
			return ErrConflict
		}
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	return nil
}
