package room

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-aie/teamx/internal/authn"
	"github.com/go-aie/teamx/internal/authn/opencsg"
)

const hostedDefaultModel = "deepseek-v4.1-flash-1017"
const hostedGatewayBaseURL = "https://ai.space.opencsg.com/v1"

// Model credential retrieval is optional and separate from authentication.
// Identity and browser sessions never contain the returned key.
type gatewayCredentialSource interface {
	GatewayAPIKey(context.Context, authn.Identity, url.Values) (string, error)
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
		return nil
	}
	gateway, ok := provider.(gatewayCredentialSource)
	if !ok {
		return nil
	}
	s.access.login.provision = func(ctx context.Context, identity authn.Identity, values url.Values) error {
		entry, err := s.access.users.reserve(identity, s.access.config.Deployment.Authentication)
		if err != nil {
			return err
		}
		key, err := gateway.GatewayAPIKey(ctx, identity, values)
		if err != nil {
			return err
		}
		return s.access.users.saveGatewayKey(entry.ID, key)
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
	f.mu.Lock()
	if f.blocked != nil || f.service == nil || len(f.service.snapshot().Entries) != 0 || f.operation != nil && (f.operation.Operation.Status == "validating" || f.operation.Operation.Status == "saved") {
		f.mu.Unlock()
		return
	}
	parameters := ModelParameters{Protocol: "openai-completions", BaseURL: hostedGatewayBaseURL, ModelID: hostedDefaultModel}
	f.prefill, f.prefillKey = parameters, key
	id, err := newOpaqueID()
	input := setupModelInput{RequestID: id, ExpectedVersion: f.service.snapshot().Version, ModelParameters: parameters, APIKey: key}
	f.mu.Unlock()
	if err == nil {
		_, _ = f.submit(input) // Submission/validation failures remain in the setup UI.
	}
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
