package room

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func workspaceRequest(t *testing.T, m *roomManager, room roomSummary, target workspaceConfig) workspaceApplyRequest {
	t.Helper()
	id, _ := newOpaqueID()
	req := workspaceApplyRequest{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, OperationID: id, ExpectedVersion: 1, Target: target}
	if target.Binding != nil {
		file, err := m.platforms.Load()
		if err != nil {
			t.Fatal(err)
		}
		var c managedPlatform
		for _, v := range file.Platforms {
			if v.Provider == "gitlab" && v.URL == target.Binding.URL && (target.Binding.PlatformID == "" || target.Binding.PlatformID == v.ID) {
				c.Platform = v.Platform
				break
			}
		}
		if c.ID == "" {
			q := platformTestRequest(m)
			q.URL = target.Binding.URL
			c = platformTestSave(t, m, q)
		}
		binding := *target.Binding
		binding.PlatformID = c.ID
		binding.PlatformGeneration = ""
		binding.ProjectID = 0
		req.Target.Binding = &binding
		req.PlatformVersion = c.Version
	}
	return req
}
func awaitWorkspace(t *testing.T, m *roomManager) workspaceOperation {
	t.Helper()
	m.mu.Lock()
	op := m.index.WorkspaceOperations[len(m.index.WorkspaceOperations)-1]
	done := m.runtimes[op.RoomIdentity].operationDone
	m.mu.Unlock()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("workspace operation did not settle")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneWorkspaceOperation(m.index.WorkspaceOperations[len(m.index.WorkspaceOperations)-1])
}
func applyWorkspace(t *testing.T, m *roomManager, req workspaceApplyRequest) workspaceOperation {
	t.Helper()
	r := managerHTTP(m, "/v1/workspace/apply", req)
	var op workspaceOperation
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &op) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	if strings.Contains(r.Body.String(), "prepared_config") || strings.Contains(r.Body.String(), "previous_config") {
		t.Fatal("private config exposed")
	}
	return op
}
func queryWorkspace(t *testing.T, m *roomManager, room roomSummary, id string) workspaceResponse {
	t.Helper()
	q := url.Values{"service_epoch": {m.epoch}, "caller_id": {humanCaller.ID}, "room_id": {room.RoomID}, "room_instance": {room.RoomInstance}, "operation_id": {id}}
	w := httptest.NewRecorder()
	m.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/workspace?"+q.Encode(), nil))
	var response workspaceResponse
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "prepared_config") || strings.Contains(w.Body.String(), "previous_config") || strings.Contains(w.Body.String(), "binary_path") {
		t.Fatal("private runtime leaked")
	}
	return response
}
func retryWorkspaceHTTP(t *testing.T, m *roomManager, room roomSummary, id string) {
	t.Helper()
	r := managerHTTP(m, "/v1/workspace/retry", map[string]any{"service_epoch": m.epoch, "caller": humanCaller, "room_id": room.RoomID, "room_instance": room.RoomInstance, "operation_id": id})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
}
