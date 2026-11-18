package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

// Notification policy and handoff receipts are independent of the connection
// result. A failed Inbox write must never turn a saved connection into failure.
type platformOperationNotification struct {
	RoomEvent   *RoomEvent `json:"room_event,omitempty"`
	RoomSummary string     `json:"room_summary,omitempty"`
	Schema      int        `json:"schema"`
	MemberIdentity
	RoomConsumed   bool            `json:"room_consumed,omitempty"`
	RoomReceipt    *PostReceipt    `json:"room_receipt,omitempty"`
	OperationID    string          `json:"operation_id"`
	Mode           string          `json:"mode"`
	Event          *InboxTaskEvent `json:"event,omitempty"`
	Receipt        *InboxReceipt   `json:"receipt,omitempty"`
	HandledVersion uint64          `json:"handled_version,omitempty"`
	ErrorCode      string          `json:"error_code,omitempty"`
}

type platformOperationContinuation struct {
	Mode           string `json:"mode"`
	State          string `json:"state"`
	HandledVersion uint64 `json:"handled_version"`
	RoomSeq        uint64 `json:"room_seq,omitempty"`
	InboxSeq       uint64 `json:"inbox_seq,omitempty"`
	BlockedReason  string `json:"blocked_reason,omitempty"`
}

func (m *roomManager) connectNotificationPath(id string) string {
	return filepath.Join(m.store.dir, "platform-connect-notifications", id+".json")
}

func connectResultEvent(op platformConnectOperation) InboxTaskEvent {
	kind := "operation_failed"
	if op.Status == "completed" {
		kind = "operation_completed"
	}
	return InboxTaskEvent{EventID: fmt.Sprintf("platform-connect:%s:%d", op.ID, op.ResultVersion),
		OperationID: op.ID, ResultVersion: op.ResultVersion, SourceSeq: op.Source.Seq,
		EventType: kind, ResultRef: "platforms/connect/" + op.ID, CreatedAt: op.UpdatedAt}
}

func (m *roomManager) readConnectNotificationLocked(op platformConnectOperation) (platformOperationNotification, error) {
	n := platformOperationNotification{Schema: 1, MemberIdentity: op.MemberIdentity, OperationID: op.ID, Mode: "none"}
	err := readSnapshot(m.connectNotificationPath(op.ID), &n, maxPrivateFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return n, nil
	}
	if err != nil {
		return n, err
	}
	if !validRoomOperationIntent(n) || n.Schema != 1 || n.MemberIdentity != op.MemberIdentity || n.OperationID != op.ID ||
		(n.Mode != "inbox" && n.Mode != "none") || n.HandledVersion > op.ResultVersion ||
		n.Event != nil && (!connectTerminal(op.Status) || !reflect.DeepEqual(*n.Event, connectResultEvent(op))) ||
		n.Receipt != nil && (n.Event == nil || n.Receipt.MemberIdentity != op.MemberIdentity || n.Receipt.Seq == 0 || n.Receipt.DeliveryID != (InboxEntry{MemberIdentity: op.MemberIdentity, Type: InboxTaskEventType, TaskEvent: n.Event}).metadata().deliveryID) {
		return n, ErrInvalid
	}
	return n, nil
}

func (m *roomManager) saveConnectNotificationLocked(n platformOperationNotification) error {
	if err := m.store.check(true); err != nil {
		return err
	}
	err := m.store.save(m.connectNotificationPath(n.OperationID), n, maxPrivateFileBytes)
	m.invalidateRoomLocked("interactions", n.MemberIdentity)
	return err
}

func (m *roomManager) setConnectNotificationLocked(op platformConnectOperation, mode string) error {
	if mode != "inbox" && mode != "none" {
		return ErrRequest
	}
	n, err := m.readConnectNotificationLocked(op)
	if err != nil {
		return err
	}
	n.Mode = mode
	// Saving subscription and an already completed result is one atomic handoff.
	if mode == "inbox" && connectTerminal(op.Status) && n.Event == nil && n.HandledVersion != op.ResultVersion {
		event := connectResultEvent(op)
		n.Event = &event
	}
	return m.saveConnectNotificationLocked(n)
}

