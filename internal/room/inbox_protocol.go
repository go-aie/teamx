package room

import (
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
)

// InboxLimits are persisted on first creation. Consumption retains entries and
// receipts; a full inbox rejects deliveries rather than evicting evidence.
type InboxLimits struct {
	Entries    int   `json:"entries"`
	Bytes      int64 `json:"bytes"` // Encoded durable records, including receipts and metadata.
	EntryBytes int64 `json:"entry_bytes"`
	PageSize   int   `json:"page_size"`
	PageBytes  int64 `json:"page_bytes"` // Encoded records in one preview page.
}

func DefaultInboxLimits() InboxLimits {
	return InboxLimits{Entries: 10000, Bytes: 64 << 20, EntryBytes: 128 << 10, PageSize: 100, PageBytes: 1 << 20}
}

func (l InboxLimits) valid() bool {
	d := DefaultInboxLimits()
	return l.Entries > 0 && l.Entries <= d.Entries && l.Bytes > 0 && l.Bytes <= d.Bytes &&
		l.EntryBytes > 0 && l.EntryBytes <= d.EntryBytes && l.EntryBytes <= l.Bytes &&
		l.PageSize > 0 && l.PageSize <= d.PageSize && l.PageBytes >= l.EntryBytes && l.PageBytes <= d.PageBytes
}

type InboxEntry struct {
	Version int `json:"version"`
	MemberIdentity
	Seq        uint64            `json:"seq"`
	Item       notification.Item `json:"item"`
	ReceivedAt time.Time         `json:"received_at"`
}

// ReceiveInbox is internal to the service, never a member-authorized tool call.
// Call[InboxReceipt] success (not Send success) is the durable handoff boundary.
type ReceiveInbox struct {
	MemberIdentity
	Item notification.Item
}

type InboxReceipt struct {
	MemberIdentity
	Seq        uint64 `json:"seq"`
	DeliveryID string `json:"delivery_id"`
}

// PreviewInbox is a service-internal read-only protocol. Composition must check
// the human/member caller before forwarding it. Nil SnapshotSeq fixes a new
// snapshot; continuations keep that value and use NextAfter. It never consumes.
type PreviewInbox struct {
	MemberIdentity
	After       uint64
	SnapshotSeq *uint64
	Limit       int // Zero uses the inbox's page size.
}

type InboxPage struct {
	MemberIdentity
	SnapshotSeq uint64       `json:"snapshot_seq"`
	NextAfter   uint64       `json:"next_after"`
	HasMore     bool         `json:"has_more"`
	Entries     []InboxEntry `json:"entries"`
}

type InspectInbox struct{ MemberIdentity }

type InboxStatus struct {
	MemberIdentity
	AutomaticChecked bool        `json:"automatic_checked,omitempty"`
	AutomaticReady   bool        `json:"automatic_ready,omitempty"`
	AutomaticBlocked bool        `json:"automatic_blocked,omitempty"`
	ConsumedThrough  uint64      `json:"consumed_through"`
	Batch            *InboxBatch `json:"batch,omitempty"`
	LatestSeq        uint64      `json:"latest_seq"`
	Bytes            int64       `json:"bytes"`
	Limits           InboxLimits `json:"limits"`
	Committing       bool        `json:"committing"`
	Previewing       bool        `json:"previewing"`
	WriteError       string      `json:"write_error,omitempty"`
}
