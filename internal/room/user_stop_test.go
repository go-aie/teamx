package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/inputstream"
)

func TestUserStopPersistsIntentAndPageOnlyEvent(t *testing.T) {
	for _, fail := range []error{nil, dsh.ErrUnavailable} {
		t.Run(map[bool]string{true: "healthy", false: "backend_failure"}[fail == nil], func(t *testing.T) {
			state := historyRoom(1, "request")
			state.IsDirect = true
			state.InputLog = &roomLogRoot{Version: 1}
			h := newRoomHarness(t, state, nil)
			id := h.memberID("A", "generation-A")
			entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			var runs atomic.Int32
			backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
				return &adapterComponent{run: func(ctx context.Context, _ string) (dsh.TurnResult, error) {
					if runs.Add(1) != 1 {
						return dsh.TurnResult{StopReason: "end_turn"}, nil
					}
					close(entered)
					<-ctx.Done()
					close(cancelled)
					<-release
					return dsh.TurnResult{StopReason: "cancelled"}, errors.Join(ctx.Err(), dsh.ErrCancelled, fail)
				}}, nil
			}}
			svc := &service{ctx: h.ctx, store: h.store, info: ServiceInfo{RoomIdentity: id.RoomIdentity}, room: h.addr,
				targets: map[string]actorx.Addr{}, dshProfiles: map[string]dshProfile{"fixture-ignore": {}}}
			addr, err := h.spawn(&MemberAgent{Options: adapterOptions(h.store, id), Store: h.store, Room: h.addr,
				Backend: backend, SessionMaxTurns: 16, RequestRecovery: svc.requestMemberRecovery})
			if err != nil {
				t.Fatal(err)
			}
			svc.setTarget(id.MembershipID, addr)
			input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
			controlMember(t, h, addr, id, "run", input, false, false)
			select {
			case <-entered:
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			active := inspectMember(t, h.ctx, addr, id)
			req := apiRequest{Caller: humanCaller, AgentID: id.AgentID, MembershipID: id.MembershipID, ExecutionID: active.ExecutionID}
			for range 2 {
				if _, err := svc.control(h.ctx, req, "cancel"); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-cancelled:
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
			saved, err := h.store.loadScheduling(id)
			if err != nil || saved.UserStop == nil || saved.UserStop.Turn != 1 || saved.UserStop.Published || saved.UserStop.Outcome != "" || !saved.Enabled {
				t.Fatal("Stop intent was not durable before settlement", saved, err)
			}
			room, err := h.store.LoadRoom()
			if err != nil || len(room.Messages) != 1 {
				t.Fatal("Stop event published before settlement", room, err)
			}
			unblock()
			stopped := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active })
			wantText, wantOutcome := "用户已停止当前执行", userStopConfirmed
			if fail != nil {
				wantText, wantOutcome = "用户要求停止当前执行（结果未确认）", userStopUnconfirmed
			}
			room, err = h.store.LoadRoom()
			if err != nil || len(room.Messages) != 2 || room.Messages[1].Text != wantText || room.Messages[1].Event.Kind != "user_stop" || len(room.Messages[1].Event.Targets) != 0 {
				t.Fatal("missing or duplicated page-only Stop result", room, err)
			}
			saved, err = h.store.loadScheduling(id)
			if err != nil || !saved.UserStop.Published || saved.UserStop.Outcome != wantOutcome || saved.UserStop.FinishedAt.Before(saved.UserStop.CreatedAt) {
				t.Fatal("Stop result was not durable", saved, err)
			}
			list := h.list(t)
			if list.Members[0].UnreadCount != 1 || list.Members[0].NewestUnreadSeq != 1 {
				t.Fatal("Stop event became pending work", list)
			}
			page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: agentCallerIdentity(id), Mode: ReadLatest, Latest: 20})
			if len(page.Messages) != 2 || page.Messages[1].Event == nil {
				t.Fatal("Agent history cannot see Stop", page)
			}
			if stopped.Agent.Turn.StopIntent != stopIntentUser || stopped.SessionRecovery != nil {
				t.Fatal("Stop was lost or scheduled automatic recovery", stopped)
			}
			time.Sleep(80 * time.Millisecond)
			if runs.Load() != 1 {
				t.Fatal("Stop replayed the old input", runs.Load())
			}
			if fail != nil {
				if !stopped.Paused || !stopped.SessionResetRequired {
					t.Fatal("Stop concealed backend failure", stopped)
				}
				return
			}
			automaticPost(t, h, addr, id, 3)
			next := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
			if next.Paused || next.Agent.Turn.StopIntent != stopIntentNone {
				t.Fatal("Stop leaked into new work", next)
			}
			if _, err := svc.control(h.ctx, req, "cancel"); !errors.Is(err, ErrConflict) {
				t.Fatal("stale Stop affected the next execution", err)
			}
		})
	}
}

