package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func taskEventFixture(id string) InboxTaskEvent {
	return InboxTaskEvent{EventID: id, OperationID: "connect-original", ResultVersion: 2, SourceSeq: 1,
		EventType: "operation_completed", ResultRef: "connect/connect-original/2", CreatedAt: time.Now().UTC()}
}

func receiveTestTaskEvent(t *testing.T, h *inboxHarness, event InboxTaskEvent) InboxReceipt {
	t.Helper()
	for {
		r, err := referenceTestService(h).receiveTaskEvent(h.ctx, h.id, event)
		if !errors.Is(err, ErrBusy) {
			if err != nil {
				t.Fatal(err)
			}
			return r
		}
		// A concurrent Room commit or Inbox maintenance is ordinary backpressure.
		// Retry the same immutable event, just as a service-internal producer will.
		select {
		case <-h.ctx.Done():
			t.Fatal(h.ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
}

func readMixedTestInbox(t *testing.T, h *inboxHarness, turn uint64) referenceInboxPage {
	t.Helper()
	page, err := referenceTestService(h).readReferenceInbox(h.ctx, h.id, turn, 25, referenceTestResolver)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestInboxMixedPayloadReadConfirmRecoveryAndReclamation(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	first := receiveReference(t, h, "platform", referenceTestSource("platform"))
	event := taskEventFixture("result")
	receipt := receiveTestTaskEvent(t, h, event)
	last := receiveReference(t, h, "platform2", referenceTestSource("platform2"))
	if first.Seq != 1 || receipt.Seq != 2 || last.Seq != 3 {
		t.Fatal(first, receipt, last)
	}
	inboxRunning(t, h, 1, 3)
	page := readMixedTestInbox(t, h, 1)
	if len(page.Entries) != 3 || len(page.Sources) != 2 || len(page.Followups) != 1 {
		t.Fatal(page)
	}
	view, err := projectAgentInboxRead(h.id, page)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(view)
	var output struct {
		Entries []map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(b, &output); err != nil {
		t.Fatal(err)
	}
	if string(output.Entries[0]["type"]) != `"platform_notification"` || string(output.Entries[1]["type"]) != `"task_event"` ||
		output.Entries[1]["item"] != nil || output.Entries[1]["source"] != nil || output.Entries[1]["notification_source"] != nil ||
		!reflect.DeepEqual(view.Entries[1].TaskEvent, &event) {
		t.Fatal(string(b))
	}
	preview := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
	human, err := h.store.projectInboxPreview(preview, InboxStatus{})
	if err != nil || human.Entries[1].Type != InboxTaskEventType || human.Entries[1].NotificationSource != nil {
		t.Fatal(human, err)
	}
	if s := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); s.ConsumedThrough != 0 {
		t.Fatal("preview consumed", s)
	}
	// A local event is never a native source, even on explicit re-resolution.
	called := false
	_, err = referenceTestService(h).resolveFollowupEvent(h.ctx, h.id, 2, 1, true,
		func(context.Context, MemberIdentity, platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
			called = true
			return platform.TargetResolution{}, platform.AccessContext{}, nil
		})
	if !errors.Is(err, ErrRequest) || called {
		t.Fatal("event entered native resolver", err, called)
	}
	// Reopen with both kinds, issued page and durable associations but no ack.
	h.reload(t, true)
	again := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
	if again.Batch.Receipt != page.Batch.Receipt || again.Batch.ID != page.Batch.ID {
		t.Fatal("lost batch", again)
	}
	if s := confirmInboxPage(t, h, again); s.ConsumedThrough != 3 {
		t.Fatal(s)
	}
	if _, err := os.Stat(h.store.eventPath(h.id, 2)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("event fabricated platform followup", err)
	}
	units, err := h.store.listFollowups(h.id, followupQuery{Limit: 25})
	if err != nil || units.Total != 1 || units.Items[0].Status != "pending" {
		t.Fatal("consumption completed business", units, err)
	}
	settleReclaimTurn(t, h)
	if s := maintainTestInbox(t, h, time.Now().Add(9*24*time.Hour), true); s.ExpiredThrough != 3 || s.RetainedEntries != 0 {
		t.Fatal(s)
	}
	h.reload(t, true)
	// Internal dedup survives body cleanup and a full Store reopen.
	if retry := inboxCall[InboxReceipt](t, h, ReceiveInbox{MemberIdentity: h.id, Type: InboxTaskEventType, TaskEvent: &event}); retry != receipt {
		t.Fatal(retry, receipt)
	}
	if s := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); s.LatestSeq != 3 {
		t.Fatal("replayed event", s)
	}
	units, err = h.store.listFollowups(h.id, followupQuery{Limit: 25})
	if err != nil || units.Items[0].Status != "pending" {
		t.Fatal("reclaim waited on/completed long-lived work", units, err)
	}
}

func TestInboxTaskEventLinkAndConsumptionWriteInterruptions(t *testing.T) {
	for _, boundary := range []string{"link_before_rename", "link_after_rename", "ack_before_rename", "ack_after_rename"} {
		t.Run(boundary, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			initReferenceTest(t, h)
			event := taskEventFixture(boundary)
			receiveTestTaskEvent(t, h, event)
			inboxRunning(t, h, 1, 1)
			page := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
			inboxError[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: page.Batch.Receipt}, os.ErrNotExist)
			if strings.HasPrefix(boundary, "ack") {
				readMixedTestInbox(t, h, 1)
			}
			original := h.store.ops.rename
			boom := errors.New("injected interruption")
			h.store.ops.rename = func(from, to string) error {
				match := strings.Contains(to, string(filepath.Separator)+"links"+string(filepath.Separator))
				if strings.HasPrefix(boundary, "ack") {
					match = filepath.Base(to) == "consumption.json"
				}
				if !match {
					return original(from, to)
				}
				if strings.HasSuffix(boundary, "after_rename") {
					if err := original(from, to); err != nil {
						return err
					}
				}
				return boom
			}
			if strings.HasPrefix(boundary, "link") {
				_, err := referenceTestService(h).readReferenceInbox(h.ctx, h.id, 1, 25, nil)
				if err == nil {
					t.Fatal("link failure reported success")
				}
			} else {
				_, err := h.addr.Call[InboxStatus](h.ctx, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: page.Batch.Receipt})
				if err == nil {
					t.Fatal("ack failure reported success")
				}
			}
			h.store.ops.rename = original
			h.reload(t, true)
			status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
			if boundary == "ack_after_rename" {
				if status.ConsumedThrough != 1 {
					t.Fatal(status)
				}
				return
			}
			if status.ConsumedThrough != 0 {
				t.Fatal("failed ack advanced cursor", status)
			}
			// Resume same turn/page using the persisted references, then acknowledge.
			entry := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id}).Entries[0]
			if err := h.store.linkTaskEvent(h.id, entry, 1); err != nil {
				t.Fatal(err)
			}
			if status := confirmInboxPage(t, h, page); status.ConsumedThrough != 1 {
				t.Fatal(status)
			}
		})
	}
}

