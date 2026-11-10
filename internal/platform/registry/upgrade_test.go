package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/go-aie/teamx/internal/platform"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestUpgradePreservesPlatformIdentityAndExistingOAuth(t *testing.T) {
	m, _ := oauthFixture(t)
	file, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(file)
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &raw)
	putJSON(raw, "version", 3)
	var records []map[string]json.RawMessage
	_ = json.Unmarshal(raw["platforms"], &records)
	legacy := map[string]json.RawMessage{}
	for k, v := range records[0] {
		legacy[k] = v
	}
	putJSON(legacy, "id", strings.Repeat("d", 32))
	putJSON(legacy, "credential_kind", "env")
	putJSON(legacy, "token_env", "OLD_TOKEN")
	delete(legacy, "credential_id")
	delete(legacy, "tool_entry_id")
	records = append(records, legacy)
	putJSON(raw, "platforms", records)
	putJSON(raw, "credentials", []any{})
	putJSON(raw, "oauth_initializations", []any{})
	old, _ := json.Marshal(raw)
	if err = os.WriteFile(m.store.Path, old, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Load(); !errors.Is(err, ErrInvalid) {
		t.Fatal("runtime accepted historical authentication")
	}
	before, _ := os.ReadFile(m.store.Path)
	if !bytes.Equal(old, before) {
		t.Fatal("runtime rewrote historical data")
	}
	count, err := m.Upgrade()
	if err != nil || count != 1 {
		t.Fatal(count, err)
	}
	after, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if after.Platforms[0].ID != file.Platforms[0].ID || after.Platforms[0].Version != file.Platforms[0].Version || after.OAuthCredentials[0].AccessToken != file.OAuthCredentials[0].AccessToken {
		t.Fatal("changed existing authorization")
	}
	c := after.Platforms[1]
	if c.ID != strings.Repeat("d", 32) || c.Generation != file.Platforms[0].Generation || c.CredentialKind != "oauth" || c.CredentialConfigured || c.CredentialID != "" || c.Validation.Status != "unverified" || c.Version != file.Platforms[0].Version+1 {
		t.Fatal("conversion lost reference identity or retained authorization")
	}
	disk, _ := os.ReadFile(m.store.Path)
	for _, removed := range []string{"OLD_TOKEN", "token_env", "oauth_initializations", "\"credentials\""} {
		if bytes.Contains(disk, []byte(removed)) {
			t.Fatal("retained obsolete credential data")
		}
	}
	if count, err = m.Upgrade(); err != nil || count != 0 {
		t.Fatal("upgrade is not idempotent", count, err)
	}
}

