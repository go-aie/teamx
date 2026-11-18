package notification

import (
	"context"
	"errors"
	"testing"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
)

func TestSourceReleaseUsesExactFrozenSubscriptionEvidence(t *testing.T) {
	source := SourceID{"fixture", "changes"}
	f := newNotificationFixture(t, fixtureSource{id: source})
	state := fixtureSubscription("subscription", source)
	item := Item{Key: "native", DeliveryID: DeliveryID(state.K, "native"), Route: Route{Version: 1, Target: state.Target}, Source: platform.SourceRef{Provider: source.Provider, Channel: source.Channel, SubscriptionID: state.K.SubscriptionID, ItemKey: "native"}}
	item.DeliveryID = item.Route.DeliveryID(item.DeliveryID)
	state.Inbox[item.Key] = item
	state.Seen[item.Key] = true
	f.save(t, state)
	candidate := SourceReleaseCandidate{Owner: state.K.Owner, InboxID: state.Target.MembershipID, Location: inputstream.Location{Partition: "partition", Seq: 1}, Item: item, Digest: "saved-record"}
	check := func(c SourceReleaseCandidate) SourceRelease {
		result, err := f.m.CheckSourceRelease(context.Background(), []SourceReleaseCandidate{c})
		if err != nil {
			t.Fatal(err)
		}
		return result[0]
	}
	before := check(candidate)
	if result := before; result.Status != "held" {
		t.Fatal(result)
	}
	f.save(t, state.Acknowledge(item.Key))
	if result := before; result.Status != "held" {
		t.Fatal("snapshot changed after capture", result)
	}
	after := check(candidate)
	if result := after; result.Status != "released" || result.Revision == 0 {
		t.Fatal(result)
	}
	missing := candidate
	missing.Item.Source.SubscriptionID = "missing-history"
	if result := check(missing); result.Status != "unknown" {
		t.Fatal("unrelated channel inventory proved release", result)
	}
	rawID := candidate
	rawID.Item.DeliveryID = DeliveryID(state.K, item.Key)
	if result := check(rawID); result.Status != "unknown" {
		t.Fatal("unrouted source identity proved target release", result)
	}
	wrong := candidate
	wrong.InboxID = "other-member"
	if result := check(wrong); result.Status != "unknown" {
		t.Fatal(result)
	}
	f.gate = errors.New("uncertain source commit")
	if result := check(candidate); result.Status != "unknown" {
		t.Fatal("uncertain source proved release", result)
	}
}

func TestSourceReleaseTerminalSurvivesDisableRetireAndReopen(t *testing.T) {
	source := SourceID{"fixture", "changes"}
	f := newNotificationFixture(t, fixtureSource{id: source})
	state := fixtureSubscription("terminal", source)
	item := Item{Key: "native", DeliveryID: DeliveryID(state.K, "native"), Route: Route{Version: 1, Target: state.Target}, Source: platform.SourceRef{Provider: source.Provider, Channel: source.Channel, SubscriptionID: state.K.SubscriptionID, ItemKey: "native"}}
	item.DeliveryID = item.Route.DeliveryID(item.DeliveryID)
	state.Seen[item.Key] = true
	state.Enabled = false
	state.Retired = true
	state.Version++
	state.RuntimeState.Reset(false, false)
	f.save(t, state)
	c := SourceReleaseCandidate{Owner: state.K.Owner, InboxID: state.Target.MembershipID, Location: inputstream.Location{Partition: "partition", Seq: 1}, Item: item, Digest: "record"}
	read := func() SourceRelease {
		r, err := f.m.CheckSourceRelease(t.Context(), []SourceReleaseCandidate{c})
		if err != nil {
			t.Fatal(err)
		}
		return r[0]
	}
	before := read()
	if before.Status != "released" || !f.m.ReleaseCurrent(before) {
		t.Fatal(before)
	}
	f.m.ForgetRoomLocked(state.K.Owner)
	if f.m.ReleaseCurrent(before) {
		t.Fatal("unloaded source activation remained valid")
	}
	if read().Status != "unknown" {
		t.Fatal("missing historical namespace proved release")
	}
	if err := f.m.LoadLocked(state.K.Owner); err != nil {
		t.Fatal(err)
	}
	after := read()
	if after.Status != "released" || after.Activation == before.Activation {
		t.Fatal("terminal was not reconstructed from durable history", after)
	}
	if err := f.m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if read().Status != "unknown" || f.m.ReleaseCurrent(after) {
		t.Fatal("closed owner supplied release evidence")
	}
}
