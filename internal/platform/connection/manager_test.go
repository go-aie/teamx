package connection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// The fixture supplies a snapshot owner, rather than a second production writer.
// Room tests exercise the real atomic replacement and commit-unknown latch.
func testManager(t *testing.T) (*Manager, *int, *error) {
	t.Helper()
	writes, gate := 0, error(nil)
	m := New(Store{
		Path: filepath.Join(t.TempDir(), "platform-connections.json"),
		Check: func(writing bool) error {
			if writing {
				return gate
			}
			return nil
		},
		Save: func(path string, value any, _ int64) error {
			writes++
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			return os.WriteFile(path, data, 0600)
		},
	}, func(name, provider, origin string) (string, string, error) {
		if provider != "fixture" {
			return "", "", ErrRequest
		}
		return Normalize(name, origin)
	}, func(ctx context.Context, provider, origin string, secret platform.Secret) platform.Validation {
		if provider != "fixture" || origin != "https://example.invalid" || secret.Reveal() == "" {
			t.Error("wrong immutable verification scope")
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 8*time.Second {
			t.Error("verification is not bounded")
		}
		return platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), Username: "operator", UserID: 101}
	})
	return m, &writes, &gate
}
func request() Request {
	return Request{ID: strings.Repeat("a", 32), Name: "Fixture", Provider: "fixture", URL: "https://example.invalid/", PAT: "private-fixture-token"}
}
func commitRequest(t *testing.T, m *Manager, req Request, remove bool) Connection {
	t.Helper()
	change, err := m.Plan(req, remove)
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Commit(change)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConnectionCandidateAndCredentialSources(t *testing.T) {
	m, writes, gate := testManager(t)
	req := request()
	change, err := m.Plan(req, false)
	if err != nil || change.Rotating() || *writes != 0 {
		t.Fatal("planning wrote data", err)
	}
	if _, err := os.Stat(m.store.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("candidate escaped before commit")
	}
	// A failed owner gate leaves both connection and credential unpublished.
	*gate = errors.New("owner refuses writes after uncertain commit")
	if _, err := m.Commit(change); !errors.Is(err, *gate) || *writes != 0 {
		t.Fatal("bypassed owner latch", err)
	}
	*gate = nil
	c, err := m.Commit(change)
	if err != nil || c.Version != 1 || c.CredentialVersion != 1 {
		t.Fatal(c, err)
	}
	file, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := ResolveCredential(file, file.Connections[0], c.Version, c.CredentialVersion)
	if err != nil || secret.Reveal() != req.PAT {
		t.Fatal("credential resolution failed", err)
	}
	if _, err := ResolveCredential(file, file.Connections[0], c.Version+1, c.CredentialVersion); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted stale revision")
	}
	for _, value := range []any{req, change, file, file.Connections[0], file.Credentials[0], secret} {
		if strings.Contains(fmt.Sprintf("%+v", value), req.PAT) {
			t.Fatal("format leaked credential")
		}
	}
	data, _ := json.Marshal(secret)
	if strings.Contains(string(data), req.PAT) {
		t.Fatal("generic JSON leaked resolved credential")
	}
	req.Version, req.PAT, req.TokenEnv = c.Version, "", "TEAMX_CONNECTION_UNIT_TOKEN"
	t.Setenv(req.TokenEnv, "environment-fixture-token")
	change, err = m.Plan(req, false)
	if err != nil || !change.Rotating() {
		t.Fatal("source switch was not rotation", err)
	}
	c, err = m.Commit(change)
	if err != nil || c.CredentialVersion != 2 || c.Validation.Status != "unverified" {
		t.Fatal(c, err)
	}
	file, err = m.Load()
	if err != nil || len(file.Credentials) != 0 {
		t.Fatal("old PAT retained", err)
	}
	secret, err = ResolveCredential(file, file.Connections[0], c.Version, c.CredentialVersion)
	if err != nil || secret.Reveal() != "environment-fixture-token" {
		t.Fatal("environment not resolved", err)
	}
	data, _ = os.ReadFile(m.store.Path)
	if strings.Contains(string(data), "environment-fixture-token") {
		t.Fatal("environment value persisted")
	}
	req.Version = c.Version
	commitRequest(t, m, req, true)
	file, err = m.Load()
	if err != nil || len(file.Connections) != 0 || len(file.Credentials) != 0 {
		t.Fatal("delete retained data", err)
	}
}

