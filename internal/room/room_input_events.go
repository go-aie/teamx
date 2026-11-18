package room

import (
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/inputstream"
)

// Audience is immutable delivery evidence, never an Agent-visible authority.
// Details remain in the operation service, whose access checks are independent.
type RoomEvent struct {
	ID            string   `json:"event_id"`
	Kind          string   `json:"event_type"`
	SourceSeq     uint64   `json:"source_seq"`
	OperationID   string   `json:"operation_id"`
	ResultVersion uint64   `json:"result_version"`
	ResultRef     string   `json:"result_ref"`
	Targets       []string `json:"target_memberships"`
	Actor         Author   `json:"actor"`
}
type roomEventReceipt struct {
	Seq    uint64 `json:"seq"`
	Digest string `json:"digest"`
}
type AppendRoomEvent struct {
	RoomIdentity
	Event     RoomEvent
	Summary   string
	CreatedAt time.Time
}

func roomEventKey(e RoomEvent) string      { return inputstream.Ref("event_", e.ID, e.ResultVersion) }
func roomEventDigest(m RoomMessage) string { b, _ := json.Marshal(m); return inputstream.Digest(b) }
func validateRoomEvent(m RoomMessage) error {
	if m.Event == nil {
		if m.Type != "" && m.Type != "message" {
			return ErrInvalid
		}
		return nil
	}
	e := m.Event
	if m.Type != "event" || !validText(e.ID) || !validText(e.Kind) || e.SourceSeq >= m.Seq || !validText(e.OperationID) || e.ResultVersion == 0 || !validText(e.ResultRef) || e.Actor != m.Author || len(e.Targets) > maxMembers || e.Actor.Kind != "human" && e.Actor.Kind != "system" {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, id := range e.Targets {
		if !safeMembershipID(id) || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	return nil
}
func roomDeliverable(m RoomMessage, id MemberIdentity) bool {
	if m.Event != nil {
		return slices.Contains(m.Event.Targets, id.MembershipID)
	}
	return !(m.Author.Kind == "agent" && m.Author.ID == id.AgentID && (m.AuthorMembershipID == "" || m.AuthorMembershipID == id.MembershipID))
}
func roomVisible(m RoomMessage, caller Caller, history bool) bool {
	if m.Event == nil {
		return true
	}
	if caller.Kind == "human" {
		return true
	} // Summary only; details are separately authorized.
	if history && len(m.Event.Targets) == 0 {
		return true
	}
	return slices.Contains(m.Event.Targets, caller.MembershipID)
}
func (r *Room) AppendEvent(ctx actorx.Context, msg AppendRoomEvent, reply actorx.Reply[PostReceipt]) error {
	if err := r.writable(msg.RoomIdentity); err != nil {
		return err
	}
	if r.state.InputLog == nil {
		return ErrVersion
	}
	key := roomEventKey(msg.Event)
	event := msg.Event
	event.Targets = slices.Clone(msg.Event.Targets)
	m := RoomMessage{Type: "event", Event: &event, Seq: r.state.LatestSeq + 1, ClientMessageID: key, Author: msg.Event.Actor, Text: msg.Summary, CreatedAt: msg.CreatedAt}
	if msg.CreatedAt.IsZero() || !validText(msg.Summary) || len(msg.Summary) > maxTextBytes {
		return ErrRequest
	}
	if err := validateRoomEvent(m); err != nil {
		return err
	}
	if receipt, ok := r.state.EventReceipts[key]; ok {
		m.Seq = receipt.Seq
		if roomEventDigest(m) != receipt.Digest {
			return ErrConflict
		}
		reply.Complete(PostReceipt{RoomIdentity: r.state.RoomIdentity, Seq: receipt.Seq, ClientMessageID: key}, nil)
		return nil
	}
	for _, target := range msg.Event.Targets {
		found := false
		for _, member := range r.state.Members {
			if member.MembershipID == target {
				found = true
				break
			}
		}
		if !found {
			return ErrIdentity
		}
	}
	if len(r.state.Messages) >= maxMessages || len(r.state.EventReceipts) >= maxRoomReceipts {
		return ErrCapacity
	}
	next := r.candidate()
	next.EventReceipts = maps.Clone(next.EventReceipts)
	if next.EventReceipts == nil {
		next.EventReceipts = map[string]roomEventReceipt{}
	}
	next.LatestSeq = m.Seq
	next.Messages = append(next.Messages, m)
	next.EventReceipts[key] = roomEventReceipt{m.Seq, roomEventDigest(m)}
	return commitRoom(r, ctx, next, reply, PostReceipt{RoomIdentity: next.RoomIdentity, Seq: m.Seq, ClientMessageID: key}, nil)
}
