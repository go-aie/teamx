package room

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform"
)

func inboxRunning(t *testing.T, h *inboxHarness, turn, end uint64) {
	t.Helper()
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: h.id, TurnNumber: turn,
		LastOfferedInboxSeq: end, Turn: &TurnRecord{Number: turn, Status: "running", InboxSnapshotSeq: end}}); err != nil {
		t.Fatal(err)
	}
}

func TestInboxConsumptionRemovalJoinsReconciliation(t *testing.T) {
	svc, err := startService(t.TempDir(), "inbox-removal", serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: "unused", dshProfiles: map[string]dshProfile{"runtime": {}}}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.close(ctx); err != nil {
			t.Error(err)
		}
	})
	entered, gate := make(chan struct{}), make(chan struct{})
	var once sync.Once
	svc.memberFactory = func(id MemberIdentity, _ RunnerConfig, _ *AgentSnapshot) actorx.Actor {
		return &MemberAgent{Options: SessionOptions{MemberIdentity: id}, Store: svc.store, Room: svc.room, Backend: newControlledBackend(),
			Inbox: func(ctx context.Context) (InboxStatus, error) {
				once.Do(func() { close(entered) })
				<-gate
				return svc.memberInboxStatus(ctx, id)
			}}
	}
	member, err := svc.add(apiRequest{AgentID: "assistant", Runner: RunnerConfig{Kind: "dsh", Profile: "runtime"}})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	original := svc.store.ops.syncDir
	var release sync.Once
	svc.store.ops.syncDir = func(dir string) error { release.Do(func() { close(gate) }); return original(dir) }
	start := time.Now()
	if _, err := svc.remove(apiRequest{AgentID: member.AgentID, MembershipID: member.MembershipID}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("removal waited on its own Inbox lock")
	}
}

func linkInboxPage(t *testing.T, h *inboxHarness, page InboxBatchPage) []MatterRecord {
	t.Helper()
	r, err := h.store.associateInboxPage(page.InboxPage, page.Batch.TurnNumber)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func confirmInboxPage(t *testing.T, h *inboxHarness, page InboxBatchPage) InboxStatus {
	t.Helper()
	return inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: page.Batch.TurnNumber, Receipt: page.Batch.Receipt})
}

func TestInboxConsumptionLinkBeforeAckAndResume(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	for i := 1; i <= 3; i++ {
		inboxCall[InboxReceipt](t, h, h.receive(fmt.Sprint(i)))
	}
	inboxRunning(t, h, 1, 2)
	p := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Limit: 1})
	if p.SnapshotSeq != 2 || p.NextAfter != 1 || !p.HasMore {
		t.Fatal(p)
	}
	if _, err := h.addr.Call[InboxStatus](h.ctx, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: p.Batch.Receipt}); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("ack without matter", err)
	}
	r := linkInboxPage(t, h, p)
	updated, err := h.store.updateMatter(h.id, MatterUpdate{Seq: 1, ID: "main", Version: 1, TurnNumber: 1, Status: "waiting_user", Summary: "Need a date", Next: "Ask user to choose the date", Evidence: "Read issue #42"})
	if err != nil {
		t.Fatal(err)
	}
	// Commit linkage, lose the acknowledgment, rebuild both actor and Store.
	h.reload(t, true)
	p2 := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Limit: 1})
	if p2.Batch.ID != p.Batch.ID || p2.Batch.Receipt != p.Batch.Receipt {
		t.Fatal("lost durable batch", p, p2)
	}
	r = linkInboxPage(t, h, p2)
	if !reflect.DeepEqual(r[0].Matters[0], updated.Matters[0]) {
		t.Fatal("association overwrote work")
	}
	if s := confirmInboxPage(t, h, p2); s.ConsumedThrough != 1 {
		t.Fatal(s)
	}
	if s := confirmInboxPage(t, h, p2); s.ConsumedThrough != 1 {
		t.Fatal("ack not idempotent")
	}
	inboxCall[InboxReceipt](t, h, h.receive("4"))
	p3 := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Limit: 1})
	if p3.SnapshotSeq != 2 || p3.NextAfter != 2 || p3.HasMore {
		t.Fatal("new arrival entered old batch", p3)
	}
	linkInboxPage(t, h, p3)
	confirmInboxPage(t, h, p3)
	inboxError[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1}, ErrConflict)
	// Reading and business completion are distinct; two concerns remain separate.
	_, err = h.store.updateMatter(h.id, MatterUpdate{Seq: 1, ID: "review", TurnNumber: 1, Status: "pending", Summary: "Separate review request"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := h.store.listMatters(h.id, r[0].ObjectKey, 0, 25)
	if err != nil || len(list.Records) != 1 || len(list.Records[0].Matters) != 2 {
		t.Fatal(list, err)
	}
	inboxRunning(t, h, 2, 4)
	if _, err = h.store.updateMatter(h.id, MatterUpdate{Seq: 1, ID: "main", Version: 2, TurnNumber: 1, Status: "pending", Summary: "late"}); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted late turn", err)
	}
	if _, err = h.store.updateMatter(h.id, MatterUpdate{Seq: 1, ID: "main", Version: 1, TurnNumber: 2, Status: "pending", Summary: "stale"}); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted stale version", err)
	}
	p4 := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 2})
	if p4.SnapshotSeq != 4 || p4.Entries[0].Seq != 3 {
		t.Fatal(p4)
	}
	linkInboxPage(t, h, p4)
	confirmInboxPage(t, h, p4)
	h.reload(t, true)
	if s := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); s.ConsumedThrough != 4 {
		t.Fatal(s)
	}
	// Neither old batches nor other memberships can acknowledge this consumer.
	inboxError[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: p.Batch.Receipt}, ErrConflict)
	other := h.id
	other.AgentID = "B"
	other.MembershipID = "generation-B"
	inboxError[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: other, TurnNumber: 2, Receipt: p4.Batch.Receipt}, ErrIdentity)
}

