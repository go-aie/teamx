package connection

import (
	"fmt"
	"io"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Service-root connections are independent of Room snapshots and credentials.
// Supported providers are selected by the injected configuration normalizer.
type Connection struct {
	ID                   string               `json:"id"`
	Name                 string               `json:"name"`
	Provider             string               `json:"provider"`
	URL                  string               `json:"url"`
	Version              uint64               `json:"version"`
	CredentialVersion    uint64               `json:"credential_version"`
	CredentialKind       string               `json:"credential_kind"`
	CLIProfile           *platform.CLIProfile `json:"cli_profile,omitempty"`
	CredentialConfigured bool                 `json:"credential_configured"`
	Validation           platform.Validation  `json:"validation"`
	RevocationStatus     string               `json:"revocation_status,omitempty"`
	AuthorizationStatus  string               `json:"authorization_status,omitempty"`
	TokenExpiresAt       time.Time            `json:"token_expires_at,omitzero"`
}

// The disk record refers to an independently identified credential. Both are
// atomically committed in one private service snapshot, never in Room data.
type Record struct {
	Connection
	identity        platform.AccountIdentity // provider-derived, never serialized
	CredentialID    string                   `json:"credential_id,omitempty"`
	Generation      string                   `json:"generation"`
	IdentityBinding *IdentityBinding         `json:"identity_binding,omitempty"`
}

// IdentityBinding is private evidence for a verified account, never a public
// credential fingerprint. Metadata renames do not invalidate this evidence.
type IdentityBinding struct {
	Generation            string `json:"generation"`
	CredentialVersion     uint64 `json:"credential_version"`
	CredentialFingerprint string `json:"credential_fingerprint"`
}

func (Record) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[REDACTED platform connection]")
}

type Snapshot struct {
	Version            int                 `json:"version"`
	Connections        []Record            `json:"connections"`
	InstallationID     string              `json:"installation_id,omitempty"`
	OAuthApplications  []OAuthApplication  `json:"oauth_applications,omitempty"`
	OAuthCredentials   []OAuthCredential   `json:"oauth_credentials,omitempty"`
	OAuthCleanup       []OAuthCleanup      `json:"oauth_cleanup,omitempty"`
	OAuthClientImports []OAuthClientImport `json:"oauth_client_imports,omitempty"`
}

// Private OAuth records. Multiple connections may share one authorization.
type OAuthApplication struct {
	ID           string   `json:"id"`
	Origin       string   `json:"origin"`
	OwnerID      int64    `json:"owner_id"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	RedirectURI  string   `json:"redirect_uri"`
	Scopes       []string `json:"scopes"`
}

type OAuthCredential struct {
	ID               string    `json:"id"`
	ApplicationID    string    `json:"application_id"`
	UserID           int64     `json:"user_id"`
	GrantGeneration  uint64    `json:"grant_generation"`
	Scopes           []string  `json:"scopes"`
	State            string    `json:"state"`
	AccessToken      string    `json:"access_token,omitempty"`
	RefreshToken     string    `json:"refresh_token,omitempty"`
	ExpiresAt        time.Time `json:"expires_at,omitzero"`
	TokenRevision    uint64    `json:"token_revision"`
	RefreshAttemptID string    `json:"refresh_attempt_id,omitempty"`
}

func (OAuthApplication) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[REDACTED OAuth application]")
}
func (OAuthCredential) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[REDACTED OAuth credential]")
}

func (Snapshot) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[REDACTED platform connections]")
}
