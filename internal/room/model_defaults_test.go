package room

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/authn"
)

func hostedGatewayFixture(t *testing.T, home string, admission externalAuthConfig, fail bool) *spaceServer {
	t.Helper()
	p := hostedOAuthProviderFixture{}
	s := hostedFixtureWithProvider(t, home, admission, p)
	s.configureOAuthModelDefaults(p, &oauthModelGatewayFixture{fail: fail})
	return s
}

type hostedOAuthProviderFixture struct{ hostedProviderFixture }

func (p hostedOAuthProviderFixture) CompleteWithToken(ctx context.Context, values url.Values) (authn.Identity, string, error) {
	id, err := p.Complete(ctx, values)
	return id, "iam-private-for-" + id.Subject, err
}

func TestHostedDefaultModelsUseEachOwnersQuotaAndOrdinaryCatalog(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "deployment-key-must-not-be-used")
	home := t.TempDir()
	s := hostedGatewayFixture(t, home, externalAuthConfig{Provider: "fixture", AllowedSubjects: []string{"alice", "bob"}}, false)
	var mu sync.Mutex
	probed := map[string]int{}
	s.assembly.config.modelProbe = func(ctx context.Context, opts dsh.Options) (dsh.ProbeResult, error) {
		if opts.Credential == nil || !strings.HasPrefix(opts.Credential.Value, "gk_quota-for-") || opts.Model.BaseURL != hostedGatewayBaseURL || opts.Model.ModelID != hostedDefaultModel {
			t.Error("probe did not bind the owner's quota and default route")
			return dsh.ProbeResult{}, errors.New("wrong quota")
		}
		mu.Lock()
		probed[opts.Credential.Value]++
		mu.Unlock()
		return successfulModelProbe(ctx, opts)
	}
	refs := map[string]string{}
	for _, account := range []string{"alice", "bob"} {
		cookie, subject, space := hostedLogin(t, s, account)
		runtime := waitSpaceState(t, s, space, "ready")
		models := runtime.manager.config.models
		entry, err := models.catalog().resolve(modelSelection{Mode: "default"})
		if err != nil || entry.ModelID != hostedDefaultModel || entry.BaseURL != hostedGatewayBaseURL || !models.hasValidation(entry.ID) {
			t.Fatal("default was not published through normal model validation", err)
		}
		if !reflect.DeepEqual(entry.ModelParameters, hostedDefaultModelParameters()) {
			t.Fatal("default model is missing advanced parameters", entry.ModelParameters)
		}
		credential, err := models.Credentials.get(entry.CredentialRef)
		if err != nil || credential.Value != "gk_quota-for-"+account {
			t.Fatal("runtime catalog is bound to another quota", err)
		}
		refs[account] = entry.CredentialRef
		if account == "bob" {
			if _, err := models.Credentials.get(refs["alice"]); err == nil {
				t.Fatal("another owner's credential reference resolved")
			}
		}
		response := hostedRequest(s, spaceRoute(space)+"/v1/models", nil, cookie, "")
		if response.Code != 200 || strings.Contains(response.Body.String(), "gk_quota-for-") {
			t.Fatal("model management is unavailable or exposed a key", response.Code)
		}
		for _, path := range []string{filepath.Join(home, "users.json"), filepath.Join(runtime.roots.Home, "config.toml")} {
			data, err := os.ReadFile(path)
			if err != nil || strings.Contains(string(data), "gk_quota-for-") {
				t.Fatal("model key escaped private credential storage", err)
			}
		}
		info, err := os.Stat(s.access.users.gatewayPath(subject.UserID))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("bootstrap key is not private", err)
		}
		// A later login supplies the key again, without replacing user settings
		// or revalidating an already configured model catalog.
		_, _, same := hostedLogin(t, s, account)
		if same != space || !reflect.DeepEqual(models.catalog().Entries, []modelEntry{entry}) {
			t.Fatal("repeat login changed the user's model settings")
		}
		// Extra Spaces use this same owner's credential, never another owner.
		id, _ := newOpaqueID()
		if w := hostedRequest(s, "/v1/spaces", spaceChangeInput{RequestID: id, Name: "工作"}, cookie, ""); w.Code != 202 {
			t.Fatal("new Space failed", w.Code)
		}
		extra := waitSpaceState(t, s, id, "ready")
		extraEntry, _ := extra.manager.config.models.catalog().resolve(modelSelection{Mode: "default"})
		if !reflect.DeepEqual(extraEntry.ModelParameters, hostedDefaultModelParameters()) {
			t.Fatal("additional Space lost advanced defaults", extraEntry.ModelParameters)
		}
		extraKey, err := extra.manager.config.models.Credentials.get(extraEntry.CredentialRef)
		if err != nil || extraKey.Value != credential.Value {
			t.Fatal("extra Space charged a different owner", err)
		}
	}
	mu.Lock()
	if probed["gk_quota-for-alice"] != 2 || probed["gk_quota-for-bob"] != 2 || len(probed) != 2 {
		mu.Unlock()
		t.Fatal("unexpected model probes", probed)
	}
	mu.Unlock()
	if err := s.closeWithTimeout(); err != nil {
		t.Fatal(err)
	}
	restored := hostedFixture(t, home)
	for _, record := range restored.directory.Spaces {
		restored.start(record, nil, spaceAddress("127.0.0.1:8088"))
		runtime := waitSpaceState(t, restored, record.ID, "ready")
		entry, _ := runtime.manager.config.models.catalog().resolve(modelSelection{Mode: "default"})
		key, err := runtime.manager.config.models.Credentials.get(entry.CredentialRef)
		owner, _ := restored.access.users.user(record.OwnerUserID)
		if err != nil || key.Value != "gk_quota-for-"+owner.Identity.Subject {
			t.Fatal("restart changed credential ownership", err)
		}
	}
}

