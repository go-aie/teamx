package room

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	"github.com/gorilla/websocket"
)

type computerEnvironment struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	ViewerURL string `json:"viewer_url"`
}

func (m *roomManager) sharedComputer() *computerMembers {
	if m.settings == nil || m.settings.computer == nil {
		return nil
	}
	return m.settings.computer.members
}

// Reading the picker is local. A screen is created only when a human opens it.
func (m *roomManager) computerEnvironments(id RoomIdentity) ([]computerEnvironment, map[string]MemberIdentity) {
	views := []computerEnvironment{}
	ids := map[string]MemberIdentity{}
	x := m.sharedComputer()
	if x == nil || !x.connection.current.Load().Enabled {
		return views, ids
	}
	members, err := x.roomPreferences(x.memberDirectory(MemberIdentity{RoomIdentity: id}), id)
	if err != nil {
		return views, ids
	}
	for _, member := range members {
		target := MemberIdentity{RoomIdentity: id, AgentID: member.AgentID, MembershipID: member.MembershipID}
		if !member.ComputerEnabled || !m.environmentMemberCurrent(target) {
			continue
		}
		key := environmentID(m.epoch, target)
		name := m.agentNames.name(target)
		if name == "" {
			name = target.AgentID
		}
		viewer := "/v1/environment/" + key + "/viewer"
		if m.config.access != nil && m.config.access.spaces != nil {
			viewer = spaceRoute(m.config.access.space.SpaceID) + viewer
		}
		views = append(views, computerEnvironment{key, name, viewer})
		ids[key] = target
	}
	return views, ids
}
func (m *roomManager) computerEnvironmentMember(key string) (MemberIdentity, bool) {
	m.mu.Lock()
	rooms := append([]roomIndexEntry(nil), m.index.Rooms...)
	m.mu.Unlock()
	for _, room := range rooms {
		if room.Status != "ready" {
			continue
		}
		_, ids := m.computerEnvironments(room.RoomIdentity)
		if id, ok := ids[key]; ok {
			return id, true
		}
	}
	return MemberIdentity{}, false
}
func (x *computerMembers) display(ctx context.Context, id MemberIdentity) (computer.DisplayScreen, error) {
	if !x.connection.current.Load().Enabled || x.memberPending(id) {
		return nil, computer.Fail(computer.Disabled, "Computer unavailable during configuration changes")
	}
	pref, err := x.preference(id, x.memberDirectory(id))
	if err != nil {
		return nil, err
	}
	if !pref.ComputerEnabled {
		return nil, computer.Fail(computer.Disabled, "Member Computer disabled")
	}
	e, err := x.screen(ctx, id, filepath.Join(x.memberDirectory(id), "agents", id.MembershipID))
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	s := e.screen
	e.mu.Unlock()
	d, ok := s.(computer.DisplayScreen)
	if !ok {
		return nil, computer.Fail(computer.Unavailable, "Computer display unavailable")
	}
	return d, nil
}
func (m *roomManager) serveComputerEnvironment(w http.ResponseWriter, r *http.Request, id MemberIdentity, asset string) {
	if asset == "control" {
		m.serveEnvironmentControl(w, r, id)
		return
	}
	if asset == "viewer" {
		serveEnvironmentViewer(w)
		return
	}
	if r.Method != http.MethodGet {
		environmentError(w, 405)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	x := m.sharedComputer()
	screen, err := x.display(ctx, id)
	if err != nil {
		environmentError(w, 503)
		return
	}
	if strings.HasPrefix(asset, "vnc/") {
		b, err := screen.ViewAsset(ctx, strings.TrimPrefix(asset, "vnc/"))
		if err != nil {
			environmentError(w, 404)
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(b)
		return
	}
	if asset != "websockify" {
		environmentError(w, 404)
		return
	}
	if !websocket.IsWebSocketUpgrade(r) || r.Header.Get("Origin") == "" || !sameOrigin(r) {
		environmentError(w, 403)
		return
	}
	req := computer.ViewRequest{ID: r.URL.Query().Get("control_id"), Client: r.URL.Query().Get("client_id")}
	input := req.ID != "" || req.Client != ""
	if input && !m.environmentInputAllowed(id, req.ID, req.Client) {
		environmentError(w, 403)
		return
	}
	select {
	case environmentStreams <- struct{}{}:
		defer func() { <-environmentStreams }()
	default:
		environmentError(w, 429)
		return
	}
	var registered *environmentInputStream
	if input {
		registered, err = m.registerEnvironmentInput(id, req.ID, req.Client, cancel)
		if err != nil {
			environmentError(w, 403)
			return
		}
		defer m.releaseEnvironmentInput(id, req.ID, registered)
	}
	stream, err := screen.View(ctx, req)
	if err != nil {
		environmentError(w, 503)
		return
	}
	defer stream.Close()
	upgrader := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 32768, Subprotocols: []string{"binary"}, CheckOrigin: sameOrigin}
	w.Header().Del("Content-Type")
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ws.SetReadLimit(1 << 20)
	connectionVersion := x.connection.current.Load().Version
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				stream.Close()
				ws.Close()
				return
			case <-ticker.C:
				if x.connection.current.Load().Version != connectionVersion || !x.connection.current.Load().Enabled || x.memberPending(id) || !m.environmentMemberCurrent(id) || !environmentAccessCurrent(r) || input && !m.environmentInputAllowed(id, req.ID, req.Client) {
					cancel()
				}
			}
		}
	}()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer cancel()
		for {
			kind, reader, err := ws.NextReader()
			if err != nil {
				return
			}
			if kind != websocket.BinaryMessage {
				return
			}
			if _, err = io.Copy(stream, reader); err != nil {
				return
			}
		}
	}()
	buffer := make([]byte, 32768)
	for {
		n, err := stream.Read(buffer)
		if n > 0 {
			_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if ws.WriteMessage(websocket.BinaryMessage, buffer[:n]) != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	cancel()
	stream.Close()
	ws.Close()
	<-readDone
	<-done
}
func serveEnvironmentViewer(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data: blob:; frame-ancestors 'self'; base-uri 'none'; form-action 'none'")
	_, _ = io.WriteString(w, `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>电脑画面</title><link rel="stylesheet" href="/ui/styles/environment-viewer.css"><body><div id="screen" aria-label="电脑画面"></div><p id="status" role="status">正在连接电脑…</p><button id="keyboard-toggle" type="button" hidden>键盘</button><input id="keyboard" type="password" autocomplete="off" aria-label="远端键盘输入" autocapitalize="off" spellcheck="false"><script type="module" src="/ui/environment-viewer.mjs"></script></body></html>`)
}

// Scheduling intent stays Actor-owned; remote leases are Runtime-owned. This
// coordinator performs network waits outside Actor, manager and Store locks.
func (m *roomManager) syncComputerControl(ctx context.Context, svc *service, id MemberIdentity, action string, call apiRequest, status MemberStatus) (MemberStatus, error) {
	x := m.sharedComputer()
	if x == nil {
		return status, nil
	}
	pref, err := x.preference(id, x.memberDirectory(id))
	if err != nil || !pref.ComputerEnabled {
		return status, err
	}
	h := status.EnvironmentControl
	if h == nil || h.Owner != call.environmentOwner {
		return status, nil
	}
	e := x.entry(id)
	if e != nil {
		e.mu.Lock()
		e.humanUntil = time.Now().Add(environmentControlLifetime)
		blocked := e.blocked
		e.mu.Unlock()
		if blocked && !status.Active && (h.Phase == "blocked" || h.Phase == "ready") {
			if err := x.settleScope(ctx, id, computer.Scope{}, true); err != nil {
				return status, err
			}
		}
	}
	screen, err := x.display(ctx, id)
	if err != nil {
		return status, err
	}
	e = x.entry(id)
	e.mu.Lock()
	e.humanUntil = time.Now().Add(environmentControlLifetime)
	e.mu.Unlock()
	remote := computer.ControlRequest{Context: svc.info.ServiceEpoch, Sequence: h.Sequence, ID: h.ID, Client: h.Owner, Action: "hold"}
	if action == "finish" || action == "abandon" || h.Phase == "expired" || h.Phase == "abandoned" || h.Phase == "completed" {
		remote.Action = "release"
		_, err = screen.Control(ctx, remote)
		if computer.Is(err, computer.Unknown) {
			// A missing RFB exit acknowledgement is not a stop proof. Use the
			// existing screen settlement and keep the faulted Session paused.
			if err = x.settleScope(ctx, id, computer.Scope{}, true); err != nil {
				return status, err
			}
			if h.Phase == "finishing" {
				status, err = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, environmentID: h.ID, environmentOwner: h.Owner}, "environment_review")
				if err != nil {
					return status, err
				}
			}
			_, err = screen.Control(ctx, remote)
		}
		if err == nil {
			e.mu.Lock()
			e.humanUntil = time.Time{}
			e.mu.Unlock()
		}
		return status, err
	}
	if h.Phase != "waiting" && h.Phase != "ready" && h.Phase != "blocked" {
		return status, nil
	}
	fact, err := screen.Control(ctx, remote)
	if err != nil {
		return status, err
	}
	if fact.Phase == "expired" || fact.Phase == "released" {
		status, err = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, environmentID: h.ID, environmentOwner: h.Owner}, "environment_abandon")
		return status, err
	}
	if status.Active {
		return status, nil
	}
	// Finish any recovered external scopes before activating human input. Unknown
	// business outcomes allow inspection only after a verified stop, and never resume.
	e.mu.Lock()
	blocked := e.blocked
	e.mu.Unlock()
	if err = x.settleScope(ctx, id, computer.Scope{}, blocked); err != nil {
		return status, err
	}
	e.mu.Lock()
	review := e.resource.Unknown
	e.mu.Unlock()
	if review && !h.Review {
		status, err = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, environmentID: h.ID, environmentOwner: h.Owner}, "environment_review")
		if err != nil {
			return status, err
		}
		h = status.EnvironmentControl
	}
	if h.Phase != "ready" {
		return status, nil
	}
	screen, err = x.display(ctx, id)
	if err != nil {
		return status, err
	}
	remote.Action = "activate"
	_, err = screen.Control(ctx, remote)
	return status, err
}
