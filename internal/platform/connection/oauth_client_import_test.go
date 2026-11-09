package connection

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestOAuthClientImportWithoutInitialCredential(t *testing.T) {
	m, _, _ := testManager(t)
	input := OAuthClientImport{Origin: "https://gitlab.invalid", ClientID: "import-client", ClientSecret: "import-secret", RedirectURI: "http://localhost:8088/cb", Scopes: []string{"api"}, ActorID: "tester"}
	id, err := m.ImportOAuthClient(input)
	if err != nil || id == "" {
		t.Fatal(err)
	}
	file, err := m.Load()
	if err != nil || file.Version != OAuthSnapshotVersion || len(file.Connections) != 0 || len(file.OAuthCredentials) != 0 || len(file.OAuthApplications) != 0 || len(file.OAuthClientImports) != 1 || file.OAuthClientImports[0].State != "owner_verification_required" {
		t.Fatal("import became an authorized account", err)
	}
	if strings.Contains(fmt.Sprint(file.OAuthClientImports[0]), input.ClientSecret) {
		t.Fatal("formatted secret leaked")
	}
	info, _ := os.Stat(m.store.Path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("permissions")
	}
	retry, err := m.ImportOAuthClient(input)
	if err != nil || retry != id {
		t.Fatal("identical import duplicated record", err)
	}
	input.ClientSecret = "replacement"
	if _, err = m.ImportOAuthClient(input); !errors.Is(err, ErrConflict) {
		t.Fatal("silent secret replacement", err)
	}
	input.ClientSecret = "import-secret"
	input.ActorID = "other-operator"
	if _, err = m.ImportOAuthClient(input); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign owner replaced import", err)
	}
}

func TestOAuthApplicationReplacementCommitsOnlyVerifiedSharedGrant(t *testing.T) {
	for _, clientID := range []string{"fixture-client", "replacement-client"} {
		t.Run(clientID, func(t *testing.T) {
			m, before := oauthFixture(t)
			file, _ := m.Load()
			second := file.Connections[0]
			second.ID = strings.Repeat("d", 32)
			file.Connections = append(file.Connections, second)
			if err := m.save(file); err != nil {
				t.Fatal(err)
			}
			oldApp := file.OAuthApplications[0]
			input := OAuthClientImport{Origin: oldApp.Origin, ClientID: clientID, ClientSecret: "replacement-secret", RedirectURI: oldApp.RedirectURI, Scopes: oldApp.Scopes, ActorID: "tester", ReplacesApplicationID: oldApp.ID}
			id, err := m.ImportOAuthClient(input)
			if err != nil {
				t.Fatal(err)
			}
			pending, err := m.Load()
			unchanged, resolveErr := ResolveAuthorization(pending, pending.Connections[0])
			if err != nil || resolveErr != nil || unchanged != before || pending.OAuthApplications[0].ClientSecret != oldApp.ClientSecret {
				t.Fatal("candidate replaced active configuration before login", err, resolveErr)
			}
			app := oldApp
			app.ClientID, app.ClientSecret = input.ClientID, input.ClientSecret
			tokens := platform.OAuthTokens{AccessToken: platform.NewSecret("replacement-access"), RefreshToken: platform.NewSecret("replacement-refresh"), ExpiresAt: time.Now().Add(time.Hour), Scopes: app.Scopes}
			identity := file.Connections[0].Validation
			if _, err := m.CommitOAuthLogin(file.Connections[:1], file.Connections[0].ID, "", app, id, "tester", tokens, identity); !errors.Is(err, ErrConflict) {
				t.Fatal("partial shared replacement accepted", err)
			}
			wrongApp, wrongIdentity := app, identity
			wrongApp.OwnerID, wrongIdentity.UserID = 202, 202
			if _, err := m.CommitOAuthLogin(file.Connections, file.Connections[0].ID, "", wrongApp, id, "tester", tokens, wrongIdentity); !errors.Is(err, ErrConflict) {
				t.Fatal("replacement switched the account", err)
			}
			updated, err := m.CommitOAuthLogin(file.Connections, file.Connections[0].ID, "", app, id, "tester", tokens, identity)
			if err != nil || len(updated) != 2 {
				t.Fatal("replacement failed", err)
			}
			after, err := m.Load()
			if err != nil || len(after.OAuthClientImports) != 0 || after.OAuthApplications[0].ClientID != clientID || after.OAuthApplications[0].ClientSecret != input.ClientSecret || len(after.OAuthCredentials) != 1 {
				t.Fatal("replacement not committed atomically", err)
			}
			for i, record := range after.Connections {
				account, err := ResolveAuthorization(after, record)
				if err != nil || account.UserID != before.UserID || record.ID != file.Connections[i].ID || record.CredentialID != file.Connections[i].CredentialID || record.CredentialVersion != file.Connections[i].CredentialVersion+1 || account.Authorization.Generation != before.Authorization.Generation+1 {
					t.Fatal("shared identity or grant not updated", err)
				}
			}
			if len(after.OAuthCleanup) != 1 || after.OAuthCleanup[0].ClientID != oldApp.ClientID || after.OAuthCleanup[0].ClientSecret != oldApp.ClientSecret || after.OAuthCleanup[0].State != "manual" {
				t.Fatal("old grant cleanup lost its original application credentials")
			}
		})
	}
}

