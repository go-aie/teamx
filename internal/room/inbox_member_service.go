package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type agentInboxBatchPage struct {
	InboxBatchPage
	Matters []agentMatterRecord `json:"matters"`
}

// Polling reconciles the durable Inbox progress, including after restart or a
// lost wake. It is a MemberAgent-owned task, never a synchronous actor callback.
func (s *service) memberInboxStatus(ctx context.Context, id MemberIdentity) (InboxStatus, error) {
	s.inboxMu.Lock()
	defer s.inboxMu.Unlock()
	if s.inboxes[id.MembershipID].IsZero() {
		if _, err := os.Stat(filepath.Join(s.store.dir, "inboxes", id.MembershipID)); errors.Is(err, os.ErrNotExist) {
			return InboxStatus{MemberIdentity: id}, nil
		} else if err != nil {
			return InboxStatus{}, err
		}
	}
	addr, err := s.inboxLocked(ctx, id)
	if err != nil {
		return InboxStatus{}, err
	}
	return addr.Call[InboxStatus](ctx, InspectInbox{id})
}

func (s *service) memberInbox(ctx context.Context, req apiRequest, operation string) (any, error) {
	if req.Caller.Kind != "agent" || req.AgentID != "" || req.MembershipID != "" {
		return nil, ErrIdentity
	}
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
	s.inboxMu.Lock()
	defer s.inboxMu.Unlock()
	addr, err := s.inboxLocked(ctx, id)
	if err != nil {
		return nil, err
	}
	switch operation {
	case "read":
		// Read creates a durable batch and reliable pending matters before the
		// CLI receives any body. A failed output leaves the page unconsumed.
		s.store.mu.Lock()
		agent, err := s.store.runningTurnLocked(id, req.TurnNumber)
		s.store.mu.Unlock()
		if err != nil {
			return nil, err
		}
		page, err := addr.Call[InboxBatchPage](ctx, ReadInboxBatch{MemberIdentity: id, TurnNumber: agent.TurnNumber, Limit: req.Limit})
		if err != nil {
			return nil, err
		}
		page.Matters, err = s.store.associateInboxPage(page.InboxPage, page.Batch.TurnNumber)
		view := agentInboxBatchPage{InboxBatchPage: page, Matters: make([]agentMatterRecord, 0, len(page.Matters))}
		now := time.Now()
		for _, r := range page.Matters {
			view.Matters = append(view.Matters, agentMatterRecordView(r, now))
		}
		return view, err
	case "confirm":
		return addr.Call[InboxStatus](ctx, ConfirmInboxBatch{MemberIdentity: id, TurnNumber: req.TurnNumber, Receipt: req.ReadReceipt})
	case "list":
		after := uint64(0)
		if req.After != nil {
			after = *req.After
		}
		limit := req.Limit
		if limit == 0 {
			limit = 25
		}
		page, err := s.store.listMatters(id, req.ObjectKey, after, limit)
		return agentMatterPageView(page), err
	case "update":
		if req.Matter == nil {
			return nil, ErrRequest
		}
		record, err := s.store.updateMatter(id, *req.Matter)
		return agentMatterRecordView(record, time.Now()), err
	}
	return nil, ErrRequest
}
