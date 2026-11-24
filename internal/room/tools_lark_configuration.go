package room

import (
	"cmp"
	"context"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Only server memory can carry this admission across remote preparation.
// Receipts freeze observations; they never authorize a later commit.
type preparedLarkToolChange struct {
	BindingRevision  uint64
	Account          registry.Account
	Binding          tool.Binding
	RequestedActions []tool.ActionGrant
	Capabilities     larkMemberCapabilityResult
	Preserved        bool
}

type userAuthorizationRequired struct{ Capabilities larkMemberCapabilityResult }

func (*userAuthorizationRequired) Error() string { return "user_authorization_required" }

func normalizeActionGrants(grants []tool.ActionGrant) []tool.ActionGrant {
	grants = slices.Clone(grants)
	slices.SortFunc(grants, func(a, b tool.ActionGrant) int {
		if n := cmp.Compare(a.Operation, b.Operation); n != 0 {
			return n
		}
		if n := cmp.Compare(a.TargetKind, b.TargetKind); n != 0 {
			return n
		}
		return cmp.Compare(a.ObjectType, b.ObjectType)
	})
	return slices.Compact(grants)
}

// larkToolGrantPlan resolves the existing enablement intent without I/O.
// Caller admission, account pinning and commit rechecks remain with the owner.
func larkToolGrantPlan(caps platform.ReferenceCapabilities, binding tool.Binding, actions []string, old *tool.Binding) ([]tool.ActionGrant, []tool.ActionGrant, error) {
	defaults := defaultLarkActionGrants(caps)
	requested := defaults
	if len(actions) > 0 {
		requested = nil
		for _, op := range actions {
			found := false
			for _, g := range defaults {
				if g.Operation == op {
					requested = append(requested, g)
					found = true
					break
				}
			}
			if !found {
				return nil, nil, ErrRequest
			}
		}
	}
	if binding.ActionAuthorization != nil {
		requested = binding.ActionAuthorization.Grants
	}
	requested = normalizeActionGrants(requested)
	grants := slices.Clone(requested)
	if binding.ActionAuthorization == nil && old != nil && old.ActionAuthorization != nil {
		grants = append(grants, old.ActionAuthorization.Grants...)
	}
	grants = normalizeActionGrants(grants)
	for _, g := range grants {
		cap, ok := caps.Actions[platform.Operation(g.Operation)]
		if !ok || cap.Spec.DisabledReason != "" || g.TargetKind != cap.Spec.TargetKind || !slices.Contains(cap.Spec.ObjectTypes, g.ObjectType) {
			return nil, nil, ErrRequest
		}
	}
	if !(&tool.ActionAuthorization{Version: 1, Revision: 1, Grants: grants}).Valid() {
		return nil, nil, ErrRequest
	}
	return requested, grants, nil
}

func strictLarkGrantReduction(explicit bool, old *tool.Binding, grants []tool.ActionGrant) bool {
	return explicit && old != nil && old.ActionAuthorization != nil && len(grants) < len(old.ActionAuthorization.Grants) && !slices.ContainsFunc(grants, func(g tool.ActionGrant) bool { return !slices.Contains(old.ActionAuthorization.Grants, g) })
}

func (m *roomManager) admitLarkToolChangeLocked(ctx context.Context, req toolBindingChange) (toolBindingsFile, registry.Account, error) {
	var a registry.Account
	if err := ctx.Err(); err != nil {
		return toolBindingsFile{}, a, err
	}
	if err := m.checkPlatformMemberContextLocked(ctx); err != nil {
		return toolBindingsFile{}, a, err
	}
	if err := m.checkOnboardingContextLocked(ctx); err != nil {
		return toolBindingsFile{}, a, err
	}
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		return toolBindingsFile{}, a, ErrIdentity
	}
	if m.closing || m.busy || m.roomBusyLocked(req.RoomIdentity) {
		return toolBindingsFile{}, a, ErrBusy
	}
	if req.Binding.PlatformGeneration != "" || !req.Binding.ActionAuthorization.Valid() {
		return toolBindingsFile{}, a, ErrRequest
	}
	if err := m.store.check(true); err != nil {
		return toolBindingsFile{}, a, err
	}
	entry, err := m.workspaceRoom(req.RoomIdentity)
	if err != nil || entry.Status != "ready" {
		return toolBindingsFile{}, a, ErrIdentity
	}
	members, err := m.toolRosterLocked(ctx, req.RoomIdentity)
	if err != nil {
		return toolBindingsFile{}, a, err
	}
	if !memberExists(members, req.Binding.AgentID, req.Binding.MembershipID) {
		return toolBindingsFile{}, a, &requestDiagnostic{cause: ErrIdentity, Field: "membership_id", Message: "the exact member is no longer current", Hint: "Check the original Room/member identity; do not transfer this preparation."}
	}
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err != nil {
		return f, a, err
	}
	if err := checkBindingRevision(f, req.Binding, req.ExpectedBindingRevision); err != nil {
		return f, a, err
	}
	a, err = m.checkLarkChatsLocked(ctx, larkChatsRequest{ServiceEpoch: req.ServiceEpoch, RoomIdentity: req.RoomIdentity, Caller: req.Caller, PlatformID: req.Binding.PlatformID, PlatformVersion: req.PlatformVersion}, nil)
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrIdentity) {
		err = &requestDiagnostic{cause: err, Field: "account", Message: "the selected account or credential is unavailable or changed", Hint: "Check this exact connection; configuration conflict is not evidence of prior authorization."}
	}
	return f, a, err
}

