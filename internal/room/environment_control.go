package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-aie/actorx"
)

const environmentControlLifetime = 20 * time.Second

type environmentInputStream struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (m *roomManager) registerEnvironmentInput(id MemberIdentity, control, owner string, cancel context.CancelFunc) (*environmentInputStream, error) {
	m.environmentInputsMu.Lock()
	defer m.environmentInputsMu.Unlock()
	// Recheck under the registration gate: a finish must also fence a stream
	// whose HTTP upgrade began before the member entered the finishing phase.
	if !m.environmentInputAllowed(id, control, owner) {
		return nil, ErrConflict
	}
	key := environmentID(m.epoch, id) + control
	if m.environmentInputs == nil {
		m.environmentInputs = map[string]map[*environmentInputStream]bool{}
	}
	if m.environmentInputs[key] == nil {
		m.environmentInputs[key] = map[*environmentInputStream]bool{}
	}
	stream := &environmentInputStream{cancel: cancel, done: make(chan struct{})}
	m.environmentInputs[key][stream] = true
	return stream, nil
}
func (m *roomManager) releaseEnvironmentInput(id MemberIdentity, control string, stream *environmentInputStream) {
	m.environmentInputsMu.Lock()
	defer m.environmentInputsMu.Unlock()
	key := environmentID(m.epoch, id) + control
	delete(m.environmentInputs[key], stream)
	if len(m.environmentInputs[key]) == 0 {
		delete(m.environmentInputs, key)
	}
	close(stream.done)
}
func (m *roomManager) stopEnvironmentInputs(ctx context.Context, id MemberIdentity, control string) error {
	m.environmentInputsMu.Lock()
	streams := []*environmentInputStream{}
	for stream := range m.environmentInputs[environmentID(m.epoch, id)+control] {
		stream.cancel()
		streams = append(streams, stream)
	}
	m.environmentInputsMu.Unlock()
	for _, stream := range streams {
		select {
		case <-stream.done:
		case <-ctx.Done():
			return ErrBusy
		}
	}
	return nil
}

// A handoff is transient; the scheduling pause is durable. Losing a page or
// restarting the service never silently enables the member again.
type environmentHandoff struct {
	Sequence      uint64    `json:"-"`
	ID            string    `json:"id"`
	Phase         string    `json:"phase"`
	Owned         bool      `json:"owned"`
	Resumed       bool      `json:"resumed"`
	Review        bool      `json:"review,omitempty"`
	ResumeFailed  bool      `json:"resume_failed,omitempty"`
	Owner         string    `json:"-"`
	Restore       bool      `json:"-"`
	PauseRevision uint64    `json:"-"`
	Expires       time.Time `json:"-"`
	FinishedAt    time.Time `json:"-"`
}

func (m *MemberAgent) environmentHeld() bool {
	h := m.environmentControl
	return h != nil && (h.Phase == "holding" || h.Phase == "finishing")
}
func (m *MemberAgent) expireEnvironmentControl() {
	if m.environmentHeld() && !m.now().Before(m.environmentControl.Expires) {
		m.environmentControl.Phase = "expired"
		m.environmentControl.Restore = false
	}
}
func (m *MemberAgent) environmentControlView() *environmentHandoff {
	if m.environmentControl == nil {
		return nil
	}
	h := *m.environmentControl
	h.Expires = time.Time{} // Lease heartbeats are not visible member-state changes.
	if h.Phase == "holding" {
		switch {
		case m.active:
			h.Phase = "waiting"
		case m.ModelGate != "" || m.rotationWork != nil || !h.Review && (m.fault || m.BackendBlocked != "" || m.state.Turn != nil && m.state.Turn.Status != "completed"):
			h.Phase = "blocked"
		default:
			h.Phase = "ready"
		}
	}
	return &h
}
func (m *MemberAgent) controlEnvironment(ctx actorx.Context, msg ControlMember) (MemberStatus, error) {
	if !validRestartID(msg.environmentOwner) {
		return m.status(), ErrRequest
	}
	h := m.environmentControl
	if msg.Action == "environment_inspect" || msg.Action == "environment_peek" {
		if msg.Action == "environment_inspect" && h != nil && h.Owner == msg.environmentOwner && m.environmentHeld() {
			h.Expires = m.now().Add(environmentControlLifetime)
		}
		return m.status(), nil
	}
	if !validRestartID(msg.environmentID) {
		return m.status(), ErrRequest
	}
	if msg.Action == "environment_takeover" {
		if h != nil && h.ID == msg.environmentID {
			if h.Owner != msg.environmentOwner {
				return m.status(), ErrBusy
			}
			if m.environmentHeld() {
				h.Expires = m.now().Add(environmentControlLifetime)
			}
			return m.status(), nil
		}
		if m.environmentHeld() || m.rotationWork != nil || m.ModelGate != "" {
			return m.status(), ErrBusy
		}
		restore := m.enabled && !m.paused
		if err := m.persistScheduling(false, m.turnsUsed); err != nil {
			return m.status(), err
		}
		m.paused, m.retry = true, false
		m.withdrawTurn()
		sequence := uint64(1)
		if h != nil {
			sequence = h.Sequence + 1
		}
		m.environmentControl = &environmentHandoff{Sequence: sequence, ID: msg.environmentID, Owner: msg.environmentOwner, Phase: "holding", Restore: restore, PauseRevision: m.pauseRevision, Expires: m.now().Add(environmentControlLifetime)}
		return m.status(), nil
	}
	if h == nil || h.ID != msg.environmentID || h.Owner != msg.environmentOwner {
		return m.status(), ErrConflict
	}
	if !m.environmentHeld() {
		return m.status(), nil
	} // Retried finish/abandon observes its terminal result.
	h.Expires = m.now().Add(environmentControlLifetime)
	switch msg.Action {
	case "environment_review":
		if m.active || m.rotationWork != nil {
			return m.status(), ErrBusy
		}
		h.Review = true
		h.Restore = false
	case "environment_prepare":
		if h.Phase == "finishing" {
			return m.status(), nil
		}
		if m.environmentControlView().Phase != "ready" {
			return m.status(), ErrBusy
		}
		h.Phase, h.FinishedAt = "finishing", m.now()
	case "environment_finish":
		if h.Phase != "finishing" {
			return m.status(), ErrConflict
		}
		h.Phase = "completed"
		if h.Restore && h.PauseRevision == m.pauseRevision {
			_, err := m.Control(ctx, ControlMember{MemberIdentity: msg.MemberIdentity, Action: "run", State: msg.State})
			h.Resumed = err == nil && !m.paused
			h.ResumeFailed = err != nil
		}
	case "environment_abandon":
		h.Phase, h.Restore = "abandoned", false
	default:
		return m.status(), ErrRequest
	}
	return m.status(), nil
}

