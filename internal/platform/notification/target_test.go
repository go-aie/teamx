package notification

import (
	"errors"
	"reflect"
	"testing"
)

func TestTargetNormalizationAndMembershipScope(t *testing.T) {
	for _, input := range []Target{{}, {Mode: "room"}} {
		got, err := input.Normalize()
		if err != nil || got != (Target{}) {
			t.Fatal(got, err)
		}
	}
	for _, input := range []Target{{Mode: "room", AgentID: "a"}, {Mode: "room", MembershipID: "m"}, {Mode: "inbox", AgentID: "a"}, {AgentID: "a", MembershipID: "m"}, {Mode: "inbox", MembershipID: "m"}, {Mode: "inbox", AgentID: "a", MembershipID: "../m"}} {
		if _, err := input.Normalize(); err == nil {
			t.Fatal("accepted incomplete target", input)
		}
	}
	a := Target{Mode: "inbox", AgentID: "a", MembershipID: "m"}
	b := a
	b.AgentID = "b"
	if a.ScopeKey() != b.ScopeKey() {
		t.Fatal("agent allocated another membership scope")
	}
}

func TestDestinationUniquenessPersistenceAndImmutableIdentity(t *testing.T) {
	source := fixtureSource{id: SourceID{Provider: "fixture", Channel: "updates"}}
	f := newNotificationFixture(t, source)
	room := fixtureSubscription("room", source.id)
	a, b := fixtureSubscription("a", source.id), fixtureSubscription("b", source.id)
	a.Target = Target{Mode: "inbox", AgentID: "assistant", MembershipID: "membership-a"}
	b.Target = Target{Mode: "inbox", AgentID: "reviewer", MembershipID: "membership-b"}
	a.Offset, b.Offset = 7, 19
	// A historic pending item may target a different receiver. Its route and ID
	// remain immutable even though the current subscription target has changed.
	a.Inbox["historic"] = Item{Key: "historic", DeliveryID: "old-delivery-id", Route: Route{Target: b.Target, Version: 1}}
	for _, state := range []fixtureState{room, a, b} {
		f.save(t, state)
	}
	duplicate := fixtureSubscription("duplicate", source.id)
	duplicate.Target = a.Target
	duplicate.Enabled = false
	duplicate.RuntimeState.Reset(false, false)
	if err := f.m.SaveLocked(duplicate); !errors.Is(err, ErrConflict) {
		t.Fatal("disabled duplicate", err)
	}
	impostor := duplicate
	impostor.Target.AgentID = "other"
	if err := f.m.SaveLocked(impostor); !errors.Is(err, ErrIdentity) {
		t.Fatal("membership changed agent", err)
	}
	moved := a
	moved.Target = b.Target
	if err := f.m.SaveLocked(moved); !errors.Is(err, ErrIdentity) {
		t.Fatal("retargeted subscription", err)
	}
	invalid := duplicate
	invalid.Target = Target{Mode: "room", AgentID: "a"}
	if err := f.m.SaveLocked(invalid); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid metadata", err)
	}
	if _, err := f.m.CurrentLocked(a.K.Owner, source.id, a.Platform, impostor.Target); !errors.Is(err, ErrIdentity) {
		t.Fatal("impostor lookup", err)
	}
	f.m.ForgetRoomLocked(a.K.Owner)
	if err := f.m.LoadLocked(a.K.Owner); err != nil {
		t.Fatal(err)
	}
	for _, want := range []fixtureState{room, a, b} {
		got, err := f.m.CurrentLocked(want.K.Owner, source.id, want.Platform, want.Target)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatal("reopen changed identity/progress", got, err)
		}
	}
	// A's retired worker must settle before A can be replaced, while B remains
	// immediately selectable. The worker's physical key is unchanged.
	retired := a
	retired.Retired = true
	f.save(t, retired)
	f.m.workers[a.K] = &worker{done: make(chan struct{})}
	defer delete(f.m.workers, a.K)
	if _, err := f.m.CurrentLocked(a.K.Owner, source.id, a.Platform, a.Target); !errors.Is(err, ErrBusy) {
		t.Fatal("A replacement raced predecessor", err)
	}
	if got, err := f.m.CurrentLocked(b.K.Owner, source.id, b.Platform, b.Target); err != nil || got.Metadata().Key != b.K {
		t.Fatal("A blocked B", err)
	}
}
