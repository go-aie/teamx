package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform"
)

func recoveryHarness(t *testing.T, fail error, always bool, closeFn func(context.Context) error) (*roomHarness, *service, actorx.Addr, MemberIdentity, *atomic.Int32) {
	t.Helper()
	var runs atomic.Int32
	h := newRoomHarness(t, historyRoom(0, ""), nil)
	id := h.memberID("A", "generation-A")
	opens := &atomic.Int32{}
	list := roomCall[MemberList](t, h, ListMembers{h.id})
	svc := &service{ctx: h.ctx, store: h.store, info: ServiceInfo{RoomIdentity: h.id}, room: h.addr,
		targets: map[string]actorx.Addr{}, dshProfiles: map[string]dshProfile{list.Members[0].Runner.Profile: {}}}
	backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		opens.Add(1)
		return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) {
			if runs.Add(1) == 1 || always {
				return dsh.TurnResult{}, fail
			}
			return dsh.TurnResult{StopReason: "end_turn"}, nil
		}, close: closeFn}, nil
	}}
	addr, err := h.spawn(&MemberAgent{Options: adapterOptions(h.store, id), Store: h.store, Room: h.addr, Backend: backend,
		SessionMaxTurns: 16, RequestRotation: svc.requestMemberRotation, RequestRecovery: svc.requestMemberRecovery})
	if err != nil {
		t.Fatal(err)
	}
	svc.setTarget(id.MembershipID, addr)
	return h, svc, addr, id, opens
}

func TestAutomaticRecoveryRestoresEnabledMember(t *testing.T) {
	h, _, addr, id, opens := recoveryHarness(t, dsh.ErrUnavailable, false, nil)
	controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
	automaticPost(t, h, addr, id, 1)
	recovered := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
	if recovered.Paused || recovered.SessionResetRequired || recovered.Agent.Turn.Status != "completed" || opens.Load() != 2 || recovered.Rotation.Reason != "backend_recovery" {
		t.Fatal("member did not automatically recover", recovered, opens.Load())
	}
	saved, err := h.store.loadScheduling(id)
	if err != nil || !saved.Enabled || saved.RecoveryAttempted || saved.Used != 2 {
		t.Fatal("lost intent or recovery allowance", saved, err)
	}
	room, _ := h.store.LoadRoom()
	if room.LatestSeq != 1 || room.Members[0].ReadThrough != 0 {
		t.Fatal("recovery changed history or read cursor", room)
	}
}

func TestAutomaticRecoveryIsBoundedAndSurvivesRestart(t *testing.T) {
	h, _, addr, id, opens := recoveryHarness(t, dsh.ErrUnavailable, true, nil)
	controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
	automaticPost(t, h, addr, id, 1)
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
	time.Sleep(1200 * time.Millisecond)
	s := inspectMember(t, h.ctx, addr, id)
	saved, err := h.store.loadScheduling(id)
	if err != nil || !s.Paused || opens.Load() != 2 || !saved.Enabled || !saved.RecoveryAttempted {
		t.Fatal("persistent fault retried indefinitely or lost user intent", s, saved, err)
	}
	r := recoveredMember{id: id, automatic: true, recovery: true, blocked: "backend unavailable"}
	if err := (&service{store: h.store, maxTurns: 16}).restoreScheduling(&r, *s.Agent); err != nil || r.recovery || r.resume {
		t.Fatal("restart renewed automatic recovery", r, err)
	}
}