func TestInboxObjectIndexEvidenceBoundaries(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	x := h.receive("a").Item.Source
	x.Reference.RecipientID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: "7"}
	key := notificationObjectKey(x)
	for _, change := range []func(*platform.SourceRef){
		func(s *platform.SourceRef) { s.Instance = "https://other.example.com" },
		func(s *platform.SourceRef) { s.Reference.ProjectID.Value = "999" },
		func(s *platform.SourceRef) { s.Reference.RecipientID.Value = "8" },
		func(s *platform.SourceRef) { s.Reference.Scope = "other" },
		func(s *platform.SourceRef) { s.Reference.Object.Type = "Issue" },
		func(s *platform.SourceRef) { s.Reference.Object.ID.Namespace = "gitlab:graphql:gid" },
		func(s *platform.SourceRef) {
			s.Reference.Object.ID = platform.ExternalID{}
			s.Reference.Object.IID.Value = "9001"
		},
	} {
		y := x
		change(&y)
		if notificationObjectKey(y) == key {
			t.Fatal("crossed object identity boundary", y)
		}
	}
	y := x
	y.Instance = "https://GITLAB.EXAMPLE.COM/"
	y.Project = "renamed"
	y.ItemKey = "b"
	y.Channel = "todos"
	y.SubscriptionID = strings.Repeat("b", 32)
	y.Reference.NoteID.Value = "different"
	if notificationObjectKey(y) != key {
		t.Fatal("display/source evidence changed known object identity")
	}
	for i, s := range []platform.SourceRef{x, y} {
		msg := h.receive(fmt.Sprint(i))
		msg.Item.Source = s
		msg.Item.Source.ItemKey = msg.Item.Key
		inboxCall[InboxReceipt](t, h, msg)
	}
	inboxRunning(t, h, 1, 2)
	p := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
	r := linkInboxPage(t, h, p)
	if r[0].ObjectKey != r[1].ObjectKey {
		t.Fatal("same object did not share index")
	}
	list, err := h.store.listMatters(h.id, key, 0, 25)
	if err != nil || len(list.Records) != 2 || list.Records[0].Matters[0].Status != "pending" || list.Records[1].Matters[0].Status != "pending" {
		t.Fatal(list, err)
	}
	x.Reference.RecipientID = platform.ExternalID{}
	y = x
	y.ItemKey = "different"
	if notificationObjectKey(x) == notificationObjectKey(y) {
		t.Fatal("unknown account guessed grouping")
	}
}

type inboxBrokenWriter struct{}

func (inboxBrokenWriter) Write(p []byte) (int, error) { return len(p) / 2, io.ErrClosedPipe }

