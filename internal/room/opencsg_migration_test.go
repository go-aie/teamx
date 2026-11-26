//go:build darwin || linux

package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/authn"
	"github.com/pelletier/go-toml/v2"
)

func TestOpenCSGConfigMigrationPreservesSettingsAndSecretPath(t *testing.T) {
	home := t.TempDir()
	secret := filepath.Join(home, "casdoor-client-secret")
	if err := os.WriteFile(secret, []byte("private-client-secret"), 0600); err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(hostedServiceSeed, `provider = "opencsg"`, `provider = "casdoor"`, 1) +
		"client_secret_file = '" + filepath.ToSlash(secret) + "'\n# deployment settings\n[deployment.gitlab]\nenabled = true\n"
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	var before map[string]any
	if err := toml.Unmarshal([]byte(source), &before); err != nil {
		t.Fatal(err)
	}
	c, err := readStartupConfig(path, true, false)
	if err != nil || c.Deployment.Authentication.Provider != "opencsg" || !c.legacyOpenCSGProvider {
		t.Fatal("legacy name was not recognized as OpenCSG OAuth", err)
	}
	// Reading configuration is never itself a migration write.
	if data, _ := os.ReadFile(path); string(data) != source {
		t.Fatal("read changed private configuration")
	}
	c, err = migrateOpenCSGConfig(c)
	if err != nil || c.legacyOpenCSGProvider {
		t.Fatal("name migration failed", err)
	}
	var after map[string]any
	if err := toml.Unmarshal(c.source, &after); err != nil {
		t.Fatal(err)
	}
	before["deployment"].(map[string]any)["authentication"].(map[string]any)["provider"] = "opencsg"
	if !reflect.DeepEqual(before, after) || bytes.Contains(c.source, []byte("private-client-secret")) {
		t.Fatal("migration changed other settings or copied a secret")
	}
	if data, err := os.ReadFile(secret); err != nil || string(data) != "private-client-secret" {
		t.Fatal("existing secret path was changed", err)
	}
	again, err := readStartupConfig(path, true, false)
	if err != nil || again.legacyOpenCSGProvider {
		t.Fatal("migration was not durably completed", err)
	}
	again, err = migrateOpenCSGConfig(again)
	if err != nil || !bytes.Equal(again.source, c.source) {
		t.Fatal("repeat migration was not idempotent", err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("migrated configuration is not private", err)
	}
}

func TestOpenCSGConfigMigrationRunsThroughConfiguredServerStartup(t *testing.T) {
	home := t.TempDir()
	var document map[string]any
	if err := toml.Unmarshal([]byte(hostedServiceSeed), &document); err != nil {
		t.Fatal(err)
	}
	document["server"].(map[string]any)["listen"] = "127.0.0.1:0"
	document["deployment"].(map[string]any)["authentication"].(map[string]any)["provider"] = "casdoor"
	document["runtime"] = testTeamDefinition(t).Runtime
	data, err := toml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "config.toml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	selection := personalDeployment(controlledActivationFactory(nil, nil))
	selection.factories.authentication = func(c externalAuthConfig) (authn.Provider, error) {
		if c.Provider != "opencsg" {
			t.Error("startup selected a retired provider")
		}
		return codeProviderFixture{}, nil
	}
	stop := errors.New("stop after authenticated host startup")
	err = runConfiguredServer(t.Context(), []string{"--home", home}, io.Discard, io.Discard, serverOptions{
		deployment: &selection, platformCatalog: defaultPlatformCatalog(),
		// Shared host migration check; this does not claim Linux SRT acceptance.
		execution: func(_ context.Context, c *startupConfig, _ ...string) error { return checkDeploymentRoot(*c) },
		ready:     func(any) error { return stop },
	})
	if !errors.Is(err, stop) {
		t.Fatal("migration prevented the configured host from reaching ready", err)
	}
	c, err := readStartupConfig(path, true, false)
	if err != nil || c.legacyOpenCSGProvider || c.Deployment.Authentication.Provider != "opencsg" {
		t.Fatal("server did not persist the canonical provider", err)
	}
}

func TestOpenCSGProviderRenamePreservesOwnersSpacesCredentialsAndDisabledStatus(t *testing.T) {
	home := t.TempDir()
	c := externalAuthConfig{Provider: "opencsg", Issuer: "https://opencsg.example", ClientID: "teamx-client", AllowAllAuthenticated: true}
	s := hostedFixtureWithProvider(t, home, c, codeProviderFixture{})
	cookie, alice, aliceSpace := codeLogin(t, s, "alice")
	_, bob, _ := codeLogin(t, s, "bob")
	admin, err := readAdminToken(adminTokenPath(s.access.config))
	if err != nil {
		t.Fatal(err)
	}
	if w := hostedRequest(s, "/v1/deployment/users", map[string]string{"user_id": bob.UserID, "status": "disabled"}, nil, admin); w.Code != 200 {
		t.Fatal("could not disable second user", w.Code)
	}
	if err := s.access.users.saveGatewayKey(alice.UserID, "gk_existing-alice-key"); err != nil {
		t.Fatal(err)
	}
	aliceRuntime := waitSpaceState(t, s, aliceSpace, "ready")
	room := waitSpaceAssistant(t, aliceRuntime).info.RoomIdentity
	id, _ := newOpaqueID()
	if w := hostedRequest(s, "/v1/spaces", spaceChangeInput{RequestID: id, Name: "工作"}, cookie, ""); w.Code != 202 {
		t.Fatal("could not create extra Space", w.Code)
	}
	extra := waitSpaceState(t, s, id, "ready")
	modelPath := filepath.Join(extra.roots.Home, "config.toml")
	platformKey := filepath.Join(extra.roots.Home, "platform-cli", "lark", "private", "data", "master.key")
	serviceSecret := filepath.Join(home, "gitlab-applications", "keep-secret")
	mustWriteResetFile(t, platformKey, "existing-user-platform-key")
	mustWriteResetFile(t, serviceSecret, "existing-service-application-secret")
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the persisted representation from the former direct-OIDC name.
	var legacy hostedUserSnapshot
	if err := readIdentitySnapshot(filepath.Join(home, "users.json"), &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Source[0] = "casdoor"
	for i := range legacy.Users {
		legacy.Users[i].Identity.Provider = "casdoor"
	}
	writer := &snapshotWriter{ops: defaultSnapshotOps()}
	if err := writer.save(filepath.Join(home, "users.json"), legacy, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	var sessions accessSessionSnapshot
	if err := readIdentitySnapshot(s.access.sessionPath(), &sessions); err != nil {
		t.Fatal(err)
	}
	binding := sha256.Sum256([]byte(s.access.installationID + "\x00multi-user\x00casdoor\x00" + c.Issuer + "\x00" + c.ClientID))
	sessions.Binding = hex.EncodeToString(binding[:])
	if err := writer.save(s.access.sessionPath(), sessions, maxAccessSessionBytes); err != nil {
		t.Fatal(err)
	}
	retained := map[string][]byte{}
	for _, path := range []string{filepath.Join(home, "spaces.json"), s.access.users.gatewayPath(alice.UserID), modelPath, platformKey, serviceSecret} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		retained[path] = data
	}
	s = hostedFixtureWithProvider(t, home, c, codeProviderFixture{})
	if w := hostedRequest(s, "/v1/access/session", nil, cookie, ""); w.Code != http.StatusUnauthorized {
		t.Fatal("old provider's browser session survived migration", w.Code)
	}
	for path, before := range retained {
		if data, err := os.ReadFile(path); err != nil || !bytes.Equal(before, data) {
			t.Fatal("migration changed existing Space or credential data", path, err)
		}
	}
	expected := legacy
	expected.Source[0] = "opencsg"
	for i := range expected.Users {
		expected.Users[i].Identity.Provider = "opencsg"
	}
	if !reflect.DeepEqual(expected, s.access.users.saved) {
		t.Fatal("provider rename changed local identity, reservations or revocation state")
	}
	_, current, currentSpace := codeLogin(t, s, "alice")
	if current != alice || currentSpace != aliceSpace {
		t.Fatal("login allocated a replacement user or Space")
	}
	s.start(spaceRecordForTest(t, s, aliceSpace), nil, spaceAddress("127.0.0.1:8088"))
	if restored := waitSpaceAssistant(t, waitSpaceState(t, s, aliceSpace, "ready")).info.RoomIdentity; restored != room {
		t.Fatal("migration replaced the existing Room")
	}
	if _, err := s.access.users.reserve(codeIdentity(t, "bob"), c); err == nil {
		t.Fatal("migration reenabled a disabled user")
	}
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	s = hostedFixtureWithProvider(t, home, c, codeProviderFixture{})
	_, current, currentSpace = codeLogin(t, s, "alice")
	if current != alice || currentSpace != aliceSpace || len(s.access.users.saved.Users) != 2 {
		t.Fatal("second restart changed migrated identity")
	}
}

func TestOpenCSGProviderMigrationRejectsOtherSourcesAndRecoversInterruptedWrite(t *testing.T) {
	for _, variant := range []string{"issuer", "broker", "write-failed", "write-unknown"} {
		t.Run(variant, func(t *testing.T) {
			home := t.TempDir()
			c := externalAuthConfig{Provider: "fixture", AllowAllAuthenticated: true}
			s := hostedFixtureWithProvider(t, home, c, codeProviderFixture{})
			_, alice, space := codeLogin(t, s, "alice")
			if err := s.closeWithTimeout(); err != nil {
				t.Fatal(err)
			}
			var legacy hostedUserSnapshot
			if err := readIdentitySnapshot(filepath.Join(home, "users.json"), &legacy); err != nil {
				t.Fatal(err)
			}
			legacy.Source[0] = "casdoor"
			if variant == "issuer" {
				legacy.Source[1] = "https://foreign.example"
			}
			if variant == "broker" {
				legacy.Source = [2]string{"opencsg", "https://hub.example"}
			}
			for i := range legacy.Users {
				legacy.Users[i].Identity.Provider = legacy.Source[0]
				legacy.Users[i].Identity.Authority = legacy.Source[1]
			}
			ops := defaultSnapshotOps()
			writer := &snapshotWriter{ops: ops}
			path := filepath.Join(home, "users.json")
			if err := writer.save(path, legacy, maxPrivateFileBytes); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			c = externalAuthConfig{Provider: "opencsg", Issuer: "https://opencsg.example", ClientID: "teamx-client", AllowAllAuthenticated: true}
			u := &hostedUsers{home: home, saved: legacy}
			u.writer.ops = ops
			if strings.HasPrefix(variant, "write-") {
				u.writer.ops.rename = func(from, to string) error {
					if variant == "write-unknown" {
						if err := ops.rename(from, to); err != nil {
							return err
						}
					}
					return errors.New("injected migration failure")
				}
			}
			err := u.migrateOpenCSGProvider(c)
			if err == nil || !reflect.DeepEqual(u.saved, legacy) {
				t.Fatal("failed migration published a new identity")
			}
			if variant != "write-unknown" {
				if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
					t.Fatal("rejected migration changed saved identities")
				}
			} else if !errors.Is(err, ErrCommitUnknown) {
				t.Fatal("unknown migration write was reported as settled", err)
			}
			if strings.HasPrefix(variant, "write-") {
				s = hostedFixtureWithProvider(t, home, c, codeProviderFixture{})
				_, current, currentSpace := codeLogin(t, s, "alice")
				if current != alice || currentSpace != space {
					t.Fatal("interrupted migration replaced the existing identity")
				}
			}
		})
	}
}

func codeIdentity(t *testing.T, account string) authn.Identity {
	t.Helper()
	id, err := (codeProviderFixture{}).Complete(t.Context(), url.Values{"code": {account}})
	if err != nil {
		t.Fatal(err)
	}
	return id
}
