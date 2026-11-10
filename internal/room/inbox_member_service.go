package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Polling reconciles the durable Inbox progress, including after restart or a
// lost wake. It is a MemberAgent-owned task, never a synchronous actor callback.
func (s *service) memberInboxStatus(ctx context.Context, id MemberIdentity) (InboxStatus, error) {
	var maintenanceErr error
	if hook := s.maintainInbox.Load(); hook != nil {
		maintenanceErr = hook.run(ctx, id)
	} else if s.workspaceOperationID == "" {
		maintenanceErr = s.maintainInboxLocal(ctx, id, nil, time.Now().UTC())
	}
	if maintenanceErr != nil {
		return InboxStatus{}, maintenanceErr
	}
	s.inboxMu.Lock()
	if s.inboxes[id.MembershipID].IsZero() {
		if _, err := os.Stat(filepath.Join(s.store.dir, "inboxes", id.MembershipID)); errors.Is(err, os.ErrNotExist) {
			s.inboxMu.Unlock()
			return InboxStatus{MemberIdentity: id}, nil
		} else if err != nil {
			s.inboxMu.Unlock()
			return InboxStatus{}, err
		}
	}
	addr, err := s.inboxLocked(ctx, id)
	if err != nil {
		s.inboxMu.Unlock()
		return InboxStatus{}, err
	}
	status, err := addr.Call[InboxStatus](ctx, InspectInbox{id})
	s.inboxMu.Unlock()
	if err != nil {
		return status, err
	}
	return s.reconcileInboxAdmission(ctx, id, status)
}

func (s *service) memberInbox(ctx context.Context, req apiRequest, operation string) (any, error) {
	if req.Caller.Kind != "agent" || req.AgentID != "" || req.MembershipID != "" {
		return nil, ErrIdentity
	}
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
	if err := s.store.initReferenceFollowups(id); err != nil {
		return nil, err
	}
	switch operation {
	case "read":
		s.store.mu.Lock()
		agent, err := s.store.runningTurnLocked(id, req.TurnNumber)
		s.store.mu.Unlock()
		if err != nil {
			return nil, err
		}
		page, err := s.readReferenceInbox(ctx, id, agent.TurnNumber, req.Limit, s.referenceResolver)
		if err != nil {
			return nil, err
		}
		return projectAgentInboxRead(id, page)
	case "confirm":
		s.inboxMu.Lock()
		defer s.inboxMu.Unlock()
		addr, err := s.inboxLocked(ctx, id)
		if err != nil {
			return nil, err
		}
		return addr.Call[InboxStatus](ctx, ConfirmInboxBatch{MemberIdentity: id, TurnNumber: req.TurnNumber, Receipt: req.ReadReceipt})
	}
	return nil, ErrRequest
}
