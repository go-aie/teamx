package connection

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func oauthFixture(t *testing.T) (*Manager, Account) {
	t.Helper()
	m, _, _ := testManager(t)
	m.normalize = func(name, provider, origin string) (string, string, error) { return Normalize(name, origin) }
	req := request()
	req.Provider = "gitlab"
	c := commitRequest(t, m, req, false)
	file, _ := m.Load()
	app := OAuthApplication{ID: strings.Repeat("b", 32), Origin: c.URL, OwnerID: 101, ClientID: "fixture-client", ClientSecret: "fixture-secret", RedirectURI: "http://127.0.0.1:8088/v1/platform/connections/gitlab/callback", Scopes: []string{"api"}}
	cred := OAuthCredential{ID: strings.Repeat("c", 32), ApplicationID: app.ID, UserID: 101, GrantGeneration: 1, Scopes: []string{"api"}, State: "active", AccessToken: "oauth-access", RefreshToken: "oauth-refresh", ExpiresAt: time.Now().Add(time.Hour).UTC(), TokenRevision: 1}
	file.OAuthApplications = []OAuthApplication{app}
	file.OAuthCredentials = []OAuthCredential{cred}
	record := file.Connections[0]
	record.CredentialKind = "oauth"
	record.CredentialID = cred.ID
	record.CredentialConfigured = true
	record.Validation = platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), UserID: 101, Username: "operator"}
	record.IdentityBinding = &IdentityBinding{Generation: record.Generation, CredentialVersion: record.CredentialVersion, CredentialFingerprint: oauthFingerprint(authorization(app, cred))}
	file.Connections[0] = record
	if err := m.save(file); err != nil {
		t.Fatal(err)
	}
	file, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	a, err := ResolveAuthorization(file, file.Connections[0])
	if err != nil {
		t.Fatal(err)
	}
	return m, a
}