func (m *roomManager) recheckLarkToolChangeLocked(ctx context.Context, req toolBindingChange) error {
	f, a, err := m.admitLarkToolChangeLocked(ctx, req)
	if err != nil {
		return err
	}
	p := req.prepared
	if err := checkBindingRevision(f, req.Binding, &p.BindingRevision); err != nil {
		return err
	}
	if !reflect.DeepEqual(a, p.Account) {
		return conflictReason("account", "account or credential changed during preparation", "Check the exact connection; this does not establish prior authorization.")
	}
	if !reflect.DeepEqual(req.Binding, p.Binding) {
		return conflictReason("binding", "prepared action plan changed", "Reconcile the original operation.")
	}
	access := larkReferenceAccess(MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Binding.AgentID, MembershipID: req.Binding.MembershipID}, a, platform.SourceRef{})
	if !p.Capabilities.larkPlatformCapabilityResult.current(access, time.Now()) {
		return conflictReason("capabilities", "capability evidence expired or changed", "Check the exact account and evidence before configuring again.")
	}
	return nil
}

// All three enablement entrypoints use freeze, managed remote reads, recheck.
// Neither a permission gap nor absent self proof grants executable access.
func (m *roomManager) prepareLarkToolChange(ctx context.Context, req *toolBindingChange) error {
	m.mu.Lock()
	f, a, err := m.admitLarkToolChangeLocked(ctx, *req)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	caps, err := m.referenceCapabilities("lark")
	if err != nil {
		m.mu.Unlock()
		return err
	}
	var old *tool.Binding
	for _, b := range f.Bindings {
		if b.AgentID == req.Binding.AgentID && b.MembershipID == req.Binding.MembershipID && b.Tool == "lark" && b.PlatformID == req.Binding.PlatformID && b.PlatformGeneration == a.Generation {
			copy := b
			old = &copy
			break
		}
	}
	requested, grants, err := larkToolGrantPlan(caps, req.Binding, req.requestedActions, old)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	strictReduction := strictLarkGrantReduction(req.Binding.ActionAuthorization != nil, old, grants)
	req.Binding.ActionAuthorization = &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: grants}
	p := &preparedLarkToolChange{BindingRevision: f.TargetRevisions[toolBindingKey(req.Binding)], Account: a, RequestedActions: requested}
	// A strict reduction cannot acquire new capability and must promptly cancel
	// active calls. It needs no CLI queue/remote read that could wait on those calls.
	if strictReduction {
		access := larkReferenceAccess(MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Binding.AgentID, MembershipID: req.Binding.MembershipID}, a, platform.SourceRef{})
		for _, g := range grants {
			cap := caps.Actions[platform.Operation(g.Operation)]
			if e := old.Evidence(cap.Spec.BindingEvidence); e != nil && e.Operator == req.Caller.ID && e.Current(cap.Spec.BindingEvidence, access, e.Target) && (cap.ValidateBindingEvidence == nil || cap.ValidateBindingEvidence(*e)) && applicableActionSpec(cap.Spec, e.Target) && (cap.AppliesTo == nil || cap.AppliesTo(e.Target, a.Identity)) {
				req.Binding.CapabilityEvidence = append(req.Binding.CapabilityEvidence, *e)
			}
		}
		projected := req.Binding
		projected.PlatformGeneration = a.Generation
		snap := larkPlatformCapabilityResult{PlatformID: a.PlatformID, PlatformVersion: a.PlatformVersion, Generation: a.Generation, CredentialVersion: a.CredentialVersion, PermissionCheck: lark.PermissionCheck{Account: a.Identity, CheckedAt: time.Now().UTC(), Application: lark.ScopeCheck{State: "unchecked"}, User: lark.ScopeCheck{State: "unchecked"}, Renewal: lark.ScopeCheck{State: "unchecked"}}}
		for _, dependency := range lark.CapabilityDependencies() {
			c := lark.Capability{Name: dependency.Name, State: "unchecked", ReasonCode: "check_not_run_for_revocation", NextAction: "retry_check", EvidenceState: "not_required"}
			if dependency.RequiresPin && !m.larkClients.SupportsPin() {
				c.State, c.ReasonCode, c.NextAction = "unsupported", "account_pinning_unavailable", ""
			}
			snap.Capabilities = append(snap.Capabilities, c)
		}
		p.Capabilities = projectLarkMemberCapabilities(access, req.Caller.ID, &projected, f.Revision, snap, caps, time.Now())
		p.Binding = req.Binding
		req.prepared = p
		m.mu.Unlock()
		return nil
	}
	prepare := m.bindingEvidencePreparers["lark"]
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	call := &platformCall{owner: req.RoomIdentity, platformID: a.PlatformID, agentID: req.Binding.AgentID, membershipID: req.Binding.MembershipID, tool: "lark"}
	call.Call = m.platformCalls.Register(call, cancel)
	m.mu.Unlock()
	defer m.finishPlatformCall(call)
	check, err := m.larkClients.CheckPermissions(callCtx, *a.CLIProfile, a.Identity)
	if err != nil {
		return err
	}
	// Only app-enabled permissions can be repaired by user OAuth. An app gap
	// remains a partial capability, avoiding an authorization loop.
	operations := make([]string, 0, len(grants))
	for _, grant := range grants {
		operations = append(operations, grant.Operation)
	}
	authRequired := lark.ToolAuthorizationRequired(check, operations)
	access := larkReferenceAccess(MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Binding.AgentID, MembershipID: req.Binding.MembershipID}, a, platform.SourceRef{})
	snap := larkPlatformCapabilityResult{PlatformID: a.PlatformID, PlatformVersion: a.PlatformVersion, Generation: a.Generation, CredentialVersion: a.CredentialVersion, PermissionCheck: check}
	if !authRequired && bindingNeedsEvidence(req.Binding, caps) && prepare != nil {
		// Reuse current proof without rewriting it; missing/stale proof gets a bounded
		// native check. Remote failures remain local to the evidence capability.
		if old != nil {
			for _, e := range old.CapabilityEvidence {
				spec := caps.Actions[platform.OperationMessageSendSelf].Spec
				if e.Operator == req.Caller.ID && e.AccountOwnerConfirmed && e.Current(spec.BindingEvidence, access, e.Target) && applicableActionSpec(spec, e.Target) && (caps.Actions[spec.Operation].AppliesTo == nil || caps.Actions[spec.Operation].AppliesTo(e.Target, a.Identity)) {
					req.Binding.CapabilityEvidence = append(req.Binding.CapabilityEvidence, e)
				}
			}
		}
		if len(req.Binding.CapabilityEvidence) == 0 {
			err = prepare(callCtx, req)
			if err != nil {
				req.Binding.CapabilityEvidence = nil
				if errors.Is(err, ErrIdentity) || lark.ErrorCode(err) == "identity_mismatch" {
					return err
				}
			}
		}
	}
	if err = callCtx.Err(); err != nil {
		return err
	}
	projected := req.Binding
	projected.PlatformGeneration = a.Generation
	p.Capabilities = projectLarkMemberCapabilities(access, req.Caller.ID, &projected, f.Revision, snap, caps, time.Now())
	if authRequired {
		m.mu.Lock()
		latest, bound, recheckErr := m.admitLarkToolChangeLocked(callCtx, *req)
		if recheckErr == nil && (latest.TargetRevisions[toolBindingKey(req.Binding)] != p.BindingRevision || !reflect.DeepEqual(bound, a)) {
			recheckErr = ErrConflict
		}
		if recheckErr == nil {
			recheckErr = m.applyLarkAccountCheckLocked(*a.CLIProfile, check.Account, &check)
		}
		m.mu.Unlock()
		if recheckErr != nil {
			return recheckErr
		}
		current := projectLarkMemberCapabilities(access, req.Caller.ID, old, latest.Revision, snap, caps, time.Now())
		return &userAuthorizationRequired{Capabilities: current}
	}
	if old != nil {
		previous := *old
		previous.PlatformGeneration = ""
		candidate := req.Binding
		if previous.ActionAuthorization != nil && slices.Equal(previous.ActionAuthorization.Grants, grants) {
			candidate.ActionAuthorization = previous.ActionAuthorization
		}
		p.Preserved = reflect.DeepEqual(previous, candidate)
		if p.Preserved {
			req.Binding = candidate
		}
	}
	if p.Preserved {
		p.Capabilities.ToolRevision = f.Revision
		p.Capabilities.ActionRevision = req.Binding.ActionAuthorization.Revision
	}
	p.Binding = req.Binding
	req.prepared = p
	m.mu.Lock()
	err = m.recheckLarkToolChangeLocked(callCtx, *req)
	if err == nil {
		err = m.applyLarkAccountCheckLocked(*a.CLIProfile, check.Account, &check)
	}
	m.mu.Unlock()
	return err
}

