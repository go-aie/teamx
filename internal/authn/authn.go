// Package authn defines the external authentication boundary. The host owns
// login attempts, admission, local users, sessions and revocation.
package authn

import (
	"context"
	"net"
	"net/url"
	"time"
)

// Identity contains no local user, Space, role or business credential.
type Identity struct {
	Provider    string    `json:"provider"`
	Authority   string    `json:"authority"`
	Subject     string    `json:"subject"`
	DisplayName string    `json:"display_name,omitempty"`
	ValidUntil  time.Time `json:"-"`
}

type Provider interface {
	LoginURL(callback string) (string, error)
	Complete(context.Context, url.Values) (Identity, error)
}

// Grant is private authentication material owned by the host's durable browser
// session store. It must never enter user directories, business sessions, URLs,
// responses or diagnostics. Refresh returns a replacement for durable commit;
// it never starts an independently rotating TokenSource.
type Grant struct {
	Provider       string    `json:"provider"`
	Authority      string    `json:"authority"`
	Subject        string    `json:"subject"`
	ClientID       string    `json:"client_id"`
	AccessToken    string    `json:"access_token"`
	RefreshToken   string    `json:"refresh_token"`
	AccessExpires  time.Time `json:"access_expires_at"`
	RefreshExpires time.Time `json:"refresh_expires_at,omitempty"`
	Nonce          string    `json:"nonce"`
	Scope          string    `json:"scope"`
	AuthTime       int64     `json:"auth_time,omitempty"`
}

func (Grant) String() string   { return "[private authentication grant]" }
func (Grant) GoString() string { return "[private authentication grant]" }

// IdentityVerificationWindow caps a successful remote identity check. Access
// token expiry and the local session's absolute deadline still take precedence.
const IdentityVerificationWindow = time.Hour

// Login separates the verified identity from the credentials used to maintain
// it. VerifiedUntil is a remote-check deadline, never a local session lifetime.
type Login struct {
	Identity      Identity
	Grant         Grant
	VerifiedAt    time.Time
	VerifiedUntil time.Time
}

// RenewableProvider is optional for identity-only callers and required by the
// multi-user renewable session owner. Refresh and Verify are separate so a
// rotated refresh token can be committed before a fallible userinfo request.
type RenewableProvider interface {
	Provider
	CompleteGrant(context.Context, url.Values) (Login, error)
	Refresh(context.Context, Grant) (Grant, error)
	Verify(context.Context, Grant) (Identity, error)
}

type FailureKind string

const (
	InvalidGrant FailureKind = "invalid_grant"
	Temporary    FailureKind = "temporarily_unavailable"
	ClientConfig FailureKind = "client_configuration"
)

// Failure contains only a stable category and a bounded retry delay. Provider
// response bodies and underlying network errors deliberately stay private.
type Failure struct {
	Kind       FailureKind
	RetryAfter time.Duration
}

func (e *Failure) Error() string { return "authentication: " + string(e.Kind) }

// LoopbackHTTP permits local browser SSO without weakening remote deployments.
// Literal IPs avoid trusting DNS names which might resolve outside this host.
func LoopbackHTTP(u *url.URL) bool {
	return u != nil && u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback()
}