// Acknowledgment records that this result has been reconciled with latest intent
// and step receipts. It neither consumes Inbox input nor completes the request.
func (m *roomManager) acknowledgeConnectLocked(op platformConnectOperation, version uint64) error {
	if !connectTerminal(op.Status) || version == 0 || version != op.ResultVersion {
		return ErrConflict
	}
	n, err := m.readConnectNotificationLocked(op)
	if err != nil || n.HandledVersion == version {
		return err
	}
	n.HandledVersion = version
	return m.saveConnectNotificationLocked(n)
}

// Observation never delivers, runs admission queries, or changes any cursor.
func (m *roomManager) connectContinuationLocked(op platformConnectOperation) (platformOperationContinuation, error) {
	n, err := m.readConnectNotificationLocked(op)
	if err != nil {
		return platformOperationContinuation{}, err
	}
	return m.operationContinuationLocked(op.MemberIdentity, connectTerminal(op.Status), n)
}

func (m *roomManager) operationContinuationLocked(id MemberIdentity, terminal bool, n platformOperationNotification) (platformOperationContinuation, error) {
	v := platformOperationContinuation{Mode: n.Mode, State: "none", HandledVersion: n.HandledVersion}
	if n.RoomReceipt != nil {
		v.RoomSeq = n.RoomReceipt.Seq
	}
	if n.HandledVersion > 0 {
		v.State = "handled"
		return v, nil
	}
	if n.RoomEvent != nil && len(n.RoomEvent.Targets) == 0 {
		v.State = "page_only"
		if n.RoomConsumed {
			v.State = "consumed"
		}
		return v, nil
	}
	if n.Mode == "none" {
		return v, nil
	}
	v.State = "waiting_result"
	if !terminal {
		return v, nil
	}
	v.State, v.BlockedReason = "pending_delivery", n.ErrorCode
	if n.RoomReceipt != nil {
		v.State = "delivered"
	}
	if n.Receipt != nil {
		v.State, v.InboxSeq = "delivered", n.Receipt.Seq
	}
	if n.ErrorCode != "" {
		return v, nil
	}
	svc, rt := m.runtimeServiceLocked(id.RoomIdentity)
	if svc == nil || rt.busy || svc.closing.Load() || svc.restarting.Load() {
		v.BlockedReason = "target_unavailable"
		return v, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// InspectMember carries the scheduler's existing admission decision. Reading
	// it avoids remote resolution while holding the catalog gate.
	target := svc.target(id.MembershipID)
	if target.IsZero() {
		v.BlockedReason = "member_paused"
		return v, nil
	}
	status, err := target.Call[MemberStatus](ctx, InspectMember{id})
	if err != nil {
		v.BlockedReason = "target_unavailable"
		return v, nil
	}
	if n.RoomReceipt != nil {
		state, e := svc.store.LoadRoom()
		if e != nil {
			return v, e
		}
		for _, member := range state.Members {
			if member.MembershipID == id.MembershipID && member.ReadThrough >= n.RoomReceipt.Seq {
				v.State = "consumed"
			}
		}
	}
	if n.Receipt != nil && status.Inbox != nil && status.Inbox.ConsumedThrough >= n.Receipt.Seq {
		v.State = "consumed"
	}
	switch {
	case status.Inbox != nil && status.Inbox.AutomaticBlocked:
		v.BlockedReason = "inbox_admission_blocked"
	case status.Status == "recovering":
		v.BlockedReason = "session_recovering"
	case status.Status == "budget_exhausted" || status.Status == "backend_blocked" || status.Status == "checkpoint_failed":
		v.BlockedReason = status.Status
	case status.Rotation != nil && status.Rotation.Status != "completed" && status.Rotation.Status != "failed":
		v.BlockedReason = "session_rotating"
	case status.Error != "":
		v.BlockedReason = "member_failed"
	case status.Paused:
		v.BlockedReason = "member_paused"
	}
	return v, nil
}

// One bounded, manager-owned local worker retries stable events. It uses the
// normal service/Inbox boundary and never retries Agent turns or backend work.
func (m *roomManager) startConnectNotificationsLocked() {
	if m.connectNotifyDone != nil || m.closing {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.connectNotifyCancel = cancel
	m.connectNotifyDone = make(chan struct{})
	m.connectNotifyWake = make(chan struct{}, 1)
	go func() {
		defer close(m.connectNotifyDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			m.deliverConnectNotifications(ctx)
			select {
			case <-ctx.Done():
				return
			case <-m.connectNotifyWake:
			case <-ticker.C:
			}
		}
	}()
}

func (m *roomManager) wakeConnectNotificationsLocked() {
	if m.connectNotifyWake != nil {
		select {
		case m.connectNotifyWake <- struct{}{}:
		default:
		}
	}
}

func (m *roomManager) deliverConnectNotifications(ctx context.Context) {
	m.mu.Lock()
	if m.closing || ctx.Err() != nil || m.store.check(true) != nil {
		m.mu.Unlock()
		return
	}
	ops, err := m.platformConnectOperationsLocked()
	selections, selectionErr := m.platformSelectionsLocked()
	m.mu.Unlock()
	if err != nil {
		return // status/startup expose invalid durable records; never invent results
	}
	for _, op := range ops {
		if !connectTerminal(op.Status) || ctx.Err() != nil {
			continue
		}
		m.mu.Lock()
		if !m.closing && m.store.check(true) == nil {
			m.deliverConnectResultLocked(ctx, op)
		}
		m.mu.Unlock()
	}
	if selectionErr == nil {
		for _, selection := range selections {
			m.mu.Lock()
			if !m.closing && ctx.Err() == nil && m.store.check(true) == nil {
				m.deliverPlatformSelectionLocked(ctx, selection.ID)
			}
			m.mu.Unlock()
		}
	}
}

func (m *roomManager) deliverConnectResultLocked(ctx context.Context, op platformConnectOperation) {
	n, err := m.readConnectNotificationLocked(op)
	svc, _ := m.runtimeServiceLocked(op.RoomIdentity)
	if err != nil || n.Receipt != nil || n.RoomReceipt != nil {
		return
	}
	handled := n.HandledVersion == op.ResultVersion
	if (svc == nil || !svc.unifiedInputs) && (n.Mode != "inbox" || handled) {
		return
	}
	if n.Event == nil {
		event := connectResultEvent(op)
		n.Event = &event
		if m.saveConnectNotificationLocked(n) != nil {
			return
		}
	}
	m.deliverOperationResultLocked(ctx, op.MemberIdentity, n, m.saveConnectNotificationLocked, roomOperationDelivery{
		Actor: Author{Kind: "system", ID: "system"}, Summary: "连接结果已更新",
		Notify: n.Mode == "inbox" && !handled && op.Status != "cancelled" && op.Status != "expired",
	})
}

type roomOperationDelivery struct {
	Actor   Author
	Summary string
	Notify  bool
}

func (m *roomManager) deliverOperationResultLocked(ctx context.Context, id MemberIdentity, n platformOperationNotification, save func(platformOperationNotification) error, details ...roomOperationDelivery) {
	previous := n.ErrorCode
	n.ErrorCode = "target_unavailable"
	svc, _ := m.runtimeServiceLocked(id.RoomIdentity)
	if svc != nil && !m.busy && !m.roomBusyLocked(id.RoomIdentity) && !m.platformMemberChanges[id] && !svc.closing.Load() && !svc.restarting.Load() && svc.changes.TryRLock() {
		// The manager gate plus changes and inboxMu fence removal/replacement.
		deliveryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		var receipt InboxReceipt
		var deliveryErr error
		if svc.unifiedInputs {
			d := roomOperationDelivery{Actor: Author{Kind: "system", ID: "system"}, Summary: "操作结果已更新", Notify: n.Mode == "inbox"}
			if len(details) > 0 {
				d = details[0]
			}
			targets := []string{}
			if d.Notify {
				targets = append(targets, id.MembershipID)
			}
			if n.RoomEvent == nil {
				event := RoomEvent{ID: n.Event.EventID, Kind: n.Event.EventType, SourceSeq: n.Event.SourceSeq, OperationID: n.Event.OperationID, ResultVersion: n.Event.ResultVersion, ResultRef: n.Event.ResultRef, Targets: targets, Actor: d.Actor}
				n.RoomEvent = &event
				n.RoomSummary = d.Summary
				if err := save(n); err != nil {
					cancel()
					svc.changes.RUnlock()
					return
				}
			}
			event := *n.RoomEvent
			var r PostReceipt
			r, deliveryErr = svc.room.Call[PostReceipt](deliveryCtx, AppendRoomEvent{RoomIdentity: id.RoomIdentity, Event: event, Summary: n.RoomSummary, CreatedAt: n.Event.CreatedAt})
			if deliveryErr == nil {
				n.RoomReceipt = &r
			}
		} else {
			receipt, deliveryErr = svc.receiveTaskEvent(deliveryCtx, id, *n.Event)
		}
		cancel()
		svc.changes.RUnlock()
		n.ErrorCode = ""
		if deliveryErr == nil {
			if !svc.unifiedInputs {
				n.Receipt = &receipt
			}
		} else {
			switch {
			case errors.Is(deliveryErr, ErrCapacity):
				n.ErrorCode = "inbox_capacity"
			case errors.Is(deliveryErr, ErrIdentity):
				n.ErrorCode = "target_changed"
			case errors.Is(deliveryErr, ErrBusy):
				n.ErrorCode = "inbox_busy"
			default:
				n.ErrorCode = "inbox_delivery_unconfirmed"
			}
		}
	}
	if n.Receipt != nil || n.RoomReceipt != nil || n.ErrorCode != previous {
		// Unknown acknowledgment keeps the original pending event. Reopening and
		// redelivering obtains the Inbox's original persisted receipt, even after
		// consumption/reclamation. It never allocates a second sequence.
		_ = save(n)
	}
}

func validRoomOperationIntent(n platformOperationNotification) bool {
	if n.RoomEvent == nil {
		return n.RoomReceipt == nil && n.RoomSummary == "" && !n.RoomConsumed
	}
	if n.Receipt != nil || n.Event == nil || !validText(n.RoomSummary) || len(n.RoomSummary) > maxTextBytes {
		return false
	}
	e := n.RoomEvent
	if e.ID != n.Event.EventID || e.Kind != n.Event.EventType || e.OperationID != n.OperationID || e.ResultVersion != n.Event.ResultVersion || e.SourceSeq != n.Event.SourceSeq || e.ResultRef != n.Event.ResultRef || len(e.Targets) > 1 {
		return false
	}
	if len(e.Targets) == 1 && e.Targets[0] != n.MembershipID {
		return false
	}
	if e.Actor.Kind != "human" && e.Actor.Kind != "system" || !validText(e.Actor.ID) {
		return false
	}
	if n.RoomConsumed && (n.RoomReceipt == nil || len(e.Targets) != 0) {
		return false
	}
	if n.RoomReceipt != nil && (n.RoomReceipt.RoomIdentity != n.RoomIdentity || n.RoomReceipt.Seq == 0 || n.RoomReceipt.ClientMessageID != roomEventKey(*e)) {
		return false
	}
	return true
}
