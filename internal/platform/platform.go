package platform

import "time"

// Platform is one concrete account access at service scope. ID is platform_id.
// No second platform list or identity is maintained.
// Service-root platforms are independent of Room snapshots and credentials.
// Supported providers are selected by the injected configuration normalizer.
type Platform struct {
	ID                   string      `json:"id"`
	Name                 string      `json:"name"`
	Provider             string      `json:"provider"`
	ToolEntryID          string      `json:"tool_entry_id,omitempty"`
	URL                  string      `json:"url"`
	Version              uint64      `json:"version"`
	CredentialVersion    uint64      `json:"credential_version"`
	CredentialKind       string      `json:"credential_kind"`
	CLIProfile           *CLIProfile `json:"cli_profile,omitempty"`
	CredentialConfigured bool        `json:"credential_configured"`
	Validation           Validation  `json:"validation"`
	RevocationStatus     string      `json:"revocation_status,omitempty"`
	AuthorizationStatus  string      `json:"authorization_status,omitempty"`
	TokenExpiresAt       time.Time   `json:"token_expires_at,omitzero"`
}
