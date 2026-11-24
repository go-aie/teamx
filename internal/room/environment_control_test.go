package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/actorx"
)

func handoffCall(t *testing.T, h *roomHarness, addr actorx.Addr, id MemberIdentity, action, control, owner string) MemberStatus {
	t.Helper()
	status, err := addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "environment_" + action, environmentID: control, environmentOwner: owner, State: UnreadChanged{MemberIdentity: id}})
	if err != nil {
		t.Fatal(action, err)
	}
	return status
}

func TestEnvironmentHandoffWaitsForTurnAndRestoresIntent(t *testing.T) {
	b := newControlledBackend()
	h, addr, id := memberHarness(t, b, nil, func(m *MemberAgent) { m.PersistentScheduling = true })
	n := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, addr, id, "run", n, false, false)
	awaitTurn(t, h, b)
	control, owner := strings.Repeat("a", 32), strings.Repeat("b", 32)
	status := handoffCall(t, h, addr, id, "takeover", control, owner)
	if !status.Active || !status.Paused || status.EnvironmentControl.Phase != "waiting" {
		t.Fatal(status)
	}
	if scheduling, err := h.store.loadScheduling(id); err != nil || scheduling.Enabled {
		t.Fatal("handoff pause was not durable", scheduling, err)
	}
	select {
	case <-b.canceled:
		t.Fatal("handoff canceled a remote operation instead of awaiting settlement")
	default:
	}
	for _, msg := range []ControlMember{
		{Action: "run", State: n}, {Action: "enable", State: n}, {Action: "rotate"},
		{Action: "environment_takeover", environmentID: strings.Repeat("c", 32), environmentOwner: strings.Repeat("d", 32)},
	} {
		msg.MemberIdentity = id
		if _, err := addr.Call[MemberStatus](h.ctx, msg); !errors.Is(err, ErrBusy) {
			t.Fatal("concurrent execution/control admitted", msg.Action, err)
		}
	}
	if _, err := addr.Call[MemberStatus](h.ctx, ControlMember{MemberIdentity: id, Action: "environment_prepare", environmentID: control, environmentOwner: owner}); !errors.Is(err, ErrBusy) {
		t.Fatal("input released before turn ended", err)
	}
	b.finish <- nil
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.EnvironmentControl.Phase == "ready" })
	handoffCall(t, h, addr, id, "prepare", control, owner)
	status = handoffCall(t, h, addr, id, "finish", control, owner)
	if status.Paused || !status.EnvironmentControl.Resumed || status.EnvironmentControl.Phase != "completed" {
		t.Fatal(status)
	}
	if scheduling, err := h.store.loadScheduling(id); err != nil || !scheduling.Enabled {
		t.Fatal("restored intent was not durable", scheduling, err)
	}
	status = handoffCall(t, h, addr, id, "finish", control, owner)
	if status.EnvironmentControl.Phase != "completed" {
		t.Fatal("finish retry lost its result")
	}
	// A separate explicit pause during takeover must win over restoration.
	control = strings.Repeat("e", 32)
	handoffCall(t, h, addr, id, "takeover", control, owner)
	controlMember(t, h, addr, id, "pause", n, false, false)
	handoffCall(t, h, addr, id, "prepare", control, owner)
	status = handoffCall(t, h, addr, id, "finish", control, owner)
	if !status.Paused || status.EnvironmentControl.Resumed {
		t.Fatal("overrode a newer human pause", status)
	}
}

func TestEnvironmentHandoffPauseExpiryAndFailedTurn(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	b := newControlledBackend()
	h, addr, id := memberHarness(t, b, nil, func(m *MemberAgent) { m.now = func() time.Time { return time.Unix(0, clock.Load()) } })
	control, owner := strings.Repeat("a", 32), strings.Repeat("b", 32)
	handoffCall(t, h, addr, id, "takeover", control, owner)
	handoffCall(t, h, addr, id, "prepare", control, owner)
	if s := handoffCall(t, h, addr, id, "finish", control, owner); !s.Paused || s.EnvironmentControl.Resumed {
		t.Fatal("enabled an originally paused member")
	}
	control = strings.Repeat("c", 32)
	handoffCall(t, h, addr, id, "takeover", control, owner)
	clock.Add(int64(environmentControlLifetime - time.Second))
	handoffCall(t, h, addr, id, "peek", "", owner)
	clock.Add(int64(2 * time.Second))
	status := handoffCall(t, h, addr, id, "inspect", "", owner)
	if status.EnvironmentControl.Phase != "expired" || !status.Paused {
		t.Fatal("lost viewer resumed execution", status)
	}
	n := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, addr, id, "run", n, false, false)
	awaitTurn(t, h, b)
	control = strings.Repeat("d", 32)
	handoffCall(t, h, addr, id, "takeover", control, owner)
	b.finish <- errors.New("remote operation ended with uncertain state")
	status = waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active })
	if status.EnvironmentControl.Phase != "blocked" {
		t.Fatal("failed execution allowed input", status)
	}
	handoffCall(t, h, addr, id, "abandon", control, owner)
	if s := inspectMember(t, h.ctx, addr, id); !s.Paused {
		t.Fatal("abandon resumed execution")
	}
}

