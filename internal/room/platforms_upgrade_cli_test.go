//go:build integration || e2e

package room

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/scion"
)

func TestPlatformsUpgradeRequiresStoppedServiceAndPreservesReferences(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), nil)
	c := platformTestSave(t, m, platformTestRequest(m))
	file, _ := m.platforms.Load()
	file.Version = 3
	if err := m.store.save(m.platformsPathForTest(), file, 2<<20); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(m.platformsPathForTest())
	var out, diagnostic bytes.Buffer
	args := []string{"upgrade", "--rooms-dir", m.store.dir}
	if err := runPlatformsUpgrade(args, &out, &diagnostic); err == nil {
		t.Fatal("upgraded a running service")
	}
	after, _ := os.ReadFile(m.platformsPathForTest())
	if !bytes.Equal(before, after) {
		t.Fatal("lock failure changed snapshot")
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	if err := runPlatformsUpgrade(args, &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(m.platformsPathForTest())
	if err != nil {
		t.Fatal(err)
	}
	var upgraded registry.Snapshot
	if json.Unmarshal(data, &upgraded) != nil || upgraded.Version != registry.OAuthSnapshotVersion || len(upgraded.Platforms) != 1 || upgraded.Platforms[0].ID != c.ID || upgraded.OAuthCredentials[0].AccessToken != file.OAuthCredentials[0].AccessToken {
		t.Fatal("upgrade lost existing authorization")
	}
	if strings.Contains(out.String(), file.OAuthCredentials[0].AccessToken) {
		t.Fatal("upgrade exposed token")
	}
}

func TestPlatformConfigurationUpgradeRestoresSharedUsersAndProfiles(t *testing.T) {
	parallelFixtureTest(t)
	root := t.TempDir()
	m := testManager(t, root, nil)
	platformTestSave(t, m, platformTestRequest(m))
	f, err := m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	app := f.OAuthApplications[0]
	identity := platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), UserID: 202, Username: "user202"}
	tokens := platform.OAuthTokens{AccessToken: platform.NewSecret("second-user-access"), RefreshToken: platform.NewSecret("second-user-refresh"), ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"api"}}
	if _, err := m.platforms.CommitOAuthLogin(nil, strings.Repeat("d", 32), "Second GitLab", app, "", humanCaller.ID, tokens, identity); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		id, provider, origin string
		profile              platform.CLIProfile
	}{
		{strings.Repeat("5", 32), "lark", lark.Origin("feishu"), platform.CLIProfile{Version: 1, Executable: "/fixture/lark-cli", Profile: "fixture", Brand: "feishu", CLIVersion: lark.CLIVersion}},
		{strings.Repeat("6", 32), "scion", "https://scion.invalid", platform.CLIProfile{Version: 1, Executable: "/fixture/teamx-scion", Profile: strings.Repeat("7", 32), Brand: "scion", CLIVersion: scion.CLIVersion}},
	} {
		platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: p.id, Name: p.provider, Provider: p.provider, URL: p.origin, CLIProfile: &p.profile})
	}
	f, err = m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	f.Version = 4
	for i := range f.Platforms {
		f.Platforms[i].ToolEntryID = ""
	}
	if err := m.store.save(m.platformsPathForTest(), f, 2<<20); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(m.platformsPathForTest())
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if err := runPlatformsUpgrade([]string{"upgrade", "--rooms-dir", root}, &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(m.platformsPathForTest() + ".v4.bak")
	if err != nil || !bytes.Equal(bytes.TrimSpace(backup), bytes.TrimSpace(before)) {
		t.Fatal("real Store backup changed old snapshot", err)
	}
	restarted := testManager(t, root, nil)
	after, err := restarted.platforms.Load()
	if err != nil || after.Version != registry.ConfigurationVersion || len(after.Platforms) != 4 || len(after.OAuthApplications) != 1 || len(after.OAuthCredentials) != 2 {
		t.Fatal("restart lost platform configuration", err)
	}
	entries := map[string]bool{}
	for _, c := range after.Platforms {
		if c.ToolEntryID == "" || entries[c.ToolEntryID] {
			t.Fatal("missing or duplicate entry")
		}
		entries[c.ToolEntryID] = true
	}
	after.Version = 4
	for i := range after.Platforms {
		after.Platforms[i].ToolEntryID = ""
	}
	oldJSON, _ := json.Marshal(f)
	newJSON, _ := json.Marshal(after)
	if !bytes.Equal(oldJSON, newJSON) {
		t.Fatal("real Store upgrade changed existing identities or authorization")
	}
}
