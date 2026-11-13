package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func connectObservation(req platformConnectRequest) platformConnectRequest {
	req.Input, req.Notify, req.SourceSeq = nil, nil, 0
	return req
}

func connectNotification(t *testing.T, m *roomManager, id string) platformOperationNotification {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	op, err := m.readPlatformConnectLocked(id)
	if err != nil {
		t.Fatal(err)
	}
	n, err := m.readConnectNotificationLocked(op)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func subscribeConnect(t *testing.T, m *roomManager, op platformConnectOperation, mode string) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.setConnectNotificationLocked(op, mode); err != nil {
		t.Fatal(err)
	}
}

func waitConnectDelivery(t *testing.T, m *roomManager, id string) platformOperationNotification {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n := connectNotification(t, m, id)
		if n.Receipt != nil {
			return n
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("connection result was not delivered")
	return platformOperationNotification{}
}

type platformConnectionFixture func(*testing.T) (*roomManager, *service, MemberState, platformConnectRequest)

func TestPlatformConnectNotificationSubscriptionHandoffAndLateCompletion(t *testing.T) {
	platformConnectSubscriptionHandoff(t, connectFixture)
}

func TestDefaultAssistantConnectionPauseAndLateCompletion(t *testing.T) {
	platformConnectSubscriptionHandoff(t, assistantConnectFixture)
}

func platformConnectSubscriptionHandoff(t *testing.T, fixture platformConnectionFixture) {
	t.Helper()
	for _, timing := range []string{"before", "concurrent", "after"} {
		t.Run(timing, func(t *testing.T) {
			m, svc, member, req := fixture(t)
			_, session := connectPending(t, m, svc, req)
			query := connectObservation(req)
			mode := "inbox"
			query.Notify = &mode
			if timing == "after" {
				connectSucceed(t, m, session)
			}
			var finishing chan struct{}
			if timing == "concurrent" {
				finishing = make(chan struct{})
				go func() { defer close(finishing); connectSucceed(t, m, session) }()
			}
			op := connectHTTP(t, m, "notify", query)
			if op.Continuation == nil || op.Continuation.Mode != "inbox" {
				t.Fatal(op)
			}
			if timing == "before" {
				connectSucceed(t, m, session)
			}
			if finishing != nil {
				<-finishing
			}
			n := waitConnectDelivery(t, m, req.OperationID)
			if n.Receipt.Seq != 1 || n.Event.OperationID != req.OperationID || n.Event.SourceSeq != req.SourceSeq || n.Event.EventType != "operation_completed" {
				t.Fatal(n)
			}
			// Repeated subscriptions, CLI observers, and the native completion
			// callback all keep the same result, immutable event and Inbox sequence.
			client, endpoint := toolPipeServer(t, m)
			var out bytes.Buffer
			for _, args := range [][]string{
				{"connect", "notify", "--operation-id", req.OperationID, "--mode", "inbox"},
				{"connect", "wait", "--operation-id", req.OperationID},
			} {
				out.Reset()
				if err := runPlatformConnectWithClient(context.Background(), args, &out, &out, cliToolEnv(endpoint, svc, member), client); err != nil {
					t.Fatal(err, out.String())
				}
			}
			connectSucceed(t, m, session)
			m.deliverConnectNotifications(context.Background())
			if again := connectNotification(t, m, req.OperationID); !reflect.DeepEqual(n, again) {
				t.Fatal("handoff changed stable event or receipt", again)
			}
			query.Notify = nil
			before, _ := os.ReadFile(m.connectNotificationPath(req.OperationID))
			status := connectHTTP(t, m, "status", query)
			after, _ := os.ReadFile(m.connectNotificationPath(req.OperationID))
			if !bytes.Equal(before, after) || status.ResultVersion != 1 || status.Status != "completed" || status.Continuation.State != "delivered" || status.Continuation.BlockedReason != "member_paused" {
				t.Fatal("read changed handoff or hid pause", status)
			}
		})
	}
}

func TestPlatformConnectNotificationStartPolicyIsIndependentOfBusinessInput(t *testing.T) {
	m, svc, member, req := connectFixture(t)
	original, _ := connectPending(t, m, svc, req)
	client, endpoint := toolPipeServer(t, m)
	for _, mode := range []string{"inbox", "none", "inbox"} {
		var out bytes.Buffer
		args := []string{"connect", "start", "--provider", "lark", "--name", req.Input.Name, "--operation-id", req.OperationID,
			"--source-seq", "1", "--notify", mode}
		if err := runPlatformConnectWithClient(context.Background(), args, &out, &out, cliToolEnv(endpoint, svc, member), client); err != nil {
			t.Fatal(err, out.String())
		}
		var op platformConnectOperation
		if err := json.Unmarshal(out.Bytes(), &op); err != nil || op.Input != original.Input || op.Version != original.Version || op.ID != original.ID || op.Continuation == nil || op.Continuation.Mode != mode {
			t.Fatal("policy change restarted operation", err, out.String())
		}
		if n := connectNotification(t, m, req.OperationID); n.Mode != mode || n.Event != nil {
			t.Fatal("pending start produced result or lost policy", n)
		}
	}
	query := connectObservation(req)
	bad := "arbitrary"
	query.Notify = &bad
	if w := managerHTTP(m, "/v1/platforms/connect/notify", query); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	mode := "inbox"
	query.Notify = &mode
	if w := managerHTTP(m, "/v1/platforms/connect/status", query); w.Code != 400 {
		t.Fatal("status accepted a mutation", w.Code)
	}
	query.Notify, query.ResultVersion = nil, 1
	if w := managerHTTP(m, "/v1/platforms/connect/acknowledge", query); w.Code != 409 {
		t.Fatal("pending result acknowledged", w.Code)
	}
}

func TestPlatformConnectNotificationReusedConnectionPublishesSavedResult(t *testing.T) {
	m, svc, _, req := connectFixture(t)
	_, session := connectPending(t, m, svc, req)
	connectSucceedWithNativeProfile(t, m, session)
	connected := connectHTTP(t, m, "status", connectObservation(req))
	source, err := svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: humanCaller,
		ClientMessageID: "reuse-and-continue", Text: "使用已有工作飞书连接"})
	if err != nil {
		t.Fatal(err)
	}
	req.OperationID, _ = newOpaqueID()
	req.SourceSeq = source.Seq
	req.Input = &platformConnectInput{Provider: "lark", Name: connected.Input.Name, PlatformID: connected.PlatformID, PlatformVersion: connected.Connection.Version}
	mode := "inbox"
	req.Notify = &mode
	_ = connectHTTP(t, m, "start", req)
	started := waitConnectionResult(t, m, req)
	n := waitConnectDelivery(t, m, req.OperationID)
	if started.Status != "completed" || started.UpdatedAt.IsZero() || n.Event.CreatedAt != started.UpdatedAt || n.Event.SourceSeq != source.Seq {
		t.Fatal("reused connection published an unsaved result", started, n)
	}
}