func TestInboxTaskEventDeliveryInterruptionAndIdempotence(t *testing.T) {
	for _, boundary := range []string{"body", "delivery", "header"} {
		t.Run(boundary, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			event := taskEventFixture(boundary)
			original := h.store.ops.rename
			boom := errors.New("injected interruption")
			h.store.ops.rename = func(from, to string) error {
				match := filepath.Base(to) == inboxEntryName(1)
				if boundary == "delivery" {
					match = strings.Contains(to, "/deliveries/")
				}
				if boundary == "header" {
					match = filepath.Base(to) == "inbox.json"
				}
				if err := original(from, to); err != nil {
					return err
				}
				if match {
					return boom
				}
				return nil
			}
			_, err := referenceTestService(h).receiveTaskEvent(h.ctx, h.id, event)
			if !errors.Is(err, ErrCommitUnknown) {
				t.Fatal(err)
			}
			h.store.ops.rename = original
			h.reload(t, true)
			request := ReceiveInbox{MemberIdentity: h.id, Type: InboxTaskEventType, TaskEvent: &event}
			if retry := inboxCall[InboxReceipt](t, h, request); retry.Seq != 1 {
				t.Fatal(retry)
			}
			changed := event
			changed.ResultVersion++
			request.TaskEvent = &changed
			inboxError[InboxReceipt](t, h, request, ErrConflict)
			if s := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); s.LatestSeq != 1 {
				t.Fatal(s)
			}
		})
	}
}

