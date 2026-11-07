package room

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestWorkspaceOperationPersistenceNeverAppliesOrReplays(t *testing.T) {
	root := t.TempDir()
	profiles := managerTestProfiles(t)
	m := testManager(t, root, profiles)
	room := managerCreate(t, m, "operation")
	configPath := filepath.Join(m.roomDir(room.RoomID), "room-config.json")
	before, _ := os.ReadFile(configPath)
	op := testWorkspaceOperation(t, m, room, workspaceConfig{Root: "future-workspace"})
	if err := m.saveWorkspaceOperation(op); err != nil {
		t.Fatal(err)
	}
	// The record and copyIndex must own their slices and nested config values.
	op.Members[0].Workspace = "caller-mutation"
	copy := m.copyIndex()
	copy.WorkspaceOperations[0].Members[0].Workspace = "copy-mutation"
	if m.index.WorkspaceOperations[0].Members[0].Workspace == "caller-mutation" || m.index.WorkspaceOperations[0].Members[0].Workspace == "copy-mutation" {
		t.Fatal("record aliases caller data")
	}
	if _, err := os.Stat(filepath.Join(m.roomDir(room.RoomID), "future-workspace")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("saving intent prepared the target", err)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	m, err := newRoomManager(root, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	got := m.index.WorkspaceOperations[0]
	if got.Status != "failed" || got.ErrorCode != "interrupted" || got.Target.Root != "future-workspace" || got.Phase != "waiting" {
		t.Fatal("lost interrupted intent/progress", got)
	}
	after, _ := os.ReadFile(configPath)
	if !bytes.Equal(before, after) {
		t.Fatal("restart published target configuration")
	}
	if _, err := m.activationConfig(room.RoomIdentity); !errors.Is(err, ErrBusy) {
		t.Fatal("unfinished workspace operation allowed activation", err)
	}
	if _, err := m.startOpen("must-not-open", room.RoomIdentity); !errors.Is(err, ErrBusy) {
		t.Fatal("unfinished workspace operation allowed activation receipt", err)
	}
	for _, path := range []string{"future-workspace", "agents"} {
		if _, err := os.Stat(filepath.Join(m.roomDir(room.RoomID), path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("restart prepared a directory or created a Session", err)
		}
	}
}

func TestWorkspaceOperationIdentityProgressAndValidation(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "progress")
	op := testWorkspaceOperation(t, m, room, workspaceConfig{Root: "next"})
	if err := m.saveWorkspaceOperation(op); err != nil {
		t.Fatal(err)
	}
	if err := m.saveWorkspaceOperation(op); err != nil {
		t.Fatal("same record should be reusable", err)
	}
	changed := cloneWorkspaceOperation(op)
	changed.Target.Root = "different"
	for i := range changed.Members {
		changed.Members[i].Workspace = workspaceMemberPath(changed.Target, changed.Members[i].MembershipID)
	}
	if err := m.saveWorkspaceOperation(changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed target under stable operation ID", err)
	}
	other := cloneWorkspaceOperation(op)
	other.OperationID = strings.Repeat("b", 32)
	if err := m.saveWorkspaceOperation(other); !errors.Is(err, ErrBusy) {
		t.Fatal("allowed overlapping operations", err)
	}
	op.Phase, op.Status, op.ErrorCode = "preparing", "failed", "preparation_failed"
	if err := m.saveWorkspaceOperation(op); err != nil {
		t.Fatal(err)
	}
	regressed := cloneWorkspaceOperation(op)
	regressed.Phase = "waiting"
	if err := m.saveWorkspaceOperation(regressed); !errors.Is(err, ErrConflict) {
		t.Fatal("progress moved backwards", err)
	}
	for _, mutate := range []func(*workspaceOperation){
		func(op *workspaceOperation) { op.ErrorCode = "a-raw-PAT-must-never-be-a-diagnostic" },
		func(op *workspaceOperation) { op.TargetVersion++ },
		func(op *workspaceOperation) { op.Phase = "unknown" },
		func(op *workspaceOperation) { op.Members[0].Workspace = "../outside" },
		func(op *workspaceOperation) { op.ConfigCommitted = true },
		func(op *workspaceOperation) { op.SessionsUpdated = []string{op.Members[0].MembershipID} },
	} {
		bad := cloneWorkspaceOperation(op)
		mutate(&bad)
		if err := bad.validate(); err == nil {
			t.Fatal("accepted invalid progress record")
		}
	}
	op.Status, op.ErrorCode, op.Phase, op.ConfigCommitted = "completed", "", "completed", true
	for _, member := range op.Members {
		op.SessionsUpdated = append(op.SessionsUpdated, member.MembershipID)
	}
	if err := m.saveWorkspaceOperation(op); !errors.Is(err, ErrConflict) {
		t.Fatal("completed without effective config commit", err)
	}
	// Simulate a later executor's config commit; this test does not execute any
	// preparation or Session rotation.
	c, _ := readManagedRoom(t, m, room)
	c.Workspace, c.WorkspaceVersion = &op.Target, op.TargetVersion
	for i := range c.Members {
		c.Members[i].Workspace = op.Members[i].Workspace
	}
	if err := m.store.save(filepath.Join(m.roomDir(room.RoomID), "room-config.json"), c, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	if err := m.saveWorkspaceOperation(op); err != nil {
		t.Fatal(err)
	}
	if err := m.checkWorkspaceOperations(room.RoomIdentity); err != nil {
		t.Fatal("completed receipt still blocks room", err)
	}
}

func TestWorkspaceOperationCommitUnknownFreezesManager(t *testing.T) {
	for _, afterRename := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-rename", true: "after-rename"}[afterRename], func(t *testing.T) {
			root := t.TempDir()
			profiles := managerTestProfiles(t)
			m := testManager(t, root, profiles)
			room := managerCreate(t, m, "uncertain-op")
			op := testWorkspaceOperation(t, m, room, workspaceConfig{Root: "next"})
			rename := m.store.ops.rename
			m.store.ops.rename = func(from, to string) error {
				if afterRename {
					if err := rename(from, to); err != nil {
						return err
					}
				}
				return errors.New("lost replacement result")
			}
			if err := m.saveWorkspaceOperation(op); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal(err)
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			m, err := newRoomManager(root, 7)
			if err != nil {
				t.Fatal(err)
			}
			defer m.close()
			want := 0
			if afterRename {
				want = 1
			}
			if len(m.index.WorkspaceOperations) != want {
				t.Fatal("did not reload actual replacement outcome")
			}
		})
	}
}

