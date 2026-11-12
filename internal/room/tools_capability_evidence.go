package room

import (
	"context"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/tool"
)

type bindingEvidencePreparer func(context.Context, *toolBindingChange) error

func bindingNeedsEvidence(b tool.Binding, caps platform.ReferenceCapabilities) bool {
	if b.ActionAuthorization != nil {
		for _, g := range b.ActionAuthorization.Grants {
			if caps.Actions[platform.Operation(g.Operation)].Spec.BindingEvidence != "" {
				return true
			}
		}
	}
	return false
}

func (m *roomManager) prepareBindingEvidence(ctx context.Context, req *toolBindingChange) error {
	if len(req.Binding.CapabilityEvidence) != 0 {
		return ErrRequest // evidence is never accepted from the operator or Agent
	}
	if !req.Enabled {
		return nil
	}
	m.mu.Lock()
	d, err := m.toolDefinition(req.Binding.Tool)
	var caps platform.ReferenceCapabilities
	if err == nil {
		caps, _ = m.referenceCapabilities(d.Provider)
	}
	prepare := m.bindingEvidencePreparers[d.Provider]
	configure := m.toolBindingPreparers[d.Provider]
	m.mu.Unlock()
	if err == nil && configure != nil {
		return configure(ctx, req)
	}
	if err != nil || !bindingNeedsEvidence(req.Binding, caps) {
		return err
	}
	if prepare == nil {
		return ErrIdentity
	}
	return prepare(ctx, req)
}

func currentCapabilityEvidence(e *platform.CapabilityEvidence, spec platform.ActionSpec, access platform.AccessContext, target platform.TargetIdentity) bool {
	return e.Current(spec.BindingEvidence, access, target)
}

func bindingConfirmsAccount(b tool.Binding, access platform.AccessContext, caps platform.ReferenceCapabilities) bool {
	if b.ActionAuthorization == nil {
		return false
	}
	for _, grant := range b.ActionAuthorization.Grants {
		cap := caps.Actions[platform.Operation(grant.Operation)]
		e := b.Evidence(cap.Spec.BindingEvidence)
		if e != nil && e.AccountOwnerConfirmed && e.Current(cap.Spec.BindingEvidence, access, e.Target) && (cap.AppliesTo == nil || cap.AppliesTo(e.Target, access.Account)) {
			return true
		}
	}
	return false
}

// Common binding checks compare exact identities and revisions; native target
// restrictions are declared by the registered adapter, independently of names.
func validateBindingEvidence(b tool.Binding, caps platform.ReferenceCapabilities, operator string) error {
	used := map[string]bool{}
	if b.ActionAuthorization != nil {
		for _, g := range b.ActionAuthorization.Grants {
			cap := caps.Actions[platform.Operation(g.Operation)]
			if cap.Spec.BindingEvidence == "" {
				continue
			}
			e := b.Evidence(cap.Spec.BindingEvidence)
			if e == nil && cap.AllowPendingEvidence {
				continue // saved intent is not executable proof; execution still checks Current
			}
			if e == nil || cap.ValidateBindingEvidence != nil && !cap.ValidateBindingEvidence(*e) || e.Operator != operator || !e.Current(cap.Spec.BindingEvidence, platform.AccessContext{Account: e.Account, CredentialVersion: e.CredentialVersion}, e.Target) || !applicableActionSpec(cap.Spec, e.Target) || (cap.AppliesTo != nil && !cap.AppliesTo(e.Target, e.Account)) {
				return ErrIdentity
			}
			used[e.Kind] = true
		}
	}
	if len(b.CapabilityEvidence) > 32 {
		return ErrCapacity
	}
	for _, e := range b.CapabilityEvidence {
		if !used[e.Kind] {
			return ErrRequest
		}
		delete(used, e.Kind) // duplicate evidence kinds are not allowed
	}
	return nil
}