func TestReplaceOAuthClientImportRequiresCurrentOwnedImport(t *testing.T) {
	m, _, _ := testManager(t)
	input := OAuthClientImport{Origin: "https://gitlab.invalid", ClientID: "wrong-client", ClientSecret: "wrong-secret", RedirectURI: "http://localhost:8088/cb", Scopes: []string{"api"}, ActorID: "tester"}
	id, err := m.ImportOAuthClient(input)
	if err != nil {
		t.Fatal(err)
	}
	corrected := input
	corrected.ClientSecret = "correct-secret"
	foreign := corrected
	foreign.ActorID = "other-operator"
	if _, err := m.ReplaceOAuthClientImport(id, foreign); !errors.Is(err, ErrConflict) {
		t.Fatal("foreign operator changed import", err)
	}
	foreign = corrected
	foreign.Origin = "https://other.invalid"
	if _, err := m.ReplaceOAuthClientImport(id, foreign); !errors.Is(err, ErrConflict) {
		t.Fatal("replacement crossed instances", err)
	}
	next, err := m.ReplaceOAuthClientImport(id, corrected)
	if err != nil || next == id || next == "" {
		t.Fatal("secret correction failed", err)
	}
	if _, err := m.ReplaceOAuthClientImport(id, input); !errors.Is(err, ErrConflict) {
		t.Fatal("stale edit replaced correction", err)
	}
	corrected.ClientID = "correct-client"
	final, err := m.ReplaceOAuthClientImport(next, corrected)
	if err != nil {
		t.Fatal("application ID correction failed", err)
	}
	file, err := m.Load()
	if err != nil || len(file.OAuthClientImports) != 1 || file.OAuthClientImports[0].ID != final || file.OAuthClientImports[0].ClientID != corrected.ClientID || file.OAuthClientImports[0].ClientSecret != corrected.ClientSecret || len(file.OAuthApplications) != 0 || len(file.OAuthCredentials) != 0 || len(file.Connections) != 0 {
		t.Fatal("replacement retained wrong config or authorized an account", err)
	}
	other := corrected
	other.ClientID = "another-client"
	if _, err := m.ImportOAuthClient(other); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReplaceOAuthClientImport(final, other); !errors.Is(err, ErrConflict) {
		t.Fatal("replacement silently selected another import", err)
	}
	file, err = m.Load()
	if err != nil || len(file.OAuthClientImports) != 2 || file.OAuthClientImports[0].ID != final {
		t.Fatal("conflicting replacement changed imports", err)
	}
}
