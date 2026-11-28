package room

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/authn"
	"github.com/go-aie/teamx/internal/authn/opencsg"
)

const hostedDefaultModel = "deepseek-v4.1-flash-1017"
const hostedGatewayBaseURL = "https://ai.space.opencsg.com/v1"

func hostedDefaultModelParameters() ModelParameters {
	return ModelParameters{
		Protocol: "openai-completions", BaseURL: hostedGatewayBaseURL, ModelID: hostedDefaultModel,
		InputModalities: []string{"text", "image"}, ContextWindow: 1048576, MaxTokens: 32768,
		ReasoningEffort: "off", Compat: dsh.ModelCompat{SupportsDeveloperRole: new(false)},
	}
}

// Login supplies the owner's Gateway key; model initialization, validation and
// subsequent edits all use the ordinary Space model service and catalog.
func (s *spaceServer) configureModelDefaults(provider authn.Provider) error {
	if oauth, ok := provider.(oauthIdentityProvider); ok && s.access.config.Deployment.Authentication.HubOrigin != "" {
		gateway, err := opencsg.NewOAuthGateway(s.access.config.Deployment.Authentication.HubOrigin, nil)
		if err != nil {
			return err
		}
		s.configureOAuthModelDefaults(oauth, gateway)
	}
	return nil
}

type oauthIdentityProvider interface {
	authn.Provider
	CompleteWithToken(context.Context, url.Values) (authn.Identity, string, error)
}

type oauthGatewaySource interface {
	GatewayAPIKey(context.Context, authn.Identity, string) (string, error)
}

type modelBootstrapLogin struct {
	authn.Provider
	complete func(context.Context, url.Values) (authn.Identity, error)
}

func (p modelBootstrapLogin) Complete(ctx context.Context, values url.Values) (authn.Identity, error) {
	return p.complete(ctx, values)
}

func (s *spaceServer) configureOAuthModelDefaults(provider oauthIdentityProvider, gateway oauthGatewaySource) {
	// The OAuth token stays on this request's stack. Admission precedes the Key
	// read; only the owner's Gateway key reaches the ordinary model module.
	if renewable, ok := provider.(authn.RenewableProvider); ok && s.access.renewal != nil {
		s.access.login.provider = renewableModelBootstrap{RenewableProvider: renewable, complete: func(ctx context.Context, values url.Values) (authn.Login, error) {
			login, err := renewable.CompleteGrant(ctx, values)
			if err != nil {
				return authn.Login{}, err
			}
			entry, err := s.access.users.reserve(login.Identity, s.access.config.Deployment.Authentication)
			if err != nil {
				return authn.Login{}, err
			}
			key, err := gateway.GatewayAPIKey(ctx, login.Identity, login.Grant.AccessToken)
			if err != nil {
				return authn.Login{}, err
			}
			if err = s.access.users.saveGatewayKey(entry.ID, key); err != nil {
				return authn.Login{}, err
			}
			return login, nil
		}}
		return
	}
	s.access.login.provider = modelBootstrapLogin{Provider: provider, complete: func(ctx context.Context, values url.Values) (authn.Identity, error) {
		identity, token, err := provider.CompleteWithToken(ctx, values)
		if err != nil {
			return authn.Identity{}, err
		}
		entry, err := s.access.users.reserve(identity, s.access.config.Deployment.Authentication)
		if err != nil {
			return authn.Identity{}, err
		}
		key, err := gateway.GatewayAPIKey(ctx, identity, token)
		if err != nil {
			return authn.Identity{}, err
		}
		if err := s.access.users.saveGatewayKey(entry.ID, key); err != nil {
			return authn.Identity{}, err
		}
		return identity, nil
	}}
}

type hostedGatewayCredential struct {
	APIKey string `json:"api_key"`
}

func (u *hostedUsers) gatewayPath(user string) string {
	return filepath.Join(u.home, "user-credentials", user, "ai-gateway.json")
}

func (u *hostedUsers) gatewaySignalLocked(user string) chan struct{} {
	if u.gatewayChanges == nil {
		u.gatewayChanges = map[string]chan struct{}{}
	}
	if u.gatewayChanges[user] == nil {
		u.gatewayChanges[user] = make(chan struct{})
	}
	return u.gatewayChanges[user]
}

// Read the signal before the key so a concurrent login cannot be missed.
func (u *hostedUsers) gatewayKey(user string) (string, <-chan struct{}, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	signal := u.gatewaySignalLocked(user)
	if u.failed || !validOperatorID(user) {
		return "", signal, ErrIdentity
	}
	var saved hostedGatewayCredential
	err := readIdentitySnapshot(u.gatewayPath(user), &saved)
	if errors.Is(err, os.ErrNotExist) {
		return "", signal, nil
	}
	if err != nil {
		return "", signal, err
	}
	return saved.APIKey, signal, nil
}