func TestVerificationRejectsChangedScope(t *testing.T) {
	for _, action := range []string{"rotate", "rename", "delete", "recreate", "environment"} {
		t.Run(action, func(t *testing.T) {
			m, _, _ := testManager(t)
			req := request()
			if action == "environment" {
				req.PAT, req.TokenEnv = "", "TEAMX_CONNECTION_LATE_TOKEN"
				t.Setenv(req.TokenEnv, "initial-fixture-token")
			}
			c := commitRequest(t, m, req, false)
			ticket, err := m.BeginVerification(c.ID, c.Version)
			if err != nil {
				t.Fatal(err)
			}
			result := m.Query(context.Background(), ticket, nil)
			req.Version = c.Version
			switch action {
			case "rotate":
				req.PAT = "replacement-fixture-token"
			case "rename":
				req.PAT, req.Name = "", "Renamed"
			case "environment":
				t.Setenv(req.TokenEnv, "changed-fixture-token")
			}
			if action != "environment" {
				commitRequest(t, m, req, action == "delete" || action == "recreate")
			}
			if action == "recreate" {
				req.Version = 0
				commitRequest(t, m, req, false)
			}
			if _, err := m.CompleteVerification(ticket, result); !errors.Is(err, ErrConflict) {
				t.Fatal("accepted stale verification", err)
			}
		})
	}
}

func TestVerificationAndMissingCredential(t *testing.T) {
	m, _, _ := testManager(t)
	req := request()
	req.PAT, req.TokenEnv = "", "TEAMX_CONNECTION_MISSING_TOKEN"
	t.Setenv(req.TokenEnv, "")
	c := commitRequest(t, m, req, false)
	ticket, err := m.BeginVerification(c.ID, c.Version)
	if err != nil {
		t.Fatal(err)
	}
	result := m.Query(context.Background(), ticket, func(context.Context, string, string, platform.Secret) platform.Validation {
		t.Fatal("queried with unavailable credential")
		return platform.Validation{}
	})
	if result.ErrorCode != "credential_unavailable" {
		t.Fatal(result)
	}
	c, err = m.CompleteVerification(ticket, result)
	if err != nil || c.Version != 2 {
		t.Fatal(c, err)
	}
	t.Setenv(req.TokenEnv, "available-fixture-token")
	ticket, err = m.BeginVerification(c.ID, c.Version)
	if err != nil {
		t.Fatal(err)
	}
	c, err = m.CompleteVerification(ticket, m.Query(context.Background(), ticket, nil))
	if err != nil || c.Version != 3 || c.Validation.Status != "valid" {
		t.Fatal(c, err)
	}
	if _, err := m.Load(); err != nil {
		t.Fatal("saved verification cannot reload", err)
	}
}

