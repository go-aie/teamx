package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/inputstream"
)

func TestUnifiedInputAutomaticRotationAcrossTwoThresholds(t *testing.T) {
	state := historyRoom(1, "first input")
	state.InputLog = &roomLogRoot{Version: 1}
	h := newRoomHarness(t, state, nil)
	id := h.memberID("A", "generation-A")
	svc := &service{ctx: h.ctx, rt: h.rt, store: h.store, room: h.addr,
		info: ServiceInfo{RoomIdentity: h.id}, unifiedInputs: true, inputAdapters: storageAdapters(),
		targets: map[string]actorx.Addr{}, dshProfiles: map[string]dshProfile{state.Members[0].Runner.Profile: {}}, referenceResolver: referenceTestResolver}
	p, err := svc.inputStorage(id)
	if err != nil {
		t.Fatal(err)
	}
	receivePlatform(t, p, storedGitLab(id, "notification", "events", "7", "42", "platform input"))
	var opens atomic.Int32
	backend := dshBackend{store: h.store, open: func(_ context.Context, options dsh.Options) (dshComponent, error) {
		opens.Add(1)
		return &adapterComponent{run: func(ctx context.Context, _ string) (dsh.TurnResult, error) {
			a, err := h.store.LoadAgent(id)
			if err != nil {
				return dsh.TurnResult{}, err
			}
			input, err := newInputSession(ctx, svc, p, filepath.Base(options.StateDir), a.TurnNumber, false, func(ctx context.Context) error { return ctx.Err() })
			if err != nil {
				return dsh.TurnResult{}, err
			}
			deliverRoom(t, input, inputstream.ReadRequest{Turn: a.TurnNumber, Stream: roomStreamRef(id), ID: "room"})
			if a.TurnNumber == 1 {
				deliverPlatform(t, input, streamFor(t, input, "platform input"), "platform")
			}
			return dsh.TurnResult{StopReason: "end_turn"}, nil
		}}, nil
	}}
	addr, err := h.spawn(&MemberAgent{Options: adapterOptions(h.store, id), Store: h.store, Room: h.addr,
		Backend: backend, SessionMaxTurns: 2, RequestRotation: svc.requestMemberRotation, Inputs: svc.inputStatusFor(id)})
	if err != nil {
		t.Fatal(err)
	}
	svc.setTarget(id.MembershipID, addr)
	controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
	generations := map[string]bool{}
	for n := uint64(1); n <= 5; n++ {
		if n > 1 {
			automaticPost(t, h, addr, id, int(n))
		}
		s := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == n })
		if s.Paused || s.Error != "" || s.Agent.Turn.Status != "completed" || s.SessionTurns.Used != (n-1)%2+1 {
			t.Fatal("unified input stopped at Session threshold", s)
		}
		generations[s.SessionGeneration] = true
	}
	if len(generations) != 3 || opens.Load() != 3 || h.list(t).Members[0].ReadThrough != 5 {
		t.Fatal("rotation lost native consumption", generations, opens.Load(), h.list(t))
	}
	if _, err := os.Stat(filepath.Join(h.store.dir, "inboxes", id.MembershipID, "inbox.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("automatic rotation entered the legacy Inbox", err)
	}
}

func TestAutomaticRotationAdmissionRetriesAndHonorsPause(t *testing.T) {
	for _, pause := range []bool{false, true} {
		t.Run(map[bool]string{false: "recover", true: "pause"}[pause], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, svc, addr, id, opens := automaticHarness(t, 1, nil)
				var failures atomic.Int32
				svc.maintainInbox.Store(&inboxMaintenance{run: func(context.Context, MemberIdentity) error {
					if failures.Add(1) == 1 {
						return context.DeadlineExceeded
					}
					return nil
				}})
				controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
				automaticPost(t, h, addr, id, 1)
				waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
				automaticPost(t, h, addr, id, 2)
				waiting := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.SessionRecovery != nil })
				if waiting.SessionResetRequired || waiting.Status != "recovering" || waiting.SessionRecovery.Blocked || opens.Load() != 1 {
					t.Fatal("temporary input failure became a broken Session", waiting)
				}
				saved, err := h.store.loadScheduling(id)
				if err != nil || !saved.Enabled || saved.SessionRecovery == nil || !saved.SessionRecovery.NextAt.Equal(waiting.SessionRecovery.NextAt) {
					t.Fatal("retry deadline was not durable", saved, err)
				}
				if pause {
					controlMember(t, h, addr, id, "pause", UnreadChanged{MemberIdentity: id}, false, false)
					time.Sleep(time.Second)
					synctest.Wait()
					if s := inspectMember(t, h.ctx, addr, id); !s.Paused || s.Status == "recovering" || opens.Load() != 1 {
						t.Fatal("retry overrode explicit pause", s)
					}
					return
				}
				time.Sleep(time.Until(saved.SessionRecovery.NextAt))
				last := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
				if last.Error != "" || last.SessionRecovery != nil || opens.Load() != 2 {
					t.Fatal("temporary admission did not self recover", last)
				}
			})
		})
	}
}