func TestOAuthNormalRefreshPreservesAuthorizationAndRejectsLegacyAccess(t *testing.T) {
	m, before := oauthFixture(t)
	file, _ := m.Load()
	c := file.Connections[0]
	if _, _, err := ResolveAccount(file, c); !errors.Is(err, platform.ErrCredential) {
		t.Fatal("unleased OAuth access", err)
	}
	if _, err := m.BeginVerification(c.ID, c.Version); !errors.Is(err, ErrRequest) {
		t.Fatal("profile verifier accepted OAuth", err)
	}
	ticket, err := m.BeginOAuthRefresh(before.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	file, err = m.Load()
	if err != nil {
		t.Fatal(err)
	}
	during, err := ResolveAuthorization(file, file.Connections[0])
	if err != nil || during != before {
		t.Fatal("refresh revoked stable evidence", err)
	}
	if _, err = m.BeginOAuthRefresh(before.Authorization); err == nil {
		t.Fatal("parallel destructive refresh allowed")
	}
	tokens := platform.OAuthTokens{AccessToken: platform.NewSecret("next-access"), RefreshToken: platform.NewSecret("next-refresh"), ExpiresAt: time.Now().Add(time.Hour).UTC(), Scopes: []string{"api"}}
	if err = m.CompleteOAuthRefresh(ticket, tokens, "active"); err != nil {
		t.Fatal(err)
	}
	file, err = m.Load()
	if err != nil {
		t.Fatal(err)
	}
	after, err := ResolveAuthorization(file, file.Connections[0])
	if err != nil || after != before || file.OAuthCredentials[0].TokenRevision != 2 || file.OAuthCredentials[0].AccessToken != "next-access" {
		t.Fatal("normal refresh changed grant", err)
	}
	if err = m.CompleteOAuthRefresh(ticket, tokens, "active"); !errors.Is(err, ErrConflict) {
		t.Fatal("replayed completion", err)
	}
	for _, value := range []any{file, file.OAuthCredentials[0], file.OAuthApplications[0], ticket, tokens} {
		for _, secret := range []string{"next-access", "next-refresh", "fixture-secret", "oauth-refresh"} {
			if strings.Contains(fmt.Sprintf("%+v", value), secret) {
				t.Fatal("secret formatting leak")
			}
		}
	}
	for _, value := range []any{ticket, tokens} {
		data, err := json.Marshal(value)
		if err != nil || strings.Contains(string(data), "fixture-secret") || strings.Contains(string(data), "next-access") || strings.Contains(string(data), "next-refresh") || strings.Contains(string(data), "oauth-refresh") {
			t.Fatal("transient JSON leaked credentials", err)
		}
	}
}
func TestOAuthLoggedOutSharedCredentialAndSnapshotPreflight(t *testing.T) {
	m, _ := oauthFixture(t)
	file, _ := m.Load()
	second := file.Connections[0]
	second.ID = strings.Repeat("d", 32)
	file.Connections = append(file.Connections, second)
	if err := m.save(file); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load(); err != nil {
		t.Fatal("shared credential rejected", err)
	}
	cred := &file.OAuthCredentials[0]
	cred.State = "logged_out"
	cred.AccessToken = ""
	cred.RefreshToken = ""
	cred.ExpiresAt = time.Time{}
	for i := range file.Connections {
		file.Connections[i].CredentialConfigured = false
	}
	if err := m.save(file); err != nil {
		t.Fatal(err)
	}
	file, err := m.Load()
	if err != nil {
		t.Fatal("logged out snapshot rejected", err)
	}
	for _, c := range file.Connections {
		if _, err := ResolveAuthorization(file, c); !errors.Is(err, platform.ErrCredential) {
			t.Fatal("logged out grant admitted", err)
		}
		public := Public(file, c)
		if public.AuthorizationStatus != "logged_out" || public.CredentialConfigured {
			t.Fatal("incorrect logout view")
		}
	}
	tests := []func(*Snapshot){
		func(f *Snapshot) { f.Version = 2 },
		func(f *Snapshot) { f.Version = 99 },
		func(f *Snapshot) { f.OAuthCredentials[0].State = "active" },
		func(f *Snapshot) { f.OAuthCredentials[0].UserID++ },
		func(f *Snapshot) { f.OAuthApplications[0].Origin = "http://insecure.invalid" },
		func(f *Snapshot) { f.OAuthApplications[0].ClientID = "" },
		func(f *Snapshot) { f.OAuthApplications[0].RedirectURI = "https://:8088/callback" },
		func(f *Snapshot) { f.OAuthCredentials[0].Scopes = []string{"sudo"} },
	}
	baseline, _ := json.Marshal(file)
	for i, mutate := range tests {
		var invalid Snapshot
		if err = json.Unmarshal(baseline, &invalid); err != nil {
			t.Fatal(err)
		}
		mutate(&invalid)
		b, _ := json.Marshal(invalid)
		_ = os.WriteFile(m.store.Path, b, 0600)
		if _, err = m.Load(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("malformed fixture %d accepted: %v", i, err)
		}
	}
}
func TestOAuthRefreshCrashAndFailureRecovery(t *testing.T) {
	for _, outcome := range []string{"crash", "refresh_unknown", "reauthorization_required", "save_failed", "commit_unknown"} {
		t.Run(outcome, func(t *testing.T) {
			m, account := oauthFixture(t)
			ticket, err := m.BeginOAuthRefresh(account.Authorization)
			if err != nil {
				t.Fatal(err)
			}
			originalSave := m.store.Save
			switch outcome {
			case "save_failed":
				m.store.Save = func(string, any, int64) error { return errors.New("disk failed") }
			case "commit_unknown":
				m.store.Save = func(p string, v any, n int64) error {
					if err := originalSave(p, v, n); err != nil {
						return err
					}
					return errors.New("unknown commit")
				}
			}
			tokens := platform.OAuthTokens{AccessToken: platform.NewSecret("rotated-access"), RefreshToken: platform.NewSecret("rotated-refresh"), ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"api"}}
			if outcome != "crash" {
				state := outcome
				if outcome == "save_failed" || outcome == "commit_unknown" {
					state = "active"
				}
				err = m.CompleteOAuthRefresh(ticket, tokens, state)
				if (outcome == "save_failed" || outcome == "commit_unknown") != (err != nil) {
					t.Fatal("wrong save outcome", err)
				}
			}
			m.store.Save = originalSave
			file, err := m.Load()
			if err != nil {
				t.Fatal(err)
			}
			cred := file.OAuthCredentials[0]
			if outcome == "commit_unknown" {
				if cred.State != "active" || cred.AccessToken != "rotated-access" || cred.TokenRevision != 2 {
					t.Fatal("lost committed pair")
				}
			} else if outcome == "crash" || outcome == "save_failed" {
				if cred.State != "refresh_pending" {
					t.Fatal("crash evidence lost")
				}
				if _, err = m.BeginOAuthRefresh(account.Authorization); err == nil {
					t.Fatal("replayed old refresh token")
				}
			} else {
				if _, err := ResolveAuthorization(file, file.Connections[0]); !errors.Is(err, platform.ErrCredential) {
					t.Fatal("invalid grant admitted", err)
				}
			}
		})
	}
}
func TestOAuthRefreshRejectsChangedGrant(t *testing.T) {
	m, account := oauthFixture(t)
	ticket, err := m.BeginOAuthRefresh(account.Authorization)
	if err != nil {
		t.Fatal(err)
	}
	file, _ := m.Load()
	file.OAuthCredentials[0].GrantGeneration++
	file.Connections[0].IdentityBinding.CredentialFingerprint = oauthFingerprint(authorization(file.OAuthApplications[0], file.OAuthCredentials[0]))
	if err = m.save(file); err != nil {
		t.Fatal(err)
	}
	if err = m.CompleteOAuthRefresh(ticket, platform.OAuthTokens{}, "refresh_unknown"); !errors.Is(err, ErrConflict) {
		t.Fatal("old authorization completion accepted", err)
	}
}
