package room

import (
	"encoding/json"
	"testing"
	"time"
)

type deletionRequest struct {
	ServiceEpoch string `json:"service_epoch"`
	Caller       Caller `json:"caller"`
	RoomIdentity
}

func beginDeletion(t *testing.T, m *roomManager, target roomSummary) {
	t.Helper()
	r := managerHTTP(m, "/v1/rooms/delete", deletionRequest{m.epoch, humanCaller, target.RoomIdentity})
	var result roomSummary
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &result) != nil || result.Status != "deleting" {
		t.Fatal(r.Code, r.Body.String())
	}
}

func awaitDeletion(t *testing.T, m *roomManager, target roomSummary) *roomSummary {
	t.Helper()
	m.mu.Lock()
	rt := m.runtimes[target.RoomIdentity]
	if rt == nil {
		m.mu.Unlock()
		return nil
	}
	done := rt.operationDone
	m.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("deletion did not settle")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.index.Rooms {
		if e.RoomIdentity == target.RoomIdentity {
			result := e.roomSummary
			return &result
		}
	}
	return nil
}