func TestAutomaticRecoveryHonorsPauseBeforeAdmission(t *testing.T) {
	h, svc, addr, id, opens := recoveryHarness(t, dsh.ErrUnavailable, false, nil)
	controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
	automaticPost(t, h, addr, id, 1)
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	if _, err := svc.control(h.ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "pause"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	s := inspectMember(t, h.ctx, addr, id)
	saved, err := h.store.loadScheduling(id)
	if err != nil || !s.Paused || saved.Enabled || s.Rotation != nil || opens.Load() != 1 {
		t.Fatal("fault recovery ignored explicit pause", s, saved, err)
	}
	// A later explicit enable must use one rotation, without an extra stale
	// automatic recovery request racing the manual operation.
	if _, err := svc.control(h.ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "enable"); err != nil {
		t.Fatal(err)
	}
	s = waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
	if s.Paused || opens.Load() != 2 || s.Rotation.Reason != "manual" {
		t.Fatal("explicit enable duplicated recovery", s, opens.Load())
	}
}

func TestAutomaticRecoveryPauseWinsDuringCleanup(t *testing.T) {
	closing, release := make(chan struct{}, 1), make(chan struct{})
	var closed atomic.Int32
	h, svc, addr, id, opens := recoveryHarness(t, dsh.ErrModelUnavailable, false, func(context.Context) error {
		if closed.Add(1) == 1 {
			closing <- struct{}{}
			<-release
		}
		return nil
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
	automaticPost(t, h, addr, id, 1)
	select {
	case <-closing:
	case <-time.After(5 * time.Second):
		t.Fatal("automatic recovery did not close old Session")
	}
	if _, err := svc.control(h.ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "pause"); err != nil {
		t.Fatal(err)
	}
	close(release)
	s := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.Rotation != nil && s.Rotation.Status == "completed" })
	saved, err := h.store.loadScheduling(id)
	if err != nil || !s.Paused || saved.Enabled || opens.Load() != 1 || s.Agent.TurnNumber != 1 {
		t.Fatal("recovery callback overwrote manual pause", s, saved, err)
	}
}

func TestAutomaticRecoveryPermanentErrorsStayPaused(t *testing.T) {
	for _, fail := range []error{dsh.ErrAuthentication, dsh.ErrModelNotFound, dsh.ErrRateLimited, &dsh.HTTPStatusError{StatusCode: 403}} {
		t.Run(fail.Error(), func(t *testing.T) {
			h, _, addr, id, opens := recoveryHarness(t, errors.Join(dsh.ErrUncertain, fail), true, nil)
			controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
			automaticPost(t, h, addr, id, 1)
			waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
			time.Sleep(1100 * time.Millisecond)
			s := inspectMember(t, h.ctx, addr, id)
			if !s.Paused || opens.Load() != 1 || s.Rotation != nil {
				t.Fatal("permanent provider failure automatically retried", s)
			}
		})
	}
}

func TestAutomaticRecoveryRejectsUnknownActions(t *testing.T) {
	f := referenceExecuteTest(t, "discussion")
	f.result = func(int) platform.TargetActionResult { return f.outcome("unknown", "never") }
	if _, err := f.run(); err != nil {
		t.Fatal(err)
	}
	if err := referenceTestService(f.h).checkSessionRecovery(f.h.id); err == nil || !strings.Contains(err.Error(), "action outcomes require inspection") {
		t.Fatal("unknown action permitted automatic continuation", err)
	}
}

func TestAutomaticRecoveryRejectsUnsettledOwnedSession(t *testing.T) {
	h, _, addr, id, opens := recoveryHarness(t, dsh.ErrModelUnavailable, false, func(context.Context) error {
		return errors.New("old Session settlement could not be verified")
	})
	controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
	automaticPost(t, h, addr, id, 1)
	s := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return s.Rotation != nil && s.Rotation.Status == "failed" })
	if opens.Load() != 1 || !s.SessionResetRequired || s.SessionGeneration != "session.json.dsh" || s.Agent.TurnNumber != 1 {
		t.Fatal("unsettled owned Session was replaced", s)
	}
}

func TestAutomaticRecoveryRestoresOnlyEnabledSettledSessions(t *testing.T) {
	for _, mode := range []string{"enabled", "paused", "unsettled", "already-attempted", "authentication"} {
		t.Run(mode, func(t *testing.T) {
			h := newRoomHarness(t, historyRoom(1, "pending"), nil)
			id := h.memberID("A", "generation-A")
			opts := adapterOptions(h.store, id)
			h.store.mu.Lock()
			err := h.store.save(opts.StatePath, dshReference{Version: formatVersion, MemberIdentity: id, Directory: "session.json.dsh"}, maxPrivateFileBytes)
			h.store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			dir := dshStateDir(opts.StatePath)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "backend.json"), []byte(`{"version":1,"sessionId":"old","configSHA256":"test"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if mode != "unsettled" {
				if err := os.WriteFile(filepath.Join(dir, "settled.json"), []byte(`{"version":1}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			failure := dsh.ErrUnavailable.Error()
			if mode == "authentication" {
				failure += "\n" + dsh.ErrAuthentication.Error()
			}
			agent := AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: 1, Turn: &TurnRecord{Number: 1, Status: "failed", SnapshotSeq: 1, OfferedUnreadSeq: 1, Error: failure}}
			if err := h.store.SaveAgent(agent); err != nil {
				t.Fatal(err)
			}
			if err := h.store.saveScheduling(memberScheduling{Version: schedulingVersion, MemberIdentity: id, Enabled: mode != "paused", Used: 1, SessionGeneration: "session.json.dsh", RecoveryAttempted: mode == "already-attempted"}); err != nil {
				t.Fatal(err)
			}
			room, _ := h.store.LoadRoom()
			svc := &service{ctx: h.ctx, store: h.store, info: ServiceInfo{RoomIdentity: h.id, ServiceEpoch: "test-service"}, room: h.addr, endpoint: "http://127.0.0.1:1", cliPath: os.Args[0], maxTurns: 16,
				targets: map[string]actorx.Addr{}, dshProfiles: map[string]dshProfile{room.Members[0].Runner.Profile: {}}}
			if err := svc.loadMembers(room); err != nil {
				t.Fatal(err)
			}
			member := svc.newMember(id, room.Members[0].Runner, svc.recovered[0].state).(*MemberAgent)
			var opens atomic.Int32
			member.Backend = dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
				opens.Add(1)
				return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) {
					return dsh.TurnResult{StopReason: "end_turn"}, nil
				}}, nil
			}}
			addr, err := h.spawn(member)
			if err != nil {
				t.Fatal(err)
			}
			svc.setTarget(id.MembershipID, addr)
			if opens.Load() != 0 {
				t.Fatal("execution before readiness")
			}
			if err := svc.resumeMembers(h.ctx); err != nil {
				t.Fatal(err)
			}
			if mode == "enabled" {
				s := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool {
					return !s.Active && s.Agent.TurnNumber == 2
				})
				if s.Paused || s.SessionResetRequired || s.Rotation.Status != "completed" || opens.Load() != 1 {
					t.Fatal(s)
				}
			} else {
				s := inspectMember(t, h.ctx, addr, id)
				if !s.Paused || s.Rotation != nil {
					t.Fatal("paused or permanent fault recovered", s)
				}
			}
		})
	}
}
