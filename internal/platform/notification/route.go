package notification

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Target identifies a private member Inbox or a shared body owner. Private
// routing freezes MembershipID; shared routing freezes InboxID and the body
// publication freezes the actual receiving Grants.
type Target struct {
	InboxID      string `json:"inbox_id,omitempty"`
	Mode         string `json:"mode,omitempty"`
	AgentID      string `json:"agent_id,omitempty"`
	MembershipID string `json:"membership_id,omitempty"`
}

// RetiredRoom reports the destination used by older on-disk GitLab snapshots.
func (t Target) RetiredRoom() bool {
	return t.InboxID == "" && (t.Mode == "" || t.Mode == "room") && t.AgentID == "" && t.MembershipID == ""
}

func (t Target) Valid() bool {
	if t.Mode == "shared_inbox" {
		return validComponent(t.InboxID) && t.AgentID == "" && t.MembershipID == ""
	}
	return t.InboxID == "" && t.Mode == "inbox" && strings.TrimSpace(t.AgentID) != "" && len(t.AgentID) <= 4096 && utf8.ValidString(t.AgentID) && !strings.ContainsRune(t.AgentID, 0) && validComponent(t.MembershipID)
}

// Normalize returns the canonical target without discarding invalid fields.
func (t Target) Normalize() (Target, error) {
	if !t.Valid() {
		return t, ErrRequest
	}
	return t, nil
}

// ScopeKey identifies a destination. AgentID is checked separately, so changing
// an agent cannot allocate another subscription for the same membership.
func (t Target) ScopeKey() string {
	if t.Mode == "shared_inbox" {
		return "shared_inbox:" + t.InboxID
	}
	return "inbox:" + t.MembershipID
}

// Route is frozen when the source accepts an item, in the same durable snapshot.
// Version is the subscription configuration version at that boundary. The route remains immutable for pending items after a target switch.
type Route struct {
	Target  Target `json:"target"`
	Version uint64 `json:"version"`
}

func (r Route) Valid(version uint64) bool {
	return r.Target.Valid() && r.Version <= version && r.Version > 0
}
func (r Route) DeliveryID(sourceID string) string {
	target := r.Target.MembershipID
	if r.Target.Mode == "shared_inbox" {
		target = "shared_inbox:" + r.Target.InboxID
	}
	hash := sha256.Sum256([]byte(sourceID + "|" + target))
	return fmt.Sprintf("notification-%x", hash)
}

func (t Target) InboxKey() string {
	if t.Mode == "shared_inbox" {
		return t.InboxID
	}
	return t.MembershipID
}