func TestPlatformConnectNotificationUnsubscribeAcknowledgmentAndIdentity(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		t.Run(map[bool]string{false: "before_delivery", true: "after_delivery"}[delivered], func(t *testing.T) {
			m, svc, member, req := connectFixture(t)
			_, session := connectPending(t, m, svc, req)
			connectSucceed(t, m, session)
			op := connectHTTP(t, m, "status", connectObservation(req))
			subscribeConnect(t, m, op, "inbox")
			if delivered {
				m.deliverConnectNotifications(context.Background())
			}
			subscribeConnect(t, m, op, "none")
			m.deliverConnectNotifications(context.Background())
			n := connectNotification(t, m, op.ID)
			if (n.Receipt != nil) != delivered {
				t.Fatal("unsubscribe retracted or produced a delivery", n)
			}
			query := connectObservation(req)
			query.ResultVersion = 2
			if w := managerHTTP(m, "/v1/platforms/connect/acknowledge", query); w.Code != 409 {
				t.Fatal(w.Code, w.Body.String())
			}
			client, endpoint := toolPipeServer(t, m)
			var out bytes.Buffer
			args := []string{"connect", "acknowledge", "--operation-id", op.ID, "--result-version", "1"}
			if err := runPlatformConnectWithClient(context.Background(), args, &out, &out, cliToolEnv(endpoint, svc, member), client); err != nil {
				t.Fatal(err, out.String())
			}
			subscribeConnect(t, m, op, "inbox")
			m.deliverConnectNotifications(context.Background())
			n = connectNotification(t, m, op.ID)
			if n.HandledVersion != 1 || (n.Receipt != nil) != delivered {
				t.Fatal("handled result was delivered again", n)
			}
			query.ResultVersion = 1
			query.Caller.MembershipID = "replacement"
			for _, action := range []string{"notify", "acknowledge"} {
				if w := managerHTTP(m, "/v1/platforms/connect/"+action, query); w.Code == 200 {
					t.Fatal("replacement inherited result", action)
				}
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			reopened := testManager(t, m.store.dir, nil)
			if restored := connectNotification(t, reopened, op.ID); !reflect.DeepEqual(n, restored) {
				t.Fatal("handling receipt lost on restart", restored)
			}
		})
	}
}

