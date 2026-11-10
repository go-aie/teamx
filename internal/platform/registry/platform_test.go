package registry

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestPlatformEntryLifecycle(t *testing.T) {
	m, _ := oauthFixture(t)
	f, _ := m.Load()
	c := f.Platforms[0]
	if !validToolEntry(c.Platform) || c.ToolEntryID == "" {
		t.Fatal("missing platform entry")
	}
	r := Request{ID: c.ID, Version: c.Version, Name: "Renamed", Provider: c.Provider, URL: c.URL}
	renamed := commitRequest(t, m, r, false)
	if renamed.ToolEntryID != c.ToolEntryID || renamed.ID != c.ID {
		t.Fatal("rename changed platform identity")
	}
	r.ID, r.Version = strings.Repeat("d", 32), 0
	other := commitRequest(t, m, r, false)
	if other.ToolEntryID == c.ToolEntryID || other.Name != renamed.Name {
		t.Fatal("same display name reused entry")
	}
	r.Version = other.Version
	commitRequest(t, m, r, true)
	r.Version = 0
	recreated := commitRequest(t, m, r, false)
	if recreated.ToolEntryID == other.ToolEntryID {
		t.Fatal("recreated platform reused deleted entry")
	}
}

func addSecondOAuthUser(t *testing.T, m *Manager) Record {
	t.Helper()
	f, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	app := f.OAuthApplications[0]
	tokens := platform.OAuthTokens{AccessToken: platform.NewSecret("user202-access"), RefreshToken: platform.NewSecret("user202-refresh"), ExpiresAt: time.Now().Add(time.Hour), Scopes: app.Scopes}
	identity := platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), UserID: 202, Username: "user202"}
	saved, err := m.CommitOAuthLogin(nil, strings.Repeat("d", 32), "Second account", app, "", "tester", tokens, identity)
	if err != nil || len(saved) != 1 {
		t.Fatal("cannot save second user", err)
	}
	f, err = m.Load()
	if err != nil || len(f.OAuthApplications) != 1 || len(f.OAuthCredentials) != 2 || len(f.Platforms) != 2 {
		t.Fatal("bad shared application snapshot", err)
	}
	return f.Platforms[1]
}

func TestSharedApplicationUsersSurviveRestartAndRefreshIndependently(t *testing.T) {
	m, a := oauthFixture(t)
	before, _ := m.Load()
	b := addSecondOAuthUser(t, m)
	// Reconstruct the manager, not just a cached view of the snapshot.
	restarted := New(m.store, m.normalize)
	restarted.SetIdentityResolver(m.identityResolver)
	f, err := restarted.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.Platforms[0], before.Platforms[0]) || !reflect.DeepEqual(f.OAuthCredentials[0], before.OAuthCredentials[0]) || !reflect.DeepEqual(f.OAuthApplications, before.OAuthApplications) {
		t.Fatal("adding user changed the owner or original authorization")
	}
	account, err := ResolveAuthorization(f, b)
	if err != nil || account.UserID != 202 || account.Authorization.CredentialID == a.Authorization.CredentialID {
		t.Fatal("accounts were not isolated", err)
	}
	ticket, err := restarted.BeginOAuthRefresh(a.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	tokens := platform.OAuthTokens{AccessToken: platform.NewSecret("user101-next"), RefreshToken: platform.NewSecret("user101-next-refresh"), ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"api"}}
	if err := restarted.CompleteOAuthRefresh(ticket, tokens, "active"); err != nil {
		t.Fatal(err)
	}
	after, err := restarted.Load()
	if err != nil || !reflect.DeepEqual(after.Platforms[1], b) || !reflect.DeepEqual(after.OAuthCredentials[1], f.OAuthCredentials[1]) {
		t.Fatal("refresh changed second user", err)
	}
	updated, cleanup, err := restarted.LogoutOAuth(a.Authorization.CredentialID)
	if err != nil || len(updated) != 1 || len(cleanup) != 0 {
		t.Fatal("unsafe logout", err)
	}
	after, err = restarted.Load()
	if err != nil || !reflect.DeepEqual(after.Platforms[1], b) || !reflect.DeepEqual(after.OAuthCredentials[1], f.OAuthCredentials[1]) {
		t.Fatal("logout changed second user", err)
	}
	if _, err := ResolveAuthorization(after, b); err != nil {
		t.Fatal(err)
	}
}

