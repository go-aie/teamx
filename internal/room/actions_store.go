package room

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

const actionSchema = 1

type storedAction struct {
	Version int `json:"version"`
	MemberIdentity
	Action referenceAction `json:"action"`
}

func (s *Store) actionRoot(id MemberIdentity) string {
	return filepath.Join(s.dir, "agents", id.MembershipID, "actions")
}
func (s *Store) actionPath(id MemberIdentity, key string) string {
	return filepath.Join(s.actionRoot(id), "records", referenceDigest("", key)+".json")
}

func (s *Store) loadActionsLocked(id MemberIdentity) ([]referenceAction, error) {
	files, err := followupFiles(filepath.Join(s.actionRoot(id), "records"), "records")
	if err != nil {
		return nil, err
	}
	actions := make([]referenceAction, 0, len(files))
	for _, path := range files {
		var record storedAction
		if err := readSnapshot(path, &record, followupBytes); err != nil {
			return nil, err
		}
		if record.Version != actionSchema {
			return nil, ErrVersion
		}
		a := record.Action
		if !actionAuthorityMatchesSpec(a.Request, a.Spec) || (a.Spec.DirectRoomInstructionOnly && a.Basis != nil && a.Basis.Source.ReplyTo != 0) {
			return nil, ErrInvalid
		}
		if a.IdempotencyKey != referenceDigest("", id, a.Request.ID)[:32] {
			return nil, ErrInvalid
		}
		if record.MemberIdentity != id || path != s.actionPath(id, a.Request.ID) || !validReferenceActionRequest(a.Request) || targetReference(id, a.Target) != a.Request.TargetRef || !sameNativeTarget(a.Target, a.Inspection.Target) || a.Access.RoomIdentity != id.RoomIdentity || a.Access.AgentID != id.AgentID || a.Access.MembershipID != id.MembershipID || !a.Access.Valid() || a.Access.Account != a.Inspection.Account || !validReferenceSpec(a.Spec, a.Request, a.Target) || a.Attempts < 0 || a.Attempts > a.Spec.Retry.MaxAttempts || a.UpdatedAt.IsZero() || !validActionBasis(a) {
			return nil, ErrInvalid
		}
		if a.Status != "unknown" && a.Status != "not_applied" && a.Status != "succeeded" {
			return nil, ErrInvalid
		}
		if a.Rule != nil && (!validPolicyRevision(*a.Rule) || a.Rule.ID != a.Request.Authority.RuleID || a.Rule.Version != a.Request.Authority.RuleVersion) {
			return nil, ErrInvalid
		}
		if a.Basis != nil && (!validHumanEvidence(a.Basis.Source) || a.Basis.TargetRef != a.Request.TargetRef) {
			return nil, ErrInvalid
		}
		if a.Result != nil && (!sameNativeTarget(a.Result.Target, a.Target) || a.Result.Account != a.Access.Account || a.Result.Status != a.Status) {
			return nil, ErrInvalid
		}
		if a.Status == "succeeded" && (a.Result == nil || !a.Result.ResultID.Valid() || a.Attempts == 0) {
			return nil, ErrInvalid
		}
		a.Ledger = true
		actions = append(actions, a)
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].Request.ID < actions[j].Request.ID })
	return actions, nil
}
func (s *Store) saveActionLocked(id MemberIdentity, a referenceAction) error {
	if _, _, err := s.memberPath(id, "agent.json", true); err != nil {
		return err
	}
	return s.save(s.actionPath(id, a.Request.ID), storedAction{actionSchema, id, a}, followupBytes)
}

// The unit is an optional business context. It never supplies the execution target.
func (s *Store) actionContextLocked(id MemberIdentity, q referenceActionRequest, running bool) (followupUnit, error) {
	if err := s.checkFollowupsLocked(id, running); err != nil {
		return followupUnit{}, err
	}
	if running {
		if _, err := s.runningTurnLocked(id, q.TurnNumber); err != nil {
			return followupUnit{}, err
		}
	}
	if q.UnitRef == "" {
		return followupUnit{followupHeader: followupHeader{followupSchema, id}, TargetRef: q.TargetRef, ObjectRef: q.TargetRef, Status: "pending"}, nil
	}
	return s.loadUnitLocked(id, q.ObjectRef, q.UnitRef)
}

