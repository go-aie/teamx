package room

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	runtime "github.com/go-aie/teamx/internal/computer/aio/runtime"
)

func (d *connectionDriver) OpenView(ctx context.Context, _ runtime.Resource) (io.ReadWriteCloser, error) {
	a, b := net.Pipe()
	go func() { defer b.Close(); <-ctx.Done() }()
	return a, nil
}
func (*connectionDriver) ViewAsset(context.Context, string) ([]byte, error) {
	return []byte("// bounded native viewer"), nil
}

func TestComputerPanelControlSharesMemberIntentAndRuntimeGate(t *testing.T) {
	t.Setenv("USER", humanCaller.ID)
	m, _, x, _ := managedComputerFixture(t)
	m.config.unifiedInputs = true
	m.factory = controlledActivationFactory(nil, nil)
	room := createDirectConversation(t, m, directSelection("computer-control"))
	saved, _ := readManagedRoom(t, m, room)
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: saved.Members[0].AgentID, MembershipID: saved.Members[0].MembershipID}
	openRoomOK(t, m, room, "computer-control-open")
	applyMemberComputer(t, x, id, true)
	svc := x.runtime(id)
	if _, err := svc.control(svc.ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	views, _ := m.computerEnvironments(id.RoomIdentity)
	if len(views) != 1 {
		t.Fatal(views)
	}
	endpoint := strings.TrimSuffix(views[0].ViewerURL, "viewer") + "control"
	control, client := strings.Repeat("a", 32), strings.Repeat("b", 32)
	request := func(action string) *environmentHandoff {
		t.Helper()
		response := managerHTTP(m, endpoint, map[string]any{"room_id": id.RoomID, "room_instance": id.RoomInstance, "service_epoch": svc.info.ServiceEpoch, "caller": humanCaller, "control_id": control, "client_id": client, "action": action})
		if response.Code != 200 {
			status, _ := svc.control(svc.ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, environmentOwner: client}, "environment_peek")
			_, why := m.syncComputerControl(t.Context(), svc, id, action, apiRequest{environmentOwner: client}, status)
			t.Fatalf("%s: %d %s (%v)", action, response.Code, response.Body.String(), why)
		}
		var v struct {
			Control *environmentHandoff `json:"control"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v.Control
	}
	h := request("takeover")
	if h.Phase != "ready" || !h.Owned {
		t.Fatal(h)
	}
	e := x.entry(id)
	e.mu.Lock()
	screen := e.screen
	e.mu.Unlock()
	contract, err := screen.Tools(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = screen.Call(t.Context(), computer.Call{ID: "late", Scope: computer.Scope{Session: "test", Turn: "1"}, Tool: contract.Tools[0].Name, ContractDigest: contract.Digest, Arguments: json.RawMessage(`{}`)}); !computer.Is(err, computer.HumanControl) {
		t.Fatal("runtime did not fence Agent", err)
	}
	rival := managerHTTP(m, endpoint, map[string]any{"room_id": id.RoomID, "room_instance": id.RoomInstance, "service_epoch": svc.info.ServiceEpoch, "caller": humanCaller, "control_id": strings.Repeat("c", 32), "client_id": strings.Repeat("d", 32), "action": "takeover"})
	if rival.Code != http.StatusConflict {
		t.Fatal(rival.Code, rival.Body.String())
	}
	if _, err = svc.control(svc.ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "pause"); err != nil {
		t.Fatal(err)
	}
	h = request("finish")
	if h.Resumed || h.Phase != "completed" {
		t.Fatal("new manual pause overwritten", h)
	}
	h = request("finish")
	if h.Phase != "completed" {
		t.Fatal("finish retry", h)
	}
	state, err := readPublishedRoom(m.roomDir(id.RoomID), id.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	events := 0
	for _, msg := range state.Messages {
		if msg.Event != nil && msg.Event.Kind == "environment_handoff_completed" {
			events++
			if len(msg.Event.Targets) != 1 || msg.Event.Targets[0] != id.MembershipID {
				t.Fatal(msg)
			}
		}
	}
	if events != 1 {
		t.Fatal("handoff event was not idempotent", events)
	}
	// Cancellation and expiry both retain the pause. No automatic restoration.
	control = strings.Repeat("e", 32)
	request("takeover")
	request("abandon")
	control = strings.Repeat("f", 32)
	request("takeover")
	// Expire the Runtime lease independently of the Actor heartbeat.
	d, err := x.display(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Control(t.Context(), computer.ControlRequest{Context: svc.info.ServiceEpoch, Sequence: 3, ID: control, Client: client, Action: "release"})
	if err != nil {
		t.Fatal(err)
	}
	response := managerHTTP(m, endpoint+"?client_id="+client, nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "abandoned") || !strings.Contains(response.Body.String(), `"paused":true`) {
		t.Fatal("lost lease restored scheduling", response.Code, response.Body.String())
	}
}
func TestComputerWaitingTakeoverRejectsLateHostCallsWithoutPendingRecord(t *testing.T) {
	f := newComputerAgentFixture(t, "native-provider", "native_action")
	e := f.x.entry(f.opts.MemberIdentity)
	e.mu.Lock()
	e.humanUntil = time.Now().Add(time.Minute)
	e.mu.Unlock()
	_, err := f.x.call(t.Context(), f.opts.MemberIdentity, computer.Scope{Session: f.input.execution.Generation, Turn: "1"}, f.capability, "late", "native_action", json.RawMessage(`{}`))
	if !computer.Is(err, computer.HumanControl) || len(f.x.host.Pending()) != 0 || f.driver.calls.Load() != 0 {
		t.Fatal("takeover waiting created a mutation record", err, f.x.host.Pending())
	}
}
func TestComputerManualReviewRequiresStoppedUnknownAndNeverResumes(t *testing.T) {
	// Actor transition used only by the coordinator after the C2 settlement proof.
	b := newControlledBackend()
	h, addr, id := memberHarness(t, b, nil)
	n := UnreadChanged{MemberIdentity: id, Revision: 1, LatestSeq: 1, NewestUnreadSeq: 1, UnreadCount: 1}
	controlMember(t, h, addr, id, "run", n, false, false)
	awaitTurn(t, h, b)
	control, client := strings.Repeat("a", 32), strings.Repeat("b", 32)
	handoffCall(t, h, addr, id, "takeover", control, client)
	b.finish <- computer.Fail(computer.Unknown, "website effect unknown")
	waitMember(t, h.ctx, addr, id, func(s MemberStatus) bool { return !s.Active && s.EnvironmentControl.Phase == "blocked" })
	s := handoffCall(t, h, addr, id, "review", control, client)
	if !s.EnvironmentControl.Review || s.EnvironmentControl.Phase != "ready" {
		t.Fatal(s)
	}
	handoffCall(t, h, addr, id, "prepare", control, client)
	s = handoffCall(t, h, addr, id, "finish", control, client)
	if !s.Paused || s.EnvironmentControl.Resumed {
		t.Fatal("faulted Session resumed", s)
	}
}

func TestComputerPickerUsesEnabledMembershipsWithinSelectedRoom(t *testing.T) {
	t.Setenv("USER", humanCaller.ID)
	m, _, x, _ := managedComputerFixture(t)
	team := testTeamDefinition(t)
	team.ID = "computer-team"
	second := team.Members[0]
	second.ID = "writer"
	second.Name = "Writer"
	team.Members = append(team.Members, second)
	m.teams[team.ID] = team
	room := managerCreateTeam(t, m, "computer-picker", team.ID)
	saved, _ := readManagedRoom(t, m, room)
	for _, member := range saved.Members {
		applyMemberComputer(t, x, MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}, true)
	}
	views, ids := m.computerEnvironments(room.RoomIdentity)
	if len(views) != 2 || views[0].ID == views[1].ID || len(ids) != 2 {
		t.Fatal("group picker merged independent memberships", views)
	}
	outside := createDirectConversation(t, m, directSelection("outside-computer-picker"))
	if other, _ := m.computerEnvironments(outside.RoomIdentity); len(other) != 0 {
		t.Fatal("picker crossed Room identity", other)
	}
	disabled := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: saved.Members[0].AgentID, MembershipID: saved.Members[0].MembershipID}
	applyMemberComputer(t, x, disabled, false)
	views, ids = m.computerEnvironments(room.RoomIdentity)
	if len(views) != 1 || ids[views[0].ID] == disabled {
		t.Fatal("disabled member remained selectable", views)
	}
}
