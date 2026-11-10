package room

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestOnboardingTargetsReadClosedRoomWithoutOpeningRuntime(t *testing.T) {
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "onboarding-targets")
	_, snapshot := readManagedRoom(t, m, room)
	q := url.Values{"service_epoch": {m.epoch}, "caller_id": {humanCaller.ID}, "room_id": {room.RoomID}, "room_instance": {room.RoomInstance}}
	read := func(method string, query url.Values) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		m.servePlatforms(w, httptest.NewRequest(method, "/v1/platforms/onboarding/targets?"+query.Encode(), nil))
		return w
	}
	w := read(http.MethodGet, q)
	var result struct {
		Members []MemberIdentity `json:"members"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
		t.Fatal(w.Code, w.Body.String())
	}
	expected := []MemberIdentity{}
	for _, member := range snapshot.Members {
		if member.Runner.Kind == "dsh" {
			expected = append(expected, MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID})
		}
	}
	if len(expected) == 0 || len(expected) != len(result.Members) {
		t.Fatal(expected, result.Members)
	}
	for i, target := range expected {
		if target != result.Members[i] {
			t.Fatal(target, result.Members[i])
		}
	}
	if len(m.runtimes) != 0 {
		t.Fatal("target inspection opened a runtime")
	}
	if w = read(http.MethodPost, q); w.Code != http.StatusMethodNotAllowed {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, key := range []string{"service_epoch", "caller_id", "room_instance", "room_id"} {
		bad := url.Values{}
		for field, values := range q {
			bad[field] = append([]string(nil), values...)
		}
		bad.Set(key, "")
		if w = read(http.MethodGet, bad); w.Code == 200 {
			t.Fatal("accepted incomplete identity", key, w.Body.String())
		}
	}
	q.Set("room_instance", "another-instance")
	if w = read(http.MethodGet, q); w.Code == 200 {
		t.Fatal("accepted a stale Room", w.Body.String())
	}
}
