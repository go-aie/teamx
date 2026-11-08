package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

type actionResolveRequest struct {
	AccountOwnerSeq uint64                 `json:"account_owner_seq,omitempty"` // direct human confirmation of displayed execution account ownership
	Provider        string                 `json:"provider,omitempty"`
	Locator         platform.TargetLocator `json:"locator"`
	TurnNumber      uint64                 `json:"turn_number"`
}
type actionTargetResolver func(context.Context, MemberIdentity, platform.TargetLocator) (platform.TargetResolution, referenceActionSession, error)
type actionTarget struct {
	Version int `json:"version"`
	MemberIdentity
	Ref        string                    `json:"target_ref"`
	Target     platform.TargetIdentity   `json:"target"`
	Resolution platform.TargetResolution `json:"resolution"`
	Access     platform.AccessContext    `json:"access"`
}
type actionTargetCapability struct {
	platform.ActionSpec
	Executable     bool   `json:"executable"`
	DisabledReason string `json:"disabled_reason,omitempty"`
}
type actionTargetView struct {
	TargetRef      string                    `json:"target_ref,omitempty"`
	Resolution     platform.TargetResolution `json:"resolution"`
	Actions        []actionTargetCapability  `json:"actions"`
	DisabledReason string                    `json:"disabled_reason,omitempty"`
}

func (s *Store) actionTargetPath(id MemberIdentity, ref string) string {
	return filepath.Join(s.actionRoot(id), "targets", ref+".json")
}
func (s *Store) loadActionTargetLocked(id MemberIdentity, ref string) (actionTarget, error) {
	var t actionTarget
	if !validOpaqueRef(ref, "ref_") {
		return t, ErrRequest
	}
	if err := readSnapshot(s.actionTargetPath(id, ref), &t, followupBytes); err != nil {
		return t, err
	}
	if t.Version != actionSchema {
		return t, ErrVersion
	}
	if t.Resolution.Status != platform.ResolutionResolved || !t.Resolution.Complete || !t.Resolution.ObjectVerified || (t.Target.Message.Valid() && !t.Resolution.MessageVerified) || (t.Target.Discussion.Valid() && !t.Resolution.DiscussionVerified) || t.MemberIdentity != id || t.Ref != ref || targetReference(id, t.Target) != ref || t.Resolution.Target == nil || !sameNativeTarget(t.Target, *t.Resolution.Target) || t.Resolution.Account != t.Access.Account || !t.Access.Valid() || t.Access.RoomIdentity != id.RoomIdentity || t.Access.AgentID != id.AgentID || t.Access.MembershipID != id.MembershipID || t.Resolution.CheckedAt.IsZero() {
		return t, ErrInvalid
	}
	return t, nil
}
func (s *Store) saveActionTarget(id MemberIdentity, r platform.TargetResolution, a platform.AccessContext) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkFollowupsLocked(id, true); err != nil {
		return "", err
	}
	if r.Status != platform.ResolutionResolved || !r.Complete || !r.ObjectVerified || r.Target == nil || !r.Target.Valid() || r.Account != a.Account || !a.Valid() || a.RoomIdentity != id.RoomIdentity || a.AgentID != id.AgentID || a.MembershipID != id.MembershipID || r.CheckedAt.IsZero() || (r.Target.Discussion.Valid() && !r.DiscussionVerified) || (r.Target.Message.Valid() && !r.MessageVerified) {
		return "", platform.ErrReference
	}
	ref := targetReference(id, *r.Target)
	old, err := s.loadActionTargetLocked(id, ref)
	if err == nil && !sameNativeTarget(old.Target, *r.Target) {
		return "", ErrConflict
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	files, err := followupFiles(filepath.Join(s.actionRoot(id), "targets"), "targets")
	if err != nil {
		return "", err
	}
	if len(files) >= 4*followupCapacity {
		return "", ErrCapacity
	}
	return ref, s.save(s.actionTargetPath(id, ref), actionTarget{actionSchema, id, ref, *r.Target, r, a}, followupBytes)
}