func TestAutomaticRotationTemporaryLifecycleFailureContinuesSameOperation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, svc, addr, id, opens := automaticHarness(t, 1, nil)
		var calls atomic.Int32
		svc.beginPlatformMemberChange = func(context.Context, MemberIdentity) (func(), error) {
			if calls.Add(1) == 1 {
				return nil, context.DeadlineExceeded
			}
			return func() {}, nil
		}
		controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
		automaticPost(t, h, addr, id, 1)
		waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
		automaticPost(t, h, addr, id, 2)
		first := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.Rotation != nil && s.Rotation.Status == "failed" })
		if first.SessionRecovery == nil || first.Status != "recovering" {
			t.Fatal("temporary lifecycle failure did not retain recovery ownership", first)
		}
		time.Sleep(time.Until(first.SessionRecovery.NextAt))
		last := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
		if last.Error != "" || opens.Load() != 2 || last.Rotation.OperationID != first.Rotation.OperationID || last.Rotation.ToGeneration != first.Rotation.ToGeneration {
			t.Fatal("lifecycle retry replaced its original operation", first, last)
		}
	})
}

func TestSessionRecoveryOutageBackoffAndEventualSuccess(t *testing.T) {
	for _, failure := range []error{dsh.ErrRateLimited, dsh.ErrConnection, &dsh.HTTPStatusError{StatusCode: 503}} {
		t.Run(failure.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, _, addr, id, opens := recoveryHarnessWithFailures(t, func(attempt int32) error {
					if attempt <= 3 {
						return failure
					}
					return nil
				}, nil, 5*time.Minute)
				controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
				automaticPost(t, h, addr, id, 1)
				for attempt := uint64(1); attempt <= 3; attempt++ {
					s := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == attempt })
					if s.Status != "recovering" || s.SessionRecovery == nil || s.SessionRecovery.Attempts != int(attempt-1) {
						t.Fatal("outage lost automatic recovery", s)
					}
					saved, err := h.store.loadScheduling(id)
					if err != nil || !saved.Enabled || saved.SessionRecovery == nil || !saved.SessionRecovery.NextAt.Equal(s.SessionRecovery.NextAt) {
						t.Fatal(saved, err)
					}
					// Reconstruct scheduling from disk as on restart. A restart must
					// neither renew the allowance nor move the retry deadline sooner.
					member := &MemberAgent{Options: adapterOptions(h.store, id), Store: h.store,
						Backend: dshBackend{store: h.store}, SessionMaxTurns: 16, now: time.Now}
					if err := member.initializeScheduling(); err != nil || member.sessionRecovery.Attempts != saved.SessionRecovery.Attempts || !member.rotationNext.Equal(saved.SessionRecovery.NextAt) {
						t.Fatal("restart reset the retry schedule", member.sessionRecovery, err)
					}
					time.Sleep(time.Until(saved.SessionRecovery.NextAt) - time.Millisecond)
					synctest.Wait()
					if opens.Load() != int32(attempt) {
						t.Fatal("outage retried before cooldown", opens.Load(), attempt)
					}
					time.Sleep(time.Millisecond)
				}
				last := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 4 })
				saved, err := h.store.loadScheduling(id)
				if err != nil || last.Paused || last.SessionRecovery != nil || last.Error != "" || last.Agent.Turn.Status != "completed" || saved.SessionRecovery != nil || saved.RecoveryAttempted || opens.Load() != 4 {
					t.Fatal("provider recovery still required manual enable", last, saved, err)
				}
			})
		})
	}
}

func TestAutomaticRotationInvalidInputDoesNotAuthorizeSessionReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, svc, addr, id, opens := automaticHarness(t, 1, nil)
		var checks atomic.Int32
		svc.maintainInbox.Store(&inboxMaintenance{run: func(context.Context, MemberIdentity) error {
			checks.Add(1)
			return errors.Join(ErrInvalid, context.DeadlineExceeded)
		}})
		controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
		automaticPost(t, h, addr, id, 1)
		waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
		automaticPost(t, h, addr, id, 2)
		s := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.SessionRecovery != nil })
		if !s.Paused || !s.SessionRecovery.Blocked || s.SessionResetRequired || s.Status == "recovering" || s.Rotation != nil {
			t.Fatal("invalid input was treated as a broken Session", s)
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if checks.Load() != 1 || opens.Load() != 1 {
			t.Fatal("corruption was retried or replaced", checks.Load(), opens.Load())
		}
		saved, err := h.store.loadScheduling(id)
		if err != nil || !saved.Enabled || !saved.SessionRecovery.Blocked {
			t.Fatal("failure overwrote user intent", saved, err)
		}
	})
}

