package room

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func memberCapability(t *testing.T, result larkMemberCapabilityResult, name string) lark.Capability {
	t.Helper()
	for _, c := range result.Capabilities {
		if c.Name == name {
			return c
		}
	}
	t.Fatal("missing capability", name)
	return lark.Capability{}
}

func TestLarkDefaultActionsAreClosedAndIntersectAcceptedAdapter(t *testing.T) {
	caps := lark.AcceptedActionCapabilities(nil, nil, nil)
	caps.Actions["future.write"] = platform.ActionCapability{Spec: platform.ActionSpec{Operation: "future.write", TargetKind: "object", ObjectTypes: []string{"chat"}}}
	got := defaultLarkActionGrants(caps)
	if len(got) != 3 || got[0].Operation != "message.reply" || got[1].ObjectType != "chat" || got[2].Operation != "message.send_self" {
		t.Fatal(got)
	}
	cap := caps.Actions[platform.OperationMessageSend]
	cap.Spec.ObjectTypes = []string{"user"}
	caps.Actions[platform.OperationMessageSend] = cap
	cap = caps.Actions[platform.OperationMessageReply]
	cap.Spec.DisabledReason = "unavailable"
	caps.Actions[platform.OperationMessageReply] = cap
	got = defaultLarkActionGrants(caps)
	if len(got) != 1 || got[0].Operation != "message.send_self" {
		t.Fatal(got)
	}
}