func TestInboxConsumptionCLIOutputAndIdentity(t *testing.T) {
	svc, err := startService(t.TempDir(), "inbox-cli", serviceConfig{}, func(*service, RoomSnapshot) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := svc.close(ctx); err != nil {
			t.Error(err)
		}
	})
	id := inboxRoutingMember(t, svc, "assistant", "generation-A")
	other := inboxRoutingMember(t, svc, "reviewer", "generation-B")
	h := &inboxHarness{roomHarness: &roomHarness{}, id: id}
	for i := 1; i <= 3; i++ {
		msg := h.receive(fmt.Sprint(i))
		if err := svc.receiveInbox(context.Background(), inboxTarget(id), msg.Item); err != nil {
			t.Fatal(err)
		}
	}
	h.store = svc.store
	inboxRunning(t, h, 1, 3)
	server := httptest.NewServer(svc)
	defer server.Close()
	env := func(k string) string {
		return map[string]string{"TEAMX_ENDPOINT": server.URL, "TEAMX_ROOM_ID": id.RoomID, "TEAMX_ROOM_INSTANCE": id.RoomInstance, "TEAMX_SERVICE_EPOCH": svc.info.ServiceEpoch, "TEAMX_AGENT_ID": id.AgentID, "TEAMX_MEMBERSHIP_ID": id.MembershipID}[k]
	}
	if err := runCLI(context.Background(), []string{"inbox", "read", "--limit", "1"}, inboxBrokenWriter{}, io.Discard, env); err == nil {
		t.Fatal("ignored broken output")
	}
	status, err := svc.memberInboxStatus(context.Background(), id)
	if err != nil || status.ConsumedThrough != 0 {
		t.Fatal(status, err)
	}
	var output bytes.Buffer
	if err := runCLI(context.Background(), []string{"inbox", "read", "--limit", "1"}, &output, io.Discard, env); err != nil {
		t.Fatal(err)
	}
	status, err = svc.memberInboxStatus(context.Background(), id)
	if err != nil || status.ConsumedThrough != 3 || strings.Count(output.String(), `"batch"`) != 3 {
		t.Fatal(status, err, output.String())
	}
	if status, err := svc.memberInboxStatus(context.Background(), other); err != nil || status.ConsumedThrough != 0 {
		t.Fatal("crossed members", status, err)
	}
	// A caller cannot select a different Membership; human preview never consumes.
	for _, req := range []apiRequest{
		{ServiceInfo: svc.info, Caller: humanCaller},
		{ServiceInfo: svc.info, Caller: Caller{Author: Author{Kind: "agent", ID: id.AgentID}, MembershipID: id.MembershipID}, AgentID: other.AgentID, MembershipID: other.MembershipID},
	} {
		if _, err := svc.memberInbox(context.Background(), req, "read"); !errors.Is(err, ErrIdentity) {
			t.Fatal(err)
		}
	}
}

func TestInboxConsumptionInterruptedAssociation(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	for i := 1; i <= 2; i++ {
		inboxCall[InboxReceipt](t, h, h.receive(fmt.Sprint(i)))
	}
	inboxRunning(t, h, 1, 2)
	p := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
	original := h.store.ops.createTemp
	calls := 0
	h.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		if strings.Contains(dir, string(filepath.Separator)+"matters"+string(filepath.Separator)) {
			calls++
			if calls == 2 {
				return nil, errors.New("disk unavailable")
			}
		}
		return original(dir, pattern)
	}
	if _, err := h.store.associateInboxPage(p.InboxPage, 1); err == nil {
		t.Fatal("ignored partial association")
	}
	inboxError[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: p.Batch.Receipt}, os.ErrNotExist)
	h.store.ops.createTemp = original
	h.reload(t, true)
	inboxRunning(t, h, 2, 2)
	p2 := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 2})
	if p2.Batch.ID != p.Batch.ID {
		t.Fatal("interruption replaced batch")
	}
	linkInboxPage(t, h, p2)
	confirmInboxPage(t, h, p2)
	list, err := h.store.listMatters(h.id, "", 0, 25)
	if err != nil || len(list.Records) != 2 {
		t.Fatal(list, err)
	}
}

