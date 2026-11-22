package registry

import (
	"context"
	"errors"
	"os"
	"reflect"
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
	c, err = m.CompleteVerification(ticket, m.Query(context.Background(), ticket))
	if err != nil || c.Validation.Account != identity || c.Validation.UserID != 0 || c.CredentialKind != "cli_profile" {
		t.Fatal(c, err)
	}
	file, err := m.Load()
	if err != nil || len(file.OAuthCredentials) != 0 {
		t.Fatal(err)
	}
	a, secret, err := ResolveAccount(file, file.Platforms[0])
	if err != nil || a.Identity != identity || a.CLIProfile.Profile != "fixture" || secret.Reveal() != "" {
		t.Fatal(a, err)
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
	if err != nil || file.Platforms[0].Validation.Status != "unverified" {
		t.Fatal(err)
	}
}

func TestSharedProfileVerificationCommitsOnceAndSurvivesReload(t *testing.T) {
	m, req, identity := profileManager(t)
	a := commitRequest(t, m, req, false)
	req.ID = strings.Repeat("b", 32)
	b := commitRequest(t, m, req, false)
	first, _ := m.BeginVerification(a.ID, a.Version)
	second, _ := m.BeginVerification(b.ID, b.Version)
	writes := 0
	save := m.store.Save
	m.store.Save = func(path string, value any, limit int64) error { writes++; return save(path, value, limit) }
	result := platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), Username: "共享账号", Account: identity}
	saved, err := m.CompleteVerifications([]Verification{first, second}, result)
	if err != nil || writes != 1 || len(saved) != 2 {
		t.Fatal("shared verification was not one commit", err, writes)
	}
	file, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Platforms {
		if c.Validation.Account != identity || c.Validation.Status != "valid" {
			t.Fatal("shared identity not saved")
		}
		if _, _, err := ResolveAccount(file, c); err != nil {
			t.Fatal("saved verification cannot resolve", err)
		}
	}
}

func TestSharedProfileVerificationFailureNeverPartiallyAdoptsTargets(t *testing.T) {
	for _, mode := range []string{"stale_second", "capacity_second", "save_failed"} {
		t.Run(mode, func(t *testing.T) {
			m, req, identity := profileManager(t)
			a := commitRequest(t, m, req, false)
			req.ID = strings.Repeat("b", 32)
			b := commitRequest(t, m, req, false)
			if mode == "capacity_second" {
				file, _ := m.Load()
				file.Platforms[1].Version = ^uint64(0)
				if err := m.save(file); err != nil {
					t.Fatal(err)
				}
				b.Version = ^uint64(0)
			}
			first, _ := m.BeginVerification(a.ID, a.Version)
			second, _ := m.BeginVerification(b.ID, b.Version)
			if mode == "stale_second" {
				req.Version = b.Version
				req.Name = "renamed"
				req.CLIProfile = nil
				commitRequest(t, m, req, false)
			}
			before, _ := m.Load()
			writes := 0
			m.store.Save = func(string, any, int64) error { writes++; return errors.New("injected failure") }
			_, err := m.CompleteVerifications([]Verification{first, second}, platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), Username: "共享账号", Account: identity})
			if err == nil {
				t.Fatal("incomplete batch reported success")
			}
			after, loadErr := m.Load()
			if loadErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("partial shared verification escaped", loadErr)
			}
			if mode == "save_failed" && writes != 1 || mode != "save_failed" && writes != 0 {
				t.Fatal("preflight and save boundaries changed", writes)
			}
		})
	}
}

func TestSnapshotRequiresExplicitVersion(t *testing.T) {
	m, _, _ := testManager(t)
	data := []byte(`{"platforms":[],"credentials":[]}`)
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
