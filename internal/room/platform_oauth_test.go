package room

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
)

// Business fixtures install verified OAuth grants through the existing snapshot
// owner, without performing a remote login or modifying any real account.
func installOAuthFixture(t *testing.T, m *roomManager, c platformConnection, expiry time.Time) connection.Account {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	file, err := m.connections.Load()
	if err != nil {
		t.Fatal(err)
	}
	file.Version = connection.OAuthSnapshotVersion
	file.InstallationID = strings.Repeat("e", 32)
	app := connection.OAuthApplication{ID: strings.Repeat("b", 32), Origin: c.URL, OwnerID: 101, ClientID: "fixture-client", ClientSecret: "fixture-client-secret", RedirectURI: "http://127.0.0.1:8088/callback", Scopes: []string{"api"}}
	cred := connection.OAuthCredential{ID: strings.Repeat("c", 32), ApplicationID: app.ID, UserID: 101, GrantGeneration: 1, Scopes: []string{"api"}, State: "active", AccessToken: "fixture-access", RefreshToken: "fixture-refresh", ExpiresAt: expiry.UTC(), TokenRevision: 1}
	file.OAuthApplications = []connection.OAuthApplication{app}
	file.OAuthCredentials = []connection.OAuthCredential{cred}
	e := platform.AuthorizationEvidence{Origin: app.Origin, ApplicationID: app.ID, ClientID: app.ClientID, CredentialID: cred.ID, UserID: 101, Generation: 1, Scopes: "api"}
	b, _ := json.Marshal(e)
	for i := range file.Connections {
		file.Connections[i].CredentialKind = "oauth"
		file.Connections[i].CredentialID = cred.ID
		file.Connections[i].IdentityBinding.CredentialFingerprint = fmt.Sprintf("%x", sha256.Sum256(b))
	}
	if err = m.store.save(m.connectionsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	file, err = m.connections.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := connection.ResolveAuthorization(file, file.Connections[0])
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func (m *roomManager) connectionsPathForTest() string {
	return m.store.dir + "/platform-connections.json"
}
func oauthTestManager(t *testing.T, expiry time.Time) (*roomManager, connection.Account) {
	t.Helper()
	m := testManager(t, t.TempDir(), nil)
	c := platformTestSave(t, m, platformTestRequest(m))
	c = verifyTodoAccount(t, m, c, 101)
	return m, installOAuthFixture(t, m, c, expiry)
}
func oauthTransport(posts *atomic.Int32, before func(*http.Request) error) http.RoundTripper {
	return workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		body := ""
		status := 200
		switch r.URL.Path {
		case "/oauth/token":
			posts.Add(1)
			if before != nil {
				if err := before(r); err != nil {
					return nil, err
				}
			}
			body = `{"access_token":"rotated-access","refresh_token":"rotated-refresh","token_type":"bearer","expires_in":600}`
		case "/oauth/token/info":
			body = `{"resource_owner_id":101,"scope":["api"],"application":{"uid":"fixture-client"}}`
		case "/api/v4/user":
			body = `{"id":101,"username":"user101"}`
		default:
			return nil, errors.New("unexpected endpoint")
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
}