func (s *Store) actionReferenceEvidence(id MemberIdentity, ref string, turn uint64) (platform.TargetActionRequest, platform.AccessContext, error) {
	s.mu.Lock()
	if t, err := s.loadActionTargetLocked(id, ref); err == nil {
		s.mu.Unlock()
		return platform.TargetActionRequest{Target: t.Target, Aliases: t.Resolution.Aliases}, t.Access, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		s.mu.Unlock()
		return platform.TargetActionRequest{}, platform.AccessContext{}, err
	}
	// Captured notification targets can lack current access evidence. Keep the
	// native message ref; the provider resolver must select and reverify access.
	var captured platform.TargetActionRequest
	var capturedAccess platform.AccessContext
	err := s.visitFollowupEventsLocked(id, func(e followupEvent) error {
		if e.TargetRef != ref || !e.Resolution.ObjectVerified || !e.Resolution.MessageVerified || e.Resolution.Target == nil {
			return nil
		}
		t, err := s.loadTargetLocked(id, e.ObjectRef, ref)
		if err != nil {
			return err
		}
		captured, capturedAccess = platform.TargetActionRequest{Target: t.Target, Aliases: e.Resolution.Aliases}, e.Access
		return nil
	})
	if err != nil || captured.Target.Valid() {
		s.mu.Unlock()
		return captured, capturedAccess, err
	}
	units, err := s.followupUnitsLocked(id, "")
	s.mu.Unlock()
	if err != nil {
		return platform.TargetActionRequest{}, platform.AccessContext{}, err
	}
	for _, u := range units {
		if u.TargetRef == ref {
			q := referenceActionRequest{followupCommand: followupCommand{ObjectRef: u.ObjectRef, UnitRef: u.Ref, Revision: u.Revision, TurnNumber: turn}, TargetRef: ref}
			request, access, err := s.referenceActionEvidence(id, q)
			return request, access, err
		}
	}
	return platform.TargetActionRequest{}, platform.AccessContext{}, platform.ErrReference
}

