package room

import (
	"encoding/json"
	"testing"
	"time"
)

func beginOpen(t *testing.T, m *roomManager, target RoomIdentity, id string) roomOpen {
	t.Helper()
	r := managerHTTP(m, "/v1/rooms/open", roomOpenRequest{RoomIdentity: target, OpenID: id, ServiceEpoch: m.epoch, Caller: humanCaller})
	var op roomOpen
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &op) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	return op
}

func awaitOpen(t *testing.T, m *roomManager, target RoomIdentity, id string) roomOpen {
	t.Helper()
	m.mu.Lock()
	rt := m.runtimes[target]
	var done chan struct{}
	if rt != nil {
		done = rt.operationDone
	}
	m.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("open did not settle")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, op := range m.index.Opens {
		if op.OpenID == id {
			return op
		}
	}
	t.Fatal("missing open receipt")
	return roomOpen{}
}

func openOK(t *testing.T, m *roomManager, target RoomIdentity, id string) ServiceInfo {
	t.Helper()
	beginOpen(t, m, target, id)
	op := awaitOpen(t, m, target, id)
	if op.Status != "completed" || op.Service == nil {
		t.Fatal(op)
	}
	return *op.Service
}
