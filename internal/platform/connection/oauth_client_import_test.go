package connection

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
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
