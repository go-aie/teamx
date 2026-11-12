package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

func TestCancelCurrentTurnKeepsDSHSchedulingAndSession(t *testing.T) {
	for _, failure := range []error{nil, dsh.ErrUnavailable} {
		t.Run(map[bool]string{true: "healthy", false: "cleanup_failure"}[failure == nil], func(t *testing.T) {
			h := newRoomHarness(t, historyRoom(1, "body"), nil)
			id := h.memberID("A", "generation-A")
			entered, canceled := make(chan struct{}, 4), make(chan struct{}, 1)
			settle, finish := make(chan struct{}), make(chan error, 1)
			var once sync.Once
			unblock := func() { once.Do(func() { close(settle) }) }
			t.Cleanup(unblock)
			var opens atomic.Int32
			backend := dshBackend{store: h.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
				opens.Add(1)
				return &adapterComponent{run: func(ctx context.Context, _ string) (dsh.TurnResult, error) {
					entered <- struct{}{}
					select {
					case err := <-finish:
						return dsh.TurnResult{StopReason: "end_turn"}, err
					case <-ctx.Done():
						canceled <- struct{}{}
						<-settle
						return dsh.TurnResult{StopReason: "cancelled"}, errors.Join(ctx.Err(), dsh.ErrCancelled, failure)
					}
				}}, nil
			}}
			target, err := h.spawn(&MemberAgent{Options: adapterOptions(h.store, id), Store: h.store, Room: h.addr, Backend: backend,
				PersistentScheduling: true})
			if err != nil {
				t.Fatal(err)
			}
			input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
			controlMember(t, h, target, id, "run", input, false, false)
			wait := func(ch <-chan struct{}) {
				t.Helper()
				select {
				case <-ch:
				case <-h.ctx.Done():
					t.Fatal(h.ctx.Err())
				}
			}
			wait(entered)
			active := inspectMember(t, h.ctx, target, id)
			request := ControlMember{MemberIdentity: id, Action: "cancel", ExecutionID: active.ExecutionID}
			for range 2 { // Duplicate cancellation targets the same execution.
				stopping, err := target.Call[MemberStatus](h.ctx, request)
				if err != nil || !stopping.Active || !stopping.Stopping || stopping.Paused {
					t.Fatal(stopping, err)
				}
			}
			wait(canceled)
			if status := inspectMember(t, h.ctx, target, id); !status.Active || !status.Stopping {
				t.Fatal("cancel returned before settlement", status)
			}
			unblock()
			stopped := waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return !s.Active })
			if stopped.Agent.Turn.Status != "interrupted" || stopped.Stopping || stopped.ExecutionID != 0 {
				t.Fatal(stopped)
			}
			if failure != nil {
				if !stopped.Paused || !stopped.SessionResetRequired || stopped.Error == "" {
					t.Fatal("cancellation hid backend failure", stopped)
				}
				return
			}
			if stopped.Paused || stopped.Error != "" || stopped.Status != "idle" {
				t.Fatal("healthy cancellation paused scheduling", stopped)
			}
			// The maintenance timer and duplicate input must not replay the turn.
			if err := target.Send(h.ctx, input); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
				t.Fatal("cancelled input was replayed")
			case <-time.After(80 * time.Millisecond):
			}
			appendMemberInput(t, h, 2, 2)
			input.Revision, input.LatestSeq, input.NewestUnreadSeq = 2, 2, 2
			if err := target.Send(h.ctx, input); err != nil {
				t.Fatal(err)
			}
			wait(entered)
			if status := inspectMember(t, h.ctx, target, id); status.ExecutionID == active.ExecutionID || status.Paused {
				t.Fatal("new message did not start a fresh enabled execution", status)
			}
			if _, err := target.Call[MemberStatus](h.ctx, request); !errors.Is(err, ErrConflict) {
				t.Fatal("stale stop was accepted against the next turn", err)
			}
			finish <- nil
			waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
			if opens.Load() != 1 {
				t.Fatal("healthy Session was replaced", opens.Load())
			}
		})
	}
}

