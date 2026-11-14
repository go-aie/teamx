package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform"
)

type recoveryPreviewActor struct {
	read func(ReadMessages) (ReadPage, error)
}

func (a recoveryPreviewActor) Bindings() []actorx.Binding {
	return []actorx.Binding{actorx.Handle(func(_ actorx.Context, req ReadMessages) (ReadPage, error) { return a.read(req) })}
}

func TestDSHRecoveryPreviewTimeoutAndIdentity(t *testing.T) {
	for _, mode := range []string{"timeout", "identity", "corrupt", "cleared"} {
		t.Run(mode, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			svc := referenceTestService(h)
			inboxCall[InboxReceipt](t, h, h.receive("one"))
			inboxRunning(t, h, 1, 1)
			var err error
			svc.room, err = h.rt.Spawn(recoveryPreviewActor{read: func(req ReadMessages) (ReadPage, error) {
				switch mode {
				case "timeout":
					time.Sleep(dshRecoveryPreviewTimeout + 50*time.Millisecond)
					return ReadPage{}, nil
				case "identity":
					return ReadPage{SnapshotSeq: 123}, nil
				case "corrupt":
					return ReadPage{}, ErrInvalid
				default:
					return ReadPage{}, ErrReadExpired
				}
			}})
			if err != nil {
				t.Fatal(err)
			}
			r, err := svc.prepareDSHRecoveryContext(h.ctx, materialInput(h.id, 0, 1), 1)
			if mode == "timeout" || mode == "cleared" {
				if err != nil || !r.roomUnavailable {
					t.Fatal("optional preview did not degrade", r, err)
				}
			} else if err == nil {
				t.Fatal("invalid preview treated as optional")
			}
		})
	}
}

func TestDSHRecoveryFixedRecentSnapshot(t *testing.T) {
	h := newRoomHarness(t, historyRoom(30, "saved history"), nil)
	id := h.memberID("A", "generation-A")
	svc := &service{store: h.store, room: h.addr, info: ServiceInfo{RoomIdentity: h.id}}
	saveMaterialTurn(t, svc, id, 25, 0)
	r, err := svc.prepareDSHRecoveryContext(h.ctx, materialInput(id, 25, 0), 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.room == nil || r.room.SnapshotSeq != 25 || len(r.room.Messages) != 20 || r.room.Messages[0].Seq != 6 || r.room.Messages[19].Seq != 25 {
		t.Fatal("preview escaped fixed recent snapshot", r.room)
	}
}

func TestDSHRecoveryPreviewUsesCurrentNamesAndSimplifiedMessages(t *testing.T) {
	state := historyRoom(0, "")
	h := newRoomHarness(t, state, nil)
	id := h.memberID("A", "generation-A")
	registry := &agentNameRegistry{}
	names := map[RoomIdentity]roomAgentNames{h.id: {Members: map[string]agentDisplayName{
		"generation-A": {AgentID: "A", AgentName: "当前 助手"},
	}}}
	registry.value.Store(&names)
	svc := &service{store: h.store, room: h.addr, info: ServiceInfo{RoomIdentity: h.id}, agentNames: registry}
	roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: agentCaller("A", "generation-A"), ClientMessageID: "author-name", Text: "收到"})
	roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: "mention-name", Text: "🙂 @旧名 请处理", Mentions: []MessageMention{
		{Start: 3, End: 6, Kind: "agent", ID: "A", MembershipID: "generation-A"},
	}})
	saveMaterialTurn(t, svc, id, 2, 0)
	before := recoveryFiles(t, h.store.dir)
	recovery, err := svc.prepareDSHRecoveryContext(h.ctx, materialInput(id, 2, 0), 1)
	if err != nil {
		t.Fatal(err)
	}
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: agentCaller("A", "generation-A"), Mode: ReadLatest, Latest: dshRecoveryRoomMessages})
	expected := projectRoomRead(svc.labelMessage(page))
	if recovery.room == nil || !reflect.DeepEqual(*recovery.room, expected) || recovery.room.Messages[0].AgentName != "当前 助手" || recovery.room.Messages[1].Text != "🙂 @当前 助手 请处理" {
		t.Fatal("recovery and normal reads disagree", recovery.room, expected)
	}
	encoded := string(mustContractJSON(recovery.room))
	for _, field := range []string{`"mentions"`, `"author_membership_id"`, `"display_text"`} {
		if strings.Contains(encoded, field) {
			t.Fatal("recovery leaked internal or duplicate message fields", encoded)
		}
	}
	if !reflect.DeepEqual(before, recoveryFiles(t, h.store.dir)) {
		t.Fatal("recovery preview changed saved state")
	}
	if page.Messages[1].Text != "🙂 @旧名 请处理" || page.Messages[1].Mentions[0].AgentName != "当前 助手" {
		t.Fatal("read labels unexpectedly rewrote source text", page)
	}
}

