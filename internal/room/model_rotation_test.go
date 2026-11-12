package room

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func testModelTarget(t *testing.T, id MemberIdentity, credentials *modelCredentials) modelSessionSnapshot {
	t.Helper()
	ref, err := credentials.put("new-bound-key")
	if err != nil {
		t.Fatal(err)
	}
	entry := modelTestEntry("B")
	entry.CredentialRef = ref
	return modelSessionSnapshot{Version: 1, MemberIdentity: id, Generation: "session.json.dsh", Selection: modelSelection{Mode: "explicit", EntryID: "B"}, SelectionVersion: 1, CatalogVersion: 1, EntryID: "B", EntryRevision: 1, CredentialRef: ref, ExecutionDigest: entry.executionDigest(), Runtime: dsh.RuntimeIdentity{BinaryPath: "/fixture-dsh", Version: dsh.ProbeVersion, Agent: dsh.Artifact{Source: "fixture", Version: "1"}}, Profile: dshProfile{BinaryPath: "/fixture-dsh", Workspace: t.TempDir(), Instructions: "role-B", Model: entry.materialize()}}
}

func TestModelRotationWaitsForTurnBeforePlatformAndBackend(t *testing.T) {
	for _, pause := range []bool{false, true} {
		t.Run(map[bool]string{false: "enabled", true: "later-pause"}[pause], func(t *testing.T) {
			state := historyRoom(1, "body")
			h := newRoomHarness(t, state, nil)
			id := h.memberID("A", "generation-A")
			opts := adapterOptions(h.store, id)
			credentials, err := newModelCredentials(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			entered, finish := make(chan struct{}), make(chan struct{})
			var opens, platform, closed atomic.Int32
			observed := make(chan dsh.Options, 2)
			backend := dshBackend{store: h.store, credentials: credentials, profile: dshProfile{Model: dsh.ModelConfig{ModelID: "A"}}, open: func(_ context.Context, o dsh.Options) (dshComponent, error) {
				n := opens.Add(1)
				observed <- o
				return &adapterComponent{run: func(ctx context.Context, _ string) (dsh.TurnResult, error) {
					if n == 1 {
						close(entered)
						select {
						case <-ctx.Done():
							t.Error("model change cancelled current turn")
							return dsh.TurnResult{}, ctx.Err()
						case <-finish:
						}
					}
					return dsh.TurnResult{StopReason: "end_turn"}, nil
				}, close: func(context.Context) error { closed.Add(1); return nil }}, nil
			}}
			addr, err := h.spawn(&MemberAgent{Options: opts, Store: h.store, Room: h.addr, Backend: backend, MaxTurns: 10})
			if err != nil {
				t.Fatal(err)
			}
			input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
			controlMember(t, h, addr, id, "run", input, false, false)
			<-entered
			<-observed
			target := testModelTarget(t, id, credentials)
			lease := &rotationLease{gateDone: make(chan struct{}), begin: func(context.Context) error { platform.Add(1); return nil }, release: func() {}}
			status, err := addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "rotate", modelTarget: &target, lease: lease, Reason: "model_change"})
			if err != nil {
				t.Fatal(err)
			}
			if status.Rotation.Status != "waiting" || platform.Load() != 0 || closed.Load() != 0 || opens.Load() != 1 {
				t.Fatal("early platform/backend change", status)
			}
			if pause {
				controlMember(t, h, addr, id, "pause", UnreadChanged{}, false, false)
			}
			close(finish)
			status = waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.Rotation.Status == "completed" })
			if platform.Load() != 1 || closed.Load() != 1 || opens.Load() != 1 || status.TurnBudget.Used != 1 || status.Paused != pause {
				t.Fatal("model rotation changed intent or started a turn", status)
			}
			controlMember(t, h, addr, id, "run", input, true, false)
			select {
			case actual := <-observed:
				if actual.Model.ModelID != "B" || actual.Credential == nil || actual.Credential.Value != "new-bound-key" || !strings.Contains(actual.Instructions, "private context has been reset") {
					t.Fatal("replacement still uses old backend")
				}
			case <-time.After(time.Second):
				t.Fatal("new session did not open")
			}
			waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
			record, err := h.store.loadRotation(id)
			if err != nil || record.ModelTarget == nil || record.ModelTarget.Generation != record.To {
				t.Fatal("target not durable", err)
			}
			// A future catalog edit cannot change the generation's restoration config.
			current := backend
			current.model = &target
			current.profile.Model.ModelID = "latest-wrong"
			ref, _, err := current.reference(opts)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := current.restoreModelSnapshot(opts, ref)
			if err != nil || restored.profile.Model.ModelID != "B" {
				t.Fatal("restored latest instead of frozen", err)
			}
			if restored.model.CredentialRef != target.CredentialRef {
				t.Fatal("credential version changed")
			}
			frozen, err := os.ReadFile(opts.StatePath[:len(opts.StatePath)-len("session.json")] + ref.Directory + ".model.json")
			if err != nil || strings.Contains(string(frozen), "new-bound-key") {
				t.Fatal("snapshot key leak", err)
			}
		})
	}
}

func TestModelSnapshotRequiresMatchingIdentityAndImmutableExecution(t *testing.T) {
	h := newRoomHarness(t, historyRoom(1, "body"), nil)
	id := h.memberID("A", "generation-A")
	opts := adapterOptions(h.store, id)
	credentials, err := newModelCredentials(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := testModelTarget(t, id, credentials)
	backend := dshBackend{store: h.store, credentials: credentials, model: &target}
	ref := dshReference{Directory: "session.json.dsh"}
	if err := backend.saveModelSnapshot(opts, ref); err != nil {
		t.Fatal(err)
	}
	changed := target.clone()
	changed.Profile.Model.ModelID = "changed"
	backend.model = &changed
	if err := backend.saveModelSnapshot(opts, ref); !errors.Is(err, ErrConflict) && !errors.Is(err, ErrInvalid) {
		t.Fatal("rewrote generation", err)
	}
	restored, err := backend.restoreModelSnapshot(opts, ref)
	if err != nil || !reflect.DeepEqual(restored.model, &target) {
		t.Fatal(err)
	}
	wrong := opts
	wrong.MembershipID = "someone-else"
	if _, err := backend.restoreModelSnapshot(wrong, ref); err == nil {
		t.Fatal("cross-member snapshot accepted")
	}
}

func TestManagedModelReferenceNeverFallsBackWhenSnapshotMissing(t *testing.T) {
	h := newRoomHarness(t, historyRoom(1, "body"), nil)
	id := h.memberID("A", "generation-A")
	opts := adapterOptions(h.store, id)
	h.store.mu.Lock()
	err := h.store.save(opts.StatePath, dshReference{Version: formatVersion, MemberIdentity: id, Directory: "session.json.dsh", ManagedModel: true}, maxPrivateFileBytes)
	h.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	backend := dshBackend{store: h.store, recover: true, open: func(context.Context, dsh.Options) (dshComponent, error) {
		t.Fatal("fell back to the current legacy model")
		return nil, nil
	}}
	if _, err := backend.OpenSession(context.Background(), opts); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing snapshot did not block recovery", err)
	}
}