func TestLarkMemberCapabilityProjectionKeepsIntentSeparateFromReadiness(t *testing.T) {
	now := time.Now().UTC()
	account := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: lark.Origin("feishu"), Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "tenant"}}, ID: platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_reader"}}
	a := platform.AccessContext{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "instance"}, AgentID: "agent", MembershipID: "membership", Account: account, PlatformID: "lark", Generation: "generation", PlatformVersion: 1, CredentialVersion: 2}
	caps := lark.AcceptedActionCapabilities(nil, nil, nil)
	p := larkPlatformCapabilityResult{PlatformID: a.PlatformID, PlatformVersion: a.PlatformVersion, Generation: a.Generation, CredentialVersion: a.CredentialVersion, PermissionCheck: lark.PermissionCheck{Account: account, CheckedAt: now}}
	for _, d := range lark.CapabilityDependencies() {
		p.Capabilities = append(p.Capabilities, lark.Capability{Name: d.Name, State: "ready", EvidenceState: "not_required"})
	}
	b := tool.Binding{AgentID: a.AgentID, MembershipID: a.MembershipID, Tool: "lark", PlatformID: a.PlatformID, PlatformGeneration: a.Generation, ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 3, Grants: defaultLarkActionGrants(caps)}}
	target := platform.TargetIdentity{IdentityScope: account.IdentityScope, Object: platform.NativeIdentity{Type: "user", ID: account.ID}, Containers: []platform.NativeIdentity{{Type: "chat", ID: platform.ExternalID{Namespace: "lark:chat_id:cli_fixture", Value: "oc_self"}}}}
	evidence := platform.CapabilityEvidence{Kind: "self_delivery", Operator: "human", Account: account, CredentialVersion: a.CredentialVersion, Target: target, AccountOwnerConfirmed: true}
	project := func(b *tool.Binding, snap larkPlatformCapabilityResult, at time.Time) larkMemberCapabilityResult {
		return projectLarkMemberCapabilities(a, "human", b, 7, snap, caps, at)
	}
	r := project(&b, p, now)
	if c := memberCapability(t, r, "message.send_self"); c.State != "blocked" || c.EvidenceState != "missing" || c.Configured == nil || !*c.Configured || c.NextAction != "verify_self" {
		t.Fatal(c)
	}
	if memberCapability(t, r, "message.reply").State != "ready" || memberCapability(t, r, "message.send").State != "ready" {
		t.Fatal(r)
	}
	b.CapabilityEvidence = []platform.CapabilityEvidence{evidence}
	if c := memberCapability(t, project(&b, p, now), "message.send_self"); c.State != "ready" || c.EvidenceState != "current" {
		t.Fatal(c)
	}
	for _, mutate := range []func(*platform.CapabilityEvidence){
		func(e *platform.CapabilityEvidence) { e.Operator = "other" },
		func(e *platform.CapabilityEvidence) { e.CredentialVersion-- },
		func(e *platform.CapabilityEvidence) { e.Account.ID.Value = "ou_other" },
		func(e *platform.CapabilityEvidence) { e.Target.Object.ID.Value = "ou_other" },
		func(e *platform.CapabilityEvidence) { e.AccountOwnerConfirmed = false },
	} {
		wrong := evidence
		mutate(&wrong)
		b.CapabilityEvidence[0] = wrong
		if c := memberCapability(t, project(&b, p, now), "message.send_self"); c.State != "blocked" || c.EvidenceState != "stale" {
			t.Fatal(c)
		}
	}
	b.CapabilityEvidence[0] = evidence
	for _, mutate := range []func(*larkPlatformCapabilityResult){
		func(p *larkPlatformCapabilityResult) { p.PlatformVersion++ },
		func(p *larkPlatformCapabilityResult) { p.CredentialVersion++ },
		func(p *larkPlatformCapabilityResult) { p.Generation = "changed" },
		func(p *larkPlatformCapabilityResult) { p.Account.ID.Value = "ou_other" },
		func(p *larkPlatformCapabilityResult) { p.CheckedAt = time.Time{} },
	} {
		snap := p
		mutate(&snap)
		if c := memberCapability(t, project(&b, snap, now), "message.reply"); c.State != "unchecked" || !*c.Configured {
			t.Fatal(c)
		}
	}
	if c := memberCapability(t, project(&b, p, now.Add(larkCapabilityMaxAge)), "message.reply"); c.State != "unchecked" || !*c.Configured {
		t.Fatal(c)
	}
	if c := memberCapability(t, project(&b, larkPlatformCapabilityResult{}, now), "message.reply"); c.State != "unchecked" || !*c.Configured {
		t.Fatal("restart lost configured intent", c)
	}
	unsupported := p
	unsupported.Capabilities = append([]lark.Capability(nil), p.Capabilities...)
	for i := range unsupported.Capabilities {
		if unsupported.Capabilities[i].Name == "message.reply" {
			unsupported.Capabilities[i].State, unsupported.Capabilities[i].ReasonCode = "unsupported", "account_pinning_unavailable"
		}
	}
	if c := memberCapability(t, project(nil, unsupported, now), "message.reply"); c.State != "unsupported" || c.NextAction != "" {
		t.Fatal("member toggle cannot repair host support", c)
	}
	before, _ := json.Marshal(b)
	b.ActionAuthorization = nil // historical read-only binding stays read-only
	r = project(&b, p, now)
	if c := memberCapability(t, r, "message.reply"); c.State != "blocked" || *c.Configured {
		t.Fatal(c)
	}
	if memberCapability(t, r, "message.read").State != "ready" {
		t.Fatal(r)
	}
	if c := memberCapability(t, project(nil, p, now), "notification.group"); c.State != "ready" || c.Configured != nil {
		t.Fatal("tool choice cannot change notification capability", c)
	}
	b.ActionAuthorization = &tool.ActionAuthorization{Version: 1, Revision: 3, Grants: defaultLarkActionGrants(caps)}
	wrongMember := b
	wrongMember.MembershipID = "other"
	if c := memberCapability(t, project(&wrongMember, p, now), "message.send_self"); c.State != "blocked" || *c.Configured || c.EvidenceState != "missing" {
		t.Fatal(c)
	}
	after, _ := json.Marshal(b)
	if string(before) != string(after) || !reflect.DeepEqual(p.Capabilities[0].Configured, (*bool)(nil)) {
		t.Fatal("projection mutated its inputs")
	}
}
