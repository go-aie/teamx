package notification

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlatformChannelUniquenessAndRecovery(t *testing.T) {
	source := fixtureSource{id: SourceID{Provider: "fixture", Channel: "updates"}}
	f := newNotificationFixture(t, source)
	a := fixtureSubscription("a", source.id)
	b := fixtureSubscription("b", source.id)
	b.Platform = "second-platform"
	f.save(t, a)
	f.save(t, b)
	duplicate := fixtureSubscription("duplicate", source.id)
	duplicate.Enabled = false
	duplicate.RuntimeState.Reset(false, false)
	if err := f.m.SaveLocked(duplicate); !errors.Is(err, ErrConflict) {
		t.Fatal("disabled duplicate acquired platform/channel", err)
	}
	moved := a
	moved.Platform = b.Platform
	if err := f.m.SaveLocked(moved); !errors.Is(err, ErrIdentity) {
		t.Fatal("subscription changed platform", err)
	}
	f.m.ForgetRoomLocked(a.K.Owner)
	if err := f.m.LoadLocked(a.K.Owner); err != nil {
		t.Fatal(err)
	}
	for _, want := range []fixtureState{a, b} {
		got, err := f.m.CurrentLocked(want.K.Owner, source.id, want.Platform)
		if err != nil || got == nil || got.Metadata().Key != want.K {
			t.Fatal(got, err)
		}
	}
	// The reader also rejects a duplicated live identity written by an old or
	// corrupt producer, without changing either saved subscription.
	path, err := f.m.StatePathLocked(a.K)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// fixture snapshots use exported Key field names.
	data = []byte(strings.ReplaceAll(string(data), `"SubscriptionID":"a"`, `"SubscriptionID":"duplicate"`))
	duplicatePath := filepath.Join(filepath.Dir(path), "fixture.updates.duplicate.json")
	if err := os.WriteFile(duplicatePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	f.m.ForgetRoomLocked(a.K.Owner)
	if err := f.m.LoadLocked(a.K.Owner); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate loaded", err)
	}
}
