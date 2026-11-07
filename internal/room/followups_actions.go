package room

import (
	"encoding/json"
	"reflect"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Adapter-specific JSON input. Native execution IDs are never accepted from Agent.
type ActionInput struct {
	Body string `json:"body"`
	raw  string
}

// Preserve arbitrary adapter input without making a body mandatory in the core.
func (i ActionInput) MarshalJSON() ([]byte, error) {
	if i.raw != "" {
		return []byte(i.raw), nil
	}
	return json.Marshal(struct {
		Body string `json:"body"`
	}{i.Body})
}
func (i *ActionInput) UnmarshalJSON(b []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	if fields == nil {
		return ErrRequest
	}
	i.Body, i.raw = "", ""
	if body, ok := fields["body"]; ok {
		if err := json.Unmarshal(body, &i.Body); err != nil {
			return err
		}
	}
	if len(fields) == 1 && fields["body"] != nil {
		return nil
	}
	canonical, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	i.raw = string(canonical)
	return nil
}

// CommentInput keeps existing in-package callers source compatible. New input
// schemas belong to adapters and need not contain a body.
type CommentInput = ActionInput

type referenceActionAuthority struct {
	RuleID             string   `json:"rule_id,omitempty"`
	RuleVersion        uint64   `json:"rule_version,omitempty"`
	InstructionRef     string   `json:"instruction_ref,omitempty"`
	InstructionVersion uint64   `json:"instruction_version,omitempty"`
	RoomSeq            uint64   `json:"room_seq,omitempty"`
	QuestionRef        string   `json:"question_ref,omitempty"`
	MessageRefs        []string `json:"message_refs,omitempty"`
	Constraints        string   `json:"constraints,omitempty"`
	Reason             string   `json:"reason"`
}

type referenceActionRequest struct {
	followupCommand
	TargetRef   string                   `json:"target_ref"`
	ID          string                   `json:"id"`
	PolicyToken string                   `json:"policy_token"`
	Authority   referenceActionAuthority `json:"authority"`
	Operation   platform.Operation       `json:"operation"`
	Input       ActionInput              `json:"input"`
}

type referenceAction struct {
	IdempotencyKey    string                          `json:"idempotency_key,omitempty"`
	Ledger            bool                            `json:"member_record,omitempty"`
	Request           referenceActionRequest          `json:"request"`
	Target            platform.TargetIdentity         `json:"target"`
	Access            platform.AccessContext          `json:"access"`
	Inspection        platform.TargetActionInspection `json:"inspection"`
	Spec              platform.ActionSpec             `json:"spec"`
	Rule              *PolicyRevision                 `json:"rule,omitempty"`
	Basis             *followupReplyBasis             `json:"basis,omitempty"`
	Status            string                          `json:"status"`
	Attempts          int                             `json:"attempts"`
	PossibleDuplicate bool                            `json:"possible_duplicate,omitempty"`
	Result            *platform.TargetActionResult    `json:"result,omitempty"`
	UpdatedAt         time.Time                       `json:"updated_at"`
}

type referenceActionReceipt struct {
	ID                string                       `json:"id"`
	Status            string                       `json:"status"`
	UnitVersion       uint64                       `json:"unit_version,omitempty"`
	Attempts          int                          `json:"attempts"`
	PossibleDuplicate bool                         `json:"possible_duplicate,omitempty"`
	Result            *platform.TargetActionResult `json:"result,omitempty"`
	TargetRef         string                       `json:"target_ref,omitempty"`
	Target            *platform.TargetIdentity     `json:"target,omitempty"`
	Account           *platform.AccountIdentity    `json:"account,omitempty"`
	Policy            *agentPolicyView             `json:"policy,omitempty"`
}

type referenceActionKey struct {
	MemberIdentity
	UnitRef, ID string
}

func (q referenceActionRequest) key(id MemberIdentity) referenceActionKey {
	return referenceActionKey{id, "", q.ID}
}
func (q referenceActionRequest) sameBusiness(o referenceActionRequest) bool {
	return q.ObjectRef == o.ObjectRef && q.UnitRef == o.UnitRef && q.TargetRef == o.TargetRef && q.Operation == o.Operation && q.Input == o.Input && reflect.DeepEqual(q.Authority, o.Authority)
}
func validReferenceActionRequest(q referenceActionRequest) bool {
	return referenceActionRequestError(q) == nil
}
func referenceActionRequestError(q referenceActionRequest) error {
	if q.UnitRef != "" && !validOpaqueRef(q.ObjectRef, "ref_") {
		return invalidField("object_ref", "required stable ref_ reference")
	}
	if q.UnitRef != "" && !validOpaqueRef(q.UnitRef, "unit_") {
		return invalidField("unit_ref", "required stable unit_ reference")
	}
	if !validOpaqueRef(q.TargetRef, "ref_") {
		return invalidField("target_ref", "required verified ref_ reference")
	}
	if q.UnitRef != "" && q.Revision == 0 {
		return invalidField("unit_version", "required positive observed unit revision")
	}
	if q.TurnNumber == 0 {
		return invalidField("turn_number", "required positive current_turn")
	}
	if !safeMembershipID(q.ID) {
		return invalidField("id", "required stable action ID; see actions execute --help")
	}
	if len(q.PolicyToken) != 64 {
		return invalidField("policy_token", "required runtime policy token (64 characters)")
	}
	if b, err := json.Marshal(q.Input); err != nil || len(b) > 16<<10 || (q.Input.raw == "" && !handlingText(q.Input.Body)) {
		return invalidField("input.body", "required nonempty text, at most 4096 bytes")
	}
	if !handlingText(q.Authority.Reason) {
		return invalidField("authority.reason", "required nonempty text, at most 4096 bytes")
	}
	if len(q.Authority.Constraints) > 4096 {
		return invalidField("authority.constraints", "must be at most 4096 bytes")
	}
	if q.Operation == "" || len(q.Operation) > 128 {
		return invalidField("operation", "must be object.comment or discussion.reply")
	}
	return nil
}

// Called only after loading the member-scoped unit. Preserve the original
// conflict sentinel even for malformed action requests at this store gate.
func referenceActionUnitConflict(u followupUnit, q referenceActionRequest) error {
	if !validReferenceActionRequest(q) {
		return conflictReason("", "action request is invalid", "See actions execute --help.")
	}
	if q.TargetRef != u.TargetRef {
		return conflictReason("target_ref", "target does not match the followup unit", "Read matters read; use the verified object/discussion target without fallback.")
	}
	if q.Revision != u.Revision {
		return versionConflict("unit_version", q.Revision, u.Revision, "Read matters read and reassess; reconcile unknown actions before any resend.")
	}
	if !activeFollowup(u.Status) {
		return conflictReason("unit_ref", "followup is completed or archived", "Read matters read before deciding further work.")
	}
	return nil
}
func unresolvedReferenceAction(u followupUnit) bool {
	for _, a := range u.Actions {
		if a.Status == "unknown" {
			return true
		}
	}
	return false
}
func validateReferenceActions(u followupUnit, target platform.TargetIdentity) error {
	if len(u.Actions) > 32 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, a := range u.Actions {
		q := a.Request
		if seen[q.ID] || !validReferenceActionRequest(q) || q.ObjectRef != u.ObjectRef || q.UnitRef != u.Ref || q.TargetRef != u.TargetRef || !sameNativeTarget(a.Target, target) || !sameNativeTarget(a.Inspection.Target, target) || !a.Access.Valid() || a.Access.RoomIdentity != u.RoomIdentity || a.Access.AgentID != u.AgentID || a.Access.MembershipID != u.MembershipID || a.Inspection.Account != a.Access.Account || !validReferenceSpec(a.Spec, q, target) || a.Access.Account != a.Access.Receiver || a.Access.Account.IdentityScope != target.IdentityScope || a.Attempts < 0 || a.Attempts > a.Spec.Retry.MaxAttempts || (a.PossibleDuplicate && a.Attempts != 2) || (a.Rule == nil) == (a.Basis == nil) || a.UpdatedAt.IsZero() || (a.Status != "unknown" && a.Status != "succeeded" && a.Status != "not_applied") {
			return ErrInvalid
		}
		if a.Rule != nil && (!validPolicyRevision(*a.Rule) || a.Rule.Status != "active" || a.Rule.ID != q.Authority.RuleID || a.Rule.Version != q.Authority.RuleVersion) {
			return ErrInvalid
		}
		if a.Basis != nil && (!validHumanEvidence(a.Basis.Source) || a.Basis.TargetRef != u.TargetRef || !referenceIntentCovers(a.Basis.Intent, followupIntent{Operation: q.Operation, Body: q.Input.Body, Constraints: q.Authority.Constraints}) || !handlingText(a.Basis.Basis)) {
			return ErrInvalid
		}
		if a.Status == "succeeded" && (a.Result == nil || !a.Result.ResultID.Valid() || a.Attempts == 0) {
			return ErrInvalid
		}
		if a.Result != nil && (!sameNativeTarget(a.Result.Target, target) || a.Result.Account != a.Access.Account || a.Result.Status != a.Status || len(a.Result.Message) > 4096 || len(a.Result.URL) > 4096) {
			return ErrInvalid
		}
		seen[q.ID] = true
	}
	if unresolvedReferenceAction(u) && !activeFollowup(u.Status) {
		return ErrInvalid
	}
	return nil
}
func (s *Store) referenceReceiptLocked(id MemberIdentity, u followupUnit, a referenceAction) referenceActionReceipt {
	status := a.Status
	if s.executingReferences[a.Request.key(id)] {
		status = "processing"
	}
	r := referenceActionReceipt{ID: a.Request.ID, Status: status, UnitVersion: u.Revision, Attempts: a.Attempts, PossibleDuplicate: a.PossibleDuplicate, Result: a.Result, TargetRef: a.Request.TargetRef}
	if a.Result == nil {
		r.Target, r.Account = &a.Target, &a.Access.Account
	}
	return r
}

// The Agent declares that the rule body permits this operation. The server
// checks lifecycle and exact target/account separately; it does not verify
// natural-language business scope or authorization.
func (s *Store) referenceAuthorityLocked(id MemberIdentity, u followupUnit, q referenceActionRequest, previous *followupReplyBasis) (*PolicyRevision, *followupReplyBasis, *agentPolicyView, error) {
	p, _, err := s.loadPolicyLocked(id, false)
	if err != nil {
		return nil, nil, nil, err
	}
	view := agentPolicy(p, q.TurnNumber, time.Now())
	if q.PolicyToken != view.PolicyToken {
		return nil, nil, &view, nil
	}
	a := q.Authority
	count := 0
	if a.RuleID != "" {
		count++
	}
	if a.InstructionRef != "" {
		count++
	}
	if a.RoomSeq != 0 {
		count++
	}
	if count != 1 || (a.RuleID == "") != (a.RuleVersion == 0) || (a.InstructionRef == "") != (a.InstructionVersion == 0) || !handlingText(a.Reason) {
		return nil, nil, nil, invalidField("authority", "provide reason and exactly ONE complete rule_id+rule_version, instruction_ref+instruction_version or room_seq")
	}
	if a.RuleID != "" {
		if a.QuestionRef != "" || len(a.MessageRefs) > 0 || a.Constraints != "" {
			return nil, nil, nil, invalidField("authority", "rule authority cannot include question_ref, message_refs or constraints")
		}
		for _, r := range view.Effective {
			if r.ID == a.RuleID && r.Version == a.RuleVersion {
				return &r, nil, nil, nil
			}
		}
		return nil, nil, nil, conflictReason("authority.rule_version", "rule version is not effective", "Read policy read and reassess the rule body; do not substitute a newer rule automatically.")
	}
	intent := followupIntent{Operation: q.Operation, Body: q.Input.Body, Constraints: a.Constraints}
	var b followupReplyBasis
	if a.InstructionRef != "" {
		if a.QuestionRef != "" || len(a.MessageRefs) > 0 {
			return nil, nil, nil, invalidField("authority", "instruction authority cannot include question_ref or message_refs")
		}
		found := false
		for _, i := range u.Instructions {
			if i.Ref == a.InstructionRef && i.Version == a.InstructionVersion && activeFollowupInstruction(i, time.Now()) && referenceIntentCovers(i.Intent, intent) {
				b = i.followupReplyBasis
				found = true
			}
		}
		if !found {
			return nil, nil, nil, conflictReason("authority.instruction_version", "instruction version is inactive, expired or does not cover the exact intent", "Read matters read and reassess the saved instruction; do not replay it.")
		}
	} else {
		if a.QuestionRef != "" {
			for _, question := range u.Questions {
				if question.Ref == a.QuestionRef && question.Proposed != nil && referenceIntentCovers(*question.Proposed, intent) {
					intent = *question.Proposed
				}
			}
		}
		c := followupInstructionChange{followupCommand: q.followupCommand, QuestionRef: a.QuestionRef, MessageRefs: a.MessageRefs, SourceSeq: a.RoomSeq, Intent: intent, Basis: a.Reason}
		// The initial action froze the direct human source before any dispatch.
		if previous != nil && a.QuestionRef == "" {
			b = *previous
		} else {
			b, err = s.instructionBasisLocked(id, u, c)
			if err != nil {
				return nil, nil, nil, err
			}
		}
		for _, i := range u.Instructions {
			if i.Ref == instructionReference(u, b) && (!activeFollowupInstruction(i, time.Now()) || !reflect.DeepEqual(i.followupReplyBasis, b)) {
				return nil, nil, nil, ErrConflict
			}
		}
	}
	if b.TargetRef != u.TargetRef || !referenceIntentCovers(b.Intent, followupIntent{Operation: q.Operation, Body: q.Input.Body, Constraints: a.Constraints}) || !validHumanEvidence(b.Source) {
		return nil, nil, nil, ErrIdentity
	}
	return nil, &b, nil, nil
}
func referenceIntentCovers(scope, actual followupIntent) bool {
	return scope.Operation == actual.Operation && scope.Constraints == actual.Constraints && (scope.Body == "" || scope.Body == actual.Body)
}

func unansweredFollowup(u followupUnit) bool {
	for _, q := range u.Questions {
		if len(q.Answers) == 0 {
			return true
		}
	}
	return false
}