func TestPlatformConnectNotificationCrashRecoveryUsesOriginalDelivery(t *testing.T) {
	for _, boundary := range []string{"outbox", "inbox_body", "handoff_receipt", "consumed"} {
		t.Run(boundary, func(t *testing.T) {
			m, svc, _, req := connectFixture(t)
			op, session := connectPending(t, m, svc, req)
			subscribeConnect(t, m, op, "inbox")
			connectSucceed(t, m, session)
			faultStore := m.store
			if boundary == "inbox_body" {
				faultStore = svc.store
			}
			original := faultStore.ops.rename
			faultStore.ops.rename = func(from, to string) error {
				match := strings.Contains(to, "/inboxes/") && filepath.Base(to) == inboxEntryName(1)
				if boundary != "inbox_body" && to == m.connectNotificationPath(op.ID) {
					data, err := os.ReadFile(from)
					if err != nil {
						return err
					}
					var n platformOperationNotification
					_ = json.Unmarshal(data, &n)
					match = boundary == "outbox" && n.Event != nil || (boundary == "handoff_receipt" || boundary == "consumed") && n.Receipt != nil
				}
				if boundary == "consumed" && match {
					return errors.New("injected crash before root acknowledgment")
				}
				// Simulate uncertain replacement, including an already durable write.
				if err := original(from, to); err != nil {
					return err
				}
				if match {
					return errors.New("injected connection handoff crash")
				}
				return nil
			}
			m.deliverConnectNotifications(context.Background())
			faultStore.ops.rename = original
			if !errors.Is(faultStore.check(true), ErrCommitUnknown) {
				t.Fatal("crash boundary was not exercised", boundary)
			}
			if boundary == "consumed" {
				state := AgentSnapshot{Version: formatVersion, MemberIdentity: op.MemberIdentity, TurnNumber: 1, LastOfferedInboxSeq: 1,
					Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}}
				if err := svc.store.SaveAgent(state); err != nil {
					t.Fatal(err)
				}
				consumeConnectTestInbox(t, svc, op.MemberIdentity, 1)
				state.Turn.Status = "completed"
				if err := svc.store.SaveAgent(state); err != nil {
					t.Fatal(err)
				}
				if err := svc.maintainInboxLocal(context.Background(), op.MemberIdentity, nil, time.Now().Add(8*24*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			reopened := testManager(t, m.store.dir, nil)
			svc = eventLiveService(t, &eventHarness{m: reopened, room: roomSummary{RoomIdentity: op.RoomIdentity}})
			reopened.mu.Lock()
			reopened.startConnectNotificationsLocked()
			reopened.mu.Unlock()
			waitConnectDelivery(t, reopened, op.ID)
			for range 2 {
				reopened.deliverConnectNotifications(context.Background())
			}
			n := connectNotification(t, reopened, op.ID)
			if n.Receipt == nil || n.Receipt.Seq != 1 {
				t.Fatal(n)
			}
			preview, err := svc.previewInbox(context.Background(), apiRequest{Caller: humanCaller, AgentID: op.AgentID, MembershipID: op.MembershipID})
			if err != nil {
				t.Fatal(err)
			}
			wire, _ := json.Marshal(preview)
			expectedBodies := 1
			if boundary == "consumed" {
				expectedBodies = 0
			}
			if bytes.Count(wire, []byte(`"type":"task_event"`)) != expectedBodies {
				t.Fatal("duplicate or missing result", string(wire))
			}
			status, err := svc.inboxes[op.MembershipID].Call[InboxStatus](context.Background(), InspectInbox{op.MemberIdentity})
			if err != nil || status.LatestSeq != 1 || boundary == "consumed" && (status.ConsumedThrough != 1 || status.ExpiredThrough != 1) {
				t.Fatal("recovery allocated a replacement sequence", status, err)
			}
		})
	}
}

func TestPlatformConnectNotificationTerminalFailuresAndRestartResult(t *testing.T) {
	for _, outcome := range []string{"failed", "expired", "cancelled", "service_restart"} {
		t.Run(outcome, func(t *testing.T) {
			m, svc, _, req := connectFixture(t)
			op, session := connectPending(t, m, svc, req)
			subscribeConnect(t, m, op, "inbox")
			m.mu.Lock()
			close(session.done)
			if outcome == "service_restart" {
				delete(m.larkAuthSessions, session.SessionID)
				if err := m.recoverPlatformConnectLocked(); err != nil {
					t.Fatal(err)
				}
			} else {
				session.Status, session.ErrorCode = outcome, "fixture_failure"
				m.finishPlatformConnectLocked(session)
			}
			m.mu.Unlock()
			m.deliverConnectNotifications(context.Background())
			n := connectNotification(t, m, op.ID)
			if n.Event == nil || n.Event.EventType != "operation_failed" || n.Receipt == nil {
				t.Fatal(n)
			}
			status := connectHTTP(t, m, "status", connectObservation(req))
			if status.Status == "completed" || status.Connection.Status == "connected" {
				t.Fatal("notification changed operation outcome", status)
			}
		})
	}
}

func TestPlatformConnectNotificationAutomaticallyContinuesWithoutChatAndDoesNotRetryFailure(t *testing.T) {
	platformConnectAutomaticContinuation(t, connectFixture)
}

func TestDefaultAssistantConnectionAutomaticContinuationAndFailure(t *testing.T) {
	platformConnectAutomaticContinuation(t, assistantConnectFixture)
}

func platformConnectAutomaticContinuation(t *testing.T, fixture platformConnectionFixture) {
	t.Helper()
	m, svc, _, req := fixture(t)
	op, session := connectPending(t, m, svc, req)
	b := newControlledBackend()
	id := op.MemberIdentity
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	member, err := spawnScheduledMember(svc.rt, &MemberAgent{Options: SessionOptions{MemberIdentity: id}, Backend: b, Store: svc.store, Room: svc.room,
		Inbox: func(ctx context.Context) (InboxStatus, error) { return svc.memberInboxStatus(ctx, id) }}, svc.scheduler)
	if err != nil {
		t.Fatal(err)
	}
	svc.setTarget(id.MembershipID, member)
	if _, err := member.Call[MemberStatus](ctx, ControlMember{MemberIdentity: id, Action: "run", State: UnreadChanged{MemberIdentity: id}}); err != nil {
		t.Fatal(err)
	}
	query := connectObservation(req)
	mode := "inbox"
	query.Notify = &mode
	connectHTTP(t, m, "notify", query)
	connectSucceed(t, m, session)
	select {
	case input := <-b.entered:
		if input.Inbox == nil || input.Inbox.SnapshotSeq != 1 || input.LatestSeq != req.SourceSeq {
			t.Fatal("result did not arrive as ordinary Inbox input", input)
		}
	case <-ctx.Done():
		t.Fatal("no automatic continuation", ctx.Err())
	}
	if err := svc.store.initReferenceFollowups(id); err != nil {
		t.Fatal(err)
	}
	page, err := svc.readReferenceInbox(ctx, id, 1, 25, nil)
	if err != nil || len(page.Entries) != 1 || page.Entries[0].TaskEvent.OperationID != op.ID {
		t.Fatal(err, page)
	}
	// Consume normally, then fail the Agent turn. Delivery and failure recovery
	// remain distinct: no replacement event or automatic retry is permitted.
	addr := svc.inboxes[id.MembershipID]
	if _, err := addr.Call[InboxStatus](ctx, ConfirmInboxBatch{MemberIdentity: id, TurnNumber: 1, Receipt: page.Batch.Receipt}); err != nil {
		t.Fatal(err)
	}
	b.finish <- errors.New("fixture continuation failure")
	waitMember(t, ctx, member, id, func(s MemberStatus) bool {
		return !s.Active && s.Error != "" && s.Inbox != nil && s.Inbox.ConsumedThrough == 1
	})
	m.deliverConnectNotifications(ctx)
	query.Notify = nil
	observed := connectHTTP(t, m, "status", query)
	if observed.Status != "completed" || observed.Continuation.BlockedReason != "member_failed" || observed.Continuation.State != "consumed" {
		t.Fatal(observed)
	}
	select {
	case <-b.entered:
		t.Fatal("consumed event retried failed turn")
	case <-time.After(150 * time.Millisecond):
	}
	if state, err := svc.store.LoadRoom(); err != nil || state.LatestSeq != req.SourceSeq {
		t.Fatal("result created a chat message", err, state.LatestSeq)
	}
}

func consumeConnectTestInbox(t *testing.T, svc *service, id MemberIdentity, turn uint64) referenceInboxPage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Initialization shares the commit admission boundary with the warmup
	// checkpoint, just like the subsequent read. Retry its explicit busy result.
	for {
		err := svc.store.initReferenceFollowups(id)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrBusy) || ctx.Err() != nil {
			t.Fatal(err, ctx.Err())
		}
		time.Sleep(time.Millisecond)
	}
	var page referenceInboxPage
	for {
		var err error
		page, err = svc.readReferenceInbox(ctx, id, turn, 25, nil)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrBusy) || ctx.Err() != nil {
			t.Fatal(err, ctx.Err())
		}
		time.Sleep(time.Millisecond)
	}
	// Confirm shares the checkpoint admission boundary too. Retry only an
	// explicit rejection, using the same batch receipt throughout.
	for {
		_, err := svc.inboxes[id.MembershipID].Call[InboxStatus](ctx, ConfirmInboxBatch{MemberIdentity: id, TurnNumber: turn, Receipt: page.Batch.Receipt})
		if err == nil {
			break
		}
		if !errors.Is(err, ErrBusy) || ctx.Err() != nil {
			t.Fatal(err, ctx.Err())
		}
		time.Sleep(time.Millisecond)
	}
	return page
}

