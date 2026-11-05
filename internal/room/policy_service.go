package room

import (
	"context"
	"time"
)

func (s *service) memberPolicy(_ context.Context, req apiRequest, operation string) (any, error) {
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
			return s.store.readAgentPolicy(id)
		}
		return s.store.readPolicy(id)
	case "update":
		if req.Policy == nil {
			return nil, ErrRequest
		}
		view, err := s.store.changePolicy(id, req.Caller, *req.Policy)
		if req.Caller.Kind == "agent" {
			return agentPolicy(view.PolicySnapshot, req.Policy.TurnNumber, time.Now()), err
		}
		return view, err
	case "decisions/read":
		if req.Caller.Kind != "human" {
			return nil, ErrIdentity
		}
		after := uint64(0)
		if req.After != nil {
			after = *req.After
		}
		limit := req.Limit
		if limit == 0 {
			limit = 25
		}
		return s.store.listMatters(id, req.ObjectKey, after, limit)
	case "decisions/update":
		if req.Policy == nil {
			return nil, ErrRequest
		}
		record, err := s.store.changeDecision(id, req.Caller, *req.Policy)
		if req.Caller.Kind == "agent" {
			return agentMatterRecordView(record, time.Now()), err
		}
		return record, err
	}
	return nil, ErrRequest
}
