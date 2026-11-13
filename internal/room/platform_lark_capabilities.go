package room

import (
	"context"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Apply a successful, revision-checked native account observation. Permission
// gaps remain visible in the cached result; restoring a credential-paused
// subscription only resumes its existing collector, not its member actions.
// Callers must verify the account outside the manager lock before entering here.
func (m *roomManager) applyLarkAccountCheckLocked(profile platform.CLIProfile, account platform.AccountIdentity, check *lark.PermissionCheck) error {
	if !account.Valid() || account.Provider != "lark" || check != nil && check.Account != account {
		return ErrIdentity
	}
	file, err := m.platforms.Load()
	if err != nil {
		return err
	}
	if check != nil && m.larkPermissionChecks == nil {
		m.larkPermissionChecks = map[string]larkPlatformCapabilityResult{}
	}
	for _, c := range file.Platforms {
		if c.CLIProfile == nil || c.CLIProfile.Profile != profile.Profile || c.Provider != "lark" {
			continue
		}
		a, _, err := registry.ResolveAccessAccount(file, c)
		if err != nil || a.Identity != account {
			continue
		}
		if check != nil {
			m.larkPermissionChecks[c.ID] = larkPlatformCapabilityResult{PlatformID: c.ID, PlatformVersion: c.Version, Generation: c.Generation, CredentialVersion: c.CredentialVersion, PermissionCheck: *check}
		}
		if err := m.resumeLarkSubscriptionsLocked(c.ID); err != nil {
			return err
		}
	}
	return nil
}

// A list/get never probes a provider or repairs intent. Expired observations
// retain configured intent while readiness becomes explicitly unchecked.
func (m *roomManager) larkPlatformCapabilitiesLocked(c registry.Record) larkPlatformCapabilityResult {
	p, ok := m.larkPermissionChecks[c.ID]
	if ok && p.PlatformVersion == c.Version && p.Generation == c.Generation && p.CredentialVersion == c.CredentialVersion && p.Account == c.Validation.Account && !time.Now().Before(p.CheckedAt) && time.Since(p.CheckedAt) < larkCapabilityMaxAge {
		return p
	}
	p = larkPlatformCapabilityResult{PlatformID: c.ID, PlatformVersion: c.Version, Generation: c.Generation, CredentialVersion: c.CredentialVersion, PermissionCheck: lark.PermissionCheck{Account: c.Validation.Account, Application: lark.ScopeCheck{State: "unchecked"}, User: lark.ScopeCheck{State: "unchecked"}, Renewal: lark.ScopeCheck{State: "unchecked"}}}
	for _, d := range lark.CapabilityDependencies() {
		p.Capabilities = append(p.Capabilities, lark.Capability{Name: d.Name, State: "unchecked", ReasonCode: "check_expired", EvidenceState: "not_required", NextAction: "retry_check"})
	}
	return p
}

func (m *roomManager) larkMemberCapabilitiesLocked(owner MemberIdentity, operator string, a registry.Account, f toolBindingsFile) larkMemberCapabilityResult {
	var b *tool.Binding
	for i := range f.Bindings {
		v := &f.Bindings[i]
		if v.AgentID == owner.AgentID && v.MembershipID == owner.MembershipID && v.PlatformID == a.PlatformID {
			b = v
			break
		}
	}
	p := larkPlatformCapabilityResult{}
	if file, err := m.platforms.Load(); err == nil {
		for _, c := range file.Platforms {
			if c.ID == a.PlatformID {
				p = m.larkPlatformCapabilitiesLocked(c)
			}
		}
	}
	caps, _ := m.referenceCapabilities("lark")
	return projectLarkMemberCapabilities(larkReferenceAccess(owner, a, platform.SourceRef{}), operator, b, f.Revision, p, caps, time.Now())
}

func (m *roomManager) checkReusedLarkConnection(ctx context.Context, op platformConnectOperation, c registry.Record, call *platformCall) {
	defer m.finishPlatformCall(call)
	check, checkErr := m.larkClients.CheckPermissions(ctx, *c.CLIProfile, op.Account)
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.readPlatformConnectLocked(op.ID)
	if err != nil || connectTerminal(current.Status) || current.CancelRequested {
		return
	}
	op = current
	file, err := m.platforms.Load()
	valid := false
	if err == nil {
		for _, now := range file.Platforms {
			if now.ID == c.ID && now.Version == c.Version && now.Generation == c.Generation && now.CredentialVersion == c.CredentialVersion {
				valid = true
			}
		}
	}
	if !valid || m.closing || m.checkOnboardingTargetLocked(ctx, platformOnboardingSpec{MemberIdentity: op.MemberIdentity}) != nil {
		op.Status, op.ErrorCode = "failed", "target_changed"
	} else if checkErr != nil && lark.ErrorCode(checkErr) == "identity_mismatch" {
		op.Status, op.ErrorCode = "failed", "identity_mismatch"
	} else {
		op.Status, op.Stage = "completed", "complete"
		if checkErr == nil {
			op.Permissions = &check
			if err := m.applyLarkAccountCheckLocked(*c.CLIProfile, check.Account, &check); err != nil {
				op.Status, op.ErrorCode = "failed", onboardingFailure(err)
			}
		}
	}
	op.ResultVersion++
	_ = m.savePlatformConnectLocked(&op)
	m.wakeConnectNotificationsLocked()
}

func sameLarkProfileRevisions(a, b registry.Snapshot, p platform.CLIProfile) bool {
	selected := func(f registry.Snapshot) map[string]larkAuthorizationTarget {
		result := map[string]larkAuthorizationTarget{}
		for _, c := range f.Platforms {
			if c.Provider == "lark" && c.CLIProfile != nil && c.CLIProfile.Profile == p.Profile {
				result[c.ID] = larkAuthorizationTarget{ID: c.ID, Version: c.Version, Generation: c.Generation, CredentialVersion: c.CredentialVersion, Profile: *c.CLIProfile, Account: c.Validation.Account}
			}
		}
		return result
	}
	x, y := selected(a), selected(b)
	if len(x) != len(y) {
		return false
	}
	for id, t := range x {
		if y[id] != t {
			return false
		}
	}
	return true
}
