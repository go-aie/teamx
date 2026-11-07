package connection

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		if provider != "fixture" && provider != "gitlab" {
			return "", "", ErrRequest
		}
		return Normalize(name, origin)
	})
	m.SetIdentityResolver(func(provider, origin string, v platform.Validation) (platform.AccountIdentity, bool) {
		if v.Account.Valid() {
			return v.Account, true
		}
		a := platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: provider, Instance: origin}, ID: platform.ExternalID{Namespace: "fixture:integer", Value: fmt.Sprint(v.UserID)}}
		return a, v.UserID > 0 && platform.ValidUsername(v.Username)
	})
	return m, &writes, &gate
}
func request() Request {
	return Request{ID: strings.Repeat("a", 32), Name: "Fixture", Provider: "fixture", URL: "https://example.invalid/"}
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

func TestOAuthMetadataPlanIsReadOnlyAndHonorsStoreGate(t *testing.T) {
	m, writes, gate := testManager(t)
	req := Request{ID: strings.Repeat("a", 32), Name: "GitLab", Provider: "gitlab", URL: "https://example.invalid"}
	change, err := m.Plan(req, false)
	if err != nil || *writes != 0 {
		t.Fatal("plan wrote data", err)
	}
	*gate = errors.New("frozen")
	if _, err = m.Commit(change); !errors.Is(err, *gate) || *writes != 0 {
		t.Fatal("ignored store gate", err)
	}
	*gate = nil
	c, err := m.Commit(change)
	if err != nil {
		t.Fatal(err)
	}
	if c.CredentialKind != "oauth" || c.CredentialConfigured || c.Validation.Status != "unverified" {
		t.Fatal("metadata granted access")
	}
	if _, err = m.Plan(req, false); !errors.Is(err, ErrConflict) {
		t.Fatal("stale version accepted", err)
	}
	req.Version = c.Version
	req.Name = "Renamed"
	renamed := commitRequest(t, m, req, false)
	if renamed.CredentialVersion != c.CredentialVersion || renamed.ID != c.ID {
		t.Fatal("rename rotated credential")
	}
}