func TestWorkspaceTargetCannotNestInsideAnotherMemberDirectory(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "overlap")
	c, _ := readManagedRoom(t, m, room)
	op := testWorkspaceOperation(t, m, room, workspaceConfig{Root: c.Members[0].Workspace + "/nested"})
	if err := m.saveWorkspaceOperation(op); !errors.Is(err, ErrInvalid) {
		t.Fatal("target directories overlap another member's existing workspace", err)
	}
}

func TestWorkspaceOperationCorruptionDoesNotRewriteEvidence(t *testing.T) {
	root := t.TempDir()
	profiles := managerTestProfiles(t)
	m := testManager(t, root, profiles)
	room := managerCreate(t, m, "bad-record")
	op := testWorkspaceOperation(t, m, room, workspaceConfig{Root: "next"})
	if err := m.saveWorkspaceOperation(op); err != nil {
		t.Fatal(err)
	}
	index := m.copyIndex()
	index.WorkspaceOperations[0].Members[0].MembershipID = "other-membership"
	index.WorkspaceOperations[0].Members[0].Workspace = "next/other-membership"
	if err := m.saveIndex(index); err != nil {
		t.Fatal(err)
	}
	name := m.indexPath()
	before, _ := os.ReadFile(name)
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	if other, err := newRoomManager(root, 7); err == nil {
		other.close()
		t.Fatal("startup accepted a mismatched operation membership")
	}
	after, _ := os.ReadFile(name)
	if !bytes.Equal(before, after) {
		t.Fatal("startup rewrote corrupt evidence")
	}
}
