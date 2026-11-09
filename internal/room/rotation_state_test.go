package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

func rotationFixture(t *testing.T) (*Store, SessionOptions, rotationRecord, memberScheduling) {
	t.Helper()
	dir, id := recoveryFixture(t)
	store := openTestStore(t, dir)
	opts := adapterOptions(store, id)
	from := "session.json.dsh"
	to := from + "-" + strings.Repeat("b", 32)
	r := rotationRecord{Version: rotationVersion, MemberIdentity: id, OperationID: strings.Repeat("a", 32), Reason: "manual", From: from, To: to, BaseUsed: 5, Phase: "prepared"}
	state := memberScheduling{Version: schedulingVersion, MemberIdentity: id, Enabled: false, Used: 5, Limit: 16, SessionGeneration: from, SessionStartUsed: 2}
	store.mu.Lock()
	err := store.save(opts.StatePath, dshReference{Version: formatVersion, MemberIdentity: id, Directory: from}, maxPrivateFileBytes)
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dshStateDir(opts.StatePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dshStateDir(opts.StatePath), "backend.json"), []byte(`{"version":1,"sessionId":"old","configSHA256":"test","stopped":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.saveScheduling(state); err != nil {
		t.Fatal(err)
	}
	return store, opts, r, state
}

func TestRotationRecoveryTable(t *testing.T) {
	for _, kind := range []string{"prepared", "unsettled", "old_closed_from", "target_old_baseline", "target_new_baseline", "completed", "completed_later_turns", "legacy_empty", "third_directory", "count_changed", "baseline_changed", "target_started", "prepared_switched", "baseline_ahead", "completed_mismatch"} {
		t.Run(kind, func(t *testing.T) {
			store, opts, r, state := rotationFixture(t)
			generation, pending := r.From, false
			valid := true
			switch kind {
			case "unsettled":
				os.Remove(filepath.Join(dshStateDir(opts.StatePath), "backend.json"))
				valid = false
			case "old_closed_from":
				r.Phase = "old_closed"
			case "target_old_baseline":
				r.Phase = "old_closed"
				generation, pending = r.To, true
			case "target_new_baseline":
				r.Phase = "old_closed"
				generation, pending = r.To, true
				state.SessionGeneration, state.SessionStartUsed = r.To, r.BaseUsed
			case "completed", "completed_later_turns":
				r.Phase = "completed"
				generation, pending = r.To, true
				state.SessionGeneration, state.SessionStartUsed = r.To, r.BaseUsed
				if kind == "completed_later_turns" {
					state.Used += 2
				}
			case "legacy_empty":
				state.SessionGeneration, state.SessionStartUsed = "", 0
			case "third_directory":
				r.Phase = "old_closed"
				generation, pending = "session.json.dsh-"+strings.Repeat("c", 32), true
				valid = false
			case "count_changed":
				r.Phase = "old_closed"
				state.Used++
				valid = false
			case "baseline_changed":
				r.Phase = "old_closed"
				generation, pending = r.To, true
				state.SessionGeneration, state.SessionStartUsed = r.To, r.BaseUsed-1
				valid = false
			case "target_started":
				r.Phase = "old_closed"
				generation = r.To
				valid = false
			case "prepared_switched":
				generation, pending = r.To, true
				valid = false
			case "baseline_ahead":
				r.Phase = "old_closed"
				state.SessionGeneration, state.SessionStartUsed = r.To, r.BaseUsed
				valid = false
			case "completed_mismatch":
				r.Phase = "completed"
				valid = false
			}
			store.mu.Lock()
			err := store.save(opts.StatePath, dshReference{Version: formatVersion, MemberIdentity: opts.MemberIdentity, Directory: generation, Pending: pending}, maxPrivateFileBytes)
			store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if err := store.saveScheduling(state); err != nil {
				t.Fatal(err)
			}
			if err := store.saveRotation(r); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(opts.StatePath)
			err = reconcileRotation(store, opts, &r, &state)
			if (err == nil) != valid {
				t.Fatalf("valid=%v error=%v", valid, err)
			}
			if !valid {
				after, _ := os.ReadFile(opts.StatePath)
				if !bytes.Equal(before, after) {
					t.Fatal("conflict changed reference")
				}
				return
			}
			if r.Phase != "completed" || state.Used < 5 || state.SessionGeneration != r.To || state.SessionStartUsed != 5 || state.Enabled {
				t.Fatal(r, state)
			}
			saved, _ := store.loadScheduling(opts.MemberIdentity)
			before, _ = json.Marshal(saved)
			if err := reconcileRotation(store, opts, &r, &state); err != nil {
				t.Fatal(err)
			}
			saved, _ = store.loadScheduling(opts.MemberIdentity)
			after, _ := json.Marshal(saved)
			if !bytes.Equal(before, after) {
				t.Fatal("completed operation rewrote count or intent")
			}
		})
	}
}

func TestRotationRecoveryAfterEachUncertainCommit(t *testing.T) {
	for _, step := range []string{"prepared", "old_closed", "session.json", "collaboration.json", "completed"} {
		t.Run(step, func(t *testing.T) {
			store, opts, r, state := rotationFixture(t)
			fired := false
			store.ops.rename = func(from, to string) error {
				match := filepath.Base(to) == step
				if filepath.Base(to) == "rotation.json" {
					var record rotationRecord
					data, _ := os.ReadFile(from)
					_ = json.Unmarshal(data, &record)
					match = record.Phase == step
				}
				if err := os.Rename(from, to); err != nil {
					return err
				}
				if match && !fired {
					fired = true
					return errors.New("replacement outcome uncertain")
				}
				return nil
			}
			err := store.saveRotation(r)
			if err == nil {
				err = reconcileRotation(store, opts, &r, &state)
			}
			if !fired || !errors.Is(err, ErrCommitUnknown) {
				t.Fatal(fired, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openTestStore(t, store.dir)
			saved, err := reopened.loadRotation(opts.MemberIdentity)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := reopened.loadScheduling(opts.MemberIdentity)
			if err != nil {
				t.Fatal(err)
			}
			if err := reconcileRotation(reopened, opts, saved, &restored); err != nil {
				t.Fatal(err)
			}
			if saved.To != r.To || saved.Phase != "completed" || restored.Used != 5 || restored.SessionStartUsed != 5 {
				t.Fatal(saved, restored)
			}
		})
	}
}

func TestRotationDurableOperationPauseAndLifecycle(t *testing.T) {
	h := newRoomHarness(t, historyRoom(1, "body"), nil)
	id := h.memberID("A", "generation-A")
	opts := adapterOptions(h.store, id)
	closing, release := make(chan struct{}), make(chan struct{})
	backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) { return dsh.TurnResult{}, nil }, close: func(context.Context) error { close(closing); <-release; return nil }}, nil
	}}
	addr, err := h.spawn(&MemberAgent{Options: opts, Store: h.store, Room: h.addr, Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, addr, id, "run", input, false, false)
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	svc := &service{ctx: h.ctx, info: ServiceInfo{RoomIdentity: id.RoomIdentity}, store: h.store, room: h.addr, targets: map[string]actorx.Addr{id.MembershipID: addr}, dshProfiles: map[string]dshProfile{"reviewer": {}}}
	// The roster in historyRoom uses a scripted fixture profile; service checks
	// the profile name while the actual actor holds the DSH adapter.
	list := roomCall[MemberList](t, h, ListMembers{h.id})
	svc.dshProfiles[list.Members[0].Runner.Profile] = dshProfile{}
	var platformReleased bool
	svc.beginPlatformMemberChange = func(context.Context, MemberIdentity) (func(), error) { return func() { platformReleased = true }, nil }
	req := apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, OperationID: strings.Repeat("d", 32), FromGeneration: "session.json.dsh", Resume: true}
	if _, err := svc.control(h.ctx, req, "rotate"); err != nil {
		t.Fatal(err)
	}
	<-closing
	if svc.management.TryLock() {
		svc.management.Unlock()
		t.Fatal("released lifecycle admission before cleanup")
	}
	if _, err := svc.control(h.ctx, req, "rotate"); err != nil {
		t.Fatal("duplicate active operation", err)
	}
	conflicting := req
	conflicting.Cancel = true
	if _, err := svc.control(h.ctx, conflicting, "rotate"); !errors.Is(err, ErrConflict) {
		t.Fatal("parameter conflict accepted", err)
	}
	other := req
	other.OperationID = strings.Repeat("e", 32)
	if _, err := svc.control(h.ctx, other, "rotate"); !errors.Is(err, ErrBusy) {
		t.Fatal("concurrent operation accepted", err)
	}
	controlMember(t, h, addr, id, "pause", UnreadChanged{}, false, false)
	close(release)
	status := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.Rotation.Status == "completed" })
	if !status.Paused || status.Agent.LastOfferedUnreadSeq != 1 || status.SessionStartUsed != 1 {
		t.Fatal(status)
	}
	// An Actor call orders after the release callback; no wall-clock guess.
	if !platformReleased {
		t.Fatal("platform admission retained after completion")
	}
	if !svc.management.TryLock() {
		t.Fatal("lifecycle admission retained")
	}
	svc.management.Unlock()
	if _, err := svc.control(h.ctx, req, "rotate"); err != nil {
		t.Fatal(err)
	}
	state, err := h.store.loadScheduling(id)
	if err != nil || state.Enabled || state.Used != 1 || state.SessionStartUsed != 1 {
		t.Fatal(state, err)
	}
	other.FromGeneration = "session.json.dsh"
	if _, err := svc.control(h.ctx, other, "rotate"); !errors.Is(err, ErrConflict) {
		t.Fatal("stale from accepted", err)
	}
}

func TestRotationConvertersAndRequiredFields(t *testing.T) {
	old := memberScheduling{Version: formatVersion, Enabled: false, Used: 23, Limit: 32}
	converted, err := convertScheduling(old)
	if err != nil || converted.Used != 23 || converted.Enabled || converted.SessionGeneration != "" || converted.Limit != 32 {
		t.Fatal(converted, err)
	}
	if _, err := convertScheduling(converted); !errors.Is(err, ErrVersion) {
		t.Fatal("converted twice", err)
	}
	c, err := convertManagedRoomConfig(managedRoomConfig{Version: formatVersion, MaxTurns: 7})
	if err != nil || c.SessionMaxTurns != 7 || c.MaxTurns != 0 || c.Version != 2 {
		t.Fatal(c, err)
	}
	if _, err := convertManagedRoomConfig(c); !errors.Is(err, ErrVersion) {
		t.Fatal("converted twice", err)
	}
	for _, key := range []string{"used", "enabled", "session_generation", "session_start_used"} {
		t.Run(key, func(t *testing.T) {
			store, opts, _, state := rotationFixture(t)
			data, _ := json.Marshal(state)
			var fields map[string]any
			_ = json.Unmarshal(data, &fields)
			delete(fields, key)
			store.mu.Lock()
			path, _, err := store.memberPath(opts.MemberIdentity, "collaboration.json", true)
			if err == nil {
				err = store.save(path, fields, maxPrivateFileBytes)
			}
			store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.loadScheduling(opts.MemberIdentity); err == nil {
				t.Fatal("missing required field accepted")
			}
		})
	}
}

func TestRotationCumulativeOverflowBlocksBeforeRun(t *testing.T) {
	h := newRoomHarness(t, historyRoom(1, "body"), nil)
	id := h.memberID("A", "generation-A")
	b := newControlledBackend()
	addr, err := h.spawn(&MemberAgent{Options: SessionOptions{MemberIdentity: id}, Store: h.store, Room: h.addr, Backend: b, MaxTurns: ^uint64(0), RestoredTurnsUsed: ^uint64(0)})
	if err != nil {
		t.Fatal(err)
	}
	// Exhaustion rejects before addition; enable also rejects limit overflow.
	_, err = addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "enable", State: UnreadChanged{MemberIdentity: id}})
	if err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatal(err)
	}
	if b.opens.Load() != 0 {
		t.Fatal("overflow admitted execution")
	}
}

func TestRotationDefaultPauseSurvivesPreparedCommitUncertainty(t *testing.T) {
	h := newRoomHarness(t, historyRoom(1, "body"), nil)
	id := h.memberID("A", "generation-A")
	opts := adapterOptions(h.store, id)
	target, err := h.spawn(&MemberAgent{Options: opts, Store: h.store, Room: h.addr, Backend: dshBackend{store: h.store}})
	if err != nil {
		t.Fatal(err)
	}
	state, err := h.store.loadScheduling(id)
	if err != nil {
		t.Fatal(err)
	}
	state.Enabled = true
	if err := h.store.saveScheduling(state); err != nil {
		t.Fatal(err)
	}
	original := h.store.ops.rename
	h.store.mu.Lock()
	h.store.ops.rename = func(from, to string) error {
		if err := original(from, to); err != nil {
			return err
		}
		if filepath.Base(to) == "rotation.json" {
			return errors.New("prepared replacement uncertain")
		}
		return nil
	}
	h.store.mu.Unlock()
	_, err = target.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "rotate"})
	if !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	saved, err := h.store.loadScheduling(id)
	if err != nil || saved.Enabled {
		t.Fatal("default pause was not durable before prepared", saved, err)
	}
	rotation, err := h.store.loadRotation(id)
	if err != nil || rotation.Phase != "prepared" {
		t.Fatal(rotation, err)
	}
	if status := inspectMember(t, h.ctx, target, id); !status.Paused || status.Rotation.Status != "failed" {
		t.Fatal(status)
	}
}

func TestRotationCountersShareRoomInboxFailureAndCancellation(t *testing.T) {
	b := newControlledBackend()
	inbox := newInboxHarness(t, InboxLimits{})
	h, id := inbox.roomHarness, inbox.id
	roomCall[PostReceipt](t, h, h.post(humanCaller, "count-room", "body"))
	target, err := h.spawn(&MemberAgent{Options: adapterOptions(h.store, id), Store: h.store, Room: h.addr, Backend: rotatingControlledBackend{b}, MaxTurns: 3,
		Inbox: func(ctx context.Context) (InboxStatus, error) {
			return inbox.addr.Call[InboxStatus](ctx, InspectInbox{id})
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, target, id, "run", input, false, false)
	if offered := awaitTurn(t, h, b); offered.Inbox != nil {
		t.Fatal("unexpected Inbox input", offered)
	}
	b.finish <- errors.New("accepted failed Room turn")
	waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	inboxCall[InboxReceipt](t, inbox, inbox.receive("count-inbox"))
	if offered := awaitTurn(t, h, b); offered.Inbox == nil || offered.Inbox.SnapshotSeq != 1 {
		t.Fatal("Inbox did not share the same scheduler", offered)
	}
	controlMember(t, h, target, id, "pause", UnreadChanged{}, false, true)
	<-b.canceled
	status := waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
	saved, err := h.store.loadScheduling(id)
	if err != nil || saved.Used != 2 || saved.SessionStartUsed != 0 || saved.Enabled || status.TurnBudget.Used != 2 || status.Agent.Turn.Status != "interrupted" {
		t.Fatal(status, saved, err)
	}
	recovered := recoveredMember{id: id, backendResume: true}
	if err := (&service{store: h.store, maxTurns: 3}).restoreScheduling(&recovered, *status.Agent); err != nil {
		t.Fatal(err)
	}
	if recovered.used != 2 || recovered.resume {
		t.Fatal("restart reset shared count or resumed cancellation", recovered)
	}
}

func TestRotationServiceQueriesOriginalEnableOptions(t *testing.T) {
	h := newRoomHarness(t, historyRoom(1, "body"), nil)
	id := h.memberID("A", "generation-A")
	target, err := h.spawn(&MemberAgent{Options: adapterOptions(h.store, id), Store: h.store, Room: h.addr, BackendBlocked: "explicit reset required", Backend: dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		t.Error("query opened Session")
		return nil, errors.New("unexpected Open")
	}}})
	if err != nil {
		t.Fatal(err)
	}
	roster := roomCall[MemberList](t, h, ListMembers{h.id})
	svc := &service{ctx: h.ctx, info: ServiceInfo{RoomIdentity: h.id}, store: h.store, room: h.addr, targets: map[string]actorx.Addr{id.MembershipID: target}, dshProfiles: map[string]dshProfile{roster.Members[0].Runner.Profile: {}}}
	req := apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}
	if _, err := svc.control(h.ctx, req, "enable"); err != nil {
		t.Fatal(err)
	}
	status := waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return s.Rotation != nil && s.Rotation.Status == "completed" })
	if !status.Rotation.Replenish || !status.Rotation.Resume {
		t.Fatal(status.Rotation)
	}
	req.OperationID, req.FromGeneration, req.Resume = status.Rotation.OperationID, status.Rotation.FromGeneration, true
	queried, err := svc.control(h.ctx, req, "rotate")
	if err != nil || queried.Rotation.OperationID != req.OperationID {
		t.Fatal("lost original enable options on query", queried, err)
	}
}
