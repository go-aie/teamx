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

// LoopbackHTTP permits local browser SSO without weakening remote deployments.
// Literal IPs avoid trusting DNS names which might resolve outside this host.
func LoopbackHTTP(u *url.URL) bool {
	return u != nil && u.Scheme == "http" && net.ParseIP(u.Hostname()).IsLoopback()
}