func TestUserStopPublicationSurvivesLostReceiptAndRestart(t *testing.T) {
	for _, test := range []struct {
		name, outcome, memberName, text string
	}{
		{"confirmed", userStopConfirmed, "", "用户已停止当前执行"},
		{"unconfirmed", userStopUnconfirmed, "", "用户要求停止当前执行（结果未确认）"},
		{"named_confirmed", userStopConfirmed, "工作管家", "用户已停止「工作管家」当前执行"},
		{"named_unconfirmed", userStopUnconfirmed, "工作管家", "用户要求停止「工作管家」当前执行（结果未确认）"},
		{"legacy_receipt", userStopUnconfirmed, "", "用户要求停止当前执行（结果未确认）"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := historyRoom(0, "")
			state.InputLog = &roomLogRoot{Version: 1}
			h := newRoomHarness(t, state, nil)
			id := h.memberID("A", "generation-A")
			now := time.Now().UTC()
			r := &userStop{ExecutionID: 1, Turn: 1, Generation: "session.json.dsh", Actor: humanCaller.Author,
				Name: test.memberName, CreatedAt: now, Outcome: test.outcome, FinishedAt: now}
			scheduling := memberScheduling{Version: schedulingVersion, MemberIdentity: id, Enabled: true, Used: 1,
				SessionGeneration: r.Generation, UserStop: r}
			if err := h.store.saveScheduling(scheduling); err != nil {
				t.Fatal(err)
			}
			wantMessages := 1
			if test.name == "legacy_receipt" {
				operation := inputstream.Ref("user_stop_", id, r.ExecutionID, r.Generation)
				roomCall[PostReceipt](t, h, AppendRoomEvent{RoomIdentity: id.RoomIdentity,
					Event: RoomEvent{ID: operation, Kind: "user_stop", OperationID: operation, ResultVersion: 1,
						ResultRef: "execution/" + operation, Actor: r.Actor},
					Summary: "用户要求停止当前执行。", CreatedAt: now})
				wantMessages++
			}
			m := &MemberAgent{Options: adapterOptions(h.store, id), Room: h.addr, Store: h.store, PersistentScheduling: true,
				userStop: r, enabled: true, turnsUsed: 1, schedulingVersion: schedulingVersion, sessionGeneration: r.Generation, now: time.Now}
			if err := m.publishUserStop(h.ctx); err != nil {
				t.Fatal(err)
			}
			// Model a crash after Room publication but before the receipt checkpoint.
			if err := h.store.saveScheduling(scheduling); err != nil {
				t.Fatal(err)
			}
			restored, err := h.store.loadScheduling(id)
			if err != nil {
				t.Fatal(err)
			}
			m.userStop = restored.UserStop
			if err := m.publishUserStop(h.ctx); err != nil {
				t.Fatal(err)
			}
			room, err := h.store.LoadRoom()
			if err != nil || len(room.Messages) != wantMessages || room.Messages[wantMessages-1].Text != test.text || !room.Messages[wantMessages-1].CreatedAt.Equal(now) {
				t.Fatal("restart lost or duplicated Stop result", room, err)
			}
			restored, err = h.store.loadScheduling(id)
			if err != nil || !restored.UserStop.Published || restored.UserStop.Outcome != test.outcome {
				t.Fatal("publication receipt was not repaired", restored, err)
			}
		})
	}
}

