package room

import (
	"context"
)

func (s *service) memberPolicy(ctx context.Context, req apiRequest, operation string) (any, error) {
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.AgentID, MembershipID: req.MembershipID}
	if req.Caller.Kind == "agent" {
		if req.AgentID != "" || req.MembershipID != "" {
			return nil, ErrIdentity
		}
		id.AgentID, id.MembershipID = req.Caller.ID, req.Caller.MembershipID
	} else if req.Caller.Kind != "human" {
		return nil, ErrIdentity
	}
	// Store mutation serialization also fences member removal and stale generations.
	switch operation {
	case "read":
		if req.Caller.Kind == "agent" {
			if req.Followup != nil && req.Followup.ID != "" {
				view, err := s.store.readPolicy(id)
				if err != nil {
					return nil, err
				}
				for _, record := range view.Current {
					if record.ID == req.Followup.ID {
						if req.Followup.Detail {
							history := []PolicyRevision{}
							for _, r := range view.History {
								if r.ID == record.ID {
									history = append(history, r)
								}
							}
							return struct {
								Revision uint64           `json:"revision"`
								Record   PolicyRevision   `json:"record"`
								History  []PolicyRevision `json:"history"`
								Complete bool             `json:"complete"`
							}{view.Revision, record, history, true}, nil
						}
						return struct {
							Revision uint64            `json:"revision"`
							Record   agentPolicyRecord `json:"record"`
						}{view.Revision, projectPolicyRecord(record)}, nil
					}
				}
				return nil, ErrRequest
			}
			return s.store.readAgentPolicy(id)
		}
		return s.store.readPolicy(id)
	case "update":
		if req.Caller.Kind == "agent" {
			if req.ConversationPolicy == nil {
				return nil, ErrRequest
			}
			if err := s.store.initReferenceFollowups(id); err != nil {
				return nil, err
			}
			return s.changeAgentPolicy(ctx, id, req.Caller, *req.ConversationPolicy)
		}
		if req.Policy == nil {
			return nil, ErrRequest
		}
		view, err := s.store.changePolicy(id, req.Caller, *req.Policy)
		return view, err
	case "publish":
		if req.Caller.Kind != "agent" || req.Followup == nil {
			return nil, ErrIdentity
		}
		return s.publishPolicySuggestion(ctx, id, req.TurnNumber, req.Followup.ID, req.Followup.Version)
	}
	return nil, ErrRequest
}
