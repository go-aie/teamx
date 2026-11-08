package room

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Agent inputs are deliberately distinct from persisted action/policy evidence.
// Removed field combinations fail strict decoding.
type agentActionAuthority struct {
	Type        string   `json:"type"`
	ID          string   `json:"id,omitempty"`
	Version     uint64   `json:"version,omitempty"`
	RoomSeq     uint64   `json:"room_seq,omitempty"`
	QuestionRef string   `json:"question_ref,omitempty"`
	MessageRefs []string `json:"message_refs,omitempty"`
	Constraints string   `json:"constraints,omitempty"`
	Reason      string   `json:"reason"`
}

type actionBusinessContext struct {
	ObjectRef   string `json:"object_ref"`
	UnitRef     string `json:"unit_ref"`
	UnitVersion uint64 `json:"unit_version"`
}
type agentActionRequest struct {
	followupCommand
	TargetRef   string                 `json:"target_ref,omitempty"`
	Context     *actionBusinessContext `json:"context,omitempty"`
	ID          string                 `json:"id"`
	PolicyToken string                 `json:"policy_token,omitempty"`
	Authority   agentActionAuthority   `json:"authority,omitzero"`
	Operation   platform.Operation     `json:"operation"`
	Input       ActionInput            `json:"input"`
}

func (q agentActionRequest) internalAuthority() (referenceActionAuthority, error) {
	a := q.Authority
	// Parsing accepts an absent business basis; the trusted registered action
	// spec decides whether operator capability evidence can authorize execution.
	if q.PolicyToken == "" && reflect.DeepEqual(a, agentActionAuthority{}) {
		return referenceActionAuthority{}, nil
	}
	r := referenceActionAuthority{RoomSeq: a.RoomSeq, QuestionRef: a.QuestionRef, MessageRefs: a.MessageRefs, Constraints: a.Constraints, Reason: a.Reason}
	switch a.Type {
	case "rule":
		if a.ID == "" || a.Version == 0 || a.RoomSeq != 0 || a.QuestionRef != "" || len(a.MessageRefs) > 0 || a.Constraints != "" {
			return r, invalidField("authority", "rule requires id/version/reason only")
		}
		r.RuleID, r.RuleVersion = a.ID, a.Version
	case "instruction":
		if a.ID == "" || a.Version == 0 || a.RoomSeq != 0 || a.QuestionRef != "" || len(a.MessageRefs) > 0 {
			return r, invalidField("authority", "instruction requires id/version/reason; only constraints is optional")
		}
		r.InstructionRef, r.InstructionVersion = a.ID, a.Version
	case "human_reply":
		if a.ID != "" || a.Version != 0 || a.RoomSeq == 0 {
			return r, invalidField("authority", "human_reply requires room_seq/reason, without id/version")
		}
	default:
		return r, invalidField("authority.type", "must be rule, instruction or human_reply; use actions execute --help")
	}
	if !handlingText(a.Reason) || len(a.Constraints) > 4096 {
		return r, invalidField("authority.reason", "reason is required (max 4096 bytes); constraints max 4096 bytes")
	}
	return r, nil
}

