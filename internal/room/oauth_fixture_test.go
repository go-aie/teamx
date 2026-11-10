package room

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/registry"
)

// Reauthorization exercises the same invalidate/settle/atomic-grant commit
// boundary as the browser callback without introducing PAT fixtures.
func testReauthorizePlatform(m *roomManager, req managedPlatformRequest) (managedPlatform, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	file, err := m.platforms.Load()
	if err != nil {
		return managedPlatform{}, err
	}
	var c registry.Record
	for _, v := range file.Platforms {
		if v.ID == req.ID && v.Version == req.Version {
			c = v
		}
	}
	if c.ID == "" {
		return managedPlatform{}, ErrConflict
	}
	refs, err := m.managedPlatformReferencesLocked(c.ID)
	if err != nil {
		return managedPlatform{}, err
	}
	if !samePlatformReferences(refs, req.ReferencedRooms) {
		return managedPlatform{}, ErrConflict
	}
	cred, app, err := registry.OAuthRecord(file, c.CredentialID)
	if err != nil {
		return managedPlatform{}, err
	}
	if err = m.invalidatePlatformQueriesLocked(c.ID); err != nil {
		return managedPlatform{}, err
	}
	if err = m.settlePlatformQueriesLocked(c.ID); err != nil {
		return managedPlatform{}, err
	}
	_, err = m.platforms.CommitOAuthLogin([]registry.Record{c}, c.ID, c.Name, app, "", "tester", platform.OAuthTokens{AccessToken: platform.NewSecret("rotated-token"), RefreshToken: platform.NewSecret("rotated-refresh"), ExpiresAt: time.Now().Add(time.Hour).UTC(), Scopes: cred.Scopes}, c.Validation)
	if err != nil {
		return managedPlatform{}, err
	}
	file, err = m.platforms.Load()
	if err != nil {
		return managedPlatform{}, err
	}
	for _, v := range file.Platforms {
		if v.ID == c.ID {
			return managedPlatform{Platform: registry.Public(file, v)}, nil
		}
	}
	return managedPlatform{}, errors.New("missing reauthorized connection")
}

func platformTestReauthorize(t *testing.T, m *roomManager, req managedPlatformRequest) managedPlatform {
	t.Helper()
	c, err := testReauthorizePlatform(m, req)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func changeTestPlatformRevision(t *testing.T, m *roomManager, id string) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	file, err := m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	for i := range file.Platforms {
		if file.Platforms[i].ID == id {
			file.Platforms[i].Version++
		}
	}
	if err = m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
}

func authorizeTestPlatform(t *testing.T, m *roomManager, c managedPlatform, user int64, token string) managedPlatform {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	file, err := m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	appID := fmt.Sprintf("%x", sha256.Sum256([]byte(c.ID+"app")))[:32]
	credID := fmt.Sprintf("%x", sha256.Sum256([]byte(c.ID+"grant")))[:32]
	app := registry.OAuthApplication{ID: appID, Origin: c.URL, OwnerID: user, ClientID: "client-" + c.ID, ClientSecret: "secret-" + c.ID, RedirectURI: "http://127.0.0.1:8088" + gitlabCallbackPath, Scopes: []string{"api", "write_repository"}}
	cred := registry.OAuthCredential{ID: credID, ApplicationID: appID, UserID: user, GrantGeneration: 1, TokenRevision: 1, State: "active", AccessToken: token, RefreshToken: "refresh-" + c.ID, ExpiresAt: time.Now().Add(time.Hour).UTC(), Scopes: app.Scopes}
	for i, v := range file.OAuthCredentials {
		if v.ID == credID {
			cred.GrantGeneration = v.GrantGeneration + 1
			file.OAuthCredentials = append(file.OAuthCredentials[:i], file.OAuthCredentials[i+1:]...)
			break
		}
	}
	for i, v := range file.OAuthApplications {
		if v.ID == appID {
			file.OAuthApplications = append(file.OAuthApplications[:i], file.OAuthApplications[i+1:]...)
			break
		}
	}
	file.OAuthApplications = append(file.OAuthApplications, app)
	file.OAuthCredentials = append(file.OAuthCredentials, cred)
	e := platform.AuthorizationEvidence{Origin: app.Origin, ApplicationID: app.ID, ClientID: app.ClientID, CredentialID: cred.ID, UserID: user, Generation: cred.GrantGeneration, Scopes: "api write_repository"}
	data, _ := json.Marshal(e)
	for i, v := range file.Platforms {
		if v.ID == c.ID {
			v.CredentialKind = "oauth"
			v.CredentialID = cred.ID
			v.CredentialConfigured = true
			v.Validation = platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), UserID: user, Username: fmt.Sprintf("user%d", user)}
			v.IdentityBinding = &registry.IdentityBinding{Generation: v.Generation, CredentialVersion: v.CredentialVersion, CredentialFingerprint: fmt.Sprintf("%x", sha256.Sum256(data))}
			file.Platforms[i] = v
			c.Platform = v.Platform
		}
	}
	if err = m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	return c
}
