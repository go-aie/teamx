package room

import (
	"errors"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/app/agents"

	"github.com/go-aie/teamx/agent"
)

var (
	ErrRequest     = errors.New("invalid room request")
	ErrConflict    = errors.New("room operation conflicts with existing data")
	ErrBusy        = errors.New("room commit or unread page already pending")
	ErrCapacity    = agents.ErrCapacity
	ErrReadExpired = errors.New("read receipt expired, replaced or unknown")
	ErrClosing     = errors.New("room service is shutting down")
	ErrTurnBudget  = errors.New("member cumulative turn budget exhausted")
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
	IsDirect   bool         `json:"is_direct"`
	LatestSeq  uint64       `json:"latest_seq"`
	Revision   uint64       `json:"revision"`
	Committing bool         `json:"committing"`
	WriteError string       `json:"write_error,omitempty"`
	Members    []MemberInfo `json:"members"`
}

type PostMessage struct {
	Attachments   []RoomAttachmentInput
	Mentions      []MessageMention
	inferMentions bool
	RoomIdentity
	Caller             Caller
	ClientMessageID    string
	Text               string
	WorkspaceRootID    string // trusted execution binding; never decoded from HTTP
	ReplyTo            uint64
	DurablePublication bool // internal accepted publication intent; never decoded from HTTP
	FollowupNotice     bool // internal durable outbox delivery; never accepted by /v1/send
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

type UnreadChanged = agent.Input
type InboxInput = agent.InboxInput

type InspectMember struct{ MemberIdentity }
type MemberStatus struct {
	SessionRecovery      *sessionRecovery    `json:"session_recovery,omitempty"`
	EnvironmentControl   *environmentHandoff `json:"environment_control,omitempty"`
	ExecutionID          uint64              `json:"execution_id,omitempty"` // Cumulative admitted attempt; never reused by Session rotation.
	Stopping             bool                `json:"stopping,omitempty"`
	CurrentGeneration    string              `json:"current_generation,omitempty"`
	PauseRevision        uint64              `json:"-"`
	SessionGeneration    string              `json:"session_generation,omitempty"`
	SessionStartUsed     uint64              `json:"session_start_used"`
	SessionResetRequired bool                `json:"session_reset_required,omitempty"`
	Inbox                *InboxStatus        `json:"inbox,omitempty"`
	InboxRecovery        *inboxRecovery      `json:"inbox_recovery,omitempty"`
	InputRecovery        *inputRecoveryView  `json:"input_recovery,omitempty"`
	Rotation             *SessionRotation    `json:"rotation,omitempty"`
	Notification         *UnreadChanged      `json:"notification"`
	Status               string              `json:"status,omitempty"`
	Paused               bool                `json:"paused"`
	Active               bool                `json:"active"`
	Agent                *AgentSnapshot      `json:"agent,omitempty"`
	Error                string              `json:"error,omitempty"`
	Notice               *ExecutionNotice    `json:"notice,omitempty"` // User-facing message; Error retains operator diagnostics.
	SessionTurns         *SessionTurns       `json:"session_turns,omitempty"`
	TurnBudget           *TurnBudget         `json:"turn_budget,omitempty"`
}

type ExecutionNotice struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type SessionRotation struct {
	Cancel         bool   `json:"cancel"`
	Resume         bool   `json:"resume"`
	Replenish      bool   `json:"replenish"`
	OperationID    string `json:"operation_id"`
	Reason         string `json:"reason"`
	FromGeneration string `json:"from_generation"`
	ToGeneration   string `json:"to_generation"`
	BaseUsed       uint64 `json:"base_used"`
	Phase          string `json:"phase"`
	FailedStep     string `json:"failed_step,omitempty"`
	Status         string `json:"status"` // waiting, closing, completed, failed
	ContextReset   bool   `json:"private_context_reset"`
	Error          string `json:"error,omitempty"`
}

// Admission budget is persisted separately from the worker turn checkpoints.
type SessionTurns struct {
	BaselineKnown bool   `json:"baseline_known"`
	Limit         uint64 `json:"limit"`
	Used          uint64 `json:"used"`
	Total         uint64 `json:"total"`
}

type TurnBudget struct {
	Limit uint64 `json:"limit"`
	Used  uint64 `json:"used"`
	Grant uint64 `json:"grant"` // Additional turns granted by an explicit enable after exhaustion.
}

type turnExecution struct {
	Generation string `json:"generation"`
	Turn       uint64 `json:"turn"`
}

type ControlMember struct {
	stopActor        Author // Authenticated operator, supplied only by the service.
	stopName         string // Frozen display name for a shared Room's stop event.
	environmentID    string
	environmentOwner string
	// Internal lifecycle controls; never decoded from Agent requests.
	expectedBinding     *turnExecution
	preserveIntent      bool
	capabilitiesChanged bool
	modelGate           *string
	modelTarget         *modelSessionSnapshot
	ExecutionID         uint64
	automatic           bool
	recovering          bool
	automaticInbox      *InboxStatus
	automaticInputs     *unifiedInputStatus
	internalPause       bool
	expectedPause       *uint64
	MemberIdentity
	Action           string
	ContinueRotation bool
	OperationID      string
	FromGeneration   string
	Reason           string
	Replenish        bool
	lease            *rotationLease
	Retry            bool
	Cancel           bool
	Resume           bool
	State            UnreadChanged
}