// Legacy records stay in their original unit and are never promoted to a new intent.
func (s *Store) existingReferenceAction(id MemberIdentity, q referenceActionRequest) (*referenceActionReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkFollowupsLocked(id, false); err != nil {
		return nil, err
	}
	actions, err := s.loadActionsLocked(id)
	if err != nil {
		return nil, err
	}
	for _, a := range actions {
		if a.Request.ID != q.ID {
			continue
		}
		if !q.sameBusiness(a.Request) {
			return nil, conflictReason("id", "action ID already exists with different business content (target, input or authority)", "Read actions read; restore the original request.")
		}
		u, err := s.actionContextLocked(id, q, false)
		if err != nil {
			return nil, err
		}
		r := s.referenceReceiptLocked(id, u, a)
		return &r, nil
	}
	units, err := s.followupUnitsLocked(id, "")
	if err != nil {
		return nil, err
	}
	var prior *referenceActionReceipt
	for _, u := range units {
		for _, a := range u.Actions {
			if a.Ledger || a.Request.ID != q.ID {
				continue
			}
			if q.UnitRef == "" || (q.UnitRef == u.Ref && q.ObjectRef == u.ObjectRef) {
				if prior != nil {
					return nil, conflictReason("id", "ambiguous historical action ID", "Supply the original object_ref and unit_ref context.")
				}
				if !q.sameBusiness(a.Request) {
					return nil, ErrConflict
				}
				r := s.referenceReceiptLocked(id, u, a)
				prior = &r
			}
		}
	}
	return prior, nil
}