func TestPlatformConnectNotificationCapacityRetainsPendingAndRetriesAfterReclamation(t *testing.T) {
	m, svc, _, req := connectFixture(t)
	op, session := connectPending(t, m, svc, req)
	id := op.MemberIdentity
	limits := DefaultInboxLimits()
	limits.Entries = 1
	addr, err := SpawnAgentInbox(svc.rt, svc.store, id, limits)
	if err != nil {
		t.Fatal(err)
	}
	svc.inboxes[id.MembershipID] = addr
	if _, err := svc.receiveTaskEvent(context.Background(), id, taskEventFixture("occupies-capacity")); err != nil {
		t.Fatal(err)
	}
	subscribeConnect(t, m, op, "inbox")
	connectSucceed(t, m, session)
	m.deliverConnectNotifications(context.Background())
	n := connectNotification(t, m, op.ID)
	if n.Event == nil || n.Receipt != nil || n.ErrorCode != "inbox_capacity" {
		t.Fatal(n)
	}
	status := connectHTTP(t, m, "status", connectObservation(req))
	if status.Status != "completed" || status.Continuation.BlockedReason != "inbox_capacity" {
		t.Fatal("capacity changed result or hid block", status)
	}
	state := AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 1,
		Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}}
	if err := svc.store.SaveAgent(state); err != nil {
		t.Fatal(err)
	}
	consumeConnectTestInbox(t, svc, id, 1)
	state.Turn.Status = "completed"
	if err := svc.store.SaveAgent(state); err != nil {
		t.Fatal(err)
	}
	if err := svc.maintainInboxLocal(context.Background(), id, nil, time.Now().Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	m.deliverConnectNotifications(context.Background())
	retried := connectNotification(t, m, op.ID)
	if retried.Receipt == nil || retried.Receipt.Seq != 2 || retried.ErrorCode != "" || !reflect.DeepEqual(n.Event, retried.Event) {
		t.Fatal("capacity retry replaced result or event", retried)
	}
}

