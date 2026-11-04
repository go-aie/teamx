package room

import (
	"context"
	"path/filepath"

	"github.com/go-aie/teamx/internal/platform/notification"
)

type toolCall struct {
	agentID, membershipID string
	owner                 RoomIdentity
	tool, connectionID    string
	cancel                context.CancelFunc
	done                  chan struct{}
}

func (m *roomManager) toolRosterLocked(ctx context.Context, owner RoomIdentity) ([]MemberState, error) {
	if m.active != nil && m.active.info.RoomIdentity == owner && !m.active.closing.Load() {
		list, err := m.active.room.Call[MemberList](ctx, ListMembers{owner})
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
func (m *roomManager) cancelToolsLocked(match func(*toolCall) bool) []<-chan struct{} {
	var done []<-chan struct{}
	for c := range m.toolCalls {
		if match(c) {
			c.cancel()
			done = append(done, c.done)
		}
	}
	return done
}
func (m *roomManager) waitTools(ctx context.Context, owner *RoomIdentity) error {
	m.mu.Lock()
	done := m.cancelToolsLocked(func(c *toolCall) bool { return owner == nil || c.owner == *owner })
	m.mu.Unlock()
	return notification.Wait(ctx, done)
}