func TestDSHRecoveryPrioritizesOnlySavedMappingsBeforePageLimit(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	for n := 1; n <= 14; n++ {
		source := referenceTestSource(fmt.Sprint(n))
		source.Reference.Object.ID.Value = fmt.Sprint(9000 + n)
		source.Reference.DiscussionID = platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: fmt.Sprint(n)}
		source.Reference.NoteID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: fmt.Sprint(100 + n)}
		receiveReference(t, h, fmt.Sprint(n), source)
	}
	inboxRunning(t, h, 1, 14)
	svc := referenceTestService(h)
	page, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, referenceTestResolver)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range page.Sources {
		if _, err := h.store.ensureDiscussionFollowup(h.id, source.Seq, 1, source.TargetRef); err != nil {
			t.Fatal(err)
		}
	}
	full, err := h.store.listFollowups(h.id, followupQuery{Limit: 25})
	if err != nil || full.Total != 28 || full.Complete {
		t.Fatal(full, err)
	}
	// Select a saved source whose unit lies beyond the ordinary first page.
	last, err := h.store.listFollowups(h.id, followupQuery{Limit: 25, Cursor: full.Next})
	if err != nil {
		t.Fatal(err)
	}
	preferred := last.Items[len(last.Items)-1].ObjectRef
	var seq uint64
	for _, source := range page.Sources {
		if source.ObjectRef == preferred {
			seq = source.Seq
		}
	}
	if seq == 0 {
		t.Fatal("missing durable source")
	}
	r, err := h.store.recoveryBusinessContext(h.id, TurnRecord{InboxStart: seq - 1, InboxSnapshotSeq: seq})
	if err != nil || r.matters.Total != 28 || len(r.matters.Items) != 25 || r.matters.Items[0].ObjectRef != preferred || r.matters.Next != "" {
		t.Fatal("saved relevance lost before limit", err)
	}
	// A notification without a mapping is never remotely resolved by preparation.
	source := referenceTestSource("unmapped")
	receiveReference(t, h, "unmapped", source)
	before := recoveryFiles(t, h.store.dir)
	if _, err := h.store.recoveryBusinessContext(h.id, TurnRecord{InboxStart: 14, InboxSnapshotSeq: 15}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, recoveryFiles(t, h.store.dir)) {
		t.Fatal("preparation linked unmapped input")
	}
}

func recoveryFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files[path] = string(body)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestDSHRecoveryReadOnlyAnsweredQuestionAndConsumedSource(t *testing.T) {
	h, svc, units, sources := conversationHarness(t, 1)
	batch := inboxCall[InboxStatus](t, h, InspectInbox{h.id}).Batch
	inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: batch.Receipt})
	u := askConversation(t, h, svc, units[0], sources[0].TargetRef, "采用方案 A？")
	postConversationReply(t, h, "answer", "采用方案 A，继续原工作", u.Questions[0].Seq)
	u, err := svc.readFollowupConversation(h.ctx, h.id, conversationCommand(u, 1))
	if err != nil {
		t.Fatal(err)
	}
	// Keep one later answer unassociated. Preparing context must not associate it.
	u = askConversation(t, h, svc, u, sources[0].TargetRef, "交付日期？")
	answer := postConversationReply(t, h, "later-answer", "周五", u.Questions[len(u.Questions)-1].Seq)
	saveMaterialTurn(t, svc, h.id, answer.Seq, 0)
	input := materialInput(h.id, answer.Seq, 0)
	before := recoveryFiles(t, h.store.dir)
	r, err := svc.prepareDSHRecoveryContext(h.ctx, input, 1)
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := r.appendTo([]dsh.ContentBlock{{Type: "text", Text: "normal input"}})
	if err != nil {
		t.Fatal(err)
	}
	text := materialText(blocks)
	if !strings.Contains(text, "周五") || !strings.Contains(text, "交付日期？") || !strings.Contains(text, "matters source") || r.matters.Total == 0 {
		t.Fatal("missing recovery evidence or repeated answered question", text)
	}
	for _, item := range r.matters.Items {
		for _, q := range item.Questions {
			if q.Text == "采用方案 A？" {
				t.Fatal("answered question projected as pending")
			}
		}
	}
	if got := recoveryFiles(t, h.store.dir); !reflect.DeepEqual(before, got) {
		t.Fatal("read-only preparation changed durable records/cursors")
	}
	status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if status.ConsumedThrough != 1 {
		t.Fatal(status)
	}
	// Source lookup still works after consumption, using existing stable mapping.
	if _, err := h.store.readFollowupSource(h.id, 1, sources[0].SourceRef); err != nil {
		t.Fatal(err)
	}
}

