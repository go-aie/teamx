package room

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
)

type platformCall struct {
	agentID, membershipID string
	owner                 RoomIdentity
	tool, connectionID    string
	cancel                context.CancelFunc
	done                  chan struct{}
	subscription          notification.Key // Set only for saved-notification attachments.
}

func (m *roomManager) toolRosterLocked(ctx context.Context, owner RoomIdentity) ([]MemberState, error) {
	if svc, _ := m.runtimeServiceLocked(owner); svc != nil && !svc.closing.Load() {
		list, err := svc.room.Call[MemberList](ctx, ListMembers{owner})
		if err != nil {
			return nil, err
		}
		members := make([]MemberState, 0, len(list.Members))
		for _, v := range list.Members {
			members = append(members, v.MemberState)
		}
		return members, nil
	}
	var snapshot RoomSnapshot
	if err := readSnapshot(filepath.Join(m.roomDir(owner.RoomID), "room.json"), &snapshot, maxRoomFileBytes); err != nil {
		return nil, err
	}
	if snapshot.RoomIdentity != owner || snapshot.validate() != nil {
		return nil, ErrInvalid
	}
	return snapshot.Members, nil
}
func memberExists(members []MemberState, agent, membership string) bool {
	for _, v := range members {
		if v.AgentID == agent && v.MembershipID == membership {
			return true
		}
	}
	return false
}

// Requires manager.mu. Cancellation only signals; callers join outside the gate.
func (m *roomManager) cancelPlatformCallsLocked(match func(*platformCall) bool) []<-chan struct{} {
	var done []<-chan struct{}
	for c := range m.platformCalls {
		if match(c) {
			c.cancel()
			done = append(done, c.done)
		}
	}
	return done
}

func (m *roomManager) attachmentCallsBusyLocked(key notification.Key) bool {
	for call := range m.platformCalls {
		if call.subscription == key {
			return true
		}
	}
	return false
}

func (m *roomManager) cancelAttachmentCallsLocked(key notification.Key) []<-chan struct{} {
	return m.cancelPlatformCallsLocked(func(call *platformCall) bool { return call.subscription == key })
}
func (m *roomManager) waitPlatformCalls(ctx context.Context, owner *RoomIdentity) error {
	m.mu.Lock()
	done := m.cancelPlatformCallsLocked(func(c *platformCall) bool { return owner == nil || c.owner == *owner })
	m.mu.Unlock()
	return notification.Wait(ctx, done)
}

// Shared admission deliberately does not inspect executables or Workspace files.
// Requires manager.mu; callers perform requests and join outside the gate.
type platformAdmission struct {
	definition tool.Definition
	account    connection.Account
	secret     platform.Secret
}

func (m *roomManager) preparePlatformCallLocked(ctx context.Context, req toolsRequest, help bool) (platformAdmission, *platformCall, context.Context, error) {
	var a platformAdmission
	if err := ctx.Err(); err != nil {
		return a, nil, nil, err
	}
	if err := m.checkToolMemberLocked(ctx, req); err != nil {
		return a, nil, nil, err
	}
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err != nil {
		return a, nil, nil, err
	}
	var binding *tool.Binding
	for i, b := range f.Bindings {
		if b.Tool == req.Tool && b.AgentID == req.Caller.ID && b.MembershipID == req.Caller.MembershipID {
			binding = &f.Bindings[i]
			break
		}
	}
	if binding == nil {
		return a, nil, nil, ErrRequest
	}
	a.definition, err = m.toolDefinition(req.Tool)
	if err != nil {
		return a, nil, nil, err
	}
	if a.definition.Provider != "" && !help {
		file, err := m.connections.Load()
		if err != nil {
			return a, nil, nil, err
		}
		found := false
		for _, c := range file.Connections {
			if c.ID == binding.ConnectionID && c.Generation == binding.ConnectionGeneration && c.Provider == a.definition.Provider {
				a.account, a.secret, err = connection.ResolveAccount(file, c)
				if err != nil {
					if errors.Is(err, platform.ErrCredential) {
						return a, nil, nil, platform.ErrCredential
					}
					return a, nil, nil, ErrConflict
				}
				found = true
				break
			}
		}
		if !found {
			return a, nil, nil, ErrConflict
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	call := &platformCall{owner: req.RoomIdentity, tool: req.Tool, connectionID: binding.ConnectionID, agentID: req.Caller.ID, membershipID: req.Caller.MembershipID, cancel: cancel, done: make(chan struct{})}
	if m.platformCalls == nil {
		m.platformCalls = map[*platformCall]bool{}
	}
	m.platformCalls[call] = true
	return a, call, ctx, nil
}
func (m *roomManager) finishPlatformCallLocked(call *platformCall) {
	call.cancel()
	delete(m.platformCalls, call)
	close(call.done)
}
func (m *roomManager) finishPlatformCall(call *platformCall) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finishPlatformCallLocked(call)
}

// Member removal and session rotation revoke both HTTP requests and CLI work.
// Block admission until the control operation has adopted its changed state.
func (m *roomManager) beginPlatformMemberChange(ctx context.Context, svc *service, id MemberIdentity) (func(), error) {
	m.mu.Lock()
	current, _ := m.runtimeServiceLocked(id.RoomIdentity)
	if current != svc || m.platformMemberChanges[id] {
		m.mu.Unlock()
		return nil, ErrBusy
	}
	if m.platformMemberChanges == nil {
		m.platformMemberChanges = map[MemberIdentity]bool{}
	}
	m.platformMemberChanges[id] = true
	done := m.cancelPlatformCallsLocked(func(call *platformCall) bool {
		return call.owner == id.RoomIdentity && call.agentID == id.AgentID && call.membershipID == id.MembershipID
	})
	m.mu.Unlock()
	release := func() { m.mu.Lock(); delete(m.platformMemberChanges, id); m.mu.Unlock() }
	wait, cancel := context.WithTimeout(ctx, serviceShutdownTimeout)
	defer cancel()
	if err := notification.Wait(wait, done); err != nil {
		release()
		return nil, ErrBusy
	}
	return release, nil
}
