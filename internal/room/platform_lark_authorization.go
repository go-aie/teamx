package room

import (
	"context"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Frozen public identity and revisions, never native credential material.
type larkAuthorizationTarget struct {
	ID                string                   `json:"id"`
	Version           uint64                   `json:"version"`
	Generation        string                   `json:"generation"`
	CredentialVersion uint64                   `json:"credential_version"`
	Profile           platform.CLIProfile      `json:"cli_profile"`
	Account           platform.AccountIdentity `json:"account"`
}

func validLarkAuthorizationTargets(op platformConnectOperation) bool {
	if op.Schema == 2 && op.Input.Provider != "lark" {
		return false
	}
	if op.Permissions != nil && (!op.Permissions.Account.Valid() || op.Permissions.Account != op.Account || op.Permissions.CheckedAt.IsZero() || len(op.Permissions.Capabilities) > len(lark.CapabilityDependencies())) {
		return false
	}
	if connectIntent(op.Input) != "authorize" {
		return len(op.AuthorizationTargets) == 0 && len(op.AuthorizationImpact) == 0
	}
	if op.Schema != 2 || op.Profile == nil || !op.Account.Valid() || len(op.AuthorizationTargets) == 0 || len(op.AuthorizationTargets) > 128 {
		return false
	}
	if len(op.AuthorizationImpact) != len(op.AuthorizationTargets) {
		return false
	}
	seen := map[string]bool{}
	for _, t := range op.AuthorizationTargets {
		if seen[t.ID] || !tool.OpaqueID(t.ID) || t.Version == 0 || t.CredentialVersion == 0 || t.Generation == "" || !lark.ValidProfile(t.Profile) || t.Profile.Profile != op.Profile.Profile {
			return false
		}
		if t.Account != (platform.AccountIdentity{}) && (!t.Account.Valid() || t.Account.Provider != "lark") {
			return false
		}
		seen[t.ID] = true
		if t.ID == op.PlatformID && (t.Account != op.Account || t.Profile != *op.Profile || t.Version != op.Input.PlatformVersion+1) {
			return false
		}
	}
	impact := map[string]bool{}
	for _, a := range op.AuthorizationImpact {
		if impact[a.ID] || !seen[a.ID] {
			return false
		}
		impact[a.ID] = true
		for _, target := range op.AuthorizationTargets {
			if target.ID == a.ID && target.Version != a.Version+1 {
				return false
			}
		}
	}
	return seen[op.PlatformID]
}

func (m *roomManager) startLarkAuthorizeLocked(ctx context.Context, op platformConnectOperation) (platformConnectOperation, error) {
	file, err := m.platforms.Load()
	if err != nil {
		return op, err
	}
	var selected *registry.Record
	for i := range file.Platforms {
		c := &file.Platforms[i]
		if c.ID == op.Input.PlatformID && c.Version == op.Input.PlatformVersion && c.Name == op.Input.Name && c.Provider == "lark" && c.CLIProfile != nil && c.CLIProfile.Brand == "feishu" {
			selected = c
		}
	}
	if selected == nil {
		return op, ErrConflict
	}
	a, _, err := registry.ResolveAccessAccount(file, *selected)
	if err != nil {
		return op, ErrConflict
	}
	if m.busy || m.larkAuthBusyLocked(selected.CLIProfile) {
		return op, ErrBusy
	}
	m.pruneLarkAuthSessionsLocked()
	if len(m.larkAuthSessions) >= 32 {
		return op, ErrCapacity
	}
	id, err := newOpaqueID()
	if err != nil {
		return op, err
	}
	runCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	s := &larkAuthSession{larkAuthView: larkAuthView{SessionID: id, Status: "starting", Stage: "authorization", ConfigurationFound: true}, owner: op.Source.Author.ID, profile: *selected.CLIProfile, operationID: op.ID, cancel: cancel, done: make(chan struct{}), expires: time.Now().Add(11 * time.Minute)}
	if m.larkAuthSessions == nil {
		m.larkAuthSessions = map[string]*larkAuthSession{}
	}
	m.larkAuthSessions[id] = s
	fail := func(err error) (platformConnectOperation, error) {
		cancel()
		close(s.done)
		delete(m.larkAuthSessions, id)
		return op, err
	}
	req := larkAuthRequest{Profile: selected.CLIProfile}
	for _, c := range file.Platforms {
		if c.Provider != "lark" || c.CLIProfile == nil || c.CLIProfile.Profile != selected.CLIProfile.Profile {
			continue
		}
		refs, err := m.managedPlatformReferencesLocked(c.ID)
		if err != nil {
			return fail(err)
		}
		rooms := []RoomIdentity{}
		for _, ref := range refs {
			rooms = append(rooms, ref.RoomIdentity)
		}
		req.Affected = append(req.Affected, larkAuthPlatform{ID: c.ID, Version: c.Version, ReferencedRooms: rooms})
	}
	// Persist the operation before credential side effects. Targets are the exact
	// post-invalidation revisions; startup never infers completion from old state.
	op.Schema, op.PlatformID, op.Account, op.Profile = 2, selected.ID, a.Identity, selected.CLIProfile
	op.Stage, op.Application.Status = "authorization", "reused"
	op.AuthorizationImpact = req.Affected
	for _, c := range file.Platforms {
		if c.Provider == "lark" && c.CLIProfile != nil && c.CLIProfile.Profile == s.profile.Profile {
			op.AuthorizationTargets = append(op.AuthorizationTargets, larkAuthorizationTarget{ID: c.ID, Version: c.Version + 1, Generation: c.Generation, CredentialVersion: c.CredentialVersion + 1, Profile: *c.CLIProfile, Account: c.Validation.Account})
		}
	}
	if err := m.savePlatformConnectLocked(&op); err != nil {
		return fail(err)
	}
	_, targets, err := m.invalidateLarkAuthLocked(req)
	if err == nil {
		err = m.checkOnboardingTargetLocked(ctx, platformOnboardingSpec{MemberIdentity: op.MemberIdentity})
	}
	if err != nil {
		op.Status, op.ErrorCode, op.ResultVersion = "failed", "target_changed", 1
		if saveErr := m.savePlatformConnectLocked(&op); saveErr != nil {
			return fail(saveErr)
		}
		cancel()
		close(s.done)
		delete(m.larkAuthSessions, id)
		return op, nil
	}
	s.targets = targets
	go m.runLarkAuth(runCtx, s, "login")
	return op, nil
}

// Complete only the original native account. Preflight all shared revisions
// before any verification commit; no member grant/evidence is ever rewritten.
func (m *roomManager) completeLarkAuthorizationLocked(s *larkAuthSession) error {
	if m.closing {
		return ErrClosing
	}
	if s.operationID != "" {
		op, err := m.readPlatformConnectLocked(s.operationID)
		if err != nil {
			return err
		}
		if op.CancelRequested || connectTerminal(op.Status) {
			return ErrConflict
		}
		checkCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = m.checkOnboardingTargetLocked(checkCtx, platformOnboardingSpec{MemberIdentity: op.MemberIdentity})
		cancel()
		if err != nil {
			return err
		}
	}
	file, err := m.platforms.Load()
	if err != nil {
		return err
	}
	for _, c := range file.Platforms {
		if c.Provider != "lark" || c.CLIProfile == nil || c.CLIProfile.Profile != s.profile.Profile {
			continue
		}
		found := false
		for _, t := range s.targets {
			if t.ID == c.ID {
				found = true
			}
		}
		if !found {
			return ErrConflict
		}
	}
	tickets := []registry.Verification{}
	for _, t := range s.targets {
		found := false
		for _, c := range file.Platforms {
			if c.ID != t.ID {
				continue
			}
			if c.Version != t.Version || c.Generation != t.Generation || c.CredentialVersion != t.CredentialVersion || c.CLIProfile == nil || *c.CLIProfile != t.Profile {
				return ErrConflict
			}
			found = true
		}
		if !found {
			return ErrConflict
		}
		if !t.Account.Valid() {
			continue
		} // an unverified connection needs explicit verification
		if t.Account != s.validation.Account {
			return ErrIdentity
		}
		ticket, err := m.platforms.BeginVerification(t.ID, t.Version)
		if err != nil {
			return managedPlatformError(err)
		}
		tickets = append(tickets, ticket)
	}
	for _, ticket := range tickets {
		if _, err := m.platforms.CompleteVerification(ticket, s.validation); err != nil {
			return err
		}
	}
	return m.applyLarkAccountCheckLocked(s.profile, s.validation.Account, s.Permissions)
}

func (m *roomManager) commitLarkAuthorizeLocked(op *platformConnectOperation, validation platform.Validation) error {
	file, err := m.platforms.Load()
	if err != nil {
		return err
	}
	if validation.Account != op.Account {
		return ErrIdentity
	}
	for _, c := range file.Platforms {
		if c.ID != op.PlatformID {
			continue
		}
		for _, t := range op.AuthorizationTargets {
			if t.ID == c.ID && c.Version == t.Version+1 && c.Generation == t.Generation && c.CredentialVersion == t.CredentialVersion && c.CLIProfile != nil && *c.CLIProfile == t.Profile {
				if a, _, e := registry.ResolveAccessAccount(file, c); e == nil && a.Identity == op.Account {
					op.Connection = onboardingStep{Status: "connected", Version: c.Version}
					return nil
				}
			}
		}
	}
	return ErrConflict
}