// Target is uniquely determined by the member-scoped unit, whose hash includes
// its immutable target_ref. Loading validates both the unit and native target.
func (s *Store) agentAction(id MemberIdentity, q agentActionRequest) (referenceActionRequest, error) {
	a, err := q.internalAuthority()
	if err != nil {
		return referenceActionRequest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkFollowupsLocked(id, false); err != nil {
		return referenceActionRequest{}, err
	}
	if q.Context != nil {
		if q.ObjectRef != "" || q.UnitRef != "" || q.Revision != 0 {
			return referenceActionRequest{}, ErrRequest
		}
		q.ObjectRef, q.UnitRef, q.Revision = q.Context.ObjectRef, q.Context.UnitRef, q.Context.UnitVersion
	}
	targetRef := q.TargetRef
	if q.UnitRef != "" {
		u, err := s.loadUnitLocked(id, q.ObjectRef, q.UnitRef)
		if err != nil {
			return referenceActionRequest{}, err
		}
		if targetRef == "" {
			targetRef = u.TargetRef
		}
	} else if q.ObjectRef != "" || q.Revision != 0 {
		return referenceActionRequest{}, ErrRequest
	}
	r := referenceActionRequest{followupCommand: q.followupCommand, TargetRef: targetRef, ID: q.ID, PolicyToken: q.PolicyToken, Authority: a, Operation: q.Operation, Input: q.Input}
	// Existing-ID recovery retains the original observed version. New writes are
	// checked against the current version by the existing execution gates.
	if err := referenceActionRequestError(r); err != nil {
		return r, err
	}
	return r, nil
}

type agentPolicyBody struct {
	Text      string     `json:"text"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type agentPolicyChange struct {
	Type        string           `json:"type"` // save, suggest, confirm, revoke
	ID          string           `json:"id"`
	Version     uint64           `json:"version"`
	Revision    uint64           `json:"revision"`
	TurnNumber  uint64           `json:"turn_number"`
	SourceSeq   uint64           `json:"source_seq"`
	Basis       string           `json:"basis"`
	Body        *agentPolicyBody `json:"body,omitempty"`
	ProposalSeq uint64           `json:"proposal_seq,omitempty"`
}

func (q agentPolicyChange) validate() error {
	if !safeMembershipID(q.ID) {
		return invalidField("id", "required stable ID: 1-128 ASCII letters, digits, hyphens or underscores")
	}
	switch q.Type {
	case "save", "suggest":
		if q.Body == nil || q.ProposalSeq != 0 {
			return invalidField("body", "save/suggest requires body {text, expires_at?} and forbids proposal_seq; see policy update --help")
		}
	case "confirm", "revoke":
		if q.Body != nil || q.Version == 0 || (q.Type == "confirm" && q.ProposalSeq == 0) || (q.Type == "revoke" && q.ProposalSeq != 0) {
			return invalidField("type", "confirm/revoke requires an existing version and forbids body; only confirm requires proposal_seq")
		}
	default:
		return invalidField("type", "must be save, suggest, confirm or revoke")
	}
	if q.SourceSeq == 0 {
		return invalidField("source_seq", "required real human Room message sequence")
	}
	if q.TurnNumber == 0 {
		return invalidField("turn_number", "required positive current_turn from runtime policy context")
	}
	if !handlingText(q.Basis) {
		return invalidField("basis", "required nonempty interpretation, at most 4096 bytes")
	}
	if q.Body != nil {
		if !validText(q.Body.Text) || len(q.Body.Text) > maxPolicyTextBytes {
			return invalidField("body.text", "required nonempty UTF-8 text, at most 32768 bytes")
		}
		if q.Body.ExpiresAt != nil && q.Body.ExpiresAt.IsZero() {
			return invalidField("body.expires_at", "must be a nonzero RFC3339 time")
		}
	}
	return nil
}

func (s *Store) changeAgentPolicy(id MemberIdentity, caller Caller, q agentPolicyChange) (conversationPolicyResult, error) {
	if err := q.validate(); err != nil {
		return conversationPolicyResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if caller != agentCallerIdentity(id) {
		return conversationPolicyResult{}, ErrIdentity
	}
	if err := s.checkFollowupsLocked(id, true); err != nil {
		return conversationPolicyResult{}, err
	}
	c := PolicyChange{ID: q.ID, Version: q.Version, Revision: q.Revision, TurnNumber: q.TurnNumber, SourceSeq: q.SourceSeq}
	body := q.Body
	if body == nil {
		p, _, err := s.loadPolicyLocked(id, false)
		if err != nil {
			return conversationPolicyResult{}, err
		}
		// Restore the exact historical version, including for lost-response replay
		// after later amendments/revocation or public history removal.
		for _, r := range p.History {
			if r.ID == q.ID && r.Version == q.Version {
				body = &agentPolicyBody{Text: r.Text, ExpiresAt: r.ExpiresAt}
			}
		}
		if body == nil {
			return conversationPolicyResult{}, invalidField("version", "saved policy version not found; read policy read --id ID")
		}
	}
	c.Text, c.ExpiresAt = body.Text, body.ExpiresAt
	mode := "explicit_long_term"
	c.Status = "active"
	switch q.Type {
	case "suggest":
		mode, c.Status = "inferred_habit", "suggested"
	case "confirm":
		mode = "confirm_suggestion"
	case "revoke":
		c.Status = "revoked"
	}
	return s.changeConversationPolicyLocked(id, caller, conversationPolicyChange{Change: c, Mode: mode, Basis: q.Basis, ProposalSeq: q.ProposalSeq})
}

// Policy source already contains the exact human author/seq/text. Preserve
// interpretation and proposal relations separately, without repeating Message.
type agentPolicyEvidence struct {
	Mode      string                        `json:"mode"`
	Basis     string                        `json:"basis"`
	ReplyTo   uint64                        `json:"reply_to,omitempty"`
	CreatedAt time.Time                     `json:"created_at"`
	Proposal  *policySuggestionConfirmation `json:"proposal,omitempty"`
}
type agentPolicyRecord struct {
	ID           string               `json:"id"`
	Version      uint64               `json:"version"`
	Status       string               `json:"status"`
	Text         string               `json:"text"`
	ExpiresAt    *time.Time           `json:"expires_at,omitempty"`
	Source       PolicySource         `json:"source"`
	ChangedBy    Author               `json:"changed_by"`
	UpdatedAt    time.Time            `json:"updated_at"`
	Conversation *agentPolicyEvidence `json:"conversation,omitempty"`
}

func projectPolicyRecord(r PolicyRevision) agentPolicyRecord {
	v := agentPolicyRecord{r.ID, r.Version, r.Status, r.Text, r.ExpiresAt, r.Source, r.ChangedBy, r.UpdatedAt, nil}
	if e := r.Conversation; e != nil {
		v.Conversation = &agentPolicyEvidence{e.Mode, e.Basis, e.Message.ReplyTo, e.Message.CreatedAt, e.Proposal}
	}
	return v
}
func (v agentPolicyView) MarshalJSON() ([]byte, error) {
	records := make([]agentPolicyRecord, 0, len(v.Effective))
	for _, r := range v.Effective {
		records = append(records, projectPolicyRecord(r))
	}
	return json.Marshal(struct {
		policyContext
		Effective []agentPolicyRecord `json:"effective"`
	}{v.policyContext, records})
}
func (v agentPolicyUpdateResult) MarshalJSON() ([]byte, error) {
	// Effective is always a full replacement, including [] for unchanged policy.
	records := make([]agentPolicyRecord, 0, len(v.Effective))
	for _, r := range v.Effective {
		records = append(records, projectPolicyRecord(r))
	}
	return json.Marshal(struct {
		policyContext
		Effective []agentPolicyRecord `json:"effective"`
		Record    agentPolicyRecord   `json:"record"`
	}{v.policyContext, records, projectPolicyRecord(v.Record)})
}

func agentPolicyDiagnostic(err error) error {
	var d *requestDiagnostic
	if !errors.As(err, &d) {
		return err
	}
	copy := *d
	switch d.Field {
	case "change.text", "change.expires_at":
		copy.Field = "body." + strings.TrimPrefix(d.Field, "change.")
	default:
		copy.Field = strings.TrimPrefix(d.Field, "change.")
	}
	if copy.Field == "change" {
		copy.Field = "record"
	}
	if copy.Field == "mode" {
		copy.Field = "type"
	}
	return &copy
}

type agentProgressChange struct {
	followupCommand
	Status   string `json:"status"`
	Summary  string `json:"summary"`
	Next     string `json:"next,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}
type agentInstructionStatusChange struct {
	followupCommand
	InstructionRef     string `json:"instruction_ref"`
	InstructionVersion uint64 `json:"instruction_version"`
	Status             string `json:"status"`
	Evidence           string `json:"evidence"`
	SourceSeq          uint64 `json:"source_seq,omitempty"`
}

func projectActionAuthority(a referenceActionAuthority) agentActionAuthority {
	if reflect.DeepEqual(a, referenceActionAuthority{}) {
		return agentActionAuthority{}
	}
	v := agentActionAuthority{RoomSeq: a.RoomSeq, QuestionRef: a.QuestionRef, MessageRefs: a.MessageRefs, Constraints: a.Constraints, Reason: a.Reason}
	switch {
	case a.RuleID != "":
		v.Type, v.ID, v.Version = "rule", a.RuleID, a.RuleVersion
	case a.InstructionRef != "":
		v.Type, v.ID, v.Version = "instruction", a.InstructionRef, a.InstructionVersion
	default:
		v.Type = "human_reply"
	}
	return v
}

func (a *agentActionAuthority) UnmarshalJSON(data []byte) error {
	type wire agentActionAuthority
	var v wire
	if err := decodeRequestJSON(string(data), &v); err != nil {
		return prefixDiagnostic(err, "authority.")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if len(fields) == 0 {
		*a = agentActionAuthority{}
		return nil
	}
	allowed := "type reason"
	switch v.Type {
	case "rule":
		allowed += " id version"
	case "instruction":
		allowed += " id version constraints"
	case "human_reply":
		allowed += " room_seq question_ref message_refs constraints"
	default:
		return invalidField("authority.type", "must be rule, instruction or human_reply")
	}
	for name := range fields {
		if !strings.Contains(" "+allowed+" ", " "+name+" ") {
			return invalidField("authority", "field is not legal for this authority type; see actions execute --help")
		}
	}
	*a = agentActionAuthority(v)
	return nil
}
func (q *agentPolicyChange) UnmarshalJSON(data []byte) error {
	type wire agentPolicyChange
	var v wire
	if err := decodeRequestJSON(string(data), &v); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, ok := fields["body"]; ok && v.Type != "save" && v.Type != "suggest" {
		return invalidField("body", "only save/suggest accepts body")
	}
	if _, ok := fields["proposal_seq"]; ok && v.Type != "confirm" {
		return invalidField("proposal_seq", "only confirm accepts proposal_seq")
	}
	*q = agentPolicyChange(v)
	return q.validate()
}

func agentActionDiagnostic(err error) error {
	var d *requestDiagnostic
	if !errors.As(err, &d) {
		return err
	}
	copy := *d
	switch d.Field {
	case "authority.rule_version", "authority.instruction_version":
		copy.Field = "authority.version"
	case "target_ref":
		copy.Field = "unit_ref"
	}
	return &copy
}

func agentInstructionDiagnostic(err error) error {
	var d *requestDiagnostic
	if !errors.As(err, &d) {
		return err
	}
	copy := *d
	switch d.Field {
	case "version":
		copy.Field = "instruction_version"
	case "id":
		copy.Field = "instruction_ref"
	}
	return &copy
}
