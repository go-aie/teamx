package room

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-aie/teamx/internal/platform/registry"
)

// Real acceptance uses an explicitly selected, dedicated test authorization.
// Its source installation must be stopped: refresh rotates the shared remote
// grant. The source file is not changed; never use a live service snapshot.
func selectedRealOAuthSnapshot(t *testing.T, origin string) (registry.Snapshot, managedPlatform) {
	t.Helper()
	path := os.Getenv("TEAMX_REAL_OAUTH_SNAPSHOT")
	if !filepath.IsAbs(path) {
		t.Fatal("absolute TEAMX_REAL_OAUTH_SNAPSHOT required; log in through TeamX first")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("private OAuth snapshot required")
	}
	var file registry.Snapshot
	if readSnapshot(path, &file, 2<<20) != nil || file.Version != registry.OAuthSnapshotVersion {
		t.Fatal("current OAuth snapshot required")
	}
	for _, c := range file.Platforms {
		if c.Provider != "gitlab" || c.URL != origin || c.CredentialKind != "oauth" || (os.Getenv("TEAMX_REAL_OAUTH_CONNECTION") != "" && c.ID != os.Getenv("TEAMX_REAL_OAUTH_CONNECTION")) {
			continue
		}
		cred, app, err := registry.OAuthRecord(file, c.CredentialID)
		if err != nil || cred.State != "active" {
			continue
		}
		return registry.Snapshot{Version: file.Version, InstallationID: file.InstallationID, Platforms: []registry.Record{c}, OAuthApplications: []registry.OAuthApplication{app}, OAuthCredentials: []registry.OAuthCredential{cred}}, managedPlatform{Platform: c.Platform}
	}
	t.Fatal("no active OAuth connection for the requested instance")
	return registry.Snapshot{}, managedPlatform{}
}

func realOAuthPlatform(t *testing.T, m *roomManager, origin string) managedPlatform {
	t.Helper()
	file, c := selectedRealOAuthSnapshot(t, origin)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := m.platforms.Load(); err != nil {
		t.Fatal("invalid OAuth acceptance snapshot")
	}
	return c
}

func selectedRealAccessToken(t *testing.T, origin string) string {
	t.Helper()
	file, _ := selectedRealOAuthSnapshot(t, origin)
	return file.OAuthCredentials[0].AccessToken
}
