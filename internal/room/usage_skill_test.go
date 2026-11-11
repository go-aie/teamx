package room

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUsageSkillIdentityAndFixedResourceBoundary(t *testing.T) {
	m, _, _, connect := assistantConnectFixture(t)
	req := usageSkillRequest{ServiceInfo: connect.ServiceInfo, Caller: connect.Caller}
	for _, change := range []string{"human", "epoch", "room", "membership"} {
		bad := req
		switch change {
		case "human":
			bad.Caller = humanCaller
		case "epoch":
			bad.ServiceEpoch = "earlier-service"
		case "room":
			bad.RoomInstance = "another-room-instance"
		case "membership":
			bad.Caller.MembershipID = "replaced-membership"
		}
		if w := managerHTTP(m, "/v1/skills/usage", bad); w.Code != http.StatusForbidden {
			t.Fatal("wrong identity read usage guidance", change, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{
		`{"path":"skills/gitlab/SKILL.md"}`,
		`{"tool":"gitlab","args":[]}`,
		`{"caller":{"kind":"agent","id":"assistant"},"source_seq":1}`,
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/skills/usage", strings.NewReader(body))
		w := httptest.NewRecorder()
		m.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatal("usage endpoint accepted resource/operation input", w.Code, w.Body.String())
		}
	}
	if w := managerHTTP(m, "/v1/skills/usage", nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatal("usage endpoint accepted GET", w.Code)
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	// Browser requests do not become Agent requests by setting caller fields.
	r := httptest.NewRequest(http.MethodPost, "/v1/skills/usage", bytes.NewReader(data))
	r.Header.Set("Origin", "http://teamx.invalid")
	w := httptest.NewRecorder()
	m.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("browser crossed the member-only boundary", w.Code)
	}
	w = managerHTTP(m, "/v1/skills/usage", req)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/markdown") {
		t.Fatal("valid guidance read failed", w.Code, w.Header())
	}
}
