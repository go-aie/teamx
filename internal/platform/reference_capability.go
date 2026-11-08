package platform

import (
	"context"
	"encoding/json"
	"time"
)

type ResolutionStatus string

const (
	ResolutionResolved    ResolutionStatus = "resolved"
	ResolutionPartial     ResolutionStatus = "partial"
	ResolutionUnavailable ResolutionStatus = "unavailable"
	ResolutionUnsupported ResolutionStatus = "unsupported"
	ResolutionMismatch    ResolutionStatus = "mismatch"
)

// A locator is untrusted input. Neither a candidate target nor URL establishes
// native identity. Source is optional: explicit reads need no notification.
type TargetLocator struct {
	Self            bool             `json:"self,omitempty"`
	Query           *TargetQuery     `json:"query,omitempty"`
	VerifiedAliases []TargetIdentity `json:"-"` // service-owned evidence for a previously saved reference
	TargetRef       string           `json:"target_ref,omitempty"`
	Source          *SourceRef       `json:"source,omitempty"`
	Candidate       *TargetIdentity  `json:"candidate,omitempty"`
	URL             string           `json:"url,omitempty"`
}

// Query returns unverified candidates only. Selecting a candidate requires a
// separate resolve; display names never become execution identities.
type TargetQuery struct {
	ObjectType string `json:"object_type"`
	Text       string `json:"text"`
}
type TargetCandidate struct {
	Target TargetIdentity `json:"target"`
	Label  string         `json:"label"`
}

type TargetResolution struct {
	AccountOwnerConfirmed bool              `json:"account_owner_confirmed,omitempty"`
	Candidates            []TargetCandidate `json:"candidates,omitempty"`
	CandidatesIncomplete  bool              `json:"candidates_incomplete,omitempty"`
	TargetLabel           string            `json:"target_label,omitempty"` // display only; never native identity
	AccountLabel          string            `json:"account_label,omitempty"`
	// Notification evidence establishes captured native identity, not current
	// remote state or member access. Empty denotes the existing remote resolver.
	EvidenceSource     string           `json:"evidence_source,omitempty"`
	Status             ResolutionStatus `json:"status"`
	ReasonCode         string           `json:"reason_code,omitempty"` // e.g. permission_denied, unsupported, incomplete
	Reason             string           `json:"reason,omitempty"`
	CheckedAt          time.Time        `json:"checked_at"`
	Target             *TargetIdentity  `json:"target,omitempty"`
	Aliases            []TargetIdentity `json:"aliases,omitempty"` // verified alternate native identities only
	Account            AccountIdentity  `json:"account"`
	ObjectVerified     bool             `json:"object_verified"`
	DiscussionVerified bool             `json:"discussion_verified"`
	MessageVerified    bool             `json:"message_verified"`
	Complete           bool             `json:"complete"`
	URL                string           `json:"url,omitempty"` // verified object URL, even when Target includes a discussion/message
}

type ResolveTarget func(context.Context, TargetLocator, AccessContext) (TargetResolution, error)

// ReadCursor is bound to both native target and current access evidence. The
// provider still validates Token; this comparison alone is not authentication.
type ReadCursor struct {
	Token     string        `json:"token"`
	TargetKey string        `json:"target_key"`
	Access    AccessContext `json:"access"`
	Scope     string        `json:"scope"`
}

func (c ReadCursor) ValidFor(t TargetIdentity, a AccessContext, scope string) bool {
	key, err := t.Key()
	return err == nil && a.Valid() && identityText(c.Token) && identityText(scope) && c.TargetKey == key && c.Access == a && c.Scope == scope
}

type ReadLimits struct {
	Items     int `json:"items"`
	Bytes     int `json:"bytes"`
	BodyBytes int `json:"body_bytes"`
}

func (l ReadLimits) Valid() bool {
	return l.Items > 0 && l.Items <= 100 && l.Bytes > 0 && l.Bytes <= 1<<20 && l.BodyBytes > 0 && l.BodyBytes <= l.Bytes
}

type ReadTargetRequest struct {
	Target TargetIdentity `json:"target"` // resolved by the service from its stored ref
	Scope  string         `json:"scope"`
	Cursor *ReadCursor    `json:"cursor,omitempty"`
	Limits ReadLimits     `json:"limits"`
}

type TargetContent struct {
	Target   TargetIdentity `json:"target"`
	Body     string         `json:"body"`
	Complete bool           `json:"complete"` // false requires an explicit continuation/reason
}