func TestEnvironmentHandoffHeartbeatDoesNotRefreshMemberUI(t *testing.T) {
	var clock, updates atomic.Int64
	clock.Store(time.Now().UnixNano())
	h, addr, id := memberHarness(t, newControlledBackend(), nil, func(m *MemberAgent) {
		m.now = func() time.Time { return time.Unix(0, clock.Load()) }
		m.Observe = func() { updates.Add(1) }
	})
	control, owner := strings.Repeat("a", 32), strings.Repeat("b", 32)
	handoffCall(t, h, addr, id, "takeover", control, owner)
	before := updates.Load()
	clock.Add(int64(5 * time.Second))
	handoffCall(t, h, addr, id, "inspect", "", owner)
	if updates.Load() != before {
		t.Fatal("private lease heartbeat invalidated visible member state")
	}
}

func TestEnvironmentHandoffHTTPCompletionIsTargetedAndIdempotent(t *testing.T) {
	t.Setenv("USER", humanCaller.ID)
	m, _, x, _ := managedComputerFixture(t)
	m.config.unifiedInputs = true
	m.factory = controlledActivationFactory(nil, nil)
	room := createDirectConversation(t, m, directSelection("handoff"))
	saved, _ := readManagedRoom(t, m, room)
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: saved.Members[0].AgentID, MembershipID: saved.Members[0].MembershipID}
	applyMemberComputer(t, x, id, true)
	openRoomOK(t, m, room, "handoff-open")
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(room.RoomIdentity)
	m.mu.Unlock()
	if _, err := svc.control(svc.ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "pause"); err != nil {
		t.Fatal(err)
	}
	endpoint := "/v1/environment/" + environmentID(m.epoch, id) + "/control"
	control, owner := strings.Repeat("a", 32), strings.Repeat("b", 32)
	call := func(action string) *environmentHandoff {
		t.Helper()
		body := map[string]any{"room_id": id.RoomID, "room_instance": id.RoomInstance, "service_epoch": svc.info.ServiceEpoch, "caller": humanCaller, "client_id": owner, "control_id": control, "action": action}
		response := managerHTTP(m, endpoint, body)
		if response.Code != http.StatusOK {
			t.Fatal(action, response.Code, response.Body.String())
		}
		var result struct {
			Control *environmentHandoff `json:"control"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Control
	}
	if h := call("takeover"); !h.Owned || h.Phase != "ready" {
		t.Fatal(h)
	}
	if !m.environmentInputAllowed(id, control, owner) || m.environmentInputAllowed(id, control, strings.Repeat("c", 32)) {
		t.Fatal("control ownership crossed")
	}
	inputContext, cancelInput := context.WithCancel(context.Background())
	defer cancelInput()
	input, err := m.registerEnvironmentInput(id, control, owner, cancelInput)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		finished <- managerHTTP(m, endpoint, map[string]any{"room_id": id.RoomID, "room_instance": id.RoomInstance, "service_epoch": svc.info.ServiceEpoch, "caller": humanCaller, "client_id": owner, "control_id": control, "action": "finish"})
	}()
	select {
	case <-inputContext.Done():
	case <-time.After(time.Second):
		t.Fatal("finish did not revoke input")
	}
	select {
	case <-finished:
		t.Fatal("finish resumed before input stream settled")
	default:
	}
	if _, err := m.registerEnvironmentInput(id, control, owner, func() {}); err == nil {
		t.Fatal("late input stream crossed the finish fence")
	}
	m.releaseEnvironmentInput(id, control, input)
	select {
	case response := <-finished:
		if response.Code != 200 {
			t.Fatal(response.Code, response.Body.String())
		}
	case <-time.After(time.Second):
		t.Fatal("finish did not join input stream")
	}
	call("finish")
	if m.environmentInputAllowed(id, control, owner) {
		t.Fatal("completed input lease retained")
	}
	state, err := readPublishedRoom(m.roomDir(id.RoomID), id.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, message := range state.Messages {
		if message.Event != nil && message.Event.Kind == "environment_handoff_completed" {
			count++
			if len(message.Event.Targets) != 1 || message.Event.Targets[0] != id.MembershipID || !strings.Contains(message.Text, "重新查看当前") {
				t.Fatal(message)
			}
		}
	}
	if count != 1 {
		t.Fatal("completion event duplicated or missing", count)
	}
	// The completion must remain readable and writable in the same runtime,
	// before a restart strips the in-memory timestamp's monotonic clock data.
	caller := agentCallerIdentity(id)
	page, err := svc.room.Call[ReadPage](svc.ctx, ReadMessages{RoomIdentity: id.RoomIdentity, Caller: caller, Mode: ReadUnread, Limit: 20})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Event == nil {
		t.Fatal("completion event was not delivered", page, err)
	}
	if _, err := svc.room.Call[ReadConfirmation](svc.ctx, ConfirmRead{MemberIdentity: id, ReadReceipt: page.ReadReceipt}); err != nil {
		t.Fatal("completion event could not be acknowledged", err)
	}
	for _, author := range []Caller{humanCaller, caller} {
		if _, err := svc.room.Call[PostReceipt](svc.ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: author, ClientMessageID: "after-handoff-" + author.Kind, Text: "continue after handoff"}); err != nil {
			t.Fatal("completion prevented further conversation", author.Kind, err)
		}
	}
}