func (u *hostedUsers) saveGatewayKey(user, key string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.failed || !validOperatorID(user) || !strings.HasPrefix(key, "gk_") || len(key) <= 3 || len(key) > 64<<10 || strings.ContainsAny(key, "\x00\r\n") {
		return ErrIdentity
	}
	active := false
	for _, entry := range u.saved.Users {
		active = active || entry.ID == user && entry.Status == "active"
	}
	if !active {
		return ErrIdentity
	}
	path := u.gatewayPath(user)
	if err := makeDurableDir(filepath.Dir(path)); err != nil {
		return err
	}
	// Credential publication is independent of the installation's identity
	// writer. A failed model-key write must not fence every user's registration.
	if err := (&snapshotWriter{ops: defaultSnapshotOps()}).save(path, hostedGatewayCredential{key}, maxPrivateFileBytes); err != nil {
		return err
	}
	close(u.gatewaySignalLocked(user))
	delete(u.gatewayChanges, user)
	return nil
}

// Reuse first-use validation and publication. Only an empty catalog is eligible;
// existing model choices and active Sessions keep their immutable credentials.
func (f *firstModelSetup) submitGatewayModel(key string) {
	f.submitDefaultModel(hostedDefaultModelParameters(), key)
}

func (f *firstModelSetup) submitDefaultModel(parameters ModelParameters, key string) {
	f.mu.Lock()
	if f.blocked != nil || f.service == nil || len(f.service.snapshot().Entries) != 0 || f.operation != nil && (f.operation.Operation.Status == "validating" || f.operation.Operation.Status == "saved") {
		f.mu.Unlock()
		return
	}
	f.prefill, f.prefillKey = parameters.clone(), key
	id, err := newOpaqueID()
	input := setupModelInput{RequestID: id, ExpectedVersion: f.service.snapshot().Version, ModelParameters: parameters, APIKey: key}
	f.mu.Unlock()
	if err == nil {
		_, _ = f.submit(input) // Submission/validation failures remain in the setup UI.
	}
}

// Only creation may nominate an owned source. Reset does not inherit settings,
// and a configured target never depends on its source's later model changes.
func (s *spaceServer) initialSpaceModel(record SpaceRecord) (ModelParameters, string, error) {
	if s.access.users != nil || record.ModelSourceSpaceID == "" || record.Reset != nil {
		return ModelParameters{}, "", nil
	}
	s.mu.Lock()
	var source SpaceRecord
	for _, candidate := range s.directory.Spaces {
		if candidate.ID == record.ModelSourceSpaceID && candidate.OwnerUserID == record.OwnerUserID {
			source = candidate
			break
		}
	}
	s.mu.Unlock()
	if source.ID == "" || source.ID == record.ID {
		return ModelParameters{}, "", ErrIdentity
	}
	path := filepath.Join(source.Roots.Home, "config.toml")
	if source.ID == s.access.space.SpaceID {
		path = s.assembly.config.settings.path
	}
	config, err := readStartupConfig(path, true, false)
	if err != nil {
		return ModelParameters{}, "", err
	}
	if config.Models == nil || len(config.Models.Entries) == 0 {
		return ModelParameters{}, "", nil
	}
	entry, err := config.Models.resolve(modelSelection{Mode: "default"})
	if err != nil {
		return ModelParameters{}, "", err
	}
	parameters := entry.ModelParameters.clone()
	credential, err := (&modelCredentials{dir: filepath.Join(source.Roots.Home, "model-credentials")}).get(entry.CredentialRef)
	if err != nil {
		return parameters, "", err
	}
	return parameters, credential.Value, nil
}

func (f *firstModelSetup) followGatewayCredential(ctx context.Context, users *hostedUsers, owner string) {
	f.workers.Add(1)
	go func() {
		defer f.workers.Done()
		for {
			key, changed, err := users.gatewayKey(owner)
			if err == nil && key != "" {
				f.submitGatewayModel(key)
			}
			select {
			case <-ctx.Done():
				return
			case <-changed:
			}
		}
	}()
}

// Only initial login performs model bootstrap; refresh/verify forward directly
// to the provider and cannot fetch a key or replace the owner's model choices.
type renewableModelBootstrap struct {
	authn.RenewableProvider
	complete func(context.Context, url.Values) (authn.Login, error)
}

func (p renewableModelBootstrap) CompleteGrant(ctx context.Context, v url.Values) (authn.Login, error) {
	return p.complete(ctx, v)
}
