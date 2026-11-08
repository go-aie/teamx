package room

import (
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// A presentation of the authoritative action ledger, never a guess from body
// text. Rebuilt on each read so receipt settlement can enrich an earlier entry
// without rewriting source evidence or its delivery identity.
type inboxMessageOrigin struct {
	Kind          string `json:"kind"` // account, agent
	AgentID       string `json:"agent_id,omitempty"`
	ActionID      string `json:"action_id,omitempty"`
	Status        string `json:"status,omitempty"`
	UncertainSend bool   `json:"uncertain_send,omitempty"`
}

// Store lock held. Check all current members: several Agents can send through
// the same account. Their exact receipts are shared evidence in this Room.
// No receipt means only "account", never proof that a human wrote the message.
func (s *Store) inboxMessageOriginsLocked(entries []InboxEntry) (map[uint64]inboxMessageOrigin, error) {
	origins := map[uint64]inboxMessageOrigin{}
	views := map[uint64]notification.View{}
	for _, e := range entries {
		v := projectInboxEntry(e, followupSourceSummary{}).Item
		if v.ContextOnly {
			origins[e.Seq] = inboxMessageOrigin{Kind: "account"}
			views[e.Seq] = v
		}
	}
	if len(origins) == 0 {
		return origins, nil
	}
	room, err := s.loadRoom()
	if err != nil {
		return nil, err
	}
	for _, member := range room.Members {
		id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		actions, err := s.loadActionsLocked(id)
		if err != nil {
			return nil, err
		}
		for _, a := range actions {
			match := s.notificationCapabilities(a.Access.Account.Provider).MatchAction
			if match == nil || a.Status == "not_applied" {
				continue
			}
			for seq, v := range views {
				origin := origins[seq]
				if origin.Kind == "agent" {
					continue
				}
				if v.Receiver != a.Access.Account || !match(v, platform.TargetActionRequest{Target: a.Target, Operation: a.Request.Operation}, a.Access) {
					continue
				}
				if a.Result != nil && a.Result.ResultID.Valid() && a.Result.ResultID == v.MessageID {
					origin = inboxMessageOrigin{Kind: "agent", AgentID: id.AgentID, ActionID: a.Request.ID, Status: a.Status}
				} else if a.Status == "unknown" && (a.Result == nil || !a.Result.ResultID.Valid()) {
					origin.UncertainSend = true
				}
				origins[seq] = origin
			}
		}
	}
	return origins, nil
}
