package notification

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Target is a single destination. The zero value selects Room delivery.
// MembershipID, never an agent name alone, fixes the receiving generation.
type Target struct {
	Mode         string `json:"mode,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
	MembershipID string `json:"membership_id,omitempty"`
}

func (t Target) Inbox() bool { return t.Mode == "inbox" }
func (t Target) Valid() bool {
	if t.Mode == "" || t.Mode == "room" {
		return t.AgentID == "" && t.MembershipID == ""
	}
	return t.Inbox() && strings.TrimSpace(t.AgentID) != "" && len(t.AgentID) <= 4096 && utf8.ValidString(t.AgentID) && !strings.ContainsRune(t.AgentID, 0) && validComponent(t.MembershipID)
}

// Normalize returns the canonical target without discarding invalid fields.
func (t Target) Normalize() (Target, error) {
	if !t.Valid() {
		return t, ErrRequest
	}
	if !t.Inbox() {
		return Target{}, nil
	}
	return t, nil
}

// Canonical is intended for derived metadata; invalid targets remain invalid.
func (t Target) Canonical() Target {
	normalized, err := t.Normalize()
	if err != nil {
		return t
	}
	return normalized
}

// ScopeKey identifies a destination. AgentID is checked separately, so changing
// an agent cannot allocate another subscription for the same membership.
func (t Target) ScopeKey() string {
	if t.Inbox() {
		return "inbox:" + t.MembershipID
	}
	return "room"
}

// Route is frozen when the source accepts an item, in the same durable snapshot.
// Version is the subscription configuration version at that boundary. The route remains immutable for pending items after a target switch.
type Route struct {
	Target  Target `json:"target"`
	Version uint64 `json:"version"`
}

func (r Route) Valid(version uint64) bool {
	return r.Target.Valid() && r.Version <= version && (!r.Target.Inbox() || r.Version > 0)
}
func (r Route) DeliveryID(sourceID string) string {
	if !r.Target.Inbox() {
		return sourceID
	}
	hash := sha256.Sum256([]byte(sourceID + "|" + r.Target.MembershipID))
	return fmt.Sprintf("notification-%x", hash)
}
