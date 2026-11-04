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

type Validation struct {
	Status    string    `json:"status"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	Username  string    `json:"username,omitempty"`
	UserID    int64     `json:"user_id,omitempty"`
	ErrorCode string    `json:"error_code,omitempty"`
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

func ValidPAT(pat string) bool {
	if len(pat) == 0 || len(pat) > 8192 {
		return false
	}
	for _, c := range pat {
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
	ConnectionID         string `json:"connection_id,omitempty"`
	ConnectionGeneration string `json:"connection_generation,omitempty"`
	ProjectID            int64  `json:"project_id,omitempty"`
	Provider             string `json:"provider"`
	URL                  string `json:"url"`
	Project              string `json:"project"`
	TokenEnv             string `json:"token_env,omitempty"`
	CredentialID         string `json:"credential_id,omitempty"`
}

// SourceRef identifies a notification independently of its provider's object IDs.
type SourceRef struct {
	Provider       string `json:"provider"`
	Channel        string `json:"channel"`
	Instance       string `json:"instance"`
	SubscriptionID string `json:"subscription_id"`
	ItemKey        string `json:"item_key"`
	ProjectID      int64  `json:"project_id,omitempty"`
	Project        string `json:"project,omitempty"`
	Object         string `json:"object,omitempty"`
}
