package room

import (
	"net/http"
	"testing"
)

func TestEnvironmentViewerAccessExpiresOnLogout(t *testing.T) {
	a, token, _ := accessFixture(t)
	var viewerRequest *http.Request
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viewerRequest = r
		w.WriteHeader(200)
	}))
	cookie := accessLogin(t, h, token)
	if response := accessRequest(h, "GET", "/v1/environment/viewer", "", "", "", cookie); response.Code != 200 {
		t.Fatal(response.Code)
	}
	if !environmentAccessCurrent(viewerRequest) {
		t.Fatal("valid viewer session rejected")
	}
	if response := accessRequest(h, "POST", "/v1/access/logout", "", "http://127.0.0.1:8088", "", cookie); response.Code != 200 {
		t.Fatal(response.Code)
	}
	if environmentAccessCurrent(viewerRequest) {
		t.Fatal("viewer session survived logout")
	}
}