func (s *Store) referenceActionEvidence(id MemberIdentity, q referenceActionRequest) (platform.TargetActionRequest, platform.AccessContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.actionContextLocked(id, q, true)
	if err != nil {
		return platform.TargetActionRequest{}, platform.AccessContext{}, err
	}
	if q.UnitRef != "" && u.Revision != q.Revision {
		return platform.TargetActionRequest{}, platform.AccessContext{}, versionConflict("unit_version", q.Revision, u.Revision, "Read matters read and reassess; reconcile unknown actions before any resend.")
	}
	if q.UnitRef != "" && !activeFollowup(u.Status) {
		return platform.TargetActionRequest{}, platform.AccessContext{}, ErrConflict
	}
	target, err := s.loadActionTargetLocked(id, q.TargetRef)
	if err == nil {
		return platform.TargetActionRequest{Target: target.Target, Aliases: target.Resolution.Aliases, Operation: q.Operation, Input: actionInputJSON(q.Input)}, target.Access, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return platform.TargetActionRequest{}, platform.AccessContext{}, err
	}
	// Existing supported followup locators bootstrap a target; no new URL or native
	// GitLab entry is admitted here. The preparer selects current managed credentials.
	if q.UnitRef == "" || q.TargetRef != u.TargetRef {
		return platform.TargetActionRequest{}, platform.AccessContext{}, platform.ErrReference
	}
	t, err := s.loadTargetLocked(id, u.ObjectRef, q.TargetRef)
	if err != nil {
		return platform.TargetActionRequest{}, platform.AccessContext{}, err
	}
	var access platform.AccessContext
	var aliases []platform.TargetIdentity
	err = s.visitFollowupEventsLocked(id, func(e followupEvent) error {
		if e.ObjectRef != u.ObjectRef || !e.Resolution.ObjectVerified || (t.Target.Discussion.Valid() && (!e.Resolution.DiscussionVerified || e.Resolution.Target.Discussion != t.Target.Discussion)) {
			return nil
		}
		if !access.Valid() {
			access = e.Access
		}
		if e.Access.Account == access.Account {
			for _, alias := range e.Resolution.Aliases {
				alias.Discussion = t.Target.Discussion
				alias.Message = platform.ExternalID{}
				if len(aliases) < 32 {
					aliases = append(aliases, alias)
				}
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
	return platform.TargetActionRequest{Target: t.Target, Aliases: aliases, Operation: q.Operation, Input: actionInputJSON(q.Input)}, access, nil
}

func (s *Store) actionAuthorityLocked(id MemberIdentity, u followupUnit, q referenceActionRequest, previous *followupReplyBasis) (*PolicyRevision, *followupReplyBasis, *agentPolicyView, error) {
	// Instructions and question replies retain the original unit's exact scope.
	if q.Authority.InstructionRef != "" || q.Authority.QuestionRef != "" || len(q.Authority.MessageRefs) > 0 {
		if q.UnitRef == "" {
			return nil, nil, nil, ErrIdentity
		}
		if u.TargetRef == q.TargetRef {
			return s.referenceAuthorityLocked(id, u, q, previous)
		}
		// A unit remains the native object/discussion. Its authority may name an
		// exact captured message, but cannot grant another message or object.
		t, err := s.loadTargetLocked(id, u.ObjectRef, q.TargetRef)
		if err != nil || !t.Target.Message.Valid() || s.validateQuestionTargetLocked(id, u, []string{q.TargetRef}) != nil {
			return nil, nil, nil, ErrIdentity
		}
		var normalized *followupReplyBasis
		if previous != nil {
			copy := *previous
			copy.TargetRef = u.TargetRef
			normalized = &copy
		}
		rule, basis, view, err := s.referenceAuthorityLocked(id, u, q, normalized)
		if err != nil || view != nil {
			return rule, basis, view, err
		}
		if basis == nil || !slices.Contains(basis.MessageRefs, q.TargetRef) {
			return nil, nil, nil, ErrIdentity
		}
		copy := *basis
		copy.TargetRef = q.TargetRef
		return rule, &copy, nil, nil
	}
	p, _, err := s.loadPolicyLocked(id, false)
	if err != nil {
		return nil, nil, nil, err
	}
	view := agentPolicy(p, q.TurnNumber, time.Now())
	if q.PolicyToken != view.PolicyToken {
		return nil, nil, &view, nil
	}
	if q.Authority.RuleID != "" {
		return s.referenceAuthorityLocked(id, u, q, previous)
	}
	if q.Authority.RoomSeq == 0 || q.Authority.RuleVersion != 0 || q.Authority.InstructionVersion != 0 {
		return nil, nil, nil, ErrRequest
	}
	if previous != nil {
		if previous.Source.Seq != q.Authority.RoomSeq {
			return nil, nil, nil, ErrIdentity
		}
		return nil, previous, nil, nil
	}
	room, err := s.loadRoom()
	if err != nil {
		return nil, nil, nil, err
	}
	human, err := roomHumanMessage(room, q.Authority.RoomSeq)
	if err != nil {
		return nil, nil, nil, err
	}
	if human.ReplyTo != 0 || !validHumanEvidence(human) {
		return nil, nil, nil, ErrIdentity
	}
	basis := followupReplyBasis{TargetRef: q.TargetRef, Source: human, Intent: followupIntent{Operation: q.Operation, Body: q.Input.Body, Constraints: q.Authority.Constraints}, Basis: q.Authority.Reason}
	return nil, &basis, nil, nil
}

func sameActionSource(a, b referenceActionAuthority) bool {
	// Reason wording/turns are not a new authorization. Native operation changes
	// cannot evade an uncertain action from the same human/instruction/rule source.
	return a.RoomSeq == b.RoomSeq && a.RuleID == b.RuleID && a.RuleVersion == b.RuleVersion && a.InstructionRef == b.InstructionRef && a.InstructionVersion == b.InstructionVersion
}
func (s *Store) beginReferenceAction(id MemberIdentity, q referenceActionRequest, request platform.TargetActionRequest, access platform.AccessContext, i platform.TargetActionInspection, spec platform.ActionSpec) (referenceActionReceipt, bool, error) {
	return s.beginReferenceActionWithBasis(id, q, request, access, i, spec, nil)
}
func validActionBasis(a referenceAction) bool {
	if a.Spec.BindingEvidence != "" {
		return a.Rule == nil && a.Basis == nil && a.Access.ActionAuthorizationVersion > 0 && a.CapabilityBasis.Matches(a.Spec.BindingEvidence, a.Access, a.Target)
	}
	return a.CapabilityBasis == nil && (a.Rule == nil) != (a.Basis == nil)
}
func (s *Store) beginReferenceActionWithBasis(id MemberIdentity, q referenceActionRequest, request platform.TargetActionRequest, access platform.AccessContext, i platform.TargetActionInspection, spec platform.ActionSpec, evidence *platform.CapabilityEvidence) (referenceActionReceipt, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !actionAuthorityMatchesSpec(q, spec) {
		return referenceActionReceipt{}, false, ErrIdentity
	}
	u, err := s.actionContextLocked(id, q, true)
	if err != nil {
		return referenceActionReceipt{}, false, err
	}
	if q.UnitRef != "" && (q.Revision != u.Revision || !activeFollowup(u.Status)) {
		return referenceActionReceipt{}, false, ErrConflict
	}
	if targetReference(id, request.Target) != q.TargetRef || !sameNativeTarget(request.Target, i.Target) || i.Account != access.Account || !access.Valid() {
		return referenceActionReceipt{}, false, ErrIdentity
	}
	if i.ContextOnly && q.Authority.RuleID != "" {
		return referenceActionReceipt{}, false, conflictReason("target_ref", "own messages are context only and cannot trigger an automatic reply", "Use the message as progress evidence; a new reply requires explicit human authority.")
	}
	actions, err := s.loadActionsLocked(id)
	if err != nil {
		return referenceActionReceipt{}, false, err
	}
	// Scan historical records too, so alternate entry points cannot replay unknown.
	units, err := s.followupUnitsLocked(id, "")
	if err != nil {
		return referenceActionReceipt{}, false, err
	}
	for _, unit := range units {
		for _, a := range unit.Actions {
			if !a.Ledger {
				actions = append(actions, a)
			}
		}
	}
	for _, a := range actions {
		if a.Request.ID == q.ID {
			if !q.sameBusiness(a.Request) {
				return referenceActionReceipt{}, false, ErrConflict
			}
			return s.referenceReceiptLocked(id, u, a), false, nil
		}
		sameCapabilityContent := (spec.BindingEvidence != "" || a.Spec.BindingEvidence != "") && q.Input.Body != "" && q.Input.Body == a.Request.Input.Body
		if a.Status == "unknown" && (sameCapabilityContent || q.sameBusiness(a.Request) || (sameActionSource(q.Authority, a.Request.Authority) && (q.Input == a.Request.Input || (q.Input.Body != "" && q.Input.Body == a.Request.Input.Body)))) {
			return referenceActionReceipt{}, false, conflictReason("id", "the authorization already has an uncertain write", "Read actions read; do not change ID, platform or operation to resend.")
		}
	}
	if len(actions) >= followupCapacity {
		return referenceActionReceipt{}, false, ErrCapacity
	}
	var rule *PolicyRevision
	var basis *followupReplyBasis
	var view *agentPolicyView
	if spec.BindingEvidence != "" {
		if access.ActionAuthorizationVersion == 0 || !currentCapabilityEvidence(evidence, spec, access, request.Target) {
			return referenceActionReceipt{}, false, ErrIdentity
		}
	} else {
		rule, basis, view, err = s.actionAuthorityLocked(id, u, q, nil)
	}
	if err != nil {
		return referenceActionReceipt{}, false, err
	}
	if view != nil {
		return referenceActionReceipt{ID: q.ID, Status: "policy_changed", Policy: view}, false, nil
	}
	a := referenceAction{IdempotencyKey: referenceDigest("", id, q.ID)[:32], Ledger: true, CapabilityBasis: evidence, Request: q, Target: request.Target, Access: access, Inspection: i, Spec: spec, Rule: rule, Basis: basis, Status: "unknown", UpdatedAt: time.Now().UTC()}
	if err := s.saveActionLocked(id, a); err != nil {
		return referenceActionReceipt{}, false, err
	}
	if s.executingReferences == nil {
		s.executingReferences = map[referenceActionKey]bool{}
	}
	s.executingReferences[q.key(id)] = true
	if q.UnitRef != "" {
		u.Status, u.Next = "in_progress", "动作意图已保存；动作和业务进度分别记录"
		if unansweredFollowup(u) {
			u.Status = "waiting_user"
		}
		touchFollowup(&u, q.TurnNumber)
		if err := s.saveFollowupUnitLocked(id, u); err != nil {
			delete(s.executingReferences, q.key(id))
			return referenceActionReceipt{}, false, err
		}
	}
	return s.referenceReceiptLocked(id, u, a), true, nil
}
func (s *Store) referenceActionAttempt(id MemberIdentity, q referenceActionRequest, duplicate bool) (*agentPolicyView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, err := s.actionContextLocked(id, q, true)
	if err != nil {
		return nil, err
	}
	if !s.executingReferences[q.key(id)] || !activeFollowup(u.Status) {
		return nil, ErrConflict
	}
	actions, err := s.loadActionsLocked(id)
	if err != nil {
		return nil, err
	}
	for _, a := range actions {
		if a.Request.ID == q.ID {
			var view *agentPolicyView
			var err error
			if a.Spec.BindingEvidence == "" {
				_, _, view, err = s.actionAuthorityLocked(id, u, q, a.Basis)
			}
			if err != nil || view != nil {
				return view, err
			}
			if a.Status != "unknown" || a.Attempts >= a.Spec.Retry.MaxAttempts {
				return nil, ErrConflict
			}
			a.Attempts++
			a.PossibleDuplicate = duplicate
			a.UpdatedAt = time.Now().UTC()
			return nil, s.saveActionLocked(id, a)
		}
	}
	return nil, ErrRequest
}
func (s *Store) finishReferenceAction(id MemberIdentity, q referenceActionRequest, result platform.TargetActionResult) (referenceActionReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer delete(s.executingReferences, q.key(id))
	if _, _, err := s.memberPath(id, "agent.json", true); err != nil {
		return referenceActionReceipt{}, err
	}
	actions, err := s.loadActionsLocked(id)
	if err != nil {
		return referenceActionReceipt{}, err
	}
	for _, a := range actions {
		if a.Request.ID == q.ID {
			a.Status, a.Result, a.UpdatedAt = result.Status, &result, time.Now().UTC()
			// Authority settles first. An optional projection failure can never redo a write.
			if err := s.saveActionLocked(id, a); err != nil {
				return referenceActionReceipt{}, err
			}
			u, err := s.actionContextLocked(id, q, false)
			if err != nil {
				return referenceActionReceipt{}, err
			}
			if q.UnitRef != "" {
				u.Status, u.Next = "in_progress", "动作回执已保存；按业务条件继续办理"
				if unansweredFollowup(u) {
					u.Status = "waiting_user"
				}
				if unresolvedReferenceAction(u) {
					u.Status, u.Next = "unknown", "结果不确定，可能已发送；查询核实，不得自动重发"
				}
				if result.Status == "succeeded" && q.Authority.InstructionRef != "" {
					for j := range u.Instructions {
						instruction := &u.Instructions[j]
						if instruction.Ref == q.Authority.InstructionRef && instruction.Version == q.Authority.InstructionVersion && instruction.Status == "active" {
							instruction.Status = "fulfilled"
							instruction.Version++
							instruction.Evidence = "动作 " + q.ID + " 已成功"
							instruction.UpdatedAt = time.Now().UTC()
						}
					}
				}
				touchFollowup(&u, q.TurnNumber)
				if err := s.saveFollowupUnitLocked(id, u); err != nil {
					return referenceActionReceipt{}, err
				}
			}
			r := s.referenceReceiptLocked(id, u, a)
			r.Status = a.Status
			return r, nil
		}
	}
	return referenceActionReceipt{}, ErrRequest
}

// No independent action index: a unit view is rebuilt from the authoritative
// member records on every read. Saving business progress strips this projection.
func (s *Store) projectUnitActionsLocked(id MemberIdentity, u *followupUnit) error {
	actions, err := s.loadActionsLocked(id)
	if err != nil {
		return err
	}
	for _, a := range actions {
		if a.Request.UnitRef == u.Ref && a.Request.ObjectRef == u.ObjectRef {
			u.Actions = append(u.Actions, a)
		}
	}
	return nil
}

func (s *Store) queryActions(id MemberIdentity, key, object, unit string) ([]referenceAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkFollowupsLocked(id, false); err != nil {
		return nil, err
	}
	actions, err := s.loadActionsLocked(id)
	if err != nil {
		return nil, err
	}
	units, err := s.followupUnitsLocked(id, object)
	if err != nil {
		return nil, err
	}
	for _, u := range units {
		for _, a := range u.Actions {
			if !a.Ledger {
				actions = append(actions, a)
			}
		}
	}
	result := []referenceAction{}
	for _, a := range actions {
		if (key == "" || a.Request.ID == key) && (object == "" || a.Request.ObjectRef == object) && (unit == "" || a.Request.UnitRef == unit) {
			result = append(result, a)
		}
	}
	if key != "" && len(result) > 1 {
		return nil, conflictReason("id", "ambiguous historical ID", "Supply --object and --unit.")
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Request.ID == result[j].Request.ID {
			return result[i].Request.UnitRef < result[j].Request.UnitRef
		}
		return result[i].Request.ID < result[j].Request.ID
	})
	return result, nil
}
