package room

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/connection"
)

func TestGitLabSetupIsLocalAndRequiresExplicitImport(t *testing.T) {
	m := testManager(t, t.TempDir(), nil)
	m.config.endpoint = "http://127.0.0.1:8088"
	m.platformHTTP = workspaceHTTPFunc(func(*http.Request) (*http.Response, error) { t.Fatal("setup contacted GitLab"); return nil, ErrRequest })
	req := gitlabSetupRequest{ServiceEpoch: m.epoch, Caller: humanCaller, Origin: "https://gitlab.invalid", RepositoryAccess: true}
	r := managerHTTP(m, gitlabCookiePath+"prepare", req)
	var view gitlabSetupView
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &view) != nil || view.Status != "configuration_required" || view.RedirectURI != m.config.endpoint+gitlabCallbackPath {
		t.Fatal(r.Code, r.Body.String())
	}
	if _, err := os.Stat(m.connectionsPathForTest()); !os.IsNotExist(err) {
		t.Fatal("prepare wrote a snapshot")
	}
	req.Client = &gitlabClientImport{ClientID: "client", ClientSecret: "private-app-secret", RedirectURI: view.RedirectURI, Scopes: []string{"api", "write_repository"}}
	r = managerHTTP(m, gitlabCookiePath+"initialize", req)
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &view) != nil || view.Status != "owner_verification_required" || strings.Contains(r.Body.String(), "private-app-secret") {
		t.Fatal(r.Code, r.Body.String())
	}
	file, err := m.connections.Load()
	if err != nil || len(file.Connections) != 0 || len(file.OAuthCredentials) != 0 || len(file.OAuthClientImports) != 1 {
		t.Fatal("import authorized business access", err)
	}
	req.Client = nil
	r = managerHTTP(m, gitlabCookiePath+"prepare", req)
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &view) != nil || view.Status != "owner_verification_required" {
		t.Fatal("import not reused")
	}
}

func TestGitLabRejectsRemovedCredentialInputs(t *testing.T) {
	m := testManager(t, t.TempDir(), nil)
	for _, field := range []string{"pat", "token_env"} {
		q := map[string]any{"service_epoch": m.epoch, "caller": humanCaller, "id": strings.Repeat("a", 32), "name": "GitLab", "provider": "gitlab", "url": "https://gitlab.invalid", field: "never-echo-this"}
		r := managerHTTP(m, "/v1/platform/connections/save", q)
		if r.Code != 400 || strings.Contains(r.Body.String(), "never-echo-this") {
			t.Fatal("removed credential input accepted or reflected")
		}
	}
	if r := managerHTTP(m, "/v1/workspace/tool-credential", map[string]any{}); r.Code == 200 {
		t.Fatal("removed token exchange still exists")
	}
}

func TestOAuthConnectionMetadataPreservesGrant(t *testing.T) {
	m := testManager(t, t.TempDir(), nil)
	c := platformTestSave(t, m, platformTestRequest(m))
	file, _ := m.connections.Load()
	before, err := connection.ResolveAuthorization(file, file.Connections[0])
	if err != nil {
		t.Fatal(err)
	}
	q := platformTestRequest(m)
	q.Version = c.Version
	q.Name = "Renamed"
	updated, err := m.updatePlatformConnection(q, false)
	if err != nil {
		t.Fatal(err)
	}
	file, _ = m.connections.Load()
	after, err := connection.ResolveAuthorization(file, file.Connections[0])
	if err != nil || updated.Name != "Renamed" || updated.CredentialVersion != c.CredentialVersion || after.Authorization != before.Authorization || after.Identity != before.Identity {
		t.Fatal("metadata edit changed authorization", err)
	}
}
