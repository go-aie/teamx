package room

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"time"
)

func TestUnifiedConnectionCancellationAndExpiryRemainPageOnly(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  string
		handled uint64
	}{
		{name: "completed", status: "completed"},
		{name: "cancelled", status: "cancelled"},
		{name: "expired", status: "expired"},
		{name: "acknowledged before publication", status: "completed", handled: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, s := unifiedReaderFixture(t, 1)
			m := &roomManager{store: h.store, runtimes: map[RoomIdentity]*roomRuntime{h.id: {service: s.service, status: "ready"}}}
			op := platformConnectOperation{ID: strings.Repeat("a", 32), MemberIdentity: s.storage.id, Status: test.status, ResultVersion: 1, UpdatedAt: time.Now().UTC()}
			op.Source.Seq = 1
			n := platformOperationNotification{Schema: 1, MemberIdentity: s.storage.id, OperationID: op.ID, Mode: "inbox", HandledVersion: test.handled}
			if err := m.saveConnectNotificationLocked(n); err != nil {
				t.Fatal(err)
			}
			m.deliverConnectResultLocked(h.ctx, op)
			saved, err := m.readConnectNotificationLocked(op)
			if err != nil || saved.RoomReceipt == nil || saved.RoomEvent == nil {
				t.Fatal(saved, err)
			}
			want := 0
			if test.status == "completed" && test.handled == 0 {
				want = 1
			}
			if len(saved.RoomEvent.Targets) != want || h.list(t).Members[0].UnreadCount != 1+want {
				t.Fatal(saved.RoomEvent, h.list(t))
			}
			if want == 0 {
				state := "page_only"
				if test.handled > 0 {
					state = "handled"
				}
				v, err := m.operationContinuationLocked(s.storage.id, true, saved)
				if err != nil || v.State != state || v.RoomSeq != 2 || v.BlockedReason != "" {
					t.Fatal(v, err)
				}
			}
			m.deliverConnectResultLocked(h.ctx, op)
			state, err := h.store.LoadRoom()
			if err != nil || state.LatestSeq != 2 {
				t.Fatal(state.LatestSeq, err)
			}
		})
	}
}

func TestSelectionSubmitPublishesRoomEventBeforeAgentRead(t *testing.T) {
	m, svc, member, source := connectFixtureWithSource(t, true, "请选择飞书账号")
	req := platformSelectionRequest{ServiceInfo: svc.info, Caller: source.Caller, OperationID: source.OperationID,
		SourceSeq: source.SourceSeq, Input: &platformSelectionInput{Kind: "account"}, Draft: &platformSelectionDraft{Name: "工作飞书"}}
	op := selectionCall(t, m, "start", req, false)
	// Subscribe without launching the retry worker. The HTTP submission owns
	// the first delivery attempt, even with no Agent running to read it.
	m.mu.Lock()
	err := m.saveSelectionNotificationLocked(platformOperationNotification{
		Schema: 1, MemberIdentity: op.MemberIdentity, OperationID: op.ID, Mode: "inbox",
	})
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	human := selectionQuery(req, true)
	human.Version = op.Version
	first := selectionCall(t, m, "submit", human, true)
	replay := selectionCall(t, m, "submit", human, true)
	seq := source.SourceSeq + 1
	if first.Continuation.State != "delivered" || first.Continuation.RoomSeq != seq || replay.Continuation.RoomSeq != seq {
		t.Fatal(first.Continuation, replay.Continuation)
	}
	history, err := svc.room.Call[ReadPage](t.Context(), ReadMessages{RoomIdentity: svc.info.RoomIdentity, Caller: human.Caller, Mode: ReadLatest, Latest: 2})
	if err != nil || len(history.Messages) != 2 {
		t.Fatal(history, err)
	}
	event := history.Messages[1].Event
	if event == nil || event.Kind != "user_selection" || event.SourceSeq != source.SourceSeq || len(event.Targets) != 1 || event.Targets[0] != member.MembershipID {
		t.Fatal(event)
	}
}

