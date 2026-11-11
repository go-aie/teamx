package room

import (
	"fmt"
	"path/filepath"
)

// Frozen creation sources must agree even after their templates disappear.
func validRoomSource(e roomIndexEntry) bool {
	return validSelection(e.Selection) && e.Selection.TeamID == e.TeamID &&
		e.Selection.AgentRoleID == e.AgentRoleID && e.Selection.IsDirect == e.IsDirect
}

func (m *roomManager) validateRoomSnapshot(c managedRoomConfig) error {
	var snapshot RoomSnapshot
	if err := readSnapshot(filepath.Join(m.roomDir(c.RoomID), "room.json"), &snapshot, maxRoomFileBytes); err != nil {
		return err
	}
	return validateManagedSnapshot(c, snapshot)
}

func validateManagedSnapshot(c managedRoomConfig, snapshot RoomSnapshot) error {
	if err := snapshot.validate(); err != nil {
		return fmt.Errorf("%w: room.json: %w", ErrInvalid, err)
	}
	if snapshot.RoomIdentity != c.RoomIdentity || snapshot.IsDirect != c.IsDirect {
		return fmt.Errorf("%w: room snapshot and configuration disagree", ErrIdentity)
	}
	if c.IsDirect && (len(c.Members) != 1 || snapshot.Members[0].AgentID != c.Members[0].AgentID ||
		snapshot.Members[0].MembershipID != c.Members[0].MembershipID || snapshot.Members[0].Runner != c.Members[0].Runner) {
		return fmt.Errorf("%w: direct conversation membership differs from creation", ErrIdentity)
	}
	return nil
}