func TestCancelCurrentTurnPreservesExplicitPause(t *testing.T) {
	b := newControlledBackend()
	b.settle = make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(b.settle) }) }
	h, target, id := memberHarness(t, b, nil)
	t.Cleanup(unblock)
	input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, target, id, "run", input, false, false)
	awaitTurn(t, h, b)
	active := inspectMember(t, h.ctx, target, id)
	if _, err := target.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "cancel", ExecutionID: active.ExecutionID}); err != nil {
		t.Fatal(err)
	}
	controlMember(t, h, target, id, "pause", input, false, false)
	unblock()
	if stopped := waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return !s.Active }); !stopped.Paused {
		t.Fatal("stop undid a separate pause", stopped)
	}
}

func TestCancelCurrentTurnWaitsForSettlementBeforeCorrectedInput(t *testing.T) {
	b := newControlledBackend()
	b.settle = make(chan struct{})
	h, target, id := memberHarness(t, b, nil)
	var once sync.Once
	unblock := func() { once.Do(func() { close(b.settle) }) }
	t.Cleanup(unblock)
	input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, target, id, "run", input, false, false)
	awaitTurn(t, h, b)
	active := inspectMember(t, h.ctx, target, id)
	if _, err := target.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "cancel", ExecutionID: active.ExecutionID}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.canceled:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	appendMemberInput(t, h, 2, 2)
	input.Revision, input.LatestSeq, input.NewestUnreadSeq = 2, 2, 2
	if err := target.Send(h.ctx, input); err != nil {
		t.Fatal(err)
	}
	if stopping := inspectMember(t, h.ctx, target, id); !stopping.Active || !stopping.Stopping || stopping.ExecutionID != active.ExecutionID {
		t.Fatal("new input bypassed the old execution settlement", stopping)
	}
	unblock()
	if corrected := awaitTurn(t, h, b); corrected.NewestUnreadSeq != 2 {
		t.Fatal(corrected)
	}
	if next := inspectMember(t, h.ctx, target, id); next.Paused || next.ExecutionID == active.ExecutionID {
		t.Fatal("corrected input did not continue automatically", next)
	}
	b.finish <- nil
	waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
}

func TestCancelEndpointBindsOperatorMemberAndExecution(t *testing.T) {
	b := newControlledBackend()
	h, target, id := memberHarness(t, b, nil)
	input := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, target, id, "run", input, false, false)
	awaitTurn(t, h, b)
	active := inspectMember(t, h.ctx, target, id)
	s := &service{info: ServiceInfo{RoomIdentity: h.id}, room: h.addr, requests: make(chan struct{}, 4),
		targets: map[string]actorx.Addr{id.MembershipID: target}}
	valid := apiRequest{ServiceInfo: s.info, Caller: humanCaller, AgentID: id.AgentID, MembershipID: id.MembershipID, ExecutionID: active.ExecutionID}
	for _, check := range []struct {
		name string
		edit func(*apiRequest)
		code int
	}{
		{"agent", func(r *apiRequest) {
			r.Caller = Caller{Author: Author{Kind: "agent", ID: id.AgentID}, MembershipID: id.MembershipID}
		}, http.StatusForbidden},
		{"missing_execution", func(r *apiRequest) { r.ExecutionID = 0 }, http.StatusBadRequest},
		{"wrong_execution", func(r *apiRequest) { r.ExecutionID++ }, http.StatusConflict},
		{"replacement_member", func(r *apiRequest) { r.MembershipID = "replacement" }, http.StatusForbidden},
		{"valid", func(*apiRequest) {}, http.StatusOK},
	} {
		t.Run(check.name, func(t *testing.T) {
			req := valid
			check.edit(&req)
			body, _ := json.Marshal(req)
			response := httptest.NewRecorder()
			s.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/member/cancel", bytes.NewReader(body)))
			if response.Code != check.code {
				t.Fatal(response.Code, response.Body.String())
			}
		})
	}
	waitMember(t, h.ctx, target, id, func(s MemberStatus) bool { return !s.Active })
}
