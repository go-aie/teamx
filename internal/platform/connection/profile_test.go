package connection

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func profileManager(t *testing.T) (*Manager, Request, platform.AccountIdentity) {
	t.Helper()
	m, _, _ := testManager(t)
	identity := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "fixture", Instance: "https://example.invalid", Tenant: platform.ExternalID{Namespace: "tenant", Value: "tenant"}}, ID: platform.ExternalID{Namespace: "app:open_id", Value: "opaque-user"}}
	m.SetProfileSupport(func(p string) bool { return p == "fixture" }, func(p, origin string, ref platform.CLIProfile) bool {
		return p == "fixture" && origin == identity.Instance && ref.Version == 1 && ref.Profile == "fixture"
	}, func(context.Context, string, string, platform.CLIProfile) platform.Validation {
		return platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), Username: "中文账号", Account: identity}
	})
	r := request()
	r.PAT = ""
	r.CLIProfile = &platform.CLIProfile{Version: 1, Executable: "/fixture/lark-cli", Profile: "fixture", Brand: "feishu", CLIVersion: "1.0.99"}
	return m, r, identity
}
func TestProfileReferenceAndNativeIdentity(t *testing.T) {
	m, req, identity := profileManager(t)
	c := commitRequest(t, m, req, false)
	ticket, err := m.BeginVerification(c.ID, c.Version)
	if err != nil {
		t.Fatal(err)
	}
	c, err = m.CompleteVerification(ticket, m.Query(context.Background(), ticket, nil))
	if err != nil || c.Validation.Account != identity || c.Validation.UserID != 0 || c.CredentialKind != "cli_profile" {
		t.Fatal(c, err)
	}
	file, err := m.Load()
	if err != nil || len(file.Credentials) != 0 {
		t.Fatal(err)
	}
	a, secret, err := ResolveAccount(file, file.Connections[0])
	if err != nil || a.Identity != identity || a.CLIProfile.Profile != "fixture" || secret.Reveal() != "" {
		t.Fatal(a, err)
	}
	if _, err = ResolveCredential(file, file.Connections[0], c.Version, c.CredentialVersion); !errors.Is(err, platform.ErrCredential) {
		t.Fatal("profile treated as PAT", err)
	}
	disk, _ := os.ReadFile(m.store.Path)
	if strings.Contains(string(disk), `"pat"`) || strings.Contains(string(disk), "access_token") {
		t.Fatal("token in profile reference")
	}
	// Reference replacement invalidates verification and late results even when
	// the profile name is unchanged. Display-name edits do not rotate credentials.
	req.Version = c.Version
	change, err := m.Plan(req, false)
	if err != nil || !change.Rotating() {
		t.Fatal(err)
	}
	if _, err = m.Commit(change); err != nil {
		t.Fatal(err)
	}
	if _, err = m.CompleteVerification(ticket, platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), Username: "旧账号", Account: identity}); !errors.Is(err, ErrConflict) {
		t.Fatal("late result accepted", err)
	}
	file, err = m.Load()
	if err != nil || file.Connections[0].Validation.Status != "unverified" {
		t.Fatal(err)
	}
}
func TestSnapshotVersionPreflightAndExplicitUpgrade(t *testing.T) {
	m, _, _ := testManager(t)
	r := request()
	c := commitRequest(t, m, r, false)
	// Existing version 1 GitLab-style records remain readable and writable in
	// their original version. Merely opening a service never rewrites evidence.
	file, _ := m.Load()
	file.Version = 1
	b, _ := json.Marshal(file)
	if err := os.WriteFile(m.store.Path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load(); err != nil {
		t.Fatal("legacy preflight failed", err)
	}
	got, _ := os.ReadFile(m.store.Path)
	if string(got) != string(b) {
		t.Fatal("load migrated snapshot")
	}
	ticket, _ := m.BeginVerification(c.ID, c.Version)
	if _, err := m.CompleteVerification(ticket, m.Query(context.Background(), ticket, nil)); err != nil {
		t.Fatal("legacy verification failed", err)
	}
	loaded, _ := m.Load()
	if loaded.Version != 1 {
		t.Fatal("legacy mutation migrated schema")
	}
	// Use the same private snapshot with a registered reference-only provider.
	pm, pr, _ := profileManager(t)
	pm.store = m.store
	pr.ID = strings.Repeat("b", 32)
	if _, err := pm.Plan(pr, false); !errors.Is(err, ErrInvalid) {
		t.Fatal("implicit schema switch", err)
	}
	pr.SnapshotVersion = 2
	// Both providers in this fixture share a name, so the older PAT fixture is
	// accepted here solely for the mixed-schema upgrade check.
	pm.profileOnly = nil
	commitRequest(t, pm, pr, false)
	loaded, err := pm.Load()
	if err != nil || loaded.Version != 2 || len(loaded.Connections) != 2 {
		t.Fatal("explicit upgrade failed", err)
	}
	loaded.Version = 99
	b, _ = json.Marshal(loaded)
	_ = os.WriteFile(pm.store.Path, b, 0600)
	if _, err := pm.Load(); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "unsupported") {
		t.Fatal("unknown schema accepted", err)
	}
	after, _ := os.ReadFile(pm.store.Path)
	if string(after) != string(b) {
		t.Fatal("unknown schema rewritten")
	}
}

func TestSnapshotRequiresExplicitVersion(t *testing.T) {
	m, _, _ := testManager(t)
	data := []byte(`{"connections":[],"credentials":[]}`)
	if err := os.WriteFile(m.store.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load(); !errors.Is(err, ErrInvalid) {
		t.Fatal("inferred missing snapshot version", err)
	}
	after, _ := os.ReadFile(m.store.Path)
	if string(after) != string(data) {
		t.Fatal("preflight changed unversioned data")
	}
}
