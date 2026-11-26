// Package gitlaboauth separates application discovery and confidential OAuth
// operations from the Space that owns and commits a user's grant.
package gitlaboauth

import (
	"context"
	"net/http"
	"slices"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/registry"
)

// Source is selected by deployment assembly. Managed sources never return a
// client secret. Each Use is bound to one immutable application and operation.
type Source interface {
	Applications() ([]registry.OAuthApplication, error)
	Check(registry.OAuthApplication) error
	Begin(context.Context, registry.OAuthApplication, string) (*Use, error)
	Managed() bool
	BeginRevoke(context.Context, registry.OAuthApplication, string, int64) (*Use, error)
}

type Use struct {
	ctx    context.Context
	app    registry.OAuthApplication
	secret platform.Secret
	guard  func(func() error) error
	close  func()
}

func (u *Use) Context() context.Context { return u.ctx }
func (u *Use) Close()                   { u.close() }

// Commit serializes only the final owner write with application changes. Network
// work never holds the catalog lock or gains access to the Space's store.
func (u *Use) Commit(write func() error) error { return u.guard(write) }
func (u *Use) Exchange(code, verifier platform.Secret, user int64, scopes string, transport http.RoundTripper) (platform.OAuthTokens, platform.Validation, error) {
	if err := u.Commit(func() error { return nil }); err != nil {
		return platform.OAuthTokens{}, platform.Validation{}, err
	}
	return gitlab.ExchangeOAuthCode(u.ctx, gitlab.OAuthLoginRequest{Origin: u.app.Origin, ClientID: u.app.ClientID, ClientSecret: u.secret, RedirectURI: u.app.RedirectURI, Code: code, Verifier: verifier, UserID: user, Scopes: scopes}, transport)
}
func (u *Use) Refresh(token platform.Secret, user int64, scopes string, transport http.RoundTripper) (platform.OAuthTokens, error) {
	if err := u.Commit(func() error { return nil }); err != nil {
		return platform.OAuthTokens{}, err
	}
	return gitlab.RefreshOAuth(u.ctx, gitlab.OAuthRefreshRequest{Origin: u.app.Origin, ClientID: u.app.ClientID, ClientSecret: u.secret, RedirectURI: u.app.RedirectURI, RefreshToken: token, UserID: user, Scopes: scopes}, transport)
}
func (u *Use) Revoke(token platform.Secret, transport http.RoundTripper) error {
	if err := u.Commit(func() error { return nil }); err != nil {
		return err
	}
	return gitlab.RevokeOAuth(u.ctx, u.app.Origin, u.app.ClientID, u.secret, token, transport)
}

// Personal keeps the existing private application/import lifecycle. Its caller
// holds the existing Space catalog lock when checking or committing metadata.
type Personal struct {
	Load func() (registry.Snapshot, error)
}

func (Personal) Managed() bool { return false }
func (p Personal) Applications() ([]registry.OAuthApplication, error) {
	f, e := p.Load()
	return f.OAuthApplications, e
}
func Equal(a, b registry.OAuthApplication) bool {
	return a.ID == b.ID && a.Source == b.Source && a.Version == b.Version && a.Origin == b.Origin && a.OwnerID == b.OwnerID && a.ClientID == b.ClientID && a.ClientSecret == b.ClientSecret && a.RedirectURI == b.RedirectURI && slices.Equal(a.Scopes, b.Scopes)
}
func (p Personal) Check(app registry.OAuthApplication) error {
	apps, err := p.Applications()
	if err != nil {
		return err
	}
	for _, a := range apps {
		if Equal(a, app) {
			return nil
		}
	}
	return registry.ErrConflict
}
func (p Personal) Begin(ctx context.Context, app registry.OAuthApplication, operation string) (*Use, error) {
	app.Scopes = slices.Clone(app.Scopes)
	// Pending personal imports are checked by the existing owner, including remote
	// ownership proof. No second application registry is introduced for them.
	if app.Source != "" || !platform.ValidCredential(app.ClientSecret) {
		return nil, platform.ErrCredential
	}
	return &Use{ctx: ctx, app: app, secret: platform.NewSecret(app.ClientSecret), guard: func(f func() error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return f()
	}, close: func() {}}, nil
}

// Disabled has no fallback to private applications or deployment credentials.
type Disabled struct{}

func (Disabled) Managed() bool { return true }
func (Disabled) Applications() ([]registry.OAuthApplication, error) {
	return []registry.OAuthApplication{}, nil
}
func (Disabled) Check(registry.OAuthApplication) error { return platform.ErrCredential }
func (Disabled) Begin(context.Context, registry.OAuthApplication, string) (*Use, error) {
	return nil, platform.ErrCredential
}

func (p Personal) BeginRevoke(ctx context.Context, app registry.OAuthApplication, operation string, user int64) (*Use, error) {
	return p.Begin(ctx, app, operation)
}
func (Disabled) BeginRevoke(context.Context, registry.OAuthApplication, string, int64) (*Use, error) {
	return nil, platform.ErrCredential
}