func TestInboxConsumptionUnknownCommitAndMissingCursor(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	inboxCall[InboxReceipt](t, h, h.receive("1"))
	inboxRunning(t, h, 1, 1)
	p := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
	linkInboxPage(t, h, p)
	original := h.store.ops.syncDir
	h.store.ops.syncDir = func(string) error { return errors.New("directory fsync failed after replacement") }
	inboxError[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: p.Batch.Receipt}, ErrCommitUnknown)
	if s := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); s.ConsumedThrough != 0 || s.WriteError == "" {
		t.Fatal("installed uncertain state", s)
	}
	inboxError[InboxReceipt](t, h, h.receive("2"), ErrCommitUnknown)
	h.store.ops.syncDir = original
	h.reload(t, true)
	if s := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); s.ConsumedThrough != 1 || s.WriteError != "" {
		t.Fatal("did not reconcile committed cursor", s)
	}
	h.stop(t)
	entry := h.receive("1")
	matterPath := h.store.matterPath(InboxEntry{MemberIdentity: h.id, Seq: 1, Item: entry.Item})
	data, err := os.ReadFile(matterPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(matterPath); err != nil {
		t.Fatal(err)
	}
	if _, err := SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovered cursor with missing matter", err)
	}
	if err := os.WriteFile(matterPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, "consumption.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing cursor silently reset", err)
	}
}

func TestInboxSchedulingIndependentPauseBudgetAndFairness(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	backend := newControlledBackend()
	member := &MemberAgent{Options: SessionOptions{MemberIdentity: h.id}, Backend: backend, Store: h.store, Room: h.roomHarness.addr, MaxTurns: 2,
		Inbox: func(ctx context.Context) (InboxStatus, error) {
			return h.addr.Call[InboxStatus](ctx, InspectInbox{h.id})
		}}
	addr, err := h.spawn(member)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 26; i++ {
		inboxCall[InboxReceipt](t, h, h.receive(fmt.Sprint(i)))
	}
	// No Room notification exists; reconciliation independently discovers input.
	waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool { return s.Inbox != nil && s.Inbox.LatestSeq == 26 })
	if backend.opens.Load() != 0 {
		t.Fatal("paused member executed")
	}
	controlMember(t, h.roomHarness, addr, h.id, "run", UnreadChanged{MemberIdentity: h.id}, false, false)
	first := awaitTurn(t, h.roomHarness, backend)
	if first.Inbox == nil || first.Inbox.SnapshotSeq != 25 || first.LatestSeq != 0 {
		t.Fatal(first)
	}
	// Another member waits for the same Room slot while A handles a batch.
	idB := h.memberID("B", "generation-B")
	backendB := newControlledBackend()
	addrB, err := h.spawn(&MemberAgent{Options: SessionOptions{MemberIdentity: idB}, Backend: backendB, Store: h.store, Room: h.roomHarness.addr})
	if err != nil {
		t.Fatal(err)
	}
	roomCall[PostReceipt](t, h.roomHarness, PostMessage{RoomIdentity: h.id.RoomIdentity, Caller: humanCaller, ClientMessageID: "room-input", Text: "shared question"})
	controlMember(t, h.roomHarness, addrB, idB, "run", UnreadChanged{MemberIdentity: idB, Revision: 1, LatestSeq: 1, UnreadCount: 1, NewestUnreadSeq: 1}, false, false)
	waitMember(t, h.ctx, addrB, idB, func(s MemberStatus) bool { return s.Status == "waiting" })
	p := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
	linkInboxPage(t, h, p)
	confirmInboxPage(t, h, p)
	backend.finish <- nil
	_ = awaitTurn(t, h.roomHarness, backendB) // A's continuing backlog does not bypass B.
	if inspectMember(t, h.ctx, addr, h.id).Active {
		t.Fatal("concurrent Room turns")
	}
	backendB.finish <- nil
	second := awaitTurn(t, h.roomHarness, backend)
	if second.Inbox == nil || second.Inbox.Through != 25 || second.Inbox.SnapshotSeq != 26 || second.UnreadCount != 1 {
		t.Fatal("Room/Inbox did not share execution", second)
	}
	p = inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 2})
	linkInboxPage(t, h, p)
	confirmInboxPage(t, h, p)
	backend.finish <- nil
	waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool { return s.Status == "budget_exhausted" })
	inboxCall[InboxReceipt](t, h, h.receive("27"))
	waitMember(t, h.ctx, addr, h.id, func(s MemberStatus) bool { return s.Inbox != nil && s.Inbox.LatestSeq == 27 })
	if s := inspectMember(t, h.ctx, addr, h.id); s.TurnBudget.Used != 2 || s.Agent.TurnNumber != 2 {
		t.Fatal(s)
	}
}
