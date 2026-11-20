// Package identity defines persistent Room and member identities shared by
// domains and application services. Runtime epochs and Session generations
// remain with their respective owners.
package identity

import "errors"

var ErrMismatch = errors.New("snapshot identity mismatch")

type RoomIdentity struct {
	RoomID       string `json:"room_id"`
	RoomInstance string `json:"room_instance"`
}

type MemberIdentity struct {
	RoomIdentity
	AgentID      string `json:"agent_id"`
	MembershipID string `json:"membership_id"`
}