func TestInboxTaskEventExpiryAndSharedPrefix(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	now := time.Now().UTC()
	event := taskEventFixture("old-result")
	event.CreatedAt = now.Add(-60 * 24 * time.Hour)
	receiveTestTaskEvent(t, h, event)
	old := h.receive("old-platform")
	old.Item.CreatedAt = now.Add(-4 * 24 * time.Hour)
	inboxCall[InboxReceipt](t, h, old)
	if s := maintainTestInbox(t, h, now, true); s.ExpiredThrough != 0 || s.inputThrough() != 0 || s.SkippedEntries != 1 {
		t.Fatal("event inherited platform expiry", s)
	}
	inboxError[InboxStatus](t, h, ReclaimInbox{MemberIdentity: h.id, Through: 1}, ErrBusy)
	inboxRunning(t, h, 1, 2)
	page := readMixedTestInbox(t, h, 1)
	if len(page.Entries) != 1 || page.Entries[0].TaskEvent == nil {
		t.Fatal(page)
	}
	inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: page.Batch.Receipt})
	settleReclaimTurn(t, h)
	// The event retention clock starts at linkage; old creation time cannot erase it.
	if s := maintainTestInbox(t, h, now.Add(time.Hour), true); s.ExpiredThrough != 0 {
		t.Fatal(s)
	}
	if s := maintainTestInbox(t, h, now.Add(8*24*time.Hour), false); s.ExpiredThrough != 2 {
		t.Fatal(s)
	}
	h.reload(t, true)
	if s := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); s.ConsumedThrough != 2 || s.ExpiredThrough != 2 {
		t.Fatal(s)
	}
}

func TestInboxTaskEventAdmissionCannotBypassBlockedPlatform(t *testing.T) {
	for _, eventFirst := range []bool{false, true} {
		h := newInboxHarness(t, InboxLimits{})
		initReferenceTest(t, h)
		h.store.notificationAdapters = contractNotificationCapabilities
		event := taskEventFixture("internal")
		if eventFirst {
			receiveTestTaskEvent(t, h, event)
		}
		receiveReference(t, h, "blocked", contractSource("blocked", "blocked"))
		if !eventFirst {
			receiveTestTaskEvent(t, h, event)
		}
		status, err := referenceTestService(h).reconcileInboxAdmission(h.ctx, h.id, inboxCall[InboxStatus](t, h, InspectInbox{h.id}))
		if err != nil || !status.AutomaticBlocked || (&MemberAgent{inbox: &status}).inboxReady() {
			t.Fatal("event bypassed blocking input", status, err)
		}
		inboxRunning(t, h, 1, 2)
		if _, err := referenceTestService(h).readReferenceInbox(h.ctx, h.id, 1, 25, nil); !errors.Is(err, ErrBusy) {
			t.Fatal(err)
		}
		p := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
		inboxError[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: p.Batch.Receipt}, ErrBusy)
		if s := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); s.ConsumedThrough != 0 {
			t.Fatal(s)
		}
	}
}

func TestInboxTaskEventCapacityAndMemberReplacement(t *testing.T) {
	limits := DefaultInboxLimits()
	limits.Entries = 1
	h := newInboxHarness(t, limits)
	event := taskEventFixture("first")
	receipt := receiveTestTaskEvent(t, h, event)
	next := taskEventFixture("pending")
	if _, err := referenceTestService(h).receiveTaskEvent(h.ctx, h.id, next); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if err := h.store.loadTaskEventLocked(h.id, next); err != nil {
		t.Fatal("capacity lost saved result", err)
	}
	if retry := receiveTestTaskEvent(t, h, event); retry != receipt {
		t.Fatal(retry)
	}
	changed := event
	changed.OperationID = "other"
	if _, err := referenceTestService(h).receiveTaskEvent(h.ctx, h.id, changed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	wrong := h.id
	wrong.MembershipID = "replacement"
	if _, err := referenceTestService(h).receiveTaskEvent(h.ctx, wrong, event); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	h.stop(t)
	state, err := h.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	state.Members[0].MembershipID = wrong.MembershipID
	if err := h.store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	if _, err := SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{}); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	h.id = wrong
	h.addr, err = SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if p := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id}); len(p.Entries) != 0 {
		t.Fatal("replacement inherited old event", p)
	}
	// An internal transport cannot invent the old event under the new identity.
	inboxError[InboxReceipt](t, h, ReceiveInbox{MemberIdentity: h.id, Type: InboxTaskEventType, TaskEvent: &event}, os.ErrNotExist)
}