func TestHostedGatewayArrivalUsesExistingSetupAndPreservesCustomModels(t *testing.T) {
	s := hostedFixture(t, t.TempDir())
	cookie, subject, space := hostedLogin(t, s, "alice")
	waitSpaceState(t, s, space, "setup_required")
	// This Space already existed before the user's first Gateway key arrived.
	if err := s.access.users.saveGatewayKey(subject.UserID, "gk_alice-initial"); err != nil {
		t.Fatal(err)
	}
	runtime := waitSpaceState(t, s, space, "ready")
	service := runtime.manager.modelControl.service
	_, bob, bobSpace := hostedLogin(t, s, "bob")
	if err := s.access.users.saveGatewayKey(bob.UserID, "gk_bob"); err != nil {
		t.Fatal(err)
	}
	bobRuntime := waitSpaceState(t, s, bobSpace, "ready")
	bobBefore := bobRuntime.manager.config.models.catalog()
	custom := modelTestEntry("custom")
	requireModelComplete(t, modelChange(t, runtime.manager.modelControl, modelChangeInput{Kind: "save", Entry: custom, APIKey: "own-custom-key"}))
	before, err := os.ReadFile(service.document.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.access.users.saveGatewayKey(subject.UserID, "gk_alice-new"); err != nil {
		t.Fatal(err)
	}
	hostedLogin(t, s, "alice")
	after, _ := os.ReadFile(service.document.path)
	if string(before) != string(after) || len(runtime.manager.config.models.catalog().Entries) != 2 {
		t.Fatal("login overwrote a user's customized model settings")
	}
	if !reflect.DeepEqual(bobBefore, bobRuntime.manager.config.models.catalog()) {
		t.Fatal("one user's model edits changed another user's configuration")
	}
	if w := hostedRequest(s, spaceRoute(space)+"/v1/models", nil, cookie, ""); w.Code != 200 {
		t.Fatal("existing model settings became unavailable")
	}
}

func TestHostedDefaultValidationFailureStaysInSetupAndCanRetry(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "deployment-key-must-not-be-used")
	s := hostedFixture(t, t.TempDir())
	var fail atomic.Bool
	fail.Store(true)
	s.assembly.config.modelProbe = func(ctx context.Context, opts dsh.Options) (dsh.ProbeResult, error) {
		if fail.Load() {
			return dsh.ProbeResult{}, errors.New("user quota exhausted")
		}
		if opts.Credential == nil || opts.Credential.Value != "gk_alice" {
			return dsh.ProbeResult{}, errors.New("wrong quota")
		}
		return successfulModelProbe(ctx, opts)
	}
	cookie, subject, space := hostedLogin(t, s, "alice")
	waitSpaceState(t, s, space, "setup_required")
	if err := s.access.users.saveGatewayKey(subject.UserID, "gk_alice"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		w := hostedRequest(s, spaceRoute(space)+"/v1/setup", nil, cookie, "")
		if w.Code == 200 && strings.Contains(w.Body.String(), `"status":"failed"`) {
			if !strings.Contains(w.Body.String(), hostedDefaultModel) || strings.Contains(w.Body.String(), "gk_alice") {
				t.Fatal("setup lost the default or leaked the key")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("validation failure was not shown in the ordinary setup UI")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// No catalog was published using the deployment environment as a fallback.
	owner, _ := s.access.users.user(subject.UserID)
	config, err := readStartupConfig(filepath.Join(owner.DefaultSpace.Roots.Home, "config.toml"), true, false)
	if err != nil || len(config.Models.Entries) != 0 {
		t.Fatal("failed validation published a model", err)
	}
	input := setupModelInput{RequestID: "retry-own-model", ExpectedVersion: 1, ModelParameters: ModelParameters{Protocol: "openai-completions", BaseURL: hostedGatewayBaseURL, ModelID: hostedDefaultModel}}
	fail.Store(false)
	if w := hostedRequest(s, spaceRoute(space)+"/v1/setup/model", input, cookie, ""); w.Code != 202 {
		t.Fatal("ordinary setup retry unavailable", w.Code)
	}
	waitSpaceState(t, s, space, "ready")
}

func TestHostedMissingGatewayKeyCannotUseDeploymentQuota(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "deployment-key-must-not-be-used")
	s := hostedGatewayFixture(t, t.TempDir(), externalAuthConfig{Provider: "fixture", AllowAllAuthenticated: true}, true)
	callback, cookie := beginLogin(t, s.access.login)
	w := callbackLogin(s.access.login, callback, cookie)
	if w.Header().Get("Location") != "/access?sso=failed" || len(s.directory.Spaces) != 0 {
		t.Fatal("missing user credential provisioned a model or authenticated a fallback")
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == s.access.cookieName() {
			t.Fatal("missing user credential published a session")
		}
	}
}

func TestHostedLegacyDefaultUpgradesThroughSpaceStartup(t *testing.T) {
	s := hostedFixture(t, t.TempDir())
	cookie, _, space := hostedLogin(t, s, "alice")
	waitSpaceState(t, s, space, "setup_required")
	legacy := ModelParameters{Protocol: "openai-completions", BaseURL: hostedGatewayBaseURL, ModelID: hostedDefaultModel}
	input := setupModelInput{RequestID: "legacy-default", ExpectedVersion: 1, ModelParameters: legacy, APIKey: "owner-key"}
	if w := hostedRequest(s, spaceRoute(space)+"/v1/setup/model", input, cookie, ""); w.Code != 202 {
		t.Fatal("legacy setup failed", w.Code)
	}
	runtime := waitSpaceState(t, s, space, "ready")
	entry, err := runtime.manager.config.models.catalog().resolve(modelSelection{Mode: "default"})
	if err != nil || !reflect.DeepEqual(entry.ModelParameters, hostedDefaultModelParameters()) {
		t.Fatal("Space startup did not upgrade the old default", entry, err)
	}
}
