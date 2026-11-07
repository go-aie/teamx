package room

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-aie/teamx/internal/platform/connection"
)

type gitlabSetupHarness struct {
	m      *roomManager
	req    gitlabSetupRequest
	server *httptest.Server
	mu     sync.Mutex
	remote map[string]any
}

func newGitlabSetupHarness(t *testing.T) *gitlabSetupHarness {
	t.Helper()
	h := &gitlabSetupHarness{}
	h.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected remote setup request")
		http.NotFound(w, r)
	}))
	t.Cleanup(h.server.Close)
	h.m = testManager(t, t.TempDir(), nil)
	h.m.config.endpoint = "http://127.0.0.1:8088"
	req := platformTestRequest(h.m)
	req.URL = h.server.URL
	c, err := h.m.updatePlatformConnection(req, false)
	if err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	file, _ := h.m.connections.Load()
	file.OAuthApplications = []connection.OAuthApplication{{ID: strings.Repeat("b", 32), Origin: c.URL, OwnerID: 101, ClientID: "private-client-id", ClientSecret: "private-client-secret", RedirectURI: h.m.config.endpoint + gitlabCallbackPath, Scopes: []string{"api"}}}
	err = h.m.store.save(h.m.connectionsPathForTest(), file, 2<<20)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.req = gitlabSetupRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: c.ID, Version: c.Version}
	return h
}
func gitlabSetupHTTP(t *testing.T, h *gitlabSetupHarness, action string, req gitlabSetupRequest) gitlabSetupView {
	t.Helper()
	r := managerHTTP(h.m, "/v1/platform/connections/gitlab/"+action, req)
	var view gitlabSetupView
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &view) != nil {
		t.Fatal(r.Code, r.Body.String())
	}
	for _, secret := range []string{"test-platform-secret", "private-client-secret", "remote-diagnostic-secret"} {
		if strings.Contains(r.Body.String(), secret) {
			t.Fatal("secret leaked")
		}
	}
	return view
}