// Called with the manager lock. The operation freezes its grant plan before
// OAuth; an unknown submission is history and is never prepared again.
func (m *roomManager) applyOnboardingLarkToolLocked(ctx context.Context, op platformOnboarding) onboardingStep {
	fail := func(err error) onboardingStep {
		return onboardingStep{Status: "failed", ErrorCode: onboardingFailure(err)}
	}
	if err := m.checkOnboardingContextLocked(ctx); err != nil {
		return fail(err)
	}
	file, err := m.platforms.Load()
	if err != nil {
		return fail(err)
	}
	var version uint64
	for _, c := range file.Platforms {
		if c.ID == op.PlatformID {
			version = c.Version
		}
	}
	change := toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: op.Input.RoomIdentity, Caller: Caller{Kind: "human", ID: op.Owner}, ExpectedBindingRevision: op.BeforeBindingRevision, PlatformVersion: version, Enabled: true, Binding: tool.Binding{AgentID: op.Input.AgentID, MembershipID: op.Input.MembershipID, PlatformID: op.PlatformID, Tool: "lark", ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: slices.Clone(op.LarkActions)}}}
	m.mu.Unlock()
	err = m.prepareBindingEvidence(ctx, &change)
	m.mu.Lock()
	if err != nil {
		var required *userAuthorizationRequired
		if errors.As(err, &required) {
			return onboardingStep{Status: "not_applied", ErrorCode: "user_authorization_required", Capabilities: &required.Capabilities, Authorization: change.Binding.ActionAuthorization, RequestedActions: op.LarkActions}
		}
		return fail(err)
	}
	p := change.prepared
	if err = m.recheckLarkToolChangeLocked(ctx, change); err != nil {
		return fail(err)
	}
	step := onboardingStep{Status: "unknown", ErrorCode: "submission_unconfirmed", RequestedActions: p.RequestedActions, Authorization: change.Binding.ActionAuthorization, Capabilities: &p.Capabilities}
	op.Tool = step
	if err = m.saveOnboardingLocked(op); err != nil {
		return fail(err)
	}
	next, waits, err := m.changeToolBindingLocked(ctx, change)
	if err == nil {
		step.Authorization = p.Binding.ActionAuthorization
		err = m.waitOnboardingToolLocked(ctx, waits)
	}
	if err != nil {
		return step
	}
	step.Authorization = p.Binding.ActionAuthorization
	step.Capabilities = &p.Capabilities
	step.Status, step.Version, step.ErrorCode = "applied", next.Revision, ""
	if p.Preserved {
		step.Status = "preserved"
	}
	return step
}

// Historical observations are validated structurally, never refreshed or
// reinterpreted as a new enablement request when their journal is read.
func validLarkConfigurationReceipt(id MemberIdentity, platformID string, account platform.AccountIdentity, requested []tool.ActionGrant, auth *tool.ActionAuthorization, c *larkMemberCapabilityResult) bool {
	if auth == nil || !auth.Valid() || c == nil || c.MemberIdentity != id || c.PlatformID != platformID || c.Account != account || c.PlatformVersion == 0 || c.CredentialVersion == 0 || !tool.OpaqueID(c.Generation) || c.CheckedAt.IsZero() {
		return false
	}
	request := &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: requested}
	if !request.Valid() || !slices.Equal(normalizeActionGrants(requested), requested) || !slices.Equal(normalizeActionGrants(auth.Grants), auth.Grants) {
		return false
	}
	for _, g := range requested {
		if !slices.Contains(auth.Grants, g) {
			return false
		}
	}
	return true
}