func TestSharedApplicationRejectsCrossAccountCommit(t *testing.T) {
	for _, scenario := range []string{"wrong_user", "unrelated_expected", "missing_target", "changed_owner", "replace_shared_app_partial"} {
		t.Run(scenario, func(t *testing.T) {
			m, _ := oauthFixture(t)
			b := addSecondOAuthUser(t, m)
			f, _ := m.Load()
			app := f.OAuthApplications[0]
			identity := b.Validation
			expected := []Record{b}
			importID := ""
			switch scenario {
			case "wrong_user":
				identity = f.Platforms[0].Validation
			case "unrelated_expected":
				expected = f.Platforms
			case "missing_target":
				expected = nil
			case "changed_owner":
				app.OwnerID = 202
			case "replace_shared_app_partial":
				input := OAuthClientImport{ReplacesApplicationID: app.ID, Origin: app.Origin, ClientID: app.ClientID, ClientSecret: "replacement-secret", RedirectURI: app.RedirectURI, Scopes: app.Scopes, ActorID: "tester"}
				var err error
				importID, err = m.ImportOAuthClient(input)
				if err != nil {
					t.Fatal(err)
				}
				app.ClientSecret = input.ClientSecret
				identity = f.Platforms[0].Validation
				expected = []Record{b}
			}
			before, _ := os.ReadFile(m.store.Path)
			tokens := platform.OAuthTokens{AccessToken: platform.NewSecret("candidate"), RefreshToken: platform.NewSecret("candidate-refresh"), ExpiresAt: time.Now().Add(time.Hour), Scopes: app.Scopes}
			if _, err := m.CommitOAuthLogin(expected, b.ID, "", app, importID, "tester", tokens, identity); !errors.Is(err, ErrConflict) {
				t.Fatal("cross-account commit accepted", err)
			}
			after, _ := os.ReadFile(m.store.Path)
			if string(before) != string(after) {
				t.Fatal("rejection modified snapshot")
			}
		})
	}
}

func TestSharedApplicationReauthorizationUsesUserAndGrantedScopes(t *testing.T) {
	m, _ := oauthFixture(t)
	f, _ := m.Load()
	f.OAuthApplications[0].Scopes = []string{"api", "write_repository"}
	// The application's allowed scopes and each user's granted scopes are
	// distinct. Adding an allowed scope does not expand the existing grant.
	if err := m.save(f); err != nil {
		t.Fatal(err)
	}
	b := addSecondOAuthUser(t, m)
	f, _ = m.Load()
	beforeA, beforeGrantA := f.Platforms[0], f.OAuthCredentials[0]
	tokens := platform.OAuthTokens{AccessToken: platform.NewSecret("user202-reauthorized"), RefreshToken: platform.NewSecret("user202-new-refresh"), ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"api"}}
	if _, err := m.CommitOAuthLogin([]Record{b}, b.ID, "", f.OAuthApplications[0], "", "tester", tokens, b.Validation); err != nil {
		t.Fatal("non-owner reauthorization rejected", err)
	}
	after, err := m.Load()
	if err != nil || !reflect.DeepEqual(after.Platforms[0], beforeA) || !reflect.DeepEqual(after.OAuthCredentials[0], beforeGrantA) || after.OAuthApplications[0].OwnerID != 101 {
		t.Fatal("reauthorization changed the application owner or other user", err)
	}
	if !reflect.DeepEqual(after.OAuthCredentials[1].Scopes, []string{"api"}) || after.OAuthCredentials[1].ID != b.CredentialID {
		t.Fatal("reauthorization lost exact grant identity or scopes")
	}
	before, _ := os.ReadFile(m.store.Path)
	tokens.Scopes = []string{"api", "sudo"}
	if _, err := m.CommitOAuthLogin(after.Platforms[1:], b.ID, "", after.OAuthApplications[0], "", "tester", tokens, b.Validation); err == nil {
		t.Fatal("application scope boundary bypassed")
	}
	rejected, _ := os.ReadFile(m.store.Path)
	if string(before) != string(rejected) {
		t.Fatal("rejected grant modified snapshot")
	}
}

func TestPlatformSnapshotRejectsAmbiguousEntryAndAuthorization(t *testing.T) {
	for _, scenario := range []string{"entry_type", "duplicate_entry", "duplicate_user_grant"} {
		t.Run(scenario, func(t *testing.T) {
			m, _ := oauthFixture(t)
			b := addSecondOAuthUser(t, m)
			f, _ := m.Load()
			switch scenario {
			case "entry_type":
				f.Platforms[1].ToolEntryID = "lark-" + strings.Repeat("d", 32)
			case "duplicate_entry":
				f.Platforms[1].ToolEntryID = f.Platforms[0].ToolEntryID
			case "duplicate_user_grant":
				duplicate := f.OAuthCredentials[1]
				duplicate.ID = strings.Repeat("e", 32)
				f.OAuthCredentials = append(f.OAuthCredentials, duplicate)
			}
			if err := m.save(f); err != nil {
				t.Fatal(err)
			}
			if _, err := m.Load(); !errors.Is(err, ErrInvalid) {
				t.Fatal("ambiguous platform snapshot accepted", b.ID, err)
			}
		})
	}
}
