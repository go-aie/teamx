package room

import (
	"reflect"
	"strings"
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
	return validOpaqueRef(q.ObjectRef, "ref_") && validOpaqueRef(q.UnitRef, "unit_") && validOpaqueRef(q.TargetRef, "ref_") && q.Revision > 0 && q.TurnNumber > 0 && safeMembershipID(q.ID) && len(q.PolicyToken) == 64 && handlingText(q.Input.Body) && handlingText(q.Authority.Reason) && len(q.Authority.Constraints) <= 4096 && (q.Operation == platform.OperationObjectComment || q.Operation == platform.OperationDiscussionReply)
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
		if a.Rule != nil && (!validPolicyRevision(*a.Rule) || a.Rule.Kind != "rule" || a.Rule.Status != "active" || a.Rule.ID != q.Authority.RuleID || a.Rule.Version != q.Authority.RuleVersion || !referenceRuleCovers(*a.Rule, a.Inspection)) {
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
	return referenceActionReceipt{ID: a.Request.ID, Status: status, UnitVersion: u.Revision, Attempts: a.Attempts, PossibleDuplicate: a.PossibleDuplicate, Result: a.Result}
}
func (s *Store) existingReferenceLocked(id MemberIdentity, u followupUnit, q referenceActionRequest) (*referenceActionReceipt, error) {
	for _, a := range u.Actions {
		if a.Request.ID == q.ID {
			if !q.sameBusiness(a.Request) {
				return nil, ErrConflict
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
	if !validReferenceActionRequest(q) || q.TargetRef != u.TargetRef || q.Revision != u.Revision || !activeFollowup(u.Status) {
		return platform.TargetActionRequest{}, platform.AccessContext{}, ErrConflict
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

// Natural-language conditions/exceptions remain the Agent's explicit reason.
// Scope, version, source and exact single-use body are checked deterministically.
func referenceRuleCovers(r PolicyRevision, i platform.TargetActionInspection) bool {
	t := i.Target
	scope := r.Scope
	if scope.Provider != "" && scope.Provider != t.Provider || scope.Instance != "" && strings.TrimSuffix(scope.Instance, "/") != t.Instance || scope.Account != "" && scope.Account != i.AccountLabel || scope.ObjectType != "" && scope.ObjectType != t.Object.Type {
		return false
	}
	if scope.AccountID.Valid() && (scope.AccountID != i.Account.ID || scope.Tenant != i.Account.Tenant) {
		return false
	}
	if !scope.AccountID.Valid() && t.Tenant.Valid() {
		return false // a display label alone cannot select a tenant
	}
	if scope.NoContainers {
		return len(t.Containers) == 0
	}
	if scope.Project != "" {
		if scope.Project == i.ContainerLabel {
			return true
		}
		for _, c := range t.Containers {
			if c.ID.Value == scope.Project {
				return true
			}
		}
		return false
	}
	return true
}
func (s *Store) referenceAuthorityLocked(id MemberIdentity, u followupUnit, q referenceActionRequest, inspection platform.TargetActionInspection, previous *followupReplyBasis) (*PolicyRevision, *followupReplyBasis, *agentPolicyView, error) {
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
		return nil, nil, nil, ErrRequest
	}
	if a.RuleID != "" {
		if a.QuestionRef != "" || len(a.MessageRefs) > 0 || a.Constraints != "" {
			return nil, nil, nil, ErrRequest
		}
		for _, r := range view.Effective {
			if r.ID == a.RuleID && r.Version == a.RuleVersion && r.Kind == "rule" && referenceRuleCovers(r, inspection) {
				return &r, nil, nil, nil
			}
		}
		return nil, nil, nil, ErrConflict
	}
	intent := followupIntent{Operation: q.Operation, Body: q.Input.Body, Constraints: a.Constraints}
	var b followupReplyBasis
	if a.InstructionRef != "" {
		if a.QuestionRef != "" || len(a.MessageRefs) > 0 {
			return nil, nil, nil, ErrRequest
		}
		found := false
		for _, i := range u.Instructions {
			if i.Ref == a.InstructionRef && i.Version == a.InstructionVersion && activeFollowupInstruction(i, time.Now()) && referenceIntentCovers(i.Intent, intent) {
				b = i.followupReplyBasis
				found = true
			}
		}
		if !found {
			return nil, nil, nil, ErrConflict
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
	if !validReferenceActionRequest(q) || q.Revision != u.Revision || q.TargetRef != u.TargetRef || !activeFollowup(u.Status) || targetReference(id, request.Target) != q.TargetRef || !sameNativeTarget(request.Target, i.Target) || i.Account != access.Account || !access.Valid() {
		return referenceActionReceipt{}, false, ErrConflict
	}
	for _, a := range u.Actions {
		if a.Status == "unknown" && q.sameBusiness(a.Request) {
			return referenceActionReceipt{}, false, ErrConflict
		}
	}
	if len(u.Actions) >= 32 {
		return referenceActionReceipt{}, false, ErrCapacity
	}
	rule, basis, view, err := s.referenceAuthorityLocked(id, u, q, i, nil)
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
		_, _, view, err := s.referenceAuthorityLocked(id, u, q, a.Inspection, a.Basis)
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