func (m *roomManager) environmentService(id MemberIdentity) (*service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	svc, _ := m.runtimeServiceLocked(id.RoomIdentity)
	if m.closing || svc == nil || m.roomBusyLocked(id.RoomIdentity) {
		return nil, ErrBusy
	}
	return svc, nil
}

func (m *roomManager) serveEnvironmentControl(w http.ResponseWriter, r *http.Request, id MemberIdentity) {
	svc, err := m.environmentService(id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var req struct {
		ServiceInfo
		Caller    Caller `json:"caller"`
		ClientID  string `json:"client_id"`
		ControlID string `json:"control_id"`
		Action    string `json:"action"`
	}
	if r.Method == http.MethodGet {
		req.ClientID = r.URL.Query().Get("client_id")
		req.Action = "inspect"
	} else if r.Method == http.MethodPost {
		dec := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
		dec.DisallowUnknownFields()
		if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF || req.ServiceInfo != svc.info || req.Caller.Kind != "human" || req.Caller.ID != requestOperator(r) || req.Caller.MembershipID != "" {
			writeAPIError(w, ErrIdentity)
			return
		}
	} else {
		environmentError(w, 405)
		return
	}
	if !validRestartID(req.ClientID) {
		writeAPIError(w, ErrRequest)
		return
	}
	if req.Action != "inspect" && req.Action != "takeover" && req.Action != "finish" && req.Action != "abandon" {
		writeAPIError(w, ErrRequest)
		return
	}
	call := apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, environmentID: req.ControlID, environmentOwner: req.ClientID}
	action := "environment_" + req.Action
	if req.Action == "finish" {
		action = "environment_prepare"
	}
	// Calls remain owned by the service when an HTTP reply is lost; a retry uses
	// the same control ID and the same durable Room event receipt.
	status, err := svc.control(svc.ctx, call, action)
	if err == nil {
		wait, cancel := context.WithTimeout(svc.ctx, 8*time.Second)
		status, err = m.syncComputerControl(wait, svc, id, req.Action, call, status)
		cancel()
	}
	if err == nil && req.Action == "finish" && status.EnvironmentControl != nil && status.EnvironmentControl.Phase == "finishing" {
		h := status.EnvironmentControl
		wait, cancel := context.WithTimeout(svc.ctx, 5*time.Second)
		err = m.stopEnvironmentInputs(wait, id, h.ID)
		cancel()
		if err != nil {
			writeAPIError(w, err)
			return
		}
		_, err = svc.room.Call[PostReceipt](svc.ctx, AppendRoomEvent{RoomIdentity: id.RoomIdentity,
			Event:   RoomEvent{ID: "environment_" + h.ID, Kind: "environment_handoff_completed", OperationID: h.ID, ResultVersion: 1, ResultRef: "environment/" + h.ID, Targets: []string{id.MembershipID}, Actor: req.Caller.Author},
			Summary: "人工电脑操作已完成。请先核对最新用户指示：若仍有待继续的任务，重新查看当前页面并在原授权范围内继续；否则确认交还即可。不要重放登录步骤，也不要读取或回报密码。", CreatedAt: h.FinishedAt})
		if err == nil {
			status, err = svc.control(svc.ctx, call, "environment_finish")
		}
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if h := status.EnvironmentControl; h != nil {
		h.Owned = h.Owner == req.ClientID
	}
	_ = json.NewEncoder(w).Encode(struct {
		Control *environmentHandoff `json:"control"`
		Paused  bool                `json:"paused"`
	}{status.EnvironmentControl, status.Paused})
}

func (m *roomManager) environmentInputAllowed(id MemberIdentity, control, owner string) bool {
	if !validRestartID(control) || !validRestartID(owner) {
		return false
	}
	svc, err := m.environmentService(id)
	if err != nil {
		return false
	}
	status, err := svc.control(svc.ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, environmentOwner: owner}, "environment_peek")
	h := status.EnvironmentControl
	return err == nil && h != nil && h.ID == control && h.Owner == owner && h.Phase == "ready" && status.Paused && !status.Active
}
