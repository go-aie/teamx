package room

import (
	"path/filepath"
	"testing"
)

func setTestEffectiveWorkspace(t *testing.T, m *roomManager, room roomSummary, w workspaceConfig, version uint64) {
	t.Helper()
	if w.Binding != nil && w.Binding.ConnectionID != "" {
		m.mu.Lock()
		file, err := m.connections.Load()
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		for _, conn := range file.Connections {
			if conn.ID == w.Binding.ConnectionID {
				w.Binding.ConnectionGeneration = conn.Generation
			}
		}
	}
	c, _ := readManagedRoom(t, m, room)
	c.Workspace, c.WorkspaceVersion = &w, version
	for i := range c.Members {
		c.Members[i].Workspace = workspaceMemberPath(w, c.Members[i].MembershipID)
	}
	if err := m.store.save(filepath.Join(m.roomDir(room.RoomID), "room-config.json"), c, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
}
