package gitlaboauth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/registry"
)

func catalogFixture(t *testing.T) (*Catalog, *Snapshot, Application) {
	t.Helper()
	saved := &Snapshot{Version: 1, Applications: []Application{}}
	c, err := Open(Store{Load: func() (Snapshot, error) { return *saved, nil }, Save: func(f Snapshot) error { *saved = f; return nil }, OtherGrant: func(string, int64, string) (bool, error) { return false, nil }})
	if err != nil {
		t.Fatal(err)
	}
	app := Application{OAuthApplication: registry.OAuthApplication{ID: strings.Repeat("a", 32), Source: ServiceSource, Origin: "https://gitlab.example", OwnerID: 99, ClientID: "client", RedirectURI: "https://teamx.example/v1/platforms/gitlab/callback", Scopes: []string{"api"}}, Name: "Company GitLab", Secret: "control-plane-secret", Status: "active"}
	public, err := c.Update(t.Context(), app, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := c.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return c, saved, public
}
func TestServiceApplicationRotationFencesAndJoinsOriginalUses(t *testing.T) {
	c, saved, app := catalogFixture(t)
	source := c.ForSpace("alice", strings.Repeat("b", 32))
	u, err := source.Begin(t.Context(), app.OAuthApplication, "operation", "browser-state")
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := c.CallbackSpace("browser-state"); !ok || id != strings.Repeat("b", 32) {
		t.Fatal("lost callback binding")
	}
	if _, err := source.Begin(t.Context(), app.OAuthApplication, "another", "browser-state"); err == nil {
		t.Fatal("duplicate state admitted")
	}
	changed := make(chan error, 1)
	go func() {
		input := app
		input.Secret = "rotated-secret"
		_, err := c.Update(t.Context(), input, app.Version)
		changed <- err
	}()
	select {
	case <-u.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("old use not canceled")
	}
	if err := u.Commit(func() error { t.Fatal("stale writer called"); return nil }); err == nil {
		t.Fatal("stale permit committed")
	}
	select {
	case <-changed:
		t.Fatal("cancellation reported as settlement")
	default:
	}
	u.Close()
	if err := <-changed; err != nil {
		t.Fatal(err)
	}
	if err := source.Check(app.OAuthApplication); err == nil {
		t.Fatal("old application version accepted")
	}
	data, _ := json.Marshal(c.Public())
	if strings.Contains(string(data), "secret") {
		t.Fatal("public secret")
	}
	if saved.Applications[0].Secret != "rotated-secret" {
		t.Fatal("rotation not persisted")
	}
	restarted, err := Open(Store{Load: func() (Snapshot, error) { return *saved, nil }, Save: c.store.Save})
	if err != nil {
		t.Fatal(err)
	}
	current, err := restarted.ForSpace("alice", strings.Repeat("b", 32)).Applications()
	if err != nil || len(current) != 1 || current[0].ClientSecret != "" || current[0].Version != 2 {
		t.Fatal("restart reference")
	}
}
func TestServiceApplicationDisableAndUncertainSaveNeverFallback(t *testing.T) {
	c, _, app := catalogFixture(t)
	source := c.ForSpace("bob", strings.Repeat("c", 32))
	c.store.Save = func(Snapshot) error { return errors.New("uncertain save") }
	if _, err := c.Update(t.Context(), app, 1); err == nil {
		t.Fatal("save failure ignored")
	}
	if _, err := source.Begin(t.Context(), app.OAuthApplication, "after-failure", ""); err == nil {
		t.Fatal("admitted after uncertain write")
	}
	if _, err := source.Applications(); err == nil {
		t.Fatal("catalog silently reopened")
	}
}
func TestServiceApplicationRevocationBlocksNewUsesAndSharingGrant(t *testing.T) {
	c, _, app := catalogFixture(t)
	source := c.ForSpace("alice", strings.Repeat("b", 32))
	u, err := source.BeginRevoke(t.Context(), app.OAuthApplication, "revoke", 201)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Begin(t.Context(), app.OAuthApplication, "new-login", "state"); err == nil {
		t.Fatal("login raced remote revoke")
	}
	u.Close()
	c.store.OtherGrant = func(string, int64, string) (bool, error) { return true, nil }
	if _, err := source.BeginRevoke(t.Context(), app.OAuthApplication, "shared-revoke", 201); err == nil {
		t.Fatal("sharing grant revoked")
	}
}
func TestServiceApplicationChecksOriginScopesAndImmutablePublicIdentity(t *testing.T) {
	c, _, app := catalogFixture(t)
	source := c.ForSpace("alice", strings.Repeat("b", 32))
	foreign := app.OAuthApplication
	foreign.Origin = "https://evil.example"
	if _, err := source.Begin(t.Context(), foreign, "bad", ""); err == nil {
		t.Fatal("foreign origin admitted")
	}
	edited := app
	edited.ClientID = "another-client"
	if _, err := c.Update(t.Context(), edited, 1); err == nil {
		t.Fatal("grant rebound to different client")
	}
	input := app
	input.ID = strings.Repeat("d", 32)
	input.Scopes = []string{"read_repository"}
	if _, err := c.Update(t.Context(), input, 0); err == nil {
		t.Fatal("invalid application scopes accepted")
	}
}