func TestUnifiedOperationEventIntentReceiptRetryAndNoInboxDoubleWrite(t *testing.T) {
	for _, notify := range []bool{true, false} {
		t.Run(map[bool]string{true: "targeted", false: "page-only"}[notify], func(t *testing.T) {
			h, s := unifiedReaderFixture(t, 1)
			id := s.storage.id
			m := &roomManager{runtimes: map[RoomIdentity]*roomRuntime{h.id: {service: s.service, status: "ready"}}}
			task := InboxTaskEvent{EventID: "selection-result", EventType: "user_selection", OperationID: "selection", SourceSeq: 1, ResultVersion: 1, ResultRef: "platforms/selection/selection", CreatedAt: time.Now().UTC()}
			n := platformOperationNotification{Schema: 1, MemberIdentity: id, OperationID: task.OperationID, Mode: "none", Event: &task}
			if notify {
				n.Mode = "inbox"
			}
			saved := n
			loseReceipt := true
			save := func(v platformOperationNotification) error {
				if v.RoomReceipt != nil && loseReceipt {
					return errors.New("lost receipt save")
				}
				saved = v
				return nil
			}
			details := roomOperationDelivery{Actor: Author{Kind: "human", ID: "verified-clicker"}, Summary: "已完成选择", Notify: notify}
			m.deliverOperationResultLocked(context.Background(), id, n, save, details)
			if saved.RoomEvent == nil || saved.RoomReceipt != nil {
				t.Fatal("intent not durable before append")
			}
			state, err := h.store.LoadRoom()
			if err != nil || state.LatestSeq != 2 || state.Messages[1].Event == nil {
				t.Fatal(err)
			}
			// Reconfigure notification policy after receipt loss: the original audience
			// and actor remain immutable; only its exact receipt is recovered.
			saved.Mode = "none"
			loseReceipt = false
			m.deliverOperationResultLocked(context.Background(), id, saved, save, roomOperationDelivery{Actor: Author{Kind: "system", ID: "system"}, Summary: "changed", Notify: false})
			if saved.RoomReceipt == nil || saved.RoomReceipt.Seq != 2 || !validRoomOperationIntent(saved) {
				t.Fatal(saved)
			}
			state, err = h.store.LoadRoom()
			if err != nil || state.LatestSeq != 2 || state.Messages[1].Author.ID != "verified-clicker" {
				t.Fatal(err)
			}
			for _, name := range []string{"inbox.json", "consumption.json"} {
				if _, err := os.Stat(filepath.Join(h.store.dir, "inboxes", id.MembershipID, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("legacy task-event write", err)
				}
			}
			list := h.list(t)
			want := 1
			if notify {
				want = 2
			}
			if list.Members[0].UnreadCount != want || list.Members[1].UnreadCount != 1 {
				t.Fatal(list)
			}
			if _, err := s.service.receiveTaskEvent(h.ctx, id, task); !errors.Is(err, ErrVersion) {
				t.Fatal("new runtime accepted task-event Inbox", err)
			}
		})
	}
}

func TestRoomClearProtectsAnEventBeforeItsFirstRead(t *testing.T) {
	h, s := unifiedReaderFixture(t, 1)
	id := s.storage.id
	event := appendInputEvent(t, h, "awaiting-recipient", id.MembershipID)
	pageOnly := appendInputEvent(t, h, "page-only-after", "")
	req := ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: pageOnly.Seq}
	roomError[ClearReceipt](t, h, req, ErrBusy)
	if h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal("failed clear advanced the recipient")
	}
	result := readInputStream(t, s, "event", roomStreamRef(id), 10, true)
	if len(result.Entries) != 2 || h.list(t).Members[0].ReadThrough != pageOnly.Seq {
		t.Fatal(result, "saved audience did not receive the event")
	}
	if err := s.storage.repository.Seal(s.execution); err != nil {
		t.Fatal(err)
	}
	cleared := roomCall[ClearReceipt](t, h, req)
	state, err := h.store.LoadRoom()
	if err != nil || cleared.ClearedThrough != pageOnly.Seq || len(state.Messages) != 0 {
		t.Fatal(cleared, state.Messages, err)
	}
	if again := appendInputEvent(t, h, "awaiting-recipient", id.MembershipID); again != event {
		t.Fatal("clear removed operation event deduplication", again, event)
	}
}
