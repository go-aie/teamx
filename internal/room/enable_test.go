package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Exercise the existing operator endpoint, including its authorization boundary.
func enableHTTP(t *testing.T, h *roomHarness, target actorx.Addr, id MemberIdentity) MemberStatus {
	t.Helper()
	s := &service{info: ServiceInfo{RoomIdentity: h.id}, room: h.addr,
		requests: make(chan struct{}, 4), targets: map[string]actorx.Addr{id.MembershipID: target}}
	request := apiRequest{ServiceInfo: s.info, Caller: Caller{Author: Author{Kind: "agent", ID: id.AgentID}, MembershipID: id.MembershipID}, AgentID: id.AgentID, MembershipID: id.MembershipID}
	for _, operator := range []bool{false, true} {
		if operator {
			request.Caller = humanCaller
		}
		body, _ := json.Marshal(request)
		response := httptest.NewRecorder()
		s.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/member/run", bytes.NewReader(body)))
		if !operator {
			if response.Code != http.StatusForbidden {
				t.Fatal("agent renewed its own budget", response.Code, response.Body.String())
			}
			continue
		}
		if response.Code != http.StatusOK {
			t.Fatal(response.Code, response.Body.String())
		}
		var result MemberStatus
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	panic("unreachable")
}

func TestEnableReplenishesBudgetAndPersistsAcrossRecovery(t *testing.T) {
	b := newControlledBackend()
	h, target, id := memberHarness(t, b, nil, func(m *MemberAgent) { m.MaxTurns = 1; m.PersistentScheduling = true })
	input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, target, id, "run", input, false, false)
	awaitTurn(t, h, b)
	b.finish <- errors.New("failed attempt")
	waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return s.Status == "budget_exhausted" })
	enableHTTP(t, h, target, id)
	awaitTurn(t, h, b) // Failed unread input is reoffered by the same operation.
	if _, err := target.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "enable", State: input}); !errors.Is(err, ErrBusy) {
		t.Fatal("enabled an active turn", err)
	}
	b.finish <- nil
	settled := waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
	if settled.TurnBudget.Used != 2 || settled.TurnBudget.Limit != 2 || b.opens.Load() != 1 {
		t.Fatal("budget or healthy context lost", settled, b.opens.Load())
	}
	if err := target.Stop(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Wait(h.ctx); err != nil {
		t.Fatal(err)
	}
	// Reload the persisted scheduler state, then create a replacement Actor.
	s := &service{store: h.store, maxTurns: 1}
	recovered := recoveredMember{id: id, backendResume: true}
	if err := s.restoreScheduling(&recovered, *settled.Agent); err != nil {
		t.Fatal(err)
	}
	if recovered.limit != 2 || recovered.used != 2 || recovered.resume {
		t.Fatal("recovery renewed or lost budget", recovered)
	}
	target, err := h.spawn(&MemberAgent{Options: SessionOptions{MemberIdentity: id}, Backend: b, Store: h.store, Room: h.addr,
		MaxTurns: 1, Restored: settled.Agent, PersistentScheduling: true, RestoredTurnsUsed: recovered.used, RestoredTurnLimit: recovered.limit})
	if err != nil {
		t.Fatal(err)
	}
	status := inspectMember(t, h.ctx, target, id)
	if status.Status != "budget_exhausted" || status.TurnBudget.Limit != 2 || status.TurnBudget.Used != 2 {
		t.Fatal(status)
	}
}