func TestCancellationRecoveryDistinguishesUserStopAndPreservesGates(t *testing.T) {
	for _, failure := range []error{context.Canceled, dsh.ErrCancelled, errors.Join(context.Canceled, dsh.ErrUncertain)} {
		if !(dshBackend{}).recoverableFailure(failure) {
			t.Fatal("internal cancellation cannot recover", failure)
		}
	}
	for _, permanent := range []error{ErrInvalid, ErrCommitUnknown, dsh.ErrAuthentication, dsh.ErrCleanupPending, dsh.ErrLocked} {
		if (dshBackend{}).recoverableFailure(errors.Join(context.Canceled, permanent)) {
			t.Fatal("cancellation bypassed a recovery gate", permanent)
		}
	}
	for _, test := range []struct {
		name string
		stop string
		want bool
	}{
		{"legacy", "", false},
		{"internal", stopIntentNone, true},
		{"user", stopIntentUser, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			agent := AgentSnapshot{Turn: &TurnRecord{Status: "failed", StopIntent: test.stop,
				Error: "DSH member paused; inspect backend failure before continuing\ncontext canceled\nDSH turn cancelled"}}
			if got := recoverableDSHHistory(agent); got != test.want {
				t.Fatal("wrong startup cancellation policy", got)
			}
		})
	}
}

func TestInternalCancellationAutomaticallyRecovers(t *testing.T) {
	h, _, addr, id, opens := recoveryHarness(t, errors.Join(context.Canceled, dsh.ErrCancelled), false, nil)
	controlMember(t, h, addr, id, "run", UnreadChanged{MemberIdentity: id}, false, false)
	automaticPost(t, h, addr, id, 1)
	status := waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
	if status.Paused || status.SessionRecovery != nil || opens.Load() != 2 {
		t.Fatal("internal cancellation did not recover", status, opens.Load())
	}
}

func TestUserStopCheckpointWinsOverInterruptedWorkerOnStartup(t *testing.T) {
	state := historyRoom(1, "pending")
	state.InputLog = &roomLogRoot{Version: 1}
	h := newRoomHarness(t, state, nil)
	id := h.memberID("A", "generation-A")
	opts := adapterOptions(h.store, id)
	h.store.mu.Lock()
	err := h.store.save(opts.StatePath, sessionReference{Version: formatVersion, MemberIdentity: id, Directory: "session.json.dsh"}, maxPrivateFileBytes)
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
	if err := os.WriteFile(filepath.Join(dir, "settled.json"), []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	agent := AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: 1,
		Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: 1, OfferedUnreadSeq: 1, StopIntent: stopIntentNone}}
	if err := h.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	if err := h.store.saveScheduling(memberScheduling{Version: schedulingVersion, MemberIdentity: id, Enabled: true,
		Used: 1, SessionGeneration: "session.json.dsh", UserStop: &userStop{ExecutionID: 1, Turn: 1,
			Generation: "session.json.dsh", Actor: humanCaller.Author, CreatedAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	svc := &service{ctx: h.ctx, store: h.store, room: h.addr, info: ServiceInfo{RoomIdentity: h.id},
		endpoint: "http://127.0.0.1:1", cliPath: os.Args[0], dshProfiles: map[string]dshProfile{"fixture-ignore": {}}, targets: map[string]actorx.Addr{}}
	room, _ := h.store.LoadRoom()
	if err := svc.loadMembers(room); err != nil {
		t.Fatal(err)
	}
	r := svc.recovered[0]
	if r.recovery || r.state.Turn.StopIntent != stopIntentUser {
		t.Fatal("startup forgot durable Stop intent", r)
	}
	var opens atomic.Int32
	member := svc.newMember(id, room.Members[0].Runner, r.state).(*MemberAgent)
	member.Backend = dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		opens.Add(1)
		return nil, errors.New("stopped execution must not reopen")
	}}
	addr, err := h.spawn(member)
	if err != nil {
		t.Fatal(err)
	}
	svc.setTarget(id.MembershipID, addr)
	if err := svc.resumeMembers(h.ctx); err != nil {
		t.Fatal(err)
	}
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool {
		saved, err := h.store.loadScheduling(id)
		return err == nil && saved.UserStop.Published
	})
	room, err = h.store.LoadRoom()
	if err != nil || len(room.Messages) != 2 || room.Messages[1].Event.Kind != "user_stop" || room.Messages[1].Text != "用户要求停止当前执行（结果未确认）" || opens.Load() != 0 {
		t.Fatal("startup did not publish the pending event without replay", room, opens.Load(), err)
	}
}