func TestInboxPayloadLegacyAndInvalidTypes(t *testing.T) {
	for _, mode := range []string{"legacy", "unknown", "mixed", "missing_event", "platform_with_event", "bad_event_kind", "missing_operation"} {
		t.Run(mode, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			request := h.receive("legacy")
			inboxCall[InboxReceipt](t, h, request)
			h.stop(t)
			path := filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, inboxEntryName(1))
			var entry InboxEntry
			if err := readSnapshot(path, &entry, maxPrivateFileBytes); err != nil {
				t.Fatal(err)
			}
			event := taskEventFixture(mode)
			switch mode {
			case "legacy":
				entry.Type = ""
			case "unknown":
				entry.Type = "future"
			case "mixed":
				entry.Type, entry.TaskEvent = InboxTaskEventType, &event
			case "missing_event":
				entry.Type = InboxTaskEventType
				entry.Item = notification.Item{}
			case "platform_with_event":
				entry.TaskEvent = &event
			case "bad_event_kind":
				entry.Type, entry.TaskEvent = InboxTaskEventType, &event
				entry.Item = notification.Item{}
				event.EventType = "unrecognized"
			case "missing_operation":
				entry.Type, entry.TaskEvent = InboxTaskEventType, &event
				entry.Item = notification.Item{}
				event.OperationID = ""
			}
			if err := h.store.save(path, entry, maxPrivateFileBytes); err != nil {
				t.Fatal(err)
			}
			addr, err := SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{})
			if mode == "legacy" {
				if err != nil {
					t.Fatal(err)
				}
				h.addr = addr
				if p := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id}); p.Entries[0].payloadType() != InboxPlatformNotification {
					t.Fatal(p)
				}
				if r := inboxCall[InboxReceipt](t, h, request); r.Seq != 1 {
					t.Fatal(r)
				}
			} else if !errors.Is(err, ErrInvalid) {
				t.Fatal("invalid envelope accepted", mode, err)
			}
		})
	}
}

func TestInboxTaskEventMissingEvidenceNeverResetsCursor(t *testing.T) {
	for _, missing := range []string{"event", "link", "cursor", "reclaimed_link"} {
		t.Run(missing, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			initReferenceTest(t, h)
			event := taskEventFixture("result")
			receiveTestTaskEvent(t, h, event)
			inboxRunning(t, h, 1, 1)
			page := readMixedTestInbox(t, h, 1)
			inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: page.Batch.Receipt})
			if missing == "reclaimed_link" {
				settleReclaimTurn(t, h)
				maintainTestInbox(t, h, time.Now().Add(8*24*time.Hour), true)
			}
			h.stop(t)
			path := h.store.taskEventPath(h.id, "links", taskLinkKey(1))
			if missing == "event" {
				path = h.store.taskEventPath(h.id, "events", taskEventKey(event.EventID))
			}
			if missing == "cursor" {
				path = filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, "consumption.json")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if addr, err := SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{}); err == nil {
				_ = addr.Stop(nil)
				t.Fatal("missing evidence silently hidden", missing)
			}
		})
	}
}

func TestInboxTaskEventSchedulingRespectsPauseAndBudget(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	backend := newControlledBackend()
	member := &MemberAgent{Options: SessionOptions{MemberIdentity: h.id}, Backend: backend, Store: h.store, Room: h.roomHarness.addr, MaxTurns: 1,
		Inbox: func(ctx context.Context) (InboxStatus, error) {
			return referenceTestService(h).memberInboxStatus(ctx, h.id)
		}}
	addr, err := h.spawn(member)
	if err != nil {
		t.Fatal(err)
	}
	receiveTestTaskEvent(t, h, taskEventFixture("first"))
	waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool { return s.Inbox != nil && s.Inbox.LatestSeq == 1 })
	if backend.opens.Load() != 0 {
		t.Fatal("paused member executed")
	}
	controlMember(t, h.roomHarness, addr, h.id, "run", UnreadChanged{MemberIdentity: h.id}, false, false)
	input := awaitTurn(t, h.roomHarness, backend)
	if input.Inbox == nil || input.Inbox.SnapshotSeq != 1 || input.LatestSeq != 0 {
		t.Fatal(input)
	}
	page := readMixedTestInbox(t, h, 1)
	inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: page.Batch.Receipt})
	backend.finish <- nil
	waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool { return s.Status == "budget_exhausted" })
	receiveTestTaskEvent(t, h, taskEventFixture("next"))
	waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool { return s.Inbox != nil && s.Inbox.LatestSeq == 2 })
	if s := inspectMember(t, h.ctx, addr, h.id); s.TurnBudget.Used != 1 || s.Agent.TurnNumber != 1 || s.Active {
		t.Fatal(s)
	}
}