type TargetPage struct {
	Status     ResolutionStatus `json:"status"`
	ReasonCode string           `json:"reason_code,omitempty"` // permission_denied differs from unsupported
	Reason     string           `json:"reason,omitempty"`
	Target     TargetIdentity   `json:"target"`
	Account    AccountIdentity  `json:"account"`
	Contents   []TargetContent  `json:"contents"`
	Next       *ReadCursor      `json:"next,omitempty"`
	Complete   bool             `json:"complete"`
	Actions    []ActionSpec     `json:"actions,omitempty"` // current target/access capabilities
}

type ReadTarget func(context.Context, ReadTargetRequest, AccessContext) (TargetPage, error)

type Operation string

const (
	OperationObjectComment   Operation = "object.comment"
	OperationDiscussionReply Operation = "discussion.reply"
	OperationMessageReply    Operation = "message.reply"
	OperationMessageSend     Operation = "message.send"
	OperationMessageSendSelf Operation = "message.send_self"
)

type RetryPolicy struct {
	MaxAttempts  int  `json:"max_attempts"` // orchestration policy; each adapter call attempts once
	RetryUnknown bool `json:"retry_unknown"`
}

type ActionSpec struct {
	// BindingEvidence names the verified operator capability used as the action
	// basis. Empty keeps the existing rule/instruction/human business authority.
	BindingEvidence           string          `json:"binding_evidence,omitempty"`
	DirectRoomInstructionOnly bool            `json:"direct_room_instruction_only,omitempty"`
	DisabledReason            string          `json:"disabled_reason,omitempty"` // provider acceptance/readiness gate, independent of member authorization
	RequiresAuthorization     bool            `json:"requires_authorization,omitempty"`
	Operation                 Operation       `json:"operation"`
	TargetKind                string          `json:"target_kind"`            // object, discussion, message
	ObjectTypes               []string        `json:"object_types,omitempty"` // optional native object-type restriction
	InputSchema               json.RawMessage `json:"input_schema"`
	Retry                     RetryPolicy     `json:"retry"`
}

type TargetActionRequest struct {
	IdempotencyKey string           `json:"idempotency_key,omitempty"` // service generated; stable within the persisted action
	Target         TargetIdentity   `json:"target"`                    // resolved target, not arbitrary Agent-supplied IDs
	Aliases        []TargetIdentity `json:"aliases,omitempty"`         // server-owned verified native aliases
	Operation      Operation        `json:"operation"`
	Input          json.RawMessage  `json:"input"`
}

type TargetActionResult struct {
	Target     TargetIdentity  `json:"target"` // actual remote target/account
	Account    AccountIdentity `json:"account"`
	Status     string          `json:"status"` // succeeded, not_applied, unknown
	ReasonCode string          `json:"reason_code,omitempty"`
	Message    string          `json:"message,omitempty"`
	ResultID   ExternalID      `json:"result_id,omitzero"`
	URL        string          `json:"url,omitempty"`
	RetryClass string          `json:"retry_class"` // never, transient; never permission to auto-resend unknown
}

// ExecuteTargetAction performs exactly one attempt in the owner's controlled
// context. Room owns authority, intent persistence, retries and settlement.
type ExecuteTargetAction func(context.Context, TargetActionRequest, AccessContext) (TargetActionResult, error)

// Inspection is read-only and verifies the current account and exact target.
// Labels only support existing human-readable policy scopes; identity uses IDs.
type TargetActionInspection struct {
	Target         TargetIdentity  `json:"target"`
	Account        AccountIdentity `json:"account"`
	AccountLabel   string          `json:"account_label,omitempty"`
	ContainerLabel string          `json:"container_label,omitempty"`
	URL            string          `json:"url,omitempty"`
	ContextOnly    bool            `json:"context_only,omitempty"` // Native target is progress evidence, not an automatic reply trigger.
}

type InspectTargetAction func(context.Context, TargetActionRequest, AccessContext) (TargetActionInspection, error)

type ActionCapability struct {
	// AppliesTo supplements native object-type matching when a capability has
	// account-relative restrictions. It does not replace Inspect or Execute.
	AppliesTo func(TargetIdentity, AccountIdentity) bool
	Inspect   InspectTargetAction
	Spec      ActionSpec
	Execute   ExecuteTargetAction
}

// Independently optional capabilities, not a mandatory provider interface.
// CredentialKinds describes required injection, never a credential value.
type ReferenceCapabilities struct {
	CredentialKinds []string
	Resolve         ResolveTarget
	Read            ReadTarget
	Actions         map[Operation]ActionCapability
}
