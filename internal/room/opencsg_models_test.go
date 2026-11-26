package room

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-aie/teamx/internal/authn"
)

type oauthModelProviderFixture struct{ codeProviderFixture }

func (p oauthModelProviderFixture) CompleteWithToken(ctx context.Context, values url.Values) (authn.Identity, string, error) {
	id, err := p.Complete(ctx, values)
	return id, "iam-private-for-" + id.Subject, err
}

type oauthModelGatewayFixture struct {
	calls atomic.Int32
	fail  bool
}

func (g *oauthModelGatewayFixture) GatewayAPIKey(_ context.Context, id authn.Identity, token string) (string, error) {
	g.calls.Add(1)
	if g.fail || token != "iam-private-for-"+id.Subject {
		return "", errors.New("personal key unavailable")
	}
	return "gk_quota-for-" + id.Subject, nil
}

func TestOpenCSGGatewayResumesExistingSetupAndKeepsOwnersIsolated(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "deployment-key-must-not-be-used")
	home := t.TempDir()
	c := externalAuthConfig{Provider: "opencsg", Issuer: "https://opencsg.example", ClientID: "teamx-client", AllowAllAuthenticated: true}
	p := oauthModelProviderFixture{}
	s := hostedFixtureWithProvider(t, home, c, p)
	_, alice, space := codeLogin(t, s, "alice")
	waitSpaceState(t, s, space, "setup_required")
	// Upgrade an existing IAM user and Space without changing their identity.
	gateway := &oauthModelGatewayFixture{}
	s.configureOAuthModelDefaults(p, gateway)
	_, again, same := codeLogin(t, s, "alice")
	if again != alice || same != space {
		t.Fatal("enabling model bootstrap changed the existing identity or Space")
	}
	aliceRuntime := waitSpaceState(t, s, space, "ready")
	_, bob, bobSpace := codeLogin(t, s, "bob")
	bobRuntime := waitSpaceState(t, s, bobSpace, "ready")
	if bob.UserID == alice.UserID || bobSpace == space {
		t.Fatal("OIDC users shared a local identity or Space")
	}
	for _, pair := range []struct {
		user, account string
		runtime       *SpaceRuntime
	}{{alice.UserID, "alice", aliceRuntime}, {bob.UserID, "bob", bobRuntime}} {
		models := pair.runtime.manager.config.models
		entry, err := models.catalog().resolve(modelSelection{Mode: "default"})
		if err != nil || entry.ModelID != hostedDefaultModel || entry.BaseURL != hostedGatewayBaseURL || !models.hasValidation(entry.ID) {
			t.Fatal("OAuth key skipped ordinary model validation", err)
		}
		key, err := models.Credentials.get(entry.CredentialRef)
		if err != nil || key.Value != "gk_quota-for-"+pair.account {
			t.Fatal("default model used another owner's quota", err)
		}
		info, err := os.Stat(s.access.users.gatewayPath(pair.user))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("owner's bootstrap key is not private", err)
		}
		other := aliceRuntime.manager.config.models
		if pair.account == "alice" {
			other = bobRuntime.manager.config.models
		}
		if _, err := other.Credentials.get(entry.CredentialRef); err == nil {
			t.Fatal("another owner could resolve a private model key")
		}
	}
	custom := modelTestEntry("custom")
	requireModelComplete(t, modelChange(t, aliceRuntime.manager.modelControl, modelChangeInput{Kind: "save", Entry: custom, APIKey: "own-custom-key"}))
	path := aliceRuntime.manager.modelControl.service.document.path
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	codeLogin(t, s, "alice")
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || gateway.calls.Load() != 3 {
		t.Fatal("repeat login replaced customized models or fetched an unexpected key")
	}
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	// Persist only Gateway keys; the transient IAM access token must disappear.
	if err := filepath.WalkDir(home, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if strings.Contains(string(data), "iam-private-for-") {
			t.Error("IAM access token persisted beyond its login request")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	s = hostedFixtureWithProvider(t, home, c, p)
	_, restored, restoredSpace := codeLogin(t, s, "alice")
	if restored != alice || restoredSpace != space {
		t.Fatal("restart changed the upgraded OIDC user")
	}
	if key, _, err := s.access.users.gatewayKey(alice.UserID); err != nil || key != "gk_quota-for-alice" {
		t.Fatal("restart lost the owner's Gateway key", err)
	}
	for _, record := range s.directory.Spaces {
		s.start(record, nil, spaceAddress("127.0.0.1:8088"))
	}
	waitSpaceState(t, s, space, "ready")
}

func TestOpenCSGGatewayChecksAdmissionBeforeFetchingQuota(t *testing.T) {
	for _, variant := range []string{"denied", "disabled", "missing-key"} {
		t.Run(variant, func(t *testing.T) {
			c := externalAuthConfig{Provider: "opencsg", Issuer: "https://opencsg.example", ClientID: "teamx-client", AllowedSubjects: []string{"alice"}, HubOrigin: "https://hub.example"}
			p := oauthModelProviderFixture{}
			s := hostedFixtureWithProvider(t, t.TempDir(), c, p)
			if _, ok := s.access.login.provider.(modelBootstrapLogin); !ok {
				t.Fatal("explicit Hub configuration did not enable OAuth model bootstrap")
			}
			gateway := &oauthModelGatewayFixture{fail: variant == "missing-key"}
			s.configureOAuthModelDefaults(p, gateway)
			account := "alice"
			if variant == "denied" {
				account = "bob"
			}
			if variant == "disabled" {
				id, _ := p.Complete(t.Context(), url.Values{"code": {account}})
				user, err := s.access.users.reserve(id, c)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.access.users.setStatus(user.ID, "disabled"); err != nil {
					t.Fatal(err)
				}
			}
			start := hostedRequest(s, externalLoginPath, struct{}{}, nil, "")
			jump, _ := url.Parse(start.Header().Get("Location"))
			callback, _ := url.Parse(jump.Query().Get("redirect_uri"))
			callback.RawQuery = url.Values{"state": {jump.Query().Get("state")}, "code": {account}}.Encode()
			result := hostedRequest(s, callback.RequestURI(), nil, start.Result().Cookies()[0], "")
			if result.Header().Get("Location") != "/access?sso=failed" || len(s.directory.Spaces) != 0 {
				t.Fatal("failed quota provisioning published a Space or authenticated a fallback")
			}
			for _, cookie := range result.Result().Cookies() {
				if cookie.Name == s.access.cookieName() && cookie.Value != "" {
					t.Fatal("failed quota provisioning published a session")
				}
			}
			want := int32(0)
			if variant == "missing-key" {
				want = 1
			}
			if gateway.calls.Load() != want || result.Code != http.StatusSeeOther {
				t.Fatal("Gateway request ran before admission")
			}
		})
	}
}