func TestDSHRecoveryUnknownActionsAndCorruptRecords(t *testing.T) {
	f := referenceExecuteTest(t, "discussion")
	f.result = func(int) platform.TargetActionResult { return f.outcome("unknown", "never") }
	if _, err := f.run(); err != nil {
		t.Fatal(err)
	}
	r, err := f.h.store.recoveryBusinessContext(f.h.id, TurnRecord{})
	if err != nil || r.unknownActions != 1 {
		t.Fatal(r, err)
	}
	// All action records, including records outside the unfinished first page,
	// must be validated; corruption is not converted to an empty count.
	if err := os.WriteFile(f.h.store.actionPath(f.h.id, f.q.ID), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.store.recoveryBusinessContext(f.h.id, TurnRecord{}); err == nil {
		t.Fatal("corrupt action ignored")
	}
}

func TestDSHRecoveryEmptyAndInvalidIdentity(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	r, err := h.store.recoveryBusinessContext(h.id, TurnRecord{})
	if err != nil || r.unknownActions != 0 || r.matters.Total != 0 {
		t.Fatal(r, err)
	}
	if _, err := os.Stat(h.store.followupRoot(h.id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview initialized schema", err)
	}
	other := h.id
	other.AgentID = "other"
	if _, err := h.store.recoveryBusinessContext(other, TurnRecord{}); err == nil {
		t.Fatal("foreign identity accepted")
	}
	if err := os.MkdirAll(h.store.followupRoot(h.id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.store.followupRoot(h.id), "orphan.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.recoveryBusinessContext(h.id, TurnRecord{}); err == nil {
		t.Fatal("orphan records treated as empty")
	}
}

func TestDSHRecoveryBudgetsAndMissingPreview(t *testing.T) {
	r := &dshRecoveryContext{roomUnavailable: true, unknownActions: 2, matters: agentFollowupPage{Total: 1, Items: []agentFollowupSummary{{Ref: "keep-whole", Summary: strings.Repeat("<", 40<<10)}}}}
	prompt := []dsh.ContentBlock{{Type: "text", Text: strings.Repeat("<", (1<<20)-5000)}}
	blocks, err := r.appendTo(prompt)
	if err != nil {
		t.Fatal(err)
	}
	last := blocks[len(blocks)-1].Text
	if len(last) > dshRecoveryBytes || !materialPromptFits(blocks) || !strings.Contains(last, `"matters_not_supplied":1`) || !strings.Contains(last, `"saved_unknown_action_count":2`) || !strings.Contains(last, `"room_preview_unavailable":true`) || strings.Contains(last, "keep-whole") {
		t.Fatal("incorrect budget degradation")
	}
	if len(r.matters.Items) != 1 {
		t.Fatal("render mutated snapshot")
	}
	_, err = r.appendTo([]dsh.ContentBlock{{Type: "text", Text: strings.Repeat("x", (1<<20)-1)}})
	if err == nil {
		t.Fatal("mandatory guidance silently omitted")
	}
}

func TestDSHRecoverySupplyOnOpenResumeRotationAndFailedFirstTurn(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	svc := referenceTestService(h)
	inboxCall[InboxReceipt](t, h, h.receive("one"))
	inboxRunning(t, h, 1, 1)
	calls, prompts, fail := 0, 0, true
	b := dshBackend{store: h.store, recoveryContext: func(ctx context.Context, input UnreadChanged, turn uint64) (*dshRecoveryContext, error) {
		calls++
		return svc.prepareDSHRecoveryContext(ctx, input, turn)
	}, open: func(context.Context, dsh.Options) (dshComponent, error) {
		return &adapterComponent{runPrompt: func(_ context.Context, blocks []dsh.ContentBlock) (dsh.TurnResult, error) {
			prompts++
			if fail {
				return dsh.TurnResult{}, errors.New("first prompt failed")
			}
			return dsh.TurnResult{}, nil
		}}, nil
	}}
	opts := adapterOptions(h.store, h.id)
	session, err := b.OpenSession(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	input := materialInput(h.id, 0, 1)
	if err := session.Run(h.ctx, input); err == nil || calls != 1 || prompts != 1 {
		t.Fatal(err, calls, prompts)
	}
	fail = false
	if err := session.Run(h.ctx, input); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	if err := session.Run(h.ctx, input); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	if err := session.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	b.recover = true
	session, err = b.OpenSession(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Run(h.ctx, input); err != nil || calls != 3 {
		t.Fatal(err, calls)
	}
	if err := session.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	next, err := rotateAdapterForTest(b, opts, true)
	if err != nil {
		t.Fatal(err)
	}
	session, err = next.OpenSession(h.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(h.ctx)
	if err := session.Run(h.ctx, input); err != nil || calls != 4 {
		t.Fatal(err, calls)
	}
	// Mandatory records fail before the component is invoked.
	session.(*dshSession).recoverySupplied = false
	if err := os.MkdirAll(h.store.followupRoot(h.id), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.store.followupRoot(h.id), "schema.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	before := prompts
	if err := session.Run(h.ctx, input); err == nil || prompts != before {
		t.Fatal("invalid context submitted", err)
	}
}
