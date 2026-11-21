package room

import (
	"strings"
	"testing"
	"time"
)

func TestWorkspaceOperationValidation(t *testing.T) {
	now := time.Now().UTC()
	op := workspaceOperation{RoomIdentity: RoomIdentity{RoomID: "room", RoomInstance: "generation"}, OperationID: strings.Repeat("a", 32),
		PreviousVersion: 1, TargetVersion: 2, Target: workspaceConfig{Root: "next"}, Status: "failed", Phase: "preparing", ErrorCode: "preparation_failed", CreatedAt: now, UpdatedAt: now,
		Members: []workspaceMemberPathRecord{{AgentID: "A", MembershipID: "generation-A", Workspace: "next/generation-A"}}}
	if err := op.validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*workspaceOperation)
	}{
		{"raw-error", func(op *workspaceOperation) { op.ErrorCode = "a-raw-PAT-must-never-be-a-diagnostic" }},
		{"target-version", func(op *workspaceOperation) { op.TargetVersion++ }},
		{"phase", func(op *workspaceOperation) { op.Phase = "unknown" }},
		{"member-path", func(op *workspaceOperation) { op.Members[0].Workspace = "../outside" }},
		{"early-commit", func(op *workspaceOperation) { op.ConfigCommitted = true }},
		{"early-session", func(op *workspaceOperation) { op.SessionsUpdated = []string{op.Members[0].MembershipID} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := cloneWorkspaceOperation(op)
			tc.mutate(&bad)
			if err := bad.validate(); err == nil {
				t.Fatal("accepted invalid progress record")
			}
		})
	}
}

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
