package room

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Barriers exercise the production adapter and scheduler; these controlled
// components are lifecycle evidence, not proof of real model collaboration.
func TestDSHMembersQueueMergeAndIsolateFailure(t *testing.T) {
	state := historyRoom(1, "body")
	state.Members = append(state.Members, MemberState{AgentID: "B", MembershipID: "generation-B", Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}})
	h := newRoomHarness(t, state, nil)
	var targets [2]actorx.Addr
	entered := [2]chan TurnRecord{make(chan TurnRecord, 4), make(chan TurnRecord, 4)}
	finish := [2]chan error{make(chan error, 4), make(chan error, 4)}
	ids := [2]MemberIdentity{h.memberID("A", "generation-A"), h.memberID("B", "generation-B")}
	for i, id := range ids {
		backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
			return &adapterComponent{run: func(ctx context.Context, text string) (dsh.TurnResult, error) {
				agent, err := h.store.LoadAgent(id)
				if err != nil || agent.Turn == nil {
					return dsh.TurnResult{}, fmt.Errorf("missing turn snapshot: %v", err)
				}
				entered[i] <- *agent.Turn
				select {
				case err := <-finish[i]:
					return dsh.TurnResult{StopReason: "end_turn"}, err
				case <-ctx.Done():
					return dsh.TurnResult{}, ctx.Err()
				}
			}}, nil
		}}
		var err error
		targets[i], err = h.spawn(&MemberAgent{Options: adapterOptions(h.store, id), Store: h.store, Room: h.addr, Backend: backend})
		if err != nil {
			t.Fatal(err)
		}
		roomCall[MemberInfo](t, h, AttachMember{MemberIdentity: id, Target: targets[i]})
		controlMember(t, h, targets[i], id, "run", UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}, false, false)
		if i == 0 {
			select {
			case <-entered[i]:
			case <-h.ctx.Done():
				t.Fatal(h.ctx.Err())
			}
		}
	}
	waitMember(t, h.ctx, targets[1], ids[1], func(s MemberStatus) bool { return s.Status == "waiting" })
	for _, key := range []string{"burst-1", "burst-2", "burst-3", "burst-4", "burst-5"} {
		roomCall[PostReceipt](t, h, h.post(humanCaller, key, "new body"))
		h.idle(t)
	}
	waitMember(t, h.ctx, targets[0], ids[0], func(s MemberStatus) bool { return s.Active && s.Notification != nil && s.Notification.LatestSeq == 6 })
	finish[0] <- nil
	select {
	case input := <-entered[1]:
		if input.SnapshotSeq != 6 || input.OfferedUnreadSeq != 6 {
			t.Fatal(input)
		}
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	finish[1] <- nil
	waitMember(t, h.ctx, targets[1], ids[1], func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	controlMember(t, h, targets[1], ids[1], "pause", UnreadChanged{}, false, false)
	select {
	case input := <-entered[0]:
		if input.SnapshotSeq != 6 || input.OfferedUnreadSeq != 6 {
			t.Fatal(input)
		}
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	finish[0] <- &dsh.StopError{Reason: "max_tokens"}
	a := waitMember(t, h.ctx, targets[0], ids[0], func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
	if !a.Paused || a.Agent.Turn.Status != "failed" {
		t.Fatal(a)
	}
	controlMember(t, h, targets[1], ids[1], "run", UnreadChanged{MemberIdentity: ids[1], Revision: 6, LatestSeq: 6, NewestUnreadSeq: 6, UnreadCount: 6}, true, false)
	select {
	case <-entered[1]:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	finish[1] <- nil
	waitMember(t, h.ctx, targets[1], ids[1], func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
	roomCall[PostReceipt](t, h, h.post(humanCaller, "after-failure", "still available"))
	h.idle(t)
	select {
	case <-entered[1]:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	finish[1] <- nil
	waitMember(t, h.ctx, targets[1], ids[1], func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 3 })
	a = inspectMember(t, h.ctx, targets[0], ids[0])
	if !a.Paused || a.Active || a.Agent.TurnNumber != 2 {
		t.Fatal("failed member was replayed", a)
	}
	select {
	case input := <-entered[0]:
		t.Fatal("more than one merged follow-up", input)
	default:
	}
}

func TestDSHRecoveryAdmitsOnlyStoppedOriginalSession(t *testing.T) {
	t.Setenv("TEAMX_RECOVERY_TEST_KEY", "offline-test-only")
	for _, turn := range []string{"completed", "failed", "interrupted", "running"} {
		t.Run(turn, func(t *testing.T) {
			dir, id := recoveryFixture(t)
			store := openTestStore(t, dir)
			state, _ := store.LoadRoom()
			state.Members[0].Runner = RunnerConfig{Kind: "dsh", Profile: "reviewer"}
			if err := store.SaveRoom(state); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: 3, Turn: &TurnRecord{Number: 1, Status: turn, SnapshotSeq: 3, OfferedUnreadSeq: 3}}); err != nil {
				t.Fatal(err)
			}
			opts := adapterOptions(store, id)
			store.mu.Lock()
			err := store.save(opts.StatePath, dshReference{Version: formatVersion, MemberIdentity: id, Directory: "session.json.dsh"}, maxPrivateFileBytes)
			store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(dshStateDir(opts.StatePath), 0700); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(map[string]any{"version": 1, "sessionId": "original", "configSHA256": "test", "stopped": true})
			if err := os.WriteFile(filepath.Join(dshStateDir(opts.StatePath), "backend.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := store.saveScheduling(memberScheduling{Version: formatVersion, MemberIdentity: id, Enabled: true, Used: 1}); err != nil {
				t.Fatal(err)
			}
			_ = store.Close()
			config := recoveryConfig()
			config.dshProfiles = map[string]dshProfile{"reviewer": {Model: dsh.ModelConfig{APIKeyEnv: "TEAMX_RECOVERY_TEST_KEY"}}}
			svc, err := newService(dir, id.RoomID, config)
			if err != nil {
				t.Fatal(err)
			}
			defer closeService(t, svc)
			r := svc.recovered[0]
			if r.blocked != "" || !r.backendResume || r.resume != (turn != "failed") {
				t.Fatal(r)
			}
			status := inspectMember(t, context.Background(), svc.target(id.MembershipID), id)
			if !status.Paused || status.Active {
				t.Fatal("executed before readiness", status)
			}
			if turn == "running" && status.Agent.Turn.Status != "interrupted" {
				t.Fatal(status)
			}
			// Enabling scheduling must report idle while retaining the last
			// attempt. Already offered input must not launch or replay a turn.
			resumed, err := svc.control(context.Background(), apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run")
			if err != nil || resumed.Status != "idle" || resumed.Paused || resumed.Active || resumed.SessionTurns.Total != 1 {
				t.Fatal("resumed member retained historical execution status", resumed, err)
			}
			list, err := svc.members(context.Background())
			if err != nil || len(list.Members) != 1 || list.Members[0].Status != "idle" {
				t.Fatal("member list did not expose current scheduling status", list, err)
			}
			checkpoint, err := svc.store.LoadAgent(id)
			if err != nil || checkpoint.Turn.Status != status.Agent.Turn.Status || checkpoint.TurnNumber != 1 {
				t.Fatal("enabling scheduling changed historical attempt", checkpoint, err)
			}
		})
	}
}
