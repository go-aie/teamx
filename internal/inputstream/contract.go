// Package inputstream owns member-scoped storage, discovery and delivery.
// Provider codecs validate saved payloads and supply labels and grouping.
package inputstream

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalid       = errors.New("input_invalid")
	ErrIdentity      = errors.New("input_identity_mismatch")
	ErrVersion       = errors.New("input_version_unsupported")
	ErrCorrupt       = errors.New("input_storage_corrupt")
	ErrConflict      = errors.New("input_filter_conflict")
	ErrUnsupported   = errors.New("input_filter_unsupported")
	ErrExpired       = errors.New("input_snapshot_expired")
	ErrCapacity      = errors.New("input_capacity_exceeded")
	ErrCommitUnknown = errors.New("input_commit_unknown: reopen owning Store before writing")
)

const MaxRecords = 10000
const MaxBytes = 64 << 20
const MaxRecordBytes = 128 << 10
const FormatVersion = 1

type Member struct{ RoomID, RoomInstance, MembershipID string }

func (m Member) Valid() bool {
	return validKey(m.RoomID) && validKey(m.RoomInstance) && validKey(m.MembershipID)
}
func Ref(prefix string, values ...any) string {
	b, _ := json.Marshal(values)
	h := sha256.Sum256(b)
	return prefix + hex.EncodeToString(h[:])
}
func Digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func validKey(s string) bool {
	return s != "" && len(s) <= 4096 && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

type Location struct {
	Partition string `json:"partition"`
	Seq       uint64 `json:"seq"`
}

// Group contains opaque, adapter-normalized identity, never native query fields.
type Group struct {
	Version      int      `json:"version"`
	Instance     string   `json:"instance"`
	Tenant       string   `json:"tenant,omitempty"`
	Account      string   `json:"account,omitempty"`
	Context      []string `json:"context"`
	Subscription string   `json:"subscription,omitempty"`
	Precision    string   `json:"precision"`
	Missing      string   `json:"missing,omitempty"`
}

func (g Group) Validate() error {
	if g.Version < 1 || !validKey(g.Instance) || len(g.Context) == 0 || len(g.Context) > 8 {
		return ErrInvalid
	}
	for _, s := range g.Context {
		if !validKey(s) {
			return ErrInvalid
		}
	}
	if g.Precision == "source" {
		if !validKey(g.Subscription) || g.Missing == "" {
			return ErrInvalid
		}
	} else if g.Precision != "context" || !validKey(g.Account) {
		return ErrInvalid
	}
	return nil
}
func (g Group) SourceKey(provider, channel string) string {
	if g.Account != "" {
		return Ref("source_", provider, g.Instance, g.Tenant, g.Account)
	}
	return Ref("source_", provider, g.Instance, g.Tenant, channel, g.Subscription)
}
func (g Group) StreamRef(m Member, provider, channel string) string {
	var fallback any
	if g.Precision == "source" {
		fallback = []string{channel, g.Subscription}
	}
	return Ref("stream_", g.Version, m, provider, g.Instance, g.Tenant, g.Account, g.Context, fallback)
}

type Projection struct {
	Group                       Group
	Label, SourceLabel, Blocked string
	Incomplete                  bool
}

// StoredRecord is immutable; payload interpretation is exclusively codec-owned.
type StoredRecord struct {
	Recipients     []string        `json:"recipients,omitempty"`
	Location       Location        `json:"location"`
	DeliveryID     string          `json:"delivery_id"`
	Channel        string          `json:"channel"`
	Codec          string          `json:"codec"`
	Schema         int             `json:"schema"`
	Group          Group           `json:"group"`
	CreatedAt      time.Time       `json:"created_at"`
	ReceivedAt     time.Time       `json:"received_at"`
	Payload        json.RawMessage `json:"payload"`
	DeliveryDigest string          `json:"delivery_digest"`
	Digest         string          `json:"digest"`
}

func (r StoredRecord) checksum() string { r.Digest = ""; b, _ := json.Marshal(r); return Digest(b) }

type Policy struct {
	Version        string        `json:"version"`
	Retention      time.Duration `json:"retention"`
	TimeBasis      string        `json:"time_basis"` // created or received
	MaxRecords     uint64        `json:"max_records"`
	MaxBytes       uint64        `json:"max_bytes"`
	MaxRecordBytes int           `json:"max_record_bytes"`
	MaxReceipts    int           `json:"max_receipts"`
}

func PlatformPolicy() Policy {
	return Policy{"platform/v1", 3 * 24 * time.Hour, "created", MaxRecords, MaxBytes, MaxRecordBytes, 20000}
}
func RoomPolicy() Policy {
	return Policy{"room/history/v1", time.Nanosecond, "explicit", MaxRecords, MaxBytes, MaxRecordBytes, 20000}
}
func (p Policy) valid() bool {
	return p.Version != "" && p.Retention > 0 && (p.TimeBasis == "created" || p.TimeBasis == "received" || p.TimeBasis == "explicit") && p.MaxRecords > 0 && p.MaxRecords <= MaxRecords && p.MaxBytes > 0 && p.MaxBytes <= MaxBytes && p.MaxRecordBytes > 0 && p.MaxRecordBytes <= MaxRecordBytes && p.MaxReceipts > 0 && p.MaxReceipts <= 40000
}

type PartitionSpec struct {
	Owner      Member         `json:"owner"`
	Ref        string         `json:"ref"`
	Domain     string         `json:"domain"`
	Provider   string         `json:"provider"`
	Backend    string         `json:"backend"`
	Codecs     map[string]int `json:"codecs"`
	Channels   []string       `json:"channels"`
	QuotaScope string         `json:"quota_scope"`
	Policy     Policy         `json:"policy"`
}
type Descriptor struct {
	Provider, Codec         string
	Schema, GroupingVersion int
	Channels                []string
	Policy                  Policy
}

func (d Descriptor) Spec(m Member) PartitionSpec {
	return PartitionSpec{m, Ref("partition_", m, d.Provider), "notification", d.Provider, "jsonl/v1", map[string]int{d.Codec: d.Schema}, append([]string(nil), d.Channels...), Ref("quota_", m), d.Policy}
}

// Codec validates and groups a provider-owned stored payload.
type Codec interface {
	Descriptor() Descriptor
	Decode(schema int, payload json.RawMessage) (Projection, error)
}

// StoredRecordValidator checks adapter-owned envelope/payload relationships.
// Validation must not allocate a query index or consult remote state.
type StoredRecordValidator interface{ ValidateStoredRecord(StoredRecord) error }
type Snapshot struct {
	Partition string `json:"partition"`
	Revision  uint64 `json:"revision"`
	Through   uint64 `json:"through"`
	Reclaimed uint64 `json:"reclaimed"`
}
type Usage struct {
	Records uint64 `json:"records"`
	Bytes   uint64 `json:"bytes"`
}
type Receipt struct {
	Channel   string `json:"channel"`
	Partition string `json:"partition"`
	Seq       uint64 `json:"seq"`
	Key       string `json:"key"`
	Digest    string `json:"digest"`
	Operation string `json:"operation"`
	SourceRef string `json:"source_ref,omitempty"`
}
type AppendRequest struct {
	DeliveryID, Channel   string
	Seq                   uint64
	CreatedAt, ReceivedAt time.Time
	Payload               json.RawMessage
	DeliveryDigest        string
	SourceRef             string // Optional stable evidence reference assigned by the format upgrade.
}

// ReleaseFacts comes from authoritative domain state, not an Agent request.
// ConsumptionRelease overlays checkpoints and page protection on domain facts.
type ReleaseFacts struct {
	Revision                                                       string
	SourceConfirmed, BusinessReleased, Consumed, Skipped, InFlight bool
	ReceiptReleased                                                bool
}
type ReleaseSet struct {
	Revision  string
	Records   map[uint64]ReleaseFacts
	Retention time.Duration
}
type ReclaimPlan struct {
	ID            string
	Snapshot      Snapshot
	Policy        string
	FactsRevision string
	Through       uint64
	Now           time.Time
	Usage         Usage
}

func StrictDecode(data []byte, target any) error {
	if !utf8.Valid(data) || len(data) == 0 {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func validateJSONContainer(d *json.Decoder, kind json.Delim, depth, limit int) error {
	if depth > limit {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for d.More() {
		if kind == '{' {
			token, err := d.Token()
			if err != nil {
				return ErrInvalid
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return ErrInvalid
			}
			seen[key] = true
		}
		token, err := d.Token()
		if err != nil || token == nil {
			return ErrInvalid
		}
		if delim, ok := token.(json.Delim); ok {
			if delim != '{' && delim != '[' {
				return ErrInvalid
			}
			if err := validateJSONContainer(d, delim, depth+1, limit); err != nil {
				return err
			}
		}
	}
	end, err := d.Token()
	expected := json.Delim('}')
	if kind == '[' {
		expected = ']'
	}
	if err != nil || end != expected {
		return ErrInvalid
	}
	return nil
}

func (p Projection) Validate() error {
	if err := p.Group.Validate(); err != nil {
		return err
	}
	for _, s := range []string{p.Label, p.SourceLabel, p.Blocked} {
		if len(s) > 1024 || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return ErrInvalid
		}
	}
	return nil
}

// ValidateRequest bounds JSON tool arguments and rejects duplicate keys and nulls.
func ValidateRequest(data []byte) error {
	if len(data) == 0 || len(data) > 128<<10 || !utf8.Valid(data) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return ErrInvalid
	}
	if err := validateJSONContainer(d, '{', 1, 8); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}