func TestInboxMixedCLIOutputMustCompleteBeforeConfirmation(t *testing.T) {
	svc, err := startService(t.TempDir(), "mixed-inbox-cli", serviceConfig{}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := svc.close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	id := inboxRoutingMember(t, svc, "assistant", "generation-A")
	h := &inboxHarness{roomHarness: &roomHarness{store: svc.store}, id: id}
	if err := svc.receiveInbox(context.Background(), inboxTarget(id), h.receive("platform").Item); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.receiveTaskEvent(context.Background(), id, taskEventFixture("result")); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"operation_failed", "user_selection"} {
		event := taskEventFixture(kind)
		event.EventType = kind
		if _, err := svc.receiveTaskEvent(context.Background(), id, event); err != nil {
			t.Fatal(err)
		}
	}
	inboxRunning(t, h, 1, 4)
	server := httptest.NewServer(svc)
	defer server.Close()
	env := func(key string) string {
		return map[string]string{"TEAMX_ENDPOINT": server.URL, "TEAMX_ROOM_ID": id.RoomID, "TEAMX_ROOM_INSTANCE": id.RoomInstance,
			"TEAMX_SERVICE_EPOCH": svc.info.ServiceEpoch, "TEAMX_AGENT_ID": id.AgentID, "TEAMX_MEMBERSHIP_ID": id.MembershipID}[key]
	}
	var output bytes.Buffer
	if err := runCLI(context.Background(), []string{"inbox", "read", "--peek"}, &output, io.Discard, env); err != nil {
		t.Fatal(err)
	}
	var page agentReferenceInboxPage
	if err := json.Unmarshal(output.Bytes(), &page); err != nil || len(page.Entries) != 4 || page.Entries[1].TaskEvent == nil {
		t.Fatal(output.String(), err)
	}
	for _, args := range [][]string{{"inbox", "read"}, {"inbox", "read", "--peek"}} {
		if err := runCLI(context.Background(), args, inboxBrokenWriter{}, io.Discard, env); err == nil {
			t.Fatal("short output accepted")
		}
	}
	status, err := svc.memberInboxStatus(context.Background(), id)
	if err != nil || status.ConsumedThrough != 0 {
		t.Fatal("preview/failed output consumed mixed input", status, err)
	}
	output.Reset()
	if err := runCLI(context.Background(), []string{"inbox", "read", "--limit", "1"}, &output, io.Discard, env); err != nil {
		t.Fatal(err)
	}
	status, err = svc.memberInboxStatus(context.Background(), id)
	if err != nil || status.ConsumedThrough != 4 || strings.Count(output.String(), `"task_event":`) != 3 {
		t.Fatal(status, err, output.String())
	}
}

func TestInboxTaskEventReclamationInterruptedSweep(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	event := taskEventFixture("reclaim-result")
	receipt := receiveTestTaskEvent(t, h, event)
	inboxRunning(t, h, 1, 1)
	page := readMixedTestInbox(t, h, 1)
	inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: page.Batch.Receipt})
	settleReclaimTurn(t, h)
	original := h.store.ops.remove
	h.store.ops.remove = func(path string) error {
		if filepath.Base(path) == inboxEntryName(1) {
			return errors.New("interrupted cleanup")
		}
		return original(path)
	}
	inboxError[InboxStatus](t, h, MaintainInbox{MemberIdentity: h.id, Policy: notification.ExpiryPolicy{AfterDays: 3}, Now: time.Now().Add(8 * 24 * time.Hour)}, ErrCommitUnknown)
	h.store.ops.remove = original
	h.reload(t, true)
	if s := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); s.ExpiredThrough != 1 || s.RetainedEntries != 0 {
		t.Fatal(s)
	}
	if retry := inboxCall[InboxReceipt](t, h, ReceiveInbox{MemberIdentity: h.id, Type: InboxTaskEventType, TaskEvent: &event}); retry != receipt {
		t.Fatal(retry)
	}
}

func TestInboxTaskEventCapacityRetryAfterReclamation(t *testing.T) {
	limits := DefaultInboxLimits()
	limits.Entries = 1
	h := newInboxHarness(t, limits)
	initReferenceTest(t, h)
	event := taskEventFixture("first")
	first := receiveTestTaskEvent(t, h, event)
	pending := taskEventFixture("pending")
	if _, err := referenceTestService(h).receiveTaskEvent(h.ctx, h.id, pending); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	inboxRunning(t, h, 1, 1)
	page := readMixedTestInbox(t, h, 1)
	inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: page.Batch.Receipt})
	settleReclaimTurn(t, h)
	maintainTestInbox(t, h, time.Now().Add(8*24*time.Hour), false)
	if r := receiveTestTaskEvent(t, h, pending); r.Seq != 2 {
		t.Fatal(r)
	}
	if r := receiveTestTaskEvent(t, h, event); r != first {
		t.Fatal("full queue forgot reclaimed receipt", r)
	}
}