func TestLegacySnapshotAndStrictStorage(t *testing.T) {
	m, _, _ := testManager(t)
	// Literal pre-PM-01 layout: no source-directory or schema migration.
	legacy := `{"version":1,"connections":[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Fixture","provider":"fixture","url":"https://example.invalid","version":4,"credential_version":2,"credential_kind":"pat","credential_configured":true,"validation":{"status":"valid","checked_at":"2026-11-01T00:00:00Z","username":"operator"},"credential_id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","generation":"cccccccccccccccccccccccccccccccc"}],"credentials":[{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","connection_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","pat":"legacy-private-token"}]}`
	if err := os.WriteFile(m.store.Path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifiedIdentity(file, file.Connections[0]); !errors.Is(err, ErrConflict) {
		t.Fatal("legacy username established an account ID", err)
	}
	before, _ := os.ReadFile(m.store.Path)
	req := request()
	req.Version, req.PAT = 4, ""
	c := commitRequest(t, m, req, false)
	file, err = m.Load()
	if err != nil || c.Version != 5 || c.CredentialVersion != 2 || file.Connections[0].Generation != strings.Repeat("c", 32) || file.Connections[0].CredentialID != strings.Repeat("b", 32) {
		t.Fatal("legacy identity changed", err)
	}
	var old, updated map[string]any
	json.Unmarshal(before, &old)
	after, _ := os.ReadFile(m.store.Path)
	json.Unmarshal(after, &updated)
	old["connections"].([]any)[0].(map[string]any)["version"] = float64(5)
	want, _ := json.Marshal(old)
	got, _ := json.Marshal(updated)
	if string(want) != string(got) {
		t.Fatal("legacy JSON contract changed")
	}
	for _, bad := range []string{legacy + "{}", strings.Replace(legacy, `"pat":"legacy-private-token"`, `"pat":"secret with spaces"`, 1), strings.Replace(legacy, `"version":1`, `"version":1,"unknown":"private-token"`, 1)} {
		if err := os.WriteFile(m.store.Path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Load(); !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "private-token") {
			t.Fatal("unsafe storage accepted or echoed", err)
		}
	}
}

func TestStableIdentityBindingAndEnvironmentDrift(t *testing.T) {
	m, _, _ := testManager(t)
	req := request()
	req.PAT, req.TokenEnv = "", "TEAMX_IDENTITY_TEST_TOKEN"
	t.Setenv(req.TokenEnv, "first-identity-token")
	c := commitRequest(t, m, req, false)
	ticket, err := m.BeginVerification(c.ID, c.Version)
	if err != nil {
		t.Fatal(err)
	}
	verified := platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), Username: "operator", UserID: 42}
	c, err = m.CompleteVerification(ticket, verified)
	if err != nil {
		t.Fatal(err)
	}
	file, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	record := file.Connections[0]
	if id, err := VerifiedIdentity(file, record); err != nil || id.UserID != 42 {
		t.Fatal(id, err)
	}
	public, _ := json.Marshal(Public(file, record))
	if !strings.Contains(string(public), `"user_id":42`) || strings.Contains(string(public), "fingerprint") || strings.Contains(string(public), "first-identity-token") {
		t.Fatal("unsafe or missing public identity")
	}
	// A metadata edit preserves identity, unlike credential rotation.
	req.Version, req.Name = c.Version, "Renamed"
	req.TokenEnv = ""
	c = commitRequest(t, m, req, false)
	file, err = m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifiedIdentity(file, file.Connections[0]); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(m.store.Path)
	t.Setenv("TEAMX_IDENTITY_TEST_TOKEN", "second-identity-token")
	file, err = m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifiedIdentity(file, file.Connections[0]); !errors.Is(err, platform.ErrCredential) {
		t.Fatal("old identity authorized changed credential", err)
	}
	if Public(file, file.Connections[0]).Validation.Status != "unverified" {
		t.Fatal("public identity remained verified")
	}
	after, _ := os.ReadFile(m.store.Path)
	if string(before) != string(after) {
		t.Fatal("read changed snapshot")
	}
	// Revalidation may establish a different user, but never reuse the old ID.
	ticket, err = m.BeginVerification(c.ID, c.Version)
	if err != nil {
		t.Fatal(err)
	}
	verified.UserID, verified.Username = 43, "other"
	if _, err = m.CompleteVerification(ticket, verified); err != nil {
		t.Fatal(err)
	}
	file, err = m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if id, err := VerifiedIdentity(file, file.Connections[0]); err != nil || id.UserID != 43 {
		t.Fatal(id, err)
	}
	// Strict decoding refuses malformed or mismatched identity evidence.
	good, _ := os.ReadFile(m.store.Path)
	for _, bad := range []string{
		strings.Replace(string(good), `"user_id":43`, `"user_id":-1`, 1),
		strings.Replace(string(good), `"user_id":43`, `"user_id":"43"`, 1),
		strings.Replace(string(good), `"identity_binding":{`, `"identity_binding":{"unknown":1,`, 1),
		strings.Replace(string(good), `"credential_fingerprint":"`, `"credential_fingerprint":"bad`, 1),
		strings.Replace(string(good), `"credential_version":1,"credential_fingerprint"`, `"credential_version":2,"credential_fingerprint"`, 1),
	} {
		if bad == string(good) {
			t.Fatal("fixture mutation missed")
		}
		if err := os.WriteFile(m.store.Path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Load(); !errors.Is(err, ErrInvalid) {
			t.Fatal("accepted malformed identity", err)
		}
	}
}

func TestVerificationRequiresStableIDAndCurrentRecord(t *testing.T) {
	m, writes, _ := testManager(t)
	req := request()
	c := commitRequest(t, m, req, false)
	ticket, err := m.BeginVerification(c.ID, c.Version)
	if err != nil {
		t.Fatal(err)
	}
	before := *writes
	for _, validation := range []platform.Validation{
		{Status: "valid", CheckedAt: time.Now().UTC(), Username: "operator"},
		{Status: "valid", CheckedAt: time.Now().UTC(), Username: "operator", UserID: -1},
		{Status: "failed", CheckedAt: time.Now().UTC(), UserID: 1, ErrorCode: "query_failed"},
	} {
		if _, err := m.CompleteVerification(ticket, validation); !errors.Is(err, ErrInvalid) || *writes != before {
			t.Fatal("invalid identity evidence committed", err)
		}
	}
	c, err = m.CompleteVerification(ticket, m.Query(context.Background(), ticket, nil))
	if err != nil {
		t.Fatal(err)
	}
	file, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	old := file.Connections[0]
	req.Version, req.PAT = c.Version, "rotated-private-token"
	c = commitRequest(t, m, req, false)
	file, err = m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Validation.UserID != 0 || file.Connections[0].IdentityBinding != nil {
		t.Fatal("rotation retained identity")
	}
	if _, err := VerifiedIdentity(file, old); !errors.Is(err, ErrConflict) {
		t.Fatal("old record authorized after rotation", err)
	}
	req.Version = c.Version
	commitRequest(t, m, req, true)
	file, err = m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifiedIdentity(file, old); !errors.Is(err, ErrConflict) {
		t.Fatal("deleted record authorized", err)
	}
}
