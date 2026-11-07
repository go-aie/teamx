package room

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-aie/teamx/internal/platform/connection"
)

// Real acceptance uses an explicitly selected, dedicated test authorization.
// Its source installation must be stopped: refresh rotates the shared remote
// grant. The source file is not changed; never use a live service snapshot.
func selectedRealOAuthSnapshot(t *testing.T, origin string) (connection.Snapshot, platformConnection) {
	t.Helper()
	path := os.Getenv("TEAMX_REAL_OAUTH_SNAPSHOT")
	if !filepath.IsAbs(path) {
		t.Fatal("absolute TEAMX_REAL_OAUTH_SNAPSHOT required; log in through TeamX first")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("private OAuth snapshot required")
	}
	var file connection.Snapshot
	if readSnapshot(path, &file, 2<<20) != nil || file.Version != connection.OAuthSnapshotVersion {
		t.Fatal("current OAuth snapshot required")
	}
	for _, c := range file.Connections {
		if c.Provider != "gitlab" || c.URL != origin || c.CredentialKind != "oauth" || (os.Getenv("TEAMX_REAL_OAUTH_CONNECTION") != "" && c.ID != os.Getenv("TEAMX_REAL_OAUTH_CONNECTION")) {
			continue
		}
		cred, app, err := connection.OAuthRecord(file, c.CredentialID)
		if err != nil || cred.State != "active" {
			continue
		}
		return connection.Snapshot{Version: file.Version, InstallationID: file.InstallationID, Connections: []connection.Record{c}, OAuthApplications: []connection.OAuthApplication{app}, OAuthCredentials: []connection.OAuthCredential{cred}}, platformConnection{Connection: c.Connection}
	}
	t.Fatal("no active OAuth connection for the requested instance")
	return connection.Snapshot{}, platformConnection{}
}

func realOAuthConnection(t *testing.T, m *roomManager, origin string) platformConnection {
	t.Helper()
	file, c := selectedRealOAuthSnapshot(t, origin)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.save(m.connectionsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := m.connections.Load(); err != nil {
		t.Fatal("invalid OAuth acceptance snapshot")
	}
	return c
}

func selectedRealAccessToken(t *testing.T, origin string) string {
	t.Helper()
	file, _ := selectedRealOAuthSnapshot(t, origin)
	return file.OAuthCredentials[0].AccessToken
}
