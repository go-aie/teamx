package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

var ErrReference = errors.New("invalid platform reference")

// IdentityScope is canonicalized by the provider, never by URL similarity in
// common code. Instance need not be an HTTPS origin. Tenant may be absent on a
// single-tenant platform; its namespace is part of identity when present.
type IdentityScope struct {
	Provider string     `json:"provider"`
	Instance string     `json:"instance"`
	Tenant   ExternalID `json:"tenant,omitzero"`
}

type NativeIdentity struct {
	Type string     `json:"type"`
	ID   ExternalID `json:"id"`
}

// Containers are ordered outermost first, as defined by the provider. Object
// uses its canonical native ID; verified IID/GID aliases are separate evidence.
// A message may belong directly to an object without a discussion.
type TargetIdentity struct {
	IdentityScope
	Containers []NativeIdentity `json:"containers,omitempty"`
	Object     NativeIdentity   `json:"object"`
	Discussion ExternalID       `json:"discussion,omitzero"`
	Message    ExternalID       `json:"message,omitzero"`
}

type AccountIdentity struct {
	IdentityScope
	ID ExternalID `json:"id"`
}

func identityText(s string) bool {
	return strings.TrimSpace(s) != "" && referenceText(s)
}

func (id ExternalID) Valid() bool {
	return identityText(id.Namespace) && identityText(id.Value)
}

func optionalID(id ExternalID) bool { return id == (ExternalID{}) || id.Valid() }

func (s IdentityScope) Valid() bool {
	return identityText(s.Provider) && identityText(s.Instance) && optionalID(s.Tenant)
}

func (a AccountIdentity) Valid() bool { return a.IdentityScope.Valid() && a.ID.Valid() }

func (n NativeIdentity) Valid() bool { return identityText(n.Type) && n.ID.Valid() }

func (t TargetIdentity) Valid() bool {
	if !t.IdentityScope.Valid() || !t.Object.Valid() || len(t.Containers) > 16 || !optionalID(t.Discussion) || !optionalID(t.Message) {
		return false
	}
	for _, c := range t.Containers {
		if !c.Valid() {
			return false
		}
	}
	return true
}

// Key compares canonical native identities, not authorization or member refs.
// Nil/empty container lists have the same encoding. R2 owns member-scoped refs
// and persistence; an unverified candidate must never be promoted using Key.
func (t TargetIdentity) Key() (string, error) {
	if !t.Valid() {
		return "", ErrReference
	}
	b, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// AccessContext contains references only, and is selected by the service from
// current member bindings. Changing accounts invalidates access evidence but
// never changes a TargetIdentity. Credentials are injected by the tool owner.
type AccessContext struct {
	RoomIdentity
	AgentID           string          `json:"agent_id"`
	MembershipID      string          `json:"membership_id"`
	Receiver          AccountIdentity `json:"receiver,omitzero"`
	Account           AccountIdentity `json:"account"`
	ConnectionID      string          `json:"connection_id"`
	Generation        string          `json:"generation"`
	ConnectionVersion uint64          `json:"connection_version"`
	CredentialVersion uint64          `json:"credential_version"`
}

func (a AccessContext) Valid() bool {
	return identityText(a.RoomID) && identityText(a.RoomInstance) && identityText(a.AgentID) && identityText(a.MembershipID) &&
		a.Account.Valid() && (a.Receiver == (AccountIdentity{}) || a.Receiver.Valid()) &&
		identityText(a.ConnectionID) && identityText(a.Generation) && a.ConnectionVersion > 0 && a.CredentialVersion > 0
}
