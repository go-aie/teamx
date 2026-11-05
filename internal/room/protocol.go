package room

import (
	"errors"
	"time"

	"github.com/go-aie/actorx"
)

var (
	ErrRequest     = errors.New("invalid room request")
	ErrConflict    = errors.New("room operation conflicts with existing data")
	ErrBusy        = errors.New("room commit or unread page already pending")
	ErrCapacity    = errors.New("room capacity exceeded")
	ErrReadExpired = errors.New("read receipt expired, replaced or unknown")
	ErrClosing     = errors.New("room service is shutting down")
	ErrTurnBudget  = errors.New("member turn budget exhausted for this service lifetime")
)

// Caller is the business identity checked by Room. The HTTP adapter
// additionally checks ServiceEpoch before constructing these internal messages.
type Caller struct {
	Author
	MembershipID string `json:"membership_id,omitempty"`
}

// AddMember is internal: Target is never a CLI JSON field. Assembly creates a
// fresh MembershipID for each join and owns stopping/waiting for the target.
type AddMember struct {
	MemberIdentity
	Runner RunnerConfig
	Target actorx.Addr
}

type RemoveMember struct{ MemberIdentity }
type ListMembers struct{ RoomIdentity }

// AttachMember restores only a runtime address for an existing generation.
// It never joins a member, advances its cursor or changes the Room revision.
type AttachMember struct {
	MemberIdentity
	Target actorx.Addr
}

type MemberInfo struct {
	MemberState
	UnreadCount     int    `json:"unread_count"`
	NewestUnreadSeq uint64 `json:"newest_unread_seq"`
	Attached        bool   `json:"attached"`
}

type MemberList struct {
	RoomIdentity
	LatestSeq  uint64       `json:"latest_seq"`
	Revision   uint64       `json:"revision"`
	Committing bool         `json:"committing"`
	WriteError string       `json:"write_error,omitempty"`
	Members    []MemberInfo `json:"members"`
}

type PostMessage struct {
	RoomIdentity
	Caller          Caller
	ClientMessageID string
	Text            string
	ReplyTo         uint64
	MatterNotice    bool // internal durable outbox delivery; never accepted by /v1/send
}

type PostReceipt struct {
	RoomIdentity
	Seq             uint64 `json:"seq"`
	ClientMessageID string `json:"client_message_id"`
}

// ClearMessages removes the observed prefix without deleting newer arrivals.
// Sequence numbers and private member sessions survive clearing.
type ClearMessages struct {
	RoomIdentity
	Caller     Caller
	ThroughSeq uint64
}

type ClearReceipt struct {
	RoomIdentity
	ClearedThrough uint64 `json:"cleared_through"`
}

type ReadMode string

const (
	ReadAll    ReadMode = "all"
	ReadLatest ReadMode = "latest"
	ReadRange  ReadMode = "range"
	ReadUnread ReadMode = "unread"
)

// SnapshotSeq nil fixes a new snapshot; a pointer to zero represents an empty
// snapshot. Preview continuations use ReadRange + NextAfter + SnapshotSeq.
// Unread continuations use ReadUnread + NextAfter + SnapshotSeq + Remaining,
// after confirming the preceding page. Limit counts others' messages, not self.
type ReadMessages struct {
	RoomIdentity
	Caller      Caller
	Mode        ReadMode
	Latest      int
	After       *uint64
	SnapshotSeq *uint64
	Limit       int
	Peek        bool
}

type ReadPage struct {
	RoomIdentity
	MembershipID   string        `json:"membership_id,omitempty"`
	Revision       uint64        `json:"revision"`
	SnapshotSeq    uint64        `json:"snapshot_seq"`
	ClearedThrough uint64        `json:"cleared_through"`
	Messages       []RoomMessage `json:"messages"`
	FromExclusive  uint64        `json:"from_exclusive"`
	ScannedThrough uint64        `json:"scanned_through"`
	NextAfter      uint64        `json:"next_after"`
	HasMore        bool          `json:"has_more"`
	Remaining      int           `json:"remaining,omitempty"`
	ReadReceipt    string        `json:"read_receipt,omitempty"`
	ExpiresAt      time.Time     `json:"expires_at,omitzero"`
	UnreadCount    int           `json:"unread_count"`
}

type ConfirmRead struct {
	MemberIdentity
	ReadReceipt string
}

type ReadConfirmation struct {
	MemberIdentity
	ReadReceipt string `json:"read_receipt"`
	ReadThrough uint64 `json:"read_through"`
	UnreadCount int    `json:"unread_count"`
	Revision    uint64 `json:"revision"`
}

// UnreadChanged is a state snapshot, never a message body or a delta.
type UnreadChanged struct {
	MemberIdentity
	Inbox           *InboxInput `json:"inbox,omitempty"`
	Revision        uint64      `json:"revision"`
	LatestSeq       uint64      `json:"latest_seq"`
	NewestUnreadSeq uint64      `json:"newest_unread_seq"`
	UnreadCount     int         `json:"unread_count"`
}

type InboxInput struct {
	Through     uint64 `json:"through"`
	SnapshotSeq uint64 `json:"snapshot_seq"`
}

type InspectMember struct{ MemberIdentity }
type MemberStatus struct {
	Inbox        *InboxStatus     `json:"inbox,omitempty"`
	Rotation     *SessionRotation `json:"rotation,omitempty"`
	Notification *UnreadChanged   `json:"notification"`
	Status       string           `json:"status,omitempty"`
	Paused       bool             `json:"paused"`
	Active       bool             `json:"active"`
	Agent        *AgentSnapshot   `json:"agent,omitempty"`
	Error        string           `json:"error,omitempty"`
	TurnBudget   *TurnBudget      `json:"turn_budget,omitempty"`
}

type SessionRotation struct {
	Status       string `json:"status"` // waiting, closing, completed, failed
	ContextReset bool   `json:"private_context_reset"`
	Error        string `json:"error,omitempty"`
}

// Budget is runtime state, independent of the persisted lifetime turn number.
type TurnBudget struct {
	Limit uint64 `json:"limit"`
	Used  uint64 `json:"used"`
}

type ControlMember struct {
	MemberIdentity
	Action string
	Retry  bool
	Cancel bool
	Resume bool
	State  UnreadChanged
}
