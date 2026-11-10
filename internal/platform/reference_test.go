package platform

import (
	"encoding/json"
	"testing"
)

func fixtureTarget() TargetIdentity {
	return TargetIdentity{
		IdentityScope: IdentityScope{Provider: "fixture", Instance: "region:one", Tenant: ExternalID{"tenant", "org-alpha"}},
		Object:        NativeIdentity{Type: "message", ID: ExternalID{"message", "msg-alpha"}},
	}
}

func fixtureAccess() AccessContext {
	t := fixtureTarget()
	return AccessContext{RoomIdentity: RoomIdentity{"room", "instance"}, AgentID: "agent", MembershipID: "membership",
		Account:    AccountIdentity{t.IdentityScope, ExternalID{"account", "user-alpha"}},
		PlatformID: "connection", Generation: "generation", PlatformVersion: 1, CredentialVersion: 1}
}

func TestTargetIdentityIsolation(t *testing.T) {
	base := fixtureTarget()
	key, err := base.Key()
	if err != nil || !fixtureAccess().Valid() {
		t.Fatal("projectless string identities rejected", err)
	}
	for name, change := range map[string]func(*TargetIdentity){
		"provider":         func(v *TargetIdentity) { v.Provider = "other" },
		"instance":         func(v *TargetIdentity) { v.Instance = "region:two" },
		"tenant":           func(v *TargetIdentity) { v.Tenant.Value = "org-beta" },
		"tenant namespace": func(v *TargetIdentity) { v.Tenant.Namespace = "other" },
		"type":             func(v *TargetIdentity) { v.Object.Type = "thread" },
		"id namespace":     func(v *TargetIdentity) { v.Object.ID.Namespace = "other" },
		"id":               func(v *TargetIdentity) { v.Object.ID.Value = "msg-beta" },
		"container":        func(v *TargetIdentity) { v.Containers = []NativeIdentity{{"chat", ExternalID{"chat", "alpha"}}} },
		"discussion":       func(v *TargetIdentity) { v.Discussion = ExternalID{"thread", "alpha"} },
		"message":          func(v *TargetIdentity) { v.Message = ExternalID{"reply", "alpha"} },
	} {
		t.Run(name, func(t *testing.T) {
			v := base
			change(&v)
			got, err := v.Key()
			if err != nil || got == key {
				t.Fatal("distinct identities collided", got, err)
			}
		})
	}
	base.Containers = []NativeIdentity{}
	data, _ := json.Marshal(base)
	var recovered TargetIdentity
	if json.Unmarshal(data, &recovered) != nil {
		t.Fatal("cannot recover identity")
	}
	if got, err := recovered.Key(); err != nil || got != key {
		t.Fatal("identity drift after encoding/recovery", got, err)
	}
	base.Containers = []NativeIdentity{{"chat", ExternalID{"chat", "alpha"}}, {"folder", ExternalID{"folder", "beta"}}}
	first, _ := base.Key()
	base.Containers[0], base.Containers[1] = base.Containers[1], base.Containers[0]
	if second, _ := base.Key(); first == second {
		t.Fatal("container hierarchy ignored")
	}
}

func TestTargetIdentityRejectsIncompleteEvidence(t *testing.T) {
	for _, change := range []func(*TargetIdentity){
		func(v *TargetIdentity) { v.Provider = "" },
		func(v *TargetIdentity) { v.Instance = "" },
		func(v *TargetIdentity) { v.Object.ID.Value = "" },
		func(v *TargetIdentity) { v.Object.ID.Namespace = "" },
		func(v *TargetIdentity) { v.Tenant.Value = "" },
		func(v *TargetIdentity) { v.Discussion.Value = "guessed" },
		func(v *TargetIdentity) { v.Object.ID.Value = "bad\x00id" },
		func(v *TargetIdentity) { v.Containers = make([]NativeIdentity, 17) },
	} {
		v := fixtureTarget()
		change(&v)
		if key, err := v.Key(); err == nil || key != "" {
			t.Fatal("incomplete identity received key")
		}
	}
}

func TestReadCursorBindsTargetScopeAndAccess(t *testing.T) {
	target, access := fixtureTarget(), fixtureAccess()
	key, _ := target.Key()
	c := ReadCursor{Token: "opaque-page", TargetKey: key, Access: access, Scope: "messages"}
	if !c.ValidFor(target, access, "messages") {
		t.Fatal("valid cursor rejected")
	}
	for _, change := range []func(*AccessContext){
		func(a *AccessContext) { a.RoomInstance = "new-room" },
		func(a *AccessContext) { a.MembershipID = "new-member" },
		func(a *AccessContext) { a.AgentID = "other-agent" },
		func(a *AccessContext) { a.Account.ID.Value = "other-account" },
		func(a *AccessContext) { a.Receiver = a.Account },
		func(a *AccessContext) { a.PlatformID = "other" },
		func(a *AccessContext) { a.Generation = "new-generation" },
		func(a *AccessContext) { a.CredentialVersion++ },
		func(a *AccessContext) { a.PlatformVersion++ },
	} {
		a := access
		change(&a)
		if c.ValidFor(target, a, "messages") {
			t.Fatal("cursor survived changed access context")
		}
	}
	if c.ValidFor(target, access, "children") {
		t.Fatal("cursor crossed read scope")
	}
	target.Tenant.Value = "other"
	if c.ValidFor(target, access, "messages") {
		t.Fatal("cursor crossed tenant")
	}
	for _, limits := range []ReadLimits{{}, {101, 1024, 100}, {1, 1<<20 + 1, 100}, {1, 100, 101}} {
		if limits.Valid() {
			t.Fatal("unbounded read accepted", limits)
		}
	}
}
