package room

import (
	"reflect"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Isolated schema-2 execute contract. Native IDs are never accepted from Agent.
type CommentInput struct {
	Body string `json:"body"`
}

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
	Input       CommentInput             `json:"input"`
}

type referenceAction struct {
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
	UnitVersion       uint64                       `json:"unit_version"`
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
	return referenceActionKey{id, q.UnitRef, q.ID}
}
func (q referenceActionRequest) sameBusiness(o referenceActionRequest) bool {
	return q.ObjectRef == o.ObjectRef && q.UnitRef == o.UnitRef && q.TargetRef == o.TargetRef && q.Operation == o.Operation && q.Input == o.Input
}
func validReferenceActionRequest(q referenceActionRequest) bool {
	return referenceActionRequestError(q) == nil
}
func referenceActionRequestError(q referenceActionRequest) error {
	if !validOpaqueRef(q.ObjectRef, "ref_") {
		return invalidField("object_ref", "required stable ref_ reference")
	}
	if !validOpaqueRef(q.UnitRef, "unit_") {
		return invalidField("unit_ref", "required stable unit_ reference")
	}
	if !validOpaqueRef(q.TargetRef, "ref_") {
		return invalidField("target_ref", "required verified ref_ reference")
	}
	if q.Revision == 0 {
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
	if !handlingText(q.Input.Body) {
		return invalidField("input.body", "required nonempty text, at most 4096 bytes")
	}
	if !handlingText(q.Authority.Reason) {
		return invalidField("authority.reason", "required nonempty text, at most 4096 bytes")
	}
	if len(q.Authority.Constraints) > 4096 {
		return invalidField("authority.constraints", "must be at most 4096 bytes")
	}
	if q.Operation != platform.OperationObjectComment && q.Operation != platform.OperationDiscussionReply {
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
func (s *Store) existingReferenceLocked(id MemberIdentity, u followupUnit, q referenceActionRequest) (*referenceActionReceipt, error) {
	for _, a := range u.Actions {
		if a.Request.ID == q.ID {
			if !q.sameBusiness(a.Request) {
				return nil, conflictReason("id", "action ID already exists with different business content", "Restore the original action; do not change content or create a replacement ID for an uncertain write.")
			}
			r := s.referenceReceiptLocked(id, u, a)
			return &r, nil
		}
	}
	return nil, nil
}
func (s *Store) existingReferenceAction(id MemberIdentity, q referenceActionRequest) (*referenceActionReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkFollowupsLocked(id, false); err != nil {
		return nil, err
	}
	u, err := s.loadUnitLocked(id, q.ObjectRef, q.UnitRef)
	if err != nil {
		return nil, err
	}
	return s.existingReferenceLocked(id, u, q)
}

// The selected event is immutable receiving-account evidence, not permission.
func (s *Store) referenceActionEvidence(id MemberIdentity, q referenceActionRequest) (platform.TargetActionRequest, platform.AccessContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.conversationUnitLocked(id, q.followupCommand)
	if err != nil {
		return platform.TargetActionRequest{}, platform.AccessContext{}, err
	}
	if err := referenceActionUnitConflict(u, q); err != nil {
		return platform.TargetActionRequest{}, platform.AccessContext{}, err
	}
	t, err := s.loadTargetLocked(id, u.ObjectRef, u.TargetRef)
	if err != nil {
		return platform.TargetActionRequest{}, platform.AccessContext{}, err
	}
	var access platform.AccessContext
	var aliases []platform.TargetIdentity
	seenAliases := map[string]bool{}
	err = s.visitFollowupEventsLocked(id, func(e followupEvent) error {
		if e.ObjectRef != u.ObjectRef || !e.Resolution.ObjectVerified {
			return nil
		}
		if t.Target.Discussion.Valid() && (!e.Resolution.DiscussionVerified || e.Resolution.Target.Discussion != t.Target.Discussion) {
			return nil
		}
		if !access.Valid() {
			access = e.Access
		}
		if e.Access.Account != access.Account || e.Access.Receiver != access.Receiver {
			return nil
		}
		for _, alias := range e.Resolution.Aliases {
			alias.Discussion = t.Target.Discussion
			alias.Message = platform.ExternalID{}
			key, _ := alias.Key()
			if !seenAliases[key] {
				if len(aliases) >= 32 {
					return ErrCapacity
				}
				aliases = append(aliases, alias)
				seenAliases[key] = true
			}
		}
		return nil
	})
	if err != nil {
		return platform.TargetActionRequest{}, access, err
	}
	if !access.Valid() {
		return platform.TargetActionRequest{}, access, ErrIdentity
	}
	return platform.TargetActionRequest{Target: t.Target, Aliases: aliases, Operation: q.Operation, Input: mustCommentJSON(q.Input)}, access, nil
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
func (s *Store) beginReferenceAction(id MemberIdentity, q referenceActionRequest, request platform.TargetActionRequest, access platform.AccessContext, i platform.TargetActionInspection, spec platform.ActionSpec) (referenceActionReceipt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.conversationUnitLocked(id, q.followupCommand)
	if err != nil {
		return referenceActionReceipt{}, false, err
	}
	if prior, err := s.existingReferenceLocked(id, u, q); prior != nil || err != nil {
		if prior != nil {
			return *prior, false, nil
		}
		return referenceActionReceipt{}, false, err
	}
	if err := referenceActionUnitConflict(u, q); err != nil {
		return referenceActionReceipt{}, false, err
	}
	if targetReference(id, request.Target) != q.TargetRef || !sameNativeTarget(request.Target, i.Target) || i.Account != access.Account || !access.Valid() {
		return referenceActionReceipt{}, false, conflictReason("target_ref", "verified target or executing account does not match", "Restore the exact target and binding; do not redirect the action.")
	}
	for _, a := range u.Actions {
		if a.Status == "unknown" && q.sameBusiness(a.Request) {
			return referenceActionReceipt{}, false, conflictReason("id", "the same business action already has an unknown result", "Inspect remote state and the saved action; never retry unknown under a new ID/turn/Session.")
		}
	}
	if len(u.Actions) >= 32 {
		return referenceActionReceipt{}, false, ErrCapacity
	}
	rule, basis, view, err := s.referenceAuthorityLocked(id, u, q, nil)
	if err != nil {
		return referenceActionReceipt{}, false, err
	}
	if view != nil {
		return referenceActionReceipt{ID: q.ID, Status: "policy_changed", UnitVersion: u.Revision, Policy: view}, false, nil
	}
	a := referenceAction{Request: q, Target: request.Target, Access: access, Inspection: i, Spec: spec, Rule: rule, Basis: basis, Status: "unknown", UpdatedAt: time.Now().UTC()}
	u.Actions = append(u.Actions, a)
	u.Status, u.Next = "in_progress", "动作执行中；回执与业务办结分别记录"
	if unansweredFollowup(u) {
		u.Status = "waiting_user"
	}
	touchFollowup(&u, q.TurnNumber)
	if err := s.saveFollowupUnitLocked(id, u); err != nil {
		return referenceActionReceipt{}, false, err
	}
	if s.executingReferences == nil {
		s.executingReferences = map[referenceActionKey]bool{}
	}
	s.executingReferences[q.key(id)] = true
	return s.referenceReceiptLocked(id, u, a), true, nil
}
func (s *Store) referenceActionAttempt(id MemberIdentity, q referenceActionRequest, duplicate bool) (*agentPolicyView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.conversationUnitLocked(id, q.followupCommand)
	if err != nil {
		return nil, err
	}
	if !s.executingReferences[q.key(id)] || !activeFollowup(u.Status) {
		return nil, ErrConflict
	}
	for j := range u.Actions {
		a := &u.Actions[j]
		if a.Request.ID != q.ID {
			continue
		}
		_, _, view, err := s.referenceAuthorityLocked(id, u, q, a.Basis)
		if err != nil || view != nil {
			return view, err
		}
		if a.Status != "unknown" || a.Attempts >= a.Spec.Retry.MaxAttempts {
			return nil, ErrConflict
		}
		a.Attempts++
		a.PossibleDuplicate = duplicate
		a.UpdatedAt = time.Now().UTC()
		touchFollowup(&u, q.TurnNumber)
		return nil, s.saveFollowupUnitLocked(id, u)
	}
	return nil, ErrRequest
}
func (s *Store) finishReferenceAction(id MemberIdentity, q referenceActionRequest, result platform.TargetActionResult) (referenceActionReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer delete(s.executingReferences, q.key(id))
	if err := s.checkFollowupsLocked(id, true); err != nil {
		return referenceActionReceipt{}, err
	}
	u, err := s.loadUnitLocked(id, q.ObjectRef, q.UnitRef)
	if err != nil {
		return referenceActionReceipt{}, err
	}
	for j := range u.Actions {
		a := &u.Actions[j]
		if a.Request.ID != q.ID {
			continue
		}
		a.Status, a.Result, a.UpdatedAt = result.Status, &result, time.Now().UTC()
		u.Status, u.Next = "in_progress", "动作回执已保存；按业务条件继续办理"
		if unansweredFollowup(u) {
			u.Status, u.Next = "waiting_user", "动作回执已保存，仍有其他问题等待用户答复"
		}
		if result.Status == "unknown" {
			u.Status, u.Next = "unknown", "结果不确定，可能已发送；查询核实，不得自动重发"
		}
		// Fulfil only the exact persisted instruction; other pending work remains.
		if result.Status == "succeeded" && q.Authority.InstructionRef != "" {
			for k := range u.Instructions {
				i := &u.Instructions[k]
				if i.Ref == q.Authority.InstructionRef && i.Version == q.Authority.InstructionVersion && i.Status == "active" {
					i.Status = "fulfilled"
					i.Version++
					i.Evidence = "动作 " + q.ID + " 已成功"
					i.UpdatedAt = time.Now().UTC()
				}
			}
		}
		if unresolvedReferenceAction(u) {
			u.Status, u.Next = "unknown", "仍有未核实的动作；查询核实，不得自动重发"
		}
		touchFollowup(&u, q.TurnNumber)
		if err := s.saveFollowupUnitLocked(id, u); err != nil {
			return referenceActionReceipt{}, err
		}
		r := s.referenceReceiptLocked(id, u, *a)
		r.Status = a.Status
		return r, nil
	}
	return referenceActionReceipt{}, ErrRequest
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