func TestSessionRecoveryRejectsInvalidPersistedRetryState(t *testing.T) {
	h := newRoomHarness(t, historyRoom(0, ""), nil)
	id := h.memberID("A", "generation-A")
	for _, retry := range []sessionRecovery{
		{Stage: "unknown", NextAt: time.Now()},
		{Stage: "backend", Attempts: -1, NextAt: time.Now()},
		{Stage: "backend", Attempts: 65, NextAt: time.Now()},
		{Stage: "backend"},
	} {
		if err := h.store.saveScheduling(memberScheduling{Version: schedulingVersion, MemberIdentity: id, SessionRecovery: &retry}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.store.loadScheduling(id); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid retry state accepted", retry, err)
		}
	}
	if recoveryDelay(64) != 15*time.Minute {
		t.Fatal("persistent outages must stay rate bounded")
	}
}

func TestSessionRecoveryCooldownSurvivesMemberRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, svc, addr, id, opens := recoveryHarnessWithFailures(t, func(int32) error { return dsh.ErrUnavailable }, nil, 5*time.Minute)
		controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
		automaticPost(t, h, addr, id, 1)
		before := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
		if err := addr.Stop(nil); err != nil {
			t.Fatal(err)
		}
		if exit, err := addr.Wait(h.ctx); err != nil || !backendCleanupSettled(exit.Reason) {
			t.Fatal("fixture did not settle", exit, err)
		}
		// The component substitute has no native journal. Record the fixture's
		// verified Stop/Wait settlement so the production startup gate can run.
		dir := filepath.Join(h.store.dir, "agents", id.MembershipID, before.SessionGeneration)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settled.json"), []byte(`{"version":1}`), 0600); err != nil {
			t.Fatal(err)
		}
		svc.endpoint, svc.cliPath, svc.maxTurns = "http://127.0.0.1:1", os.Args[0], 16
		svc.info.ServiceEpoch = "restarted-service"
		room, err := h.store.LoadRoom()
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.loadMembers(room); err != nil {
			t.Fatal(err)
		}
		member := svc.newMember(id, room.Members[0].Runner, svc.recovered[0].state).(*MemberAgent)
		member.Backend = dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
			opens.Add(1)
			return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) {
				return dsh.TurnResult{StopReason: "end_turn"}, nil
			}}, nil
		}}
		addr, err = h.spawn(member)
		if err != nil {
			t.Fatal(err)
		}
		svc.setTarget(id.MembershipID, addr)
		if err := svc.resumeMembers(h.ctx); err != nil {
			t.Fatal(err)
		}
		after := inspectMember(t, h.ctx, addr, id)
		if after.Status != "recovering" || after.SessionRecovery == nil || !after.SessionRecovery.NextAt.Equal(before.SessionRecovery.NextAt) || after.SessionRecovery.Attempts != before.SessionRecovery.Attempts {
			t.Fatal("member restart changed its retry deadline or attempt count", before, after)
		}
		time.Sleep(time.Until(after.SessionRecovery.NextAt) - time.Millisecond)
		synctest.Wait()
		if opens.Load() != 2 {
			t.Fatal("member restart retried early", opens.Load())
		}
		time.Sleep(time.Millisecond)
		last := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 3 })
		if last.Paused || last.Error != "" || last.SessionRecovery != nil || opens.Load() != 3 {
			t.Fatal("restarted member did not recover", last)
		}
	})
}

func TestAutomaticRotationRechecksUnifiedInputReadiness(t *testing.T) {
	h := newRoomHarness(t, historyRoom(0, ""), nil)
	id := h.memberID("A", "generation-A")
	m := &MemberAgent{Options: SessionOptions{MemberIdentity: id}, Backend: rotatingControlledBackend{},
		SessionMaxTurns: 1, turnsUsed: 1, enabled: true, now: time.Now, sessionGeneration: "session.json.dsh", currentGeneration: "session.json.dsh",
		Inputs: func(context.Context) (unifiedInputStatus, error) { return unifiedInputStatus{}, nil }, inputStatus: &unifiedInputStatus{Ready: true}}
	err := m.prepareRotation(ControlMember{MemberIdentity: id, automatic: true, FromGeneration: "session.json.dsh",
		State: UnreadChanged{MemberIdentity: id}, automaticInputs: &unifiedInputStatus{Ready: false}})
	if !errors.Is(err, ErrConflict) || m.rotationRecord != nil {
		t.Fatal("stale input readiness rotated an idle Session", err)
	}
}

func TestSessionRecoveryRetainsPermanentErrorsInsideTransientChains(t *testing.T) {
	for _, permanent := range []error{ErrInvalid, ErrIdentity, ErrCommitUnknown, dsh.ErrAuthentication, dsh.ErrQuotaExceeded, dsh.ErrCleanupPending} {
		failure := errors.Join(dsh.ErrUnavailable, permanent)
		if (dshBackend{}).recoverableFailure(failure) || recoverableDSHHistory(AgentSnapshot{Turn: &TurnRecord{Status: "failed", Error: failure.Error()}}) {
			t.Fatal("transient hint bypassed a permanent failure", failure)
		}
	}
}
