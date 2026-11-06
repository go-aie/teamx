package room

import (
	"errors"
	"os"

	"github.com/go-aie/teamx/internal/platform"
)

// Validate Agent fields before building a persisted revision, so a missing ID
// or incomplete scope is never reported as an unrelated type/evidence error.
func agentPolicyScopeDiagnostic(kind string, s PolicyScope) error {
	fields := []struct{ name, value string }{
		{"description", s.Description}, {"provider", s.Provider}, {"instance", s.Instance},
		{"account", s.Account}, {"project", s.Project}, {"object_type", s.ObjectType},
	}
	for _, f := range fields {
		if len(f.value) > 4096 {
			return invalidField("body.scope."+f.name, "must be at most 4096 bytes")
		}
	}
	if s.Description != "" {
		return invalidField("body.scope.description", "must be omitted; keep requirements and exceptions together in text")
	}
	if kind == "preference" {
		if s != (PolicyScope{}) {
			return invalidField("body.scope", "preferences must not carry target scope")
		}
		return nil
	}
	if !validText(s.Provider) {
		return invalidField("body.scope.provider", "required for rules; alternatively use scope_from")
	}
	if !validText(s.Instance) {
		return invalidField("body.scope.instance", "required for rules")
	}
	if s.AccountID != (platform.ExternalID{}) && !s.AccountID.Valid() {
		return invalidField("body.scope.account_id", "requires nonempty namespace and value")
	}
	if s.Tenant != (platform.ExternalID{}) && !s.Tenant.Valid() {
		return invalidField("body.scope.tenant", "requires nonempty namespace and value")
	}
	if s.AccountID.Valid() && s.Account != "" {
		return invalidField("body.scope.account", "mutually exclusive with account_id")
	}
	if s.Tenant.Valid() && !s.AccountID.Valid() {
		return invalidField("body.scope.account_id", "required with tenant")
	}
	if !validText(s.Account) && !s.AccountID.Valid() {
		return invalidField("body.scope.account", "rules require a verified account label or account_id")
	}
	if s.NoContainers && s.Project != "" {
		return invalidField("body.scope.project", "mutually exclusive with no_containers")
	}
	if s.Project == "*" || (!s.NoContainers && !validText(s.Project)) {
		return invalidField("body.scope.project", "required exact project; wildcard is not supported; no_containers is only for objects without parents")
	}
	return nil
}

// Resolve only saved member-scoped evidence. This does not contact a provider
// or grant permission to act; execution still checks the current binding.
func (s *Store) agentPolicyScopeFromLocked(id MemberIdentity, q agentPolicyScopeFrom) (PolicyScope, error) {
	u, err := s.loadUnitLocked(id, q.ObjectRef, q.UnitRef)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return PolicyScope{}, invalidField("scope_from", "followup not found in the current membership; use verified object_ref/unit_ref from matters read")
		}
		return PolicyScope{}, prefixDiagnostic(err, "scope_from.")
	}
	t, err := s.loadTargetLocked(id, u.ObjectRef, u.TargetRef)
	if err != nil {
		return PolicyScope{}, err
	}
	var account platform.AccountIdentity
	err = s.visitFollowupEventsLocked(id, func(e followupEvent) error {
		if e.ObjectRef != u.ObjectRef || !e.Resolution.ObjectVerified ||
			(t.Target.Discussion.Valid() && (!e.Resolution.DiscussionVerified || e.Resolution.Target.Discussion != t.Target.Discussion)) {
			return nil
		}
		if !e.Access.Valid() || e.Access.Account.IdentityScope != t.Target.IdentityScope {
			return invalidField("scope_from", "saved account does not match the verified target scope")
		}
		if account.Valid() && account != e.Access.Account {
			return invalidField("scope_from", "multiple verified accounts; provide an explicit body.scope")
		}
		account = e.Access.Account
		return nil
	})
	if err != nil {
		return PolicyScope{}, err
	}
	if !account.Valid() {
		return PolicyScope{}, invalidField("scope_from", "no saved verified account evidence; provide an explicit body.scope")
	}
	scope := PolicyScope{Provider: t.Target.Provider, Instance: t.Target.Instance,
		AccountID: account.ID, Tenant: account.Tenant, ObjectType: t.Target.Object.Type}
	switch q.Level {
	case "project":
		// The current policy model has one scalar project scope. Do not flatten
		// another platform's hierarchy or guess which container the user meant.
		if len(t.Target.Containers) != 1 || t.Target.Containers[0].Type != "Project" {
			return PolicyScope{}, invalidField("scope_from.level", "project requires exactly one verified Project container; provide an explicit body.scope for other hierarchies")
		}
		scope.Project = t.Target.Containers[0].ID.Value
	case "no_containers":
		if len(t.Target.Containers) != 0 {
			return PolicyScope{}, invalidField("scope_from.level", "no_containers requires a verified target without parent containers")
		}
		scope.NoContainers = true
	default:
		return PolicyScope{}, invalidField("scope_from.level", "must explicitly select project or no_containers")
	}
	return scope, agentPolicyScopeDiagnostic("rule", scope)
}
