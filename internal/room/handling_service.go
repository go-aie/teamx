package room

import (
	"context"
	"time"
)

func (s *service) memberHandling(ctx context.Context, req apiRequest, path string) (any, error) {
	if req.Caller.Kind != "agent" || req.AgentID != "" || req.MembershipID != "" {
		return nil, ErrIdentity
	}
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
	switch path {
	case "/v1/matters/notify":
		if req.Notice == nil {
			return nil, ErrRequest
		}
		record, err := s.notifyMatter(ctx, id, *req.Notice)
		return agentMatterRecordView(record, time.Now()), err
	case "/v1/matters/reply":
		if req.MatterReply == nil {
			return nil, ErrRequest
		}
		record, err := s.store.linkMatterReply(id, *req.MatterReply)
		return agentMatterRecordView(record, time.Now()), err
	}
	return nil, ErrRequest
}
