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
	if strings.Contains(r.Body.String(), "client_secret") {
		t.Fatal("application secret field exposed")
	}
	for _, secret := range []string{"test-platform-secret", "private-client-secret", "remote-diagnostic-secret"} {
		if strings.Contains(r.Body.String(), secret) {
			t.Fatal("secret leaked")
		}
	}
	return view
}

func TestGitLabSetupCorrectsPendingApplicationAndRejectsStaleOrActiveEdits(t *testing.T) {
	m := testManager(t, t.TempDir(), nil)
	m.config.endpoint = "http://127.0.0.1:8088"
	req := gitlabSetupRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Origin: "https://gitlab.invalid", Client: &gitlabClientImport{ClientID: "wrong-client", ClientSecret: "wrong-secret", RedirectURI: m.config.endpoint + gitlabCallbackPath, Scopes: []string{"api"}}}
	initialize := func(req gitlabSetupRequest) gitlabSetupView {
		t.Helper()
		response := managerHTTP(m, gitlabCookiePath+"initialize", req)
		var view gitlabSetupView
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &view) != nil || view.Status != "owner_verification_required" || strings.Contains(response.Body.String(), req.Client.ClientSecret) {
			t.Fatal(response.Code, response.Body.String())
		}
		return view
	}
	first := initialize(req)
	req.ClientImportID = first.ClientImportID
	req.Client.ClientSecret = "correct-secret"
	done := make(chan struct{})
	m.gitlabAuthSessions = map[string]*gitlabAuthSession{"pending": {importID: first.ClientImportID, app: connection.OAuthApplication{Origin: req.Origin, ClientID: "wrong-client"}, done: done}}
	t.Cleanup(func() { m.gitlabAuthSessions = nil })
	if response := managerHTTP(m, gitlabCookiePath+"initialize", req); response.Code != http.StatusConflict {
		t.Fatal("active authorization edit accepted", response.Code, response.Body.String())
	}
	close(done)
	second := initialize(req)
	if response := managerHTTP(m, gitlabCookiePath+"initialize", req); response.Code != http.StatusConflict {
		t.Fatal("stale configuration edit accepted", response.Code, response.Body.String())
	}
	req.ClientImportID = second.ClientImportID
	req.Client.ClientID = "correct-client"
	final := initialize(req)
	req.Client, req.ClientImportID = nil, ""
	response := managerHTTP(m, gitlabCookiePath+"prepare", req)
	var prepared gitlabSetupView
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &prepared) != nil || prepared.ClientImportID != final.ClientImportID || prepared.ClientID != "correct-client" || strings.Contains(response.Body.String(), "client_secret") {
		t.Fatal("prepare reused wrong application", response.Code, response.Body.String())
	}
	file, err := m.connections.Load()
	if err != nil || len(file.OAuthClientImports) != 1 || file.OAuthClientImports[0].ClientSecret != "correct-secret" || file.OAuthClientImports[0].ClientID != "correct-client" || len(file.Connections) != 0 {
		t.Fatal("correction not saved independently of login", err)
	}
}