func TestEnableResetsFailedSessionAndReplenishesTogether(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(map[bool]string{false: "session", true: "session-and-budget"}[exhausted], func(t *testing.T) {
			h := newRoomHarness(t, historyRoom(1, "pending request"), nil)
			id := h.memberID("A", "generation-A")
			opens := 0 // Component work is serialized by the scheduler.
			backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
				opens++
				failed := opens == 1
				return &adapterComponent{run: func(context.Context, string) (dsh.TurnResult, error) {
					if failed {
						return dsh.TurnResult{}, errors.Join(dsh.ErrUncertain, dsh.ErrRateLimited)
					}
					return dsh.TurnResult{StopReason: "end_turn"}, nil
				}, close: func(context.Context) error {
					if failed {
						return &dsh.ClosedError{Err: dsh.ErrRecoveryUnsupported}
					}
					return nil
				}}, nil
			}}
			limit := uint64(3)
			if exhausted {
				limit = 1
			}
			target, err := h.spawn(&MemberAgent{Options: adapterOptions(h.store, id), Store: h.store, Room: h.addr, Backend: backend, MaxTurns: limit, PersistentScheduling: true})
			if err != nil {
				t.Fatal(err)
			}
			input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
			controlMember(t, h, target, id, "run", input, false, false)
			failed := waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
			if !failed.SessionResetRequired {
				t.Fatal("budget hid reset requirement", failed)
			}
			enableHTTP(t, h, target, id)
			recovered := waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
			expected := limit
			if exhausted {
				expected++
			}
			if recovered.Agent.Turn.Status != "completed" || recovered.SessionResetRequired || recovered.TurnBudget.Limit != expected || recovered.TurnBudget.Used != 2 || opens != 2 {
				t.Fatal("enable did not restore session and budget", recovered, opens)
			}
		})
	}
}

func TestEnableUnsettledSessionDoesNotGrantTurns(t *testing.T) {
	h := newRoomHarness(t, historyRoom(1, "pending"), nil)
	id := h.memberID("A", "generation-A")
	opts := adapterOptions(h.store, id)
	ref := dshReference{Version: formatVersion, MemberIdentity: id, Directory: "session.json.dsh"}
	h.store.mu.Lock()
	err := h.store.save(opts.StatePath, ref, maxPrivateFileBytes)
	h.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(opts.StatePath)
	target, err := h.spawn(&MemberAgent{Options: opts, Backend: dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		t.Error("launched before settlement")
		return nil, errors.New("forbidden")
	}},
		Store: h.store, Room: h.addr, MaxTurns: 1, RestoredTurnsUsed: 1, PersistentScheduling: true, BackendBlocked: "missing settlement"})
	if err != nil {
		t.Fatal(err)
	}
	enableHTTP(t, h, target, id)
	failed := waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return s.Rotation != nil && s.Rotation.Status == "failed" })
	after, _ := os.ReadFile(opts.StatePath)
	if !failed.Paused || !failed.SessionResetRequired || failed.TurnBudget.Limit != 1 || failed.TurnBudget.Used != 1 || !bytes.Equal(before, after) {
		t.Fatal("unsafe replacement or budget grant", failed)
	}
}

func TestEnableBudgetCommitFailureStaysPaused(t *testing.T) {
	b := newControlledBackend()
	h, target, id := memberHarness(t, b, nil, func(m *MemberAgent) { m.MaxTurns = 1; m.RestoredTurnsUsed = 1; m.PersistentScheduling = true })
	h.store.mu.Lock()
	original := h.store.ops.createTemp
	h.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("disk unavailable") }
	h.store.mu.Unlock()
	t.Cleanup(func() { h.store.mu.Lock(); h.store.ops.createTemp = original; h.store.mu.Unlock() })
	input := UnreadChanged{MemberIdentity: id, Revision: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	if _, err := target.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "enable", State: input}); err == nil {
		t.Fatal("ignored failed commit")
	}
	status := inspectMember(t, h.ctx, target, id)
	if !status.Paused || status.TurnBudget.Limit != 1 || status.TurnBudget.Used != 1 || b.opens.Load() != 0 {
		t.Fatal(status)
	}
}

func TestEnableRestoresLegacySchedulingLimit(t *testing.T) {
	h := newRoomHarness(t, historyRoom(1, "body"), nil)
	id := h.memberID("A", "generation-A")
	// The old collaboration.json schema has no limit field.
	if err := h.store.saveScheduling(memberScheduling{Version: formatVersion, MemberIdentity: id, Enabled: true, Used: 16}); err != nil {
		t.Fatal(err)
	}
	svc := &service{store: h.store, maxTurns: 16}
	recovered := recoveredMember{id: id, backendResume: true}
	agent := AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 16, Turn: &TurnRecord{Number: 16, Status: "completed"}}
	if err := svc.restoreScheduling(&recovered, agent); err != nil {
		t.Fatal(err)
	}
	if recovered.limit != 16 || recovered.used != 16 || recovered.resume {
		t.Fatal("legacy budget was renewed", recovered)
	}
}
