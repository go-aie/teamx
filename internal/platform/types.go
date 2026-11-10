package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// ErrCredential is shared by Workspace resolution and notification admission.
var ErrCredential = errors.New("platform credential unavailable")

// Secret is service-local and redacted by generic formatting and JSON encoding.
// Reveal is reserved for explicit credential injection and private disk DTOs.
type Secret struct{ value string }

func NewSecret(value string) Secret         { return Secret{value: value} }
func (s Secret) Reveal() string             { return s.value }
func (Secret) MarshalJSON() ([]byte, error) { return json.Marshal("[REDACTED]") }
func (Secret) Format(s fmt.State, _ rune)   { _, _ = io.WriteString(s, "[REDACTED]") }

// AccessCredential carries short-lived service-owned access material.
// Providers validate the explicit kind; token prefixes never select a protocol.
type AccessCredential struct {
	Kind  string
	Token Secret
}

func (AccessCredential) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[REDACTED platform access]")
}

// AuthorizationEvidence survives normal token rotation. Scopes is the sorted,
// space-separated granted scope set. Token material and its revision are absent.
type AuthorizationEvidence struct {
	Origin, ApplicationID, ClientID, CredentialID string
	UserID                                        int64
	Generation                                    uint64
	Scopes                                        string
}

// OAuthTokens is an in-memory protocol result, never a private disk DTO.
type OAuthTokens struct {
	AccessToken, RefreshToken Secret
	ExpiresAt                 time.Time
	Scopes                    []string
}

func (OAuthTokens) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "[REDACTED OAuth tokens]") }

type Validation struct {
	Status    string          `json:"status"`
	CheckedAt time.Time       `json:"checked_at,omitzero"`
	Username  string          `json:"username,omitempty"`
	UserID    int64           `json:"user_id,omitempty"`
	ErrorCode string          `json:"error_code,omitempty"`
	Account   AccountIdentity `json:"account,omitzero"`
}

// CLIProfile is a versioned reference to credentials owned by a CLI. It never
// contains OAuth tokens, app secrets or key material.
type CLIProfile struct {
	Version    int    `json:"version"`
	Executable string `json:"executable"`
	Profile    string `json:"profile"`
	Brand      string `json:"brand"`
	CLIVersion string `json:"cli_version"`
}

func ValidUsername(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

func ValidCredential(value string) bool {
	if len(value) == 0 || len(value) > 8192 {
		return false
	}
	for _, c := range value {
		if c < '!' || c > '~' {
			return false
		}
	}
	return true
}

// RoomIdentity identifies durable ownership; service epochs remain ephemeral.
type RoomIdentity struct {
	RoomID       string `json:"room_id"`
	RoomInstance string `json:"room_instance"`
}
type ProjectBinding struct {
	PlatformID         string `json:"platform_id,omitempty"`
	PlatformGeneration string `json:"platform_generation,omitempty"`
	ProjectID          int64  `json:"project_id,omitempty"`
	Provider           string `json:"provider"`
	URL                string `json:"url"`
	Project            string `json:"project"`
}

// SourceRef identifies a notification independently of its provider's object IDs.
type SourceRef struct {
	Provider       string                `json:"provider"`
	Channel        string                `json:"channel"`
	Instance       string                `json:"instance"`
	Tenant         ExternalID            `json:"tenant,omitzero"`
	SubscriptionID string                `json:"subscription_id"`
	ItemKey        string                `json:"item_key"`
	ProjectID      int64                 `json:"project_id,omitempty"`
	Project        string                `json:"project,omitempty"`
	Object         string                `json:"object,omitempty"`
	URL            string                `json:"url,omitempty"` // Original candidate link, never object identity.
	Reference      NotificationReference `json:"reference,omitzero"`
}

// ExternalID keeps opaque IDs and their namespaces intact. An object ID, an
// IID and a GraphQL GID are distinct even when their displayed numbers match.
type ExternalID struct {
	Namespace string `json:"namespace"`
	Value     string `json:"value"`
}

type ObjectReference struct {
	Type string     `json:"type,omitempty"` // Provider-native type; never inferred from display text.
	ID   ExternalID `json:"id,omitzero"`
	IID  ExternalID `json:"iid,omitzero"`
}

// NotificationReference is source evidence, not a verified alias/grouping index.
// Target preserves the original API target; Object is the business object (the
// noteable for a comment). Unknown IDs stay absent. Version zero represents
// a source without native evidence. Value fields keep SourceRef comparable.
type NotificationReference struct {
	Version       int             `json:"version"`
	ChatID        ExternalID      `json:"chat_id,omitzero"`
	ParentID      ExternalID      `json:"parent_id,omitzero"`
	RootID        ExternalID      `json:"root_id,omitzero"`
	AccountTenant ExternalID      `json:"account_tenant,omitzero"`
	RecipientID   ExternalID      `json:"recipient_id,omitzero"`
	Scope         string          `json:"scope,omitempty"`
	ProjectID     ExternalID      `json:"project_id,omitzero"`
	Target        ObjectReference `json:"target,omitzero"`
	Object        ObjectReference `json:"object,omitzero"`
	NoteID        ExternalID      `json:"note_id,omitzero"`
	DiscussionID  ExternalID      `json:"discussion_id,omitzero"`
	Branch        string          `json:"branch,omitempty"`
	Ref           string          `json:"ref,omitempty"`
	RefType       string          `json:"ref_type,omitempty"`
	SHA           string          `json:"sha,omitempty"`
	BeforeSHA     string          `json:"before_sha,omitempty"`
}
