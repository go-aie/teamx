package room

import (
	"cmp"
	"slices"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// A closed allowlist prevents future registry entries from expanding defaults.
// This prepares the contract only; reading it never changes saved bindings.
func defaultLarkActionGrants(caps platform.ReferenceCapabilities) []tool.ActionGrant {
	grants := []tool.ActionGrant{}
	for _, g := range []tool.ActionGrant{
		{Operation: "message.reply", TargetKind: "message", ObjectType: "message"},
		{Operation: "message.send", TargetKind: "object", ObjectType: "chat"},
		{Operation: "message.send_self", TargetKind: "object", ObjectType: "user"},
	} {
		cap, ok := caps.Actions[platform.Operation(g.Operation)]
		if ok && cap.Spec.DisabledReason == "" && cap.Spec.Operation == platform.Operation(g.Operation) && cap.Spec.TargetKind == g.TargetKind && slices.Contains(cap.Spec.ObjectTypes, g.ObjectType) {
			grants = append(grants, g)
		}
	}
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

const larkCapabilityMaxAge = 60 * time.Second

// Service-local snapshot key. No receipt or persisted grant is live readiness.
// The caller captures these versions before remote reads and rechecks them
// after preparation; this type does not authorize a stale result to be saved.
type larkPlatformCapabilityResult struct {
	PlatformID        string `json:"platform_id"`
	PlatformVersion   uint64 `json:"platform_version"`
	Generation        string `json:"generation"`
	CredentialVersion uint64 `json:"credential_version"`
	lark.PermissionCheck
}

type larkMemberCapabilityResult struct {
	MemberIdentity
	larkPlatformCapabilityResult
	ToolEnabled    bool   `json:"tool_enabled"`
	ToolRevision   uint64 `json:"tool_revision"`
	ActionRevision uint64 `json:"action_revision"`
}

func (p larkPlatformCapabilityResult) current(a platform.AccessContext, now time.Time) bool {
	return a.Valid() && p.PlatformID == a.PlatformID && p.PlatformVersion == a.PlatformVersion && p.Generation == a.Generation && p.CredentialVersion == a.CredentialVersion && p.Account == a.Account && !p.CheckedAt.IsZero() && !now.Before(p.CheckedAt) && now.Sub(p.CheckedAt) < larkCapabilityMaxAge
}

// projectLarkMemberCapabilities is a pure projection of intent, recent checks
// and current server-owned evidence. It neither probes nor repairs anything.
func projectLarkMemberCapabilities(a platform.AccessContext, operator string, binding *tool.Binding, toolRevision uint64, p larkPlatformCapabilityResult, caps platform.ReferenceCapabilities, now time.Time) larkMemberCapabilityResult {
	r := larkMemberCapabilityResult{MemberIdentity: MemberIdentity{RoomIdentity: a.RoomIdentity, AgentID: a.AgentID, MembershipID: a.MembershipID}, larkPlatformCapabilityResult: p, ToolRevision: toolRevision}
	r.Capabilities = []lark.Capability{}
	for _, d := range lark.CapabilityDependencies() {
		v := lark.Capability{Name: d.Name, State: "unchecked", ReasonCode: "check_missing", NextAction: "retry_check", EvidenceState: "not_required"}
		for _, checked := range p.Capabilities {
			if checked.Name == d.Name {
				v = checked
				break
			}
		}
		r.Capabilities = append(r.Capabilities, v)
	}
	current := p.current(a, now)
	r.ToolEnabled = binding != nil && binding.Tool == "lark" && binding.AgentID == a.AgentID && binding.MembershipID == a.MembershipID && binding.PlatformID == a.PlatformID && binding.PlatformGeneration == a.Generation
	if r.ToolEnabled && binding.ActionAuthorization != nil {
		r.ActionRevision = binding.ActionAuthorization.Revision
	}
	for i := range r.Capabilities {
		v := &r.Capabilities[i]
		if !current {
			v.State, v.ReasonCode, v.NextAction = "unchecked", "check_expired", "retry_check"
		}
		// Platform reads, renewal and notification dependencies are independent
		// of member action grants. ToolEnabled is reported separately; notification
		// subscription choices cannot be inferred from a tool binding.
		if v.Name != "message.reply" && v.Name != "message.send" && v.Name != "message.send_self" {
			continue
		}
		var g *tool.ActionGrant
		for _, candidate := range defaultLarkActionGrants(caps) {
			if candidate.Operation == v.Name {
				c := candidate
				g = &c
				break
			}
		}
		configured := false
		if g != nil && r.ToolEnabled && binding.ActionAuthorization != nil && binding.ActionAuthorization.Valid() {
			configured = slices.Contains(binding.ActionAuthorization.Grants, *g)
		}
		v.Configured = &configured
		if !configured && v.State != "unsupported" {
			v.State, v.ReasonCode, v.NextAction = "blocked", "member_tool_disabled", "enable_member_tool"
			if r.ToolEnabled {
				v.ReasonCode = "member_action_not_configured"
			}
		}
		// An absent/disabled adapter must never be mistaken for a read branch.
		if g == nil {
			v.State, v.ReasonCode, v.NextAction = "unsupported", "action_unavailable", ""
			v.Configured = new(bool)
		}
		cap := caps.Actions[platform.Operation(v.Name)]
		if cap.Spec.BindingEvidence == "" {
			continue
		}
		v.EvidenceState = "missing"
		if r.ToolEnabled {
			if e := binding.Evidence(cap.Spec.BindingEvidence); e != nil {
				v.EvidenceState = "stale"
				if e.Operator != "" && (operator == "" || e.Operator == operator) && e.AccountOwnerConfirmed && e.Current(cap.Spec.BindingEvidence, a, e.Target) && applicableActionSpec(cap.Spec, e.Target) && (cap.AppliesTo == nil || cap.AppliesTo(e.Target, a.Account)) {
					v.EvidenceState = "current"
				}
			}
		}
		if v.State == "ready" && v.EvidenceState != "current" {
			v.State, v.ReasonCode, v.NextAction = "blocked", "self_evidence_"+v.EvidenceState, "verify_self"
		}
	}
	return r
}
