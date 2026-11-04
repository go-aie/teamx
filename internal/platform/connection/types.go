package connection

import (
	"fmt"
	"io"

	"github.com/go-aie/teamx/internal/platform"
)

// Service-root connections are independent of Room snapshots and credentials.
// Supported providers are selected by the injected configuration normalizer.
type Connection struct {
	ID                   string              `json:"id"`
	Name                 string              `json:"name"`
	Provider             string              `json:"provider"`
	URL                  string              `json:"url"`
	Version              uint64              `json:"version"`
	CredentialVersion    uint64              `json:"credential_version"`
	CredentialKind       string              `json:"credential_kind"`
	TokenEnv             string              `json:"token_env,omitempty"`
	CredentialConfigured bool                `json:"credential_configured"`
	Validation           platform.Validation `json:"validation"`
}

// The disk record refers to an independently identified credential. Both are
// atomically committed in one private service snapshot, never in Room data.
type Record struct {
	Connection
	CredentialID    string           `json:"credential_id,omitempty"`
	Generation      string           `json:"generation"`
	IdentityBinding *IdentityBinding `json:"identity_binding,omitempty"`
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

// Only this private disk DTO serializes a plaintext PAT.
type Credential struct {
	ID           string `json:"id"`
	ConnectionID string `json:"connection_id"`
	PAT          string `json:"pat"`
}

func (Credential) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[REDACTED platform credential]")
}

type Snapshot struct {
	Version     int          `json:"version"`
	Connections []Record     `json:"connections"`
	Credentials []Credential `json:"credentials"`
}

func (Snapshot) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[REDACTED platform connections]")
}