func TestPlatformConnectNotificationCannotBypassAdmissionOrMemberRemoval(t *testing.T) {
	platformConnectAdmission(t, connectFixture, true)
}

func TestDefaultAssistantConnectionCannotBypassAdmission(t *testing.T) {
	platformConnectAdmission(t, assistantConnectFixture, false)
}

func platformConnectAdmission(t *testing.T, fixture platformConnectionFixture, remove bool) {
	t.Helper()
	m, svc, _, req := fixture(t)
	op, session := connectPending(t, m, svc, req)
	id := op.MemberIdentity
	svc.store.notificationAdapters = contractNotificationCapabilities
	source := contractSource("blocked", "blocked")
	if err := svc.receiveInbox(context.Background(), notification.Target{AgentID: id.AgentID, MembershipID: id.MembershipID}, notification.Item{
		Key: source.ItemKey, DeliveryID: "blocked-notification", Source: source, Text: "unverified", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	b := newControlledBackend()
	member, err := spawnScheduledMember(svc.rt, &MemberAgent{Options: SessionOptions{MemberIdentity: id}, Backend: b, Store: svc.store, Room: svc.room,
		Inbox: func(ctx context.Context) (InboxStatus, error) { return svc.memberInboxStatus(ctx, id) }}, svc.scheduler)
	if err != nil {
		t.Fatal(err)
	}
	svc.setTarget(id.MembershipID, member)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := member.Call[MemberStatus](ctx, ControlMember{MemberIdentity: id, Action: "run", State: UnreadChanged{MemberIdentity: id}}); err != nil {
		t.Fatal(err)
	}
	subscribeConnect(t, m, op, "inbox")
	connectSucceed(t, m, session)
	m.deliverConnectNotifications(ctx)
	waitMember(t, ctx, member, id, func(s MemberStatus) bool { return s.Inbox != nil && s.Inbox.LatestSeq == 2 && s.Inbox.AutomaticBlocked })
	status := connectHTTP(t, m, "status", connectObservation(req))
	if status.Continuation.State != "delivered" || status.Continuation.BlockedReason != "inbox_admission_blocked" || b.opens.Load() != 0 {
		t.Fatal("event bypassed admission", status)
	}
	if !remove {
		return
	}
	// A separate result for the removed identity stays pending and never routes
	// by the Agent's name. The successful connection receipt remains available.
	if _, err := svc.room.Call[MemberInfo](ctx, RemoveMember{id}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	n, err := m.readConnectNotificationLocked(status)
	if err == nil {
		n.Receipt = nil // simulate a lost root handoff acknowledgment
		err = m.saveConnectNotificationLocked(n)
	}
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	m.deliverConnectNotifications(ctx)
	if n := connectNotification(t, m, op.ID); n.Receipt != nil || n.ErrorCode != "target_changed" {
		t.Fatal("removed member received pending result", n)
	}
}

func TestPlatformConnectNotificationContinuesAcrossSessionRotation(t *testing.T) {
	m, svc, roster, req := connectFixture(t)
	op, session := connectPending(t, m, svc, req)
	id := op.MemberIdentity
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered, finish := make(chan struct{}, 2), make(chan struct{}, 2)
	backend := dshBackend{store: svc.store, open: func(context.Context, dsh.Options) (dshComponent, error) {
		return &adapterComponent{run: func(ctx context.Context, _ string) (dsh.TurnResult, error) {
			entered <- struct{}{}
			select {
			case <-finish:
				return dsh.TurnResult{StopReason: "end_turn"}, nil
			case <-ctx.Done():
				return dsh.TurnResult{}, ctx.Err()
			}
		}}, nil
	}}
	svc.dshProfiles = map[string]dshProfile{roster.Runner.Profile: {}}
	member, err := spawnScheduledMember(svc.rt, &MemberAgent{Options: adapterOptions(svc.store, id), Backend: backend, Store: svc.store, Room: svc.room,
		SessionMaxTurns: 1, RequestRotation: svc.requestMemberRotation,
		Inbox: func(ctx context.Context) (InboxStatus, error) { return svc.memberInboxStatus(ctx, id) }}, svc.scheduler)
	if err != nil {
		t.Fatal(err)
	}
	svc.setTarget(id.MembershipID, member)
	if _, err := svc.receiveTaskEvent(ctx, id, taskEventFixture("warmup")); err != nil {
		t.Fatal(err)
	}
	if _, err := member.Call[MemberStatus](ctx, ControlMember{MemberIdentity: id, Action: "run", State: UnreadChanged{MemberIdentity: id}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("warmup turn did not start", ctx.Err())
	}
	consumeConnectTestInbox(t, svc, id, 1)
	finish <- struct{}{}
	first := waitMember(t, ctx, member, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 1 })
	subscribeConnect(t, m, op, "inbox")
	m.mu.Lock()
	m.startConnectNotificationsLocked()
	m.mu.Unlock()
	connectSucceed(t, m, session)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("operation result did not continue after rotation", ctx.Err())
	}
	page := consumeConnectTestInbox(t, svc, id, 2)
	if len(page.Entries) != 1 || page.Entries[0].TaskEvent.OperationID != op.ID {
		t.Fatal("rotation replayed old input", page)
	}
	query := connectObservation(req)
	query.ResultVersion = 1
	if handled := connectHTTP(t, m, "acknowledge", query); handled.Continuation.State != "handled" {
		t.Fatal(handled)
	}
	finish <- struct{}{}
	second := waitMember(t, ctx, member, id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber == 2 })
	if second.SessionGeneration == first.SessionGeneration || second.SessionStartUsed != 1 {
		t.Fatalf("result did not cross a Session boundary: first=%s second=%s baseline=%d turn=%d", first.SessionGeneration, second.SessionGeneration, second.SessionStartUsed, second.Agent.TurnNumber)
	}
	select {
	case <-entered:
		t.Fatal("handled result was offered again after rotation")
	case <-time.After(150 * time.Millisecond):
	}
}
