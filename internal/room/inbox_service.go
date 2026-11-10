package room

import (
	"context"
	"fmt"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// inboxMu serializes creation and calls with membership removal. No Inbox
// handler calls back into service or manager; manager Gate remains outermost.
func (s *service) inboxLocked(ctx context.Context, id MemberIdentity) (actorx.Addr, error) {
	if s.closing.Load() {
		return actorx.Addr{}, ErrClosing
	}
	if id.RoomIdentity != s.info.RoomIdentity {
		return actorx.Addr{}, ErrIdentity
	}
	list, err := s.room.Call[MemberList](ctx, ListMembers{s.info.RoomIdentity})
	if err != nil {
		return actorx.Addr{}, err
	}
	found := false
	for _, member := range list.Members {
		if member.AgentID == id.AgentID && member.MembershipID == id.MembershipID {
			found = true
			break
		}
	}
	if !found {
		return actorx.Addr{}, ErrIdentity
	}
	if target := s.inboxes[id.MembershipID]; !target.IsZero() {
		return target, nil
	}
	target, err := SpawnAgentInbox(s.rt, s.store, id, InboxLimits{})
	if err == nil {
		s.inboxes[id.MembershipID] = target
	}
	return target, err
}

func (s *service) receiveInbox(ctx context.Context, target notification.Target, item notification.Item) error {
	s.inboxMu.Lock()
	defer s.inboxMu.Unlock()
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: target.AgentID, MembershipID: target.MembershipID}
	addr, err := s.inboxLocked(ctx, id)
	if err != nil {
		return err
	}
	_, err = addr.Call[InboxReceipt](ctx, ReceiveInbox{MemberIdentity: id, Item: item})
	return err
}

func (s *service) previewInbox(ctx context.Context, req apiRequest) (any, error) {
	if req.Caller.Kind != "human" || req.Caller.MembershipID != "" {
		return nil, ErrIdentity
	}
	s.inboxMu.Lock()
	defer s.inboxMu.Unlock()
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.AgentID, MembershipID: req.MembershipID}
	addr, err := s.inboxLocked(ctx, id)
	if err != nil {
		return nil, err
	}
	if req.After == nil {
		req.After = new(uint64)
	}
	page, err := addr.Call[InboxPage](ctx, PreviewInbox{MemberIdentity: id, After: *req.After, SnapshotSeq: req.SnapshotSeq, Limit: req.Limit})
	if err != nil {
		return nil, err
	}
	status, err := addr.Call[InboxStatus](ctx, InspectInbox{id})
	if err != nil {
		return nil, err
	}
	return s.store.projectInboxPreview(page, status)
}

// Human polling inspects durable counters only. It must not run maintenance,
// reconcile admission, create a batch or advance the Agent's input progress.
func (s *service) inspectInbox(ctx context.Context, req apiRequest) (any, error) {
	if req.Caller.Kind != "human" || req.Caller.MembershipID != "" {
		return nil, ErrIdentity
	}
	s.inboxMu.Lock()
	defer s.inboxMu.Unlock()
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.AgentID, MembershipID: req.MembershipID}
	addr, err := s.inboxLocked(ctx, id)
	if err != nil {
		return nil, err
	}
	status, err := addr.Call[InboxStatus](ctx, InspectInbox{id})
	status.Batch = nil
	return status, err
}

// Call only with inboxMu held; Stop/Wait joins all persistence/preview tasks.
func (s *service) stopInboxLocked(ctx context.Context, membership string) error {
	addr := s.inboxes[membership]
	if addr.IsZero() {
		return nil
	}
	_ = addr.Stop(nil)
	exit, err := addr.Wait(ctx)
	if err != nil {
		return fmt.Errorf("inbox cleanup incomplete (store still owned): %w", err)
	}
	// Wait success proves all owned tasks settled even when the actor failed.
	// Retain its error, but release this runtime address so Store can reopen.
	delete(s.inboxes, membership)
	if exit.Reason != nil {
		return fmt.Errorf("inbox cleanup failed: %w", exit.Reason)
	}
	return nil
}

// Query admission also checks targets when no backlog exists. Collaboration
// state and model budgets do not participate in notification admission.
func (m *roomManager) checkNotificationTargetLocked(ctx context.Context, owner RoomIdentity, target notification.Target) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !target.Valid() {
		return ErrRequest
	}
	if !target.Inbox() {
		return nil
	}
	id := MemberIdentity{RoomIdentity: owner, AgentID: target.AgentID, MembershipID: target.MembershipID}
	if m.platformMemberChanges[id] {
		return ErrBusy
	}
	roster, err := m.toolRosterLocked(ctx, owner)
	if err != nil {
		return err
	}
	if !memberExists(roster, target.AgentID, target.MembershipID) {
		return ErrIdentity
	}
	return nil
}
func (m *roomManager) validateNotificationTargetLocked(ctx context.Context, owner RoomIdentity, target notification.Target, enabled bool) error {
	if !target.Valid() {
		return ErrRequest
	}
	// Configure callers validate new scopes even when disabled. An existing
	// orphan may always be paused without requiring a live member.
	if !enabled {
		return nil
	}
	return m.checkNotificationTargetLocked(ctx, owner, target)
}

// Prefer current target diagnostics even before the next delivery attempt. A
// disabled subscription retains its pending routes and can still be inspected.
func (m *roomManager) notificationTargetErrorLocked(owner RoomIdentity, items []notification.Item) string {
	if len(items) == 0 {
		return ""
	}
	svc, _ := m.runtimeServiceLocked(owner)
	if svc == nil || m.roomBusyLocked(owner) || svc.closing.Load() || svc.restarting.Load() {
		return "target_unavailable"
	}
	list, err := svc.room.Call[MemberList](context.Background(), ListMembers{owner})
	if err != nil {
		return "target_unavailable"
	}
	for _, item := range items {
		if !item.Route.Target.Inbox() {
			continue
		}
		found := false
		for _, member := range list.Members {
			if member.AgentID == item.Route.Target.AgentID && member.MembershipID == item.Route.Target.MembershipID {
				found = true
				break
			}
		}
		if !found {
			return "target_removed"
		}
	}
	return ""
}