func writeVersion4Fixture(t *testing.T, m *Manager) ([]byte, Snapshot) {
	t.Helper()
	f, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	f.Version = 4
	for i := range f.Platforms {
		f.Platforms[i].ToolEntryID = ""
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.store.Path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return b, f
}

func TestPlatformVersion4UpgradeBackupAndRecovery(t *testing.T) {
	m, _ := oauthFixture(t)
	before, old := writeVersion4Fixture(t, m)
	if _, err := m.Load(); err == nil {
		t.Fatal("runtime implicitly upgraded snapshot")
	}
	if _, err := m.Upgrade(); err != nil {
		t.Fatal(err)
	}
	f, err := m.Load()
	if err != nil || f.Version != ConfigurationVersion || !validToolEntry(f.Platforms[0].Platform) {
		t.Fatal("upgrade failed", err)
	}
	entry := f.Platforms[0].ToolEntryID
	// Every existing identity and authorization field is preserved.
	f.Version, f.Platforms[0].ToolEntryID = 4, ""
	if !reflect.DeepEqual(f, old) {
		t.Fatal("upgrade changed existing identity or authorization")
	}
	backup, err := os.ReadFile(m.UpgradeBackupPath(4))
	if err != nil || !sameJSON(backup, before) {
		t.Fatal("backup lost original snapshot", err)
	}
	info, _ := os.Stat(m.UpgradeBackupPath(4))
	if info.Mode().Perm() != 0600 {
		t.Fatal("backup exposed credentials")
	}
	if _, err := m.Upgrade(); err != nil {
		t.Fatal(err)
	}
	f, _ = m.Load()
	if f.Platforms[0].ToolEntryID != entry {
		t.Fatal("retry regenerated entry")
	}
	// Offline rollback restores the old snapshot for use with the old binary.
	if err := os.WriteFile(m.store.Path, backup, 0600); err != nil {
		t.Fatal(err)
	}
	var restored Snapshot
	if err := readSnapshot(m.store.Path, &restored); err != nil || restored.Version != 4 {
		t.Fatal("backup cannot restore old data", err)
	}
	restoredJSON, _ := json.Marshal(restored)
	if !sameJSON(restoredJSON, before) {
		t.Fatal("restored snapshot changed persisted data")
	}
	if _, err := m.Upgrade(); err != nil {
		t.Fatal("restored backup cannot be upgraded", err)
	}
}

func TestPlatformUpgradeFailuresPreserveSourceAndBackup(t *testing.T) {
	for _, scenario := range []string{"backup_failed", "commit_failed", "commit_unknown", "conflicting_backup", "symlink_backup"} {
		t.Run(scenario, func(t *testing.T) {
			m, _ := oauthFixture(t)
			before, _ := writeVersion4Fixture(t, m)
			save := m.store.Save
			switch scenario {
			case "backup_failed", "commit_failed", "commit_unknown":
				m.store.Save = func(path string, v any, limit int64) error {
					if scenario == "backup_failed" && path == m.UpgradeBackupPath(4) || scenario == "commit_failed" && path == m.store.Path {
						return errors.New("write failed")
					}
					if err := save(path, v, limit); err != nil {
						return err
					}
					if scenario == "commit_unknown" && path == m.store.Path {
						return errors.New("commit unknown")
					}
					return nil
				}
			case "conflicting_backup":
				if err := os.WriteFile(m.UpgradeBackupPath(4), []byte(`{"version":4}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink_backup":
				if err := os.Symlink(m.store.Path, m.UpgradeBackupPath(4)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := m.Upgrade(); err == nil {
				t.Fatal("failure accepted")
			}
			after, _ := os.ReadFile(m.store.Path)
			if scenario == "commit_unknown" {
				if _, err := m.Load(); err != nil {
					t.Fatal("lost committed snapshot", err)
				}
			} else if !bytes.Equal(before, after) {
				t.Fatal("failure changed source snapshot")
			}
			m.store.Save = save
			if scenario == "commit_failed" || scenario == "commit_unknown" {
				backup, err := os.ReadFile(m.UpgradeBackupPath(4))
				if err != nil || !sameJSON(backup, before) {
					t.Fatal("failure lost backup", err)
				}
				if _, err := m.Upgrade(); err != nil {
					t.Fatal("retry failed", err)
				}
			}
		})
	}
}

func TestVersion4UpgradeDoesNotRepairOrDropMalformedData(t *testing.T) {
	for _, scenario := range []string{"missing_installation", "legacy_credentials", "legacy_token", "unknown_application_field"} {
		t.Run(scenario, func(t *testing.T) {
			m, _ := oauthFixture(t)
			before, _ := writeVersion4Fixture(t, m)
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(before, &raw); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "missing_installation":
				delete(raw, "installation_id")
			case "legacy_credentials":
				putJSON(raw, "credentials", []string{"must-not-drop"})
			case "legacy_token":
				var records []map[string]json.RawMessage
				_ = json.Unmarshal(raw["platforms"], &records)
				putJSON(records[0], "token_env", "must-not-drop")
				putJSON(raw, "platforms", records)
			case "unknown_application_field":
				var apps []map[string]json.RawMessage
				_ = json.Unmarshal(raw["oauth_applications"], &apps)
				putJSON(apps[0], "remote_id", 123)
				putJSON(raw, "oauth_applications", apps)
			}
			before, _ = json.Marshal(raw)
			if err := os.WriteFile(m.store.Path, before, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Upgrade(); err == nil {
				t.Fatal("malformed v4 snapshot was silently repaired")
			}
			after, _ := os.ReadFile(m.store.Path)
			if !bytes.Equal(before, after) {
				t.Fatal("rejected upgrade changed source")
			}
			if _, err := os.Lstat(m.UpgradeBackupPath(4)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected candidate created a backup")
			}
		})
	}
}

func TestUpgradeRejectsInvalidCandidateBeforeWriting(t *testing.T) {
	for _, kind := range []string{"unknown_version", "unknown_field", "duplicate_connection", "write_gate"} {
		t.Run(kind, func(t *testing.T) {
			m, _, gate := testManager(t)
			c := commitRequest(t, m, Request{ID: strings.Repeat("a", 32), Name: "GitLab", Provider: "gitlab", URL: "https://example.invalid"}, false)
			file, _ := m.Load()
			encoded, _ := json.Marshal(file)
			var raw map[string]json.RawMessage
			_ = json.Unmarshal(encoded, &raw)
			putJSON(raw, "version", 3)
			switch kind {
			case "unknown_version":
				putJSON(raw, "version", 99)
			case "unknown_field":
				putJSON(raw, "unexpected", true)
			case "duplicate_connection":
				putJSON(raw, "platforms", []platform.Platform{c, c})
			case "write_gate":
				*gate = errors.New("frozen")
			}
			before, _ := json.Marshal(raw)
			_ = os.WriteFile(m.store.Path, before, 0600)
			if _, err := m.Upgrade(); err == nil {
				t.Fatal("invalid upgrade accepted")
			}
			after, _ := os.ReadFile(m.store.Path)
			if !bytes.Equal(before, after) {
				t.Fatal("failed upgrade changed data")
			}
		})
	}
}