func applicableActionSpec(spec platform.ActionSpec, t platform.TargetIdentity) bool {
	return validReferenceSpec(spec, referenceActionRequest{Operation: spec.Operation}, t)
}
func (m *roomManager) resolveActionTarget(ctx context.Context, req apiRequest, q actionResolveRequest) (actionTargetView, error) {
	id := MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	err := m.checkToolMemberLocked(ctx, toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller})
	m.mu.Unlock()
	if err != nil {
		return actionTargetView{}, err
	}
	if svc == nil || req.Caller.Kind != "agent" || req.AgentID != "" || req.MembershipID != "" {
		return actionTargetView{}, ErrIdentity
	}
	svc.changes.RLock()
	defer svc.changes.RUnlock()
	if svc.closing.Load() {
		return actionTargetView{}, ErrClosing
	}
	if q.TurnNumber == 0 {
		return actionTargetView{}, ErrRequest
	}
	if err := svc.store.initReferenceFollowups(id); err != nil {
		return actionTargetView{}, err
	}
	svc.store.mu.Lock()
	_, err = svc.store.runningTurnLocked(id, q.TurnNumber)
	svc.store.mu.Unlock()
	if err != nil {
		return actionTargetView{}, err
	}
	locator := q.Locator
	var r platform.TargetResolution
	var session referenceActionSession
	if locator.TargetRef != "" {
		if locator.Self || locator.Candidate != nil || locator.Source != nil || locator.URL != "" || locator.Query != nil {
			return actionTargetView{}, ErrRequest
		}
		resolved, access, e := svc.store.actionReferenceEvidence(id, locator.TargetRef, q.TurnNumber)
		if e != nil {
			return actionTargetView{}, e
		}
		target := resolved.Target
		if q.Provider != "" && q.Provider != target.Provider {
			return actionTargetView{}, ErrIdentity
		}
		m.mu.Lock()
		prepare := m.referenceExecutors[target.Provider]
		m.mu.Unlock()
		if prepare == nil {
			return actionTargetView{Actions: []actionTargetCapability{}, DisabledReason: "No executable adapter is registered for this target."}, nil
		}
		if !access.Valid() {
			m.mu.Lock()
			resolve := m.actionResolvers[target.Provider]
			m.mu.Unlock()
			if resolve == nil {
				return actionTargetView{}, platform.ErrReference
			}
			r, session, err = resolve(ctx, id, platform.TargetLocator{Candidate: &target})
		} else {
			session, err = prepare(ctx, id, access)
		}
		if err != nil {
			if session.Close != nil {
				session.Close()
			}
			return actionTargetView{}, err
		}
		locator = platform.TargetLocator{Candidate: &target, VerifiedAliases: resolved.Aliases}
		if session.Capabilities.Resolve == nil {
			if session.Close != nil {
				session.Close()
			}
			return actionTargetView{}, platform.ErrReference
		}
		if access.Valid() {
			r, err = session.Capabilities.Resolve(ctx, locator, session.Access)
		}
		if err == nil && r.Target != nil && !sameNativeTarget(*r.Target, target) {
			err = ErrIdentity
		}
	} else {
		m.mu.Lock()
		resolve := m.actionResolvers[q.Provider]
		m.mu.Unlock()
		if resolve == nil {
			return actionTargetView{Resolution: platform.TargetResolution{Status: platform.ResolutionUnsupported, ReasonCode: "locator_not_supported", CheckedAt: time.Now().UTC()}, Actions: []actionTargetCapability{}, DisabledReason: "This provider has no approved standalone action locator."}, nil
		}
		r, session, err = resolve(ctx, id, locator)
	}
	if session.Close != nil {
		defer session.Close()
	}
	if err != nil {
		return actionTargetView{}, err
	}
	if session.Check == nil || session.Close == nil || session.Check(ctx) != nil || !session.Access.Valid() || r.Account != session.Access.Account {
		return actionTargetView{}, ErrIdentity
	}
	confirmed, err := svc.store.actionAccountOwner(id, session.Access, q.AccountOwnerSeq, q.TurnNumber)
	if err != nil {
		return actionTargetView{}, err
	}
	m.mu.Lock()
	bindings, bindingErr := m.readToolBindingsLocked(id.RoomIdentity)
	if bindingErr == nil {
		for _, b := range bindings.Bindings {
			if b.AgentID == id.AgentID && b.MembershipID == id.MembershipID && b.ConnectionID == session.Access.ConnectionID && b.ConnectionGeneration == session.Access.Generation && bindingConfirmsAccount(b, session.Access, session.Capabilities) {
				confirmed = true
			}
		}
	}
	m.mu.Unlock()
	if bindingErr != nil {
		return actionTargetView{}, bindingErr
	}
	r.AccountOwnerConfirmed = confirmed
	view := actionTargetView{Resolution: r, Actions: []actionTargetCapability{}}
	if r.Status != platform.ResolutionResolved || !r.Complete || !r.ObjectVerified || r.Target == nil {
		view.DisabledReason = "Target resolution is incomplete; execution is unavailable."
		return view, nil
	}
	if q.Provider != "" && r.Target.Provider != q.Provider {
		return actionTargetView{}, ErrIdentity
	}
	for _, cap := range session.Capabilities.Actions {
		if cap.AppliesTo != nil && !cap.AppliesTo(*r.Target, session.Access.Account) {
			continue
		}
		if cap.Inspect != nil && cap.Execute != nil && applicableActionSpec(cap.Spec, *r.Target) {
			availability := actionTargetCapability{ActionSpec: cap.Spec, Executable: cap.Spec.DisabledReason == "", DisabledReason: cap.Spec.DisabledReason}
			if availability.Executable && cap.Spec.RequiresAuthorization {
				m.mu.Lock()
				a := session.Access
				f, err := m.readToolBindingsLocked(id.RoomIdentity)
				if err == nil {
					for _, b := range f.Bindings {
						if b.AgentID == id.AgentID && b.MembershipID == id.MembershipID && b.ConnectionID == a.ConnectionID && b.ConnectionGeneration == a.Generation && b.ActionAuthorization != nil {
							a.ActionAuthorizationVersion = b.ActionAuthorization.Revision
						}
					}
					err = m.checkActionAuthorizationLocked(toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}, a, cap.Spec, *r.Target)
				}
				m.mu.Unlock()
				if err != nil {
					availability.Executable = false
					availability.DisabledReason = "Member action capability is disabled on the current binding."
				}
			}
			view.Actions = append(view.Actions, availability)
		}
	}
	sort.Slice(view.Actions, func(i, j int) bool { return view.Actions[i].Operation < view.Actions[j].Operation })
	if err := session.Check(ctx); err != nil {
		return actionTargetView{}, err
	}
	// Recheck admitted binding while committing the target. Revocation cancels
	// the managed session; it cannot leave a target executable by a different user.
	m.mu.Lock()
	defer m.mu.Unlock()
	current, _ := m.runtimeServiceLocked(req.RoomIdentity)
	if current != svc || m.checkToolMemberLocked(ctx, toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}) != nil {
		return actionTargetView{}, ErrIdentity
	}
	// session.Check can acquire manager.mu; invoke it before this commit gate.
	view.TargetRef, err = svc.store.saveActionTarget(id, r, session.Access)
	if len(view.Actions) == 0 {
		view.DisabledReason = "No executable actions are registered for this target."
	}
	return view, err
}
