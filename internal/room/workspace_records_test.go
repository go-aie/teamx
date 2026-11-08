package room

import (
	"testing"
	"time"
)

func testWorkspaceOperation(t *testing.T, m *roomManager, room roomSummary, target workspaceConfig) workspaceOperation {
	t.Helper()
	c, _ := readManagedRoom(t, m, room)
	id, err := newOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	op := workspaceOperation{RoomIdentity: room.RoomIdentity, OperationID: id, PreviousVersion: c.WorkspaceVersion,
		TargetVersion: c.WorkspaceVersion + 1, Target: target, Status: "running", Phase: "waiting", CreatedAt: now, UpdatedAt: now}
	for _, member := range c.Members {
		op.Members = append(op.Members, workspaceMemberPathRecord{AgentID: member.AgentID, MembershipID: member.MembershipID,
			Workspace: workspaceMemberPath(target, member.MembershipID)})
	}
	return op
}
