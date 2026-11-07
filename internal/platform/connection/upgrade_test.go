package connection

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestUpgradePreservesConnectionIdentityAndExistingOAuth(t *testing.T) {
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
	_ = json.Unmarshal(raw["connections"], &records)
	legacy := map[string]json.RawMessage{}
	for k, v := range records[0] {
		legacy[k] = v
	}
	putJSON(legacy, "id", strings.Repeat("d", 32))
	putJSON(legacy, "credential_kind", "env")
	putJSON(legacy, "token_env", "OLD_TOKEN")
	delete(legacy, "credential_id")
	records = append(records, legacy)
	putJSON(raw, "connections", records)
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
	if after.Connections[0].ID != file.Connections[0].ID || after.Connections[0].Version != file.Connections[0].Version || after.OAuthCredentials[0].AccessToken != file.OAuthCredentials[0].AccessToken {
		t.Fatal("changed existing authorization")
	}
	c := after.Connections[1]
	if c.ID != strings.Repeat("d", 32) || c.Generation != file.Connections[0].Generation || c.CredentialKind != "oauth" || c.CredentialConfigured || c.CredentialID != "" || c.Validation.Status != "unverified" || c.Version != file.Connections[0].Version+1 {
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
				putJSON(raw, "connections", []Connection{c, c})
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
