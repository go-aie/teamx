package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

func settleReclaimTurn(t *testing.T, h *inboxHarness) {
	t.Helper()
	agent, err := h.store.LoadAgent(h.id)
	if err != nil {
		t.Fatal(err)
	}
	agent.Turn.Status = "completed"
	if err := h.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
}

func TestInboxReclaimCapacityAndSequence(t *testing.T) {
	limits := DefaultInboxLimits()
	limits.Entries = 2
	h := newInboxHarness(t, limits)
	for _, key := range []string{"one", "two"} {
		inboxCall[InboxReceipt](t, h, h.receive(key))
	}
	inboxError[InboxReceipt](t, h, h.receive("three"), ErrCapacity)
	before := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	status := inboxCall[InboxStatus](t, h, ReclaimInbox{h.id, 1})
	info, err := os.Stat(filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, inboxEntryName(2)))
	if err != nil || status.Bytes != info.Size() || status.Bytes >= before.Bytes || status.RetainedEntries != 1 || status.LatestSeq != 2 || status.ConsumedThrough != 0 || status.ExpiredThrough != 1 {
		t.Fatal(status, info, err)
	}
	// Repeating a prefix never subtracts its bytes twice or advances consumption.
	if again := inboxCall[InboxStatus](t, h, ReclaimInbox{h.id, 1}); !reflect.DeepEqual(status, again) {
		t.Fatal(status, again)
	}
	if receipt := inboxCall[InboxReceipt](t, h, h.receive("three")); receipt.Seq != 3 {
		t.Fatal("reclaimed receipt still occupied capacity or sequence reused", receipt)
	}
	h.reload(t, true)
	page := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
	if page.ExpiredThrough != 1 || len(page.Entries) != 2 || page.Entries[0].Seq != 2 || page.Entries[1].Seq != 3 {
		t.Fatal(page)
	}
	old := uint64(3)
	inboxError[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id, SnapshotSeq: &old}, ErrInboxExpired)
	inboxCall[InboxStatus](t, h, ReclaimInbox{h.id, 3})
	h.reload(t, true)
	status = inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if status.Bytes != 0 || status.RetainedEntries != 0 || status.LatestSeq != 3 || status.ConsumedThrough != 0 {
		t.Fatal(status)
	}
	if receipt := inboxCall[InboxReceipt](t, h, h.receive("four")); receipt.Seq != 4 {
		t.Fatal("empty retained interval reset sequence", receipt)
	}
	for seq, want := range map[uint64]error{1: ErrInboxExpired, 3: ErrInboxExpired, 5: os.ErrNotExist} {
		h.store.mu.Lock()
		_, err := h.store.inboxEntryLocked(h.id, seq)
		h.store.mu.Unlock()
		if !errors.Is(err, want) {
			t.Fatalf("seq=%d: want %v, got %v", seq, want, err)
		}
	}
	// Receipt hashes and original bodies are both removed; only retained keys
	// participate in capacity and duplicate detection.
	a, _ := h.store.activeInboxes.Load(h.id.MembershipID)
	if len(a.(*AgentInbox).receipts) != 1 {
		t.Fatal("old in-memory receipt index retained")
	}
}

func TestInboxReclaimInterruptedCommitRecovery(t *testing.T) {
	for _, phase := range []string{"before_replace", "replace_not_done", "replace_done", "boundary_sync", "partial_delete", "delete_sync"} {
		t.Run(phase, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			for i := 1; i <= 3; i++ {
				inboxCall[InboxReceipt](t, h, h.receive(fmt.Sprint(i)))
			}
			// Isolate failures at the lifecycle boundary and sweep, after the
			// independent zero-consumption checkpoint has been initialized.
			if err := h.store.save(filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, "consumption.json"), inboxConsumption{Version: 1, MemberIdentity: h.id}, maxPrivateFileBytes); err != nil {
				t.Fatal(err)
			}
			real, boom := h.store.ops, errors.New("injected cleanup interruption")
			removed, synced := 0, 0
			h.store.ops.remove = func(path string) error {
				removed++
				if phase == "partial_delete" && removed == 2 {
					return boom
				}
				return real.remove(path)
			}
			switch phase {
			case "before_replace":
				h.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, boom }
			case "replace_not_done":
				h.store.ops.rename = func(string, string) error { return boom }
			case "replace_done":
				h.store.ops.rename = func(from, to string) error {
					if err := real.rename(from, to); err != nil {
						return err
					}
					return boom
				}
			case "boundary_sync", "delete_sync":
				h.store.ops.syncDir = func(dir string) error {
					synced++
					if phase == "boundary_sync" || synced == 2 {
						return boom
					}
					return real.syncDir(dir)
				}
			}
			want := error(ErrCommitUnknown)
			if phase == "before_replace" {
				want = boom
			}
			inboxError[InboxStatus](t, h, ReclaimInbox{h.id, 2}, want)
			if phase != "partial_delete" && phase != "delete_sync" && removed != 0 {
				t.Fatal("deleted before boundary durability was confirmed")
			}
			if phase != "before_replace" {
				inboxError[InboxReceipt](t, h, h.receive("blocked"), ErrCommitUnknown)
			}
			if phase == "replace_done" {
				// Once the boundary is authoritative, even a damaged leftover
				// body is garbage to sweep, never a retained record to recover.
				if err := os.WriteFile(filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, inboxEntryName(1)), []byte("interrupted old body"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			h.reload(t, true)
			status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
			expired := uint64(2)
			if phase == "before_replace" || phase == "replace_not_done" {
				expired = 0
			}
			if status.ExpiredThrough != expired || status.RetainedEntries != 3-int(expired) || status.LatestSeq != 3 || status.ConsumedThrough != 0 || status.WriteError != "" {
				t.Fatal(status)
			}
			for seq := uint64(1); seq <= expired; seq++ {
				if _, err := os.Stat(filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, inboxEntryName(seq))); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("recovery did not finish sweep", seq, err)
				}
			}
			inboxCall[InboxStatus](t, h, ReclaimInbox{h.id, 2})
			if got := inboxCall[InboxReceipt](t, h, h.receive("next")); got.Seq != 4 {
				t.Fatal(got)
			}
		})
	}
}

func TestInboxSequenceCheckpointInterruptedAfterEntry(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	real := h.store.ops.createTemp
	writes := 0
	h.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		writes++
		if writes == 2 {
			return nil, errors.New("sequence checkpoint interrupted")
		}
		return real(dir, pattern)
	}
	request := h.receive("committed-entry")
	inboxError[InboxReceipt](t, h, request, ErrCommitUnknown)
	h.reload(t, true)
	if got := inboxCall[InboxReceipt](t, h, request); got.Seq != 1 {
		t.Fatal("entry/header interruption duplicated delivery", got)
	}
}

func TestInboxReclaimMissingHeaderAfterEmptyingIsCorruption(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	inboxCall[InboxReceipt](t, h, h.receive("one"))
	inboxCall[InboxStatus](t, h, ReclaimInbox{h.id, 1})
	h.stop(t)
	if err := os.Remove(filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, "inbox.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing lifecycle header reset the monotonic sequence", err)
	}
}

func TestInboxReclaimRetainedCorruptionAndLegacyHeader(t *testing.T) {
	for _, corruption := range []string{"missing_first", "missing_last", "bad_body", "legacy_header"} {
		t.Run(corruption, func(t *testing.T) {
			h := newInboxHarness(t, InboxLimits{})
			for _, key := range []string{"one", "two", "three"} {
				inboxCall[InboxReceipt](t, h, h.receive(key))
			}
			if corruption == "legacy_header" {
				// The original v1 schema had neither lifecycle nor latest fields.
				path := filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, "inbox.json")
				if err := h.store.save(path, struct {
					Version int `json:"version"`
					MemberIdentity
					Limits InboxLimits `json:"limits"`
				}{1, h.id, DefaultInboxLimits()}, maxPrivateFileBytes); err != nil {
					t.Fatal(err)
				}
				h.reload(t, true)
				if status := inboxCall[InboxStatus](t, h, InspectInbox{h.id}); status.RetainedEntries != 3 || status.LatestSeq != 3 {
					t.Fatal(status)
				}
				return
			}
			inboxCall[InboxStatus](t, h, ReclaimInbox{h.id, 1})
			seq := uint64(2)
			if corruption == "missing_last" {
				seq = 3
			}
			path := filepath.Join(h.store.dir, "inboxes", h.id.MembershipID, inboxEntryName(seq))
			var err error
			if corruption == "bad_body" {
				err = os.WriteFile(path, []byte("broken JSON"), 0600)
			} else {
				err = os.Remove(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			h.store.mu.Lock()
			_, err = h.store.inboxEntryLocked(h.id, seq)
			h.store.mu.Unlock()
			if !errors.Is(err, ErrInvalid) || errors.Is(err, ErrInboxExpired) {
				t.Fatal("retained corruption reported as expiry/absence", err)
			}
			inboxError[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id}, ErrInvalid)
			h.stop(t)
			if _, err := SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{}); !errors.Is(err, ErrInvalid) {
				t.Fatal("retained corruption accepted on reload", err)
			}
		})
	}
}

func TestInboxReclaimProtectsTurnAndOutstandingBatch(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	for _, key := range []string{"one", "two"} {
		inboxCall[InboxReceipt](t, h, h.receive(key))
	}
	inboxRunning(t, h, 1, 2)
	inboxError[InboxStatus](t, h, ReclaimInbox{h.id, 1}, ErrBusy)
	page := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Limit: 1})
	linkInboxPage(t, h, page)
	confirmInboxPage(t, h, page)
	settleReclaimTurn(t, h)
	// Even the consumed part stays protected until the entire fixed batch settles.
	inboxError[InboxStatus](t, h, ReclaimInbox{h.id, 1}, ErrBusy)
	referenceRunning(t, h, 2, 1, 2)
	page = inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 2})
	linkInboxPage(t, h, page)
	confirmInboxPage(t, h, page)
	inboxError[InboxStatus](t, h, ReclaimInbox{h.id, 2}, ErrBusy)
	settleReclaimTurn(t, h)
	inboxCall[InboxStatus](t, h, ReclaimInbox{h.id, 2})
	h.reload(t, true)
	if agent, err := h.store.LoadAgent(h.id); err != nil || agent.LastOfferedInboxSeq != 2 {
		t.Fatal("reclaimed checkpoint could not be loaded", agent, err)
	}
}

func TestInboxReclaimSkippedPrefixBatchRecovery(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	for _, key := range []string{"one", "two", "three", "four"} {
		inboxCall[InboxReceipt](t, h, h.receive(key))
	}
	inboxCall[InboxStatus](t, h, ReclaimInbox{h.id, 2})
	referenceRunning(t, h, 1, 2, 4)
	page := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Limit: 1})
	if page.Batch.Start != 2 || page.NextAfter != 3 || page.ExpiredThrough != 2 || len(page.Entries) != 1 || page.Entries[0].Seq != 3 {
		t.Fatal(page)
	}
	h.reload(t, true)
	status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if status.ConsumedThrough != 0 || status.ExpiredThrough != 2 {
		t.Fatal("expiry fabricated Agent consumption", status)
	}
	again := inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Limit: 1})
	if !reflect.DeepEqual(page.Batch, again.Batch) {
		t.Fatal("pending batch identity lost on recovery", page.Batch, again.Batch)
	}
	linkInboxPage(t, h, again)
	if got := confirmInboxPage(t, h, again); got.ConsumedThrough != 3 || got.ExpiredThrough != 2 {
		t.Fatal(got)
	}
	again = inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1})
	linkInboxPage(t, h, again)
	confirmInboxPage(t, h, again)
	settleReclaimTurn(t, h)
	inboxCall[InboxStatus](t, h, ReclaimInbox{h.id, 4})
	h.reload(t, true)
}

func TestInboxReclaimPreservesFollowupsPolicyAndActions(t *testing.T) {
	for _, outcome := range []string{"succeeded", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			f := referenceExecuteTest(t, "discussion")
			f.result = func(int) platform.TargetActionResult { return f.outcome(outcome, "never") }
			if _, err := f.run(); err != nil {
				t.Fatal(err)
			}
			h := f.h
			sources, err := h.store.listFollowupSources(h.id, f.q.ObjectRef, 0, 25)
			if err != nil || len(sources.Items) != 1 {
				t.Fatal(sources, err)
			}
			source := sources.Items[0]
			unit := askConversation(t, h, f.svc, f.unit(t), source.TargetRef, "还需要确定后续排期吗？")
			if _, err := h.store.changePolicy(h.id, humanCaller, policyRule()); err != nil {
				t.Fatal(err)
			}
			status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
			inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: status.Batch.Receipt})
			settleReclaimTurn(t, h)
			policy, err := h.store.readAgentPolicy(h.id)
			if err != nil || len(policy.Effective) != 1 {
				t.Fatal(policy, err)
			}
			inboxCall[InboxStatus](t, h, ReclaimInbox{h.id, 1})
			reloadConversation(t, h)
			f.svc.store, f.svc.room = h.store, h.roomHarness.addr
			f.svc.inboxes[h.id.MembershipID] = h.addr
			if err := validatePlatformFollowups(h.store); err != nil {
				t.Fatal("service startup validator still requires original body", err)
			}
			after := f.unit(t)
			if !reflect.DeepEqual(unit, after) || len(after.Actions) != 1 || after.Actions[0].Status != outcome || len(after.Questions) != 1 {
				t.Fatal("business state changed with Inbox cleanup", unit, after)
			}
			if afterPolicy, err := h.store.readAgentPolicy(h.id); err != nil || !reflect.DeepEqual(policy, afterPolicy) {
				t.Fatal("policy changed with cleanup", afterPolicy, err)
			}
			if got, err := h.store.listFollowups(h.id, followupQuery{Limit: 25, IncludeCompleted: true}); err != nil || got.Total != 2 {
				t.Fatal("followup list unreadable", got, err)
			}
			if got, err := h.store.listFollowupSources(h.id, f.q.ObjectRef, 0, 25); err != nil || !reflect.DeepEqual(sources, got) {
				t.Fatal("source resolution lost", got, err)
			}
			if _, err := h.store.readFollowupSource(h.id, source.Seq, "wrong-source"); !errors.Is(err, ErrIdentity) {
				t.Fatal("expired source bypassed source identity check", err)
			}
			recovery, err := h.store.recoveryBusinessContext(h.id, TurnRecord{InboxSnapshotSeq: 1})
			unknown := 0
			if outcome == "unknown" {
				unknown = 1
			}
			if err != nil || recovery.matters.Total != 2 || recovery.unknownActions != unknown {
				t.Fatal("Session business recovery lost expired sources or actions", recovery, err)
			}
			for _, caller := range []Caller{humanCaller, {Author: Author{Kind: "agent", ID: h.id.AgentID}, MembershipID: h.id.MembershipID}} {
				req := apiRequest{Caller: caller, Followup: &followupRequest{Seq: source.Seq, SourceRef: source.SourceRef}}
				if caller.Kind == "human" {
					req.AgentID, req.MembershipID = h.id.AgentID, h.id.MembershipID
				}
				v, err := f.svc.memberFollowups(h.ctx, req, "/v1/matters/source")
				if err != nil {
					t.Fatal(err)
				}
				b, _ := json.Marshal(v)
				if !strings.Contains(string(b), `"expired"`) || strings.Contains(string(b), `"received_at"`) || strings.Contains(string(b), `source body`) {
					t.Fatal("expired source fabricated original content", string(b))
				}
			}
			if f.calls.Load() != 1 {
				t.Fatal("cleanup replayed an action")
			}
			// The expiry boundary excuses original bodies, never corrupt targets.
			if err := os.Remove(h.store.targetPath(h.id, unit.ObjectRef, unit.TargetRef)); err != nil {
				t.Fatal(err)
			}
			if _, err := h.store.listFollowups(h.id, followupQuery{Limit: 25}); err == nil || errors.Is(err, ErrInboxExpired) {
				t.Fatal("business corruption masked as expiry", err)
			}
		})
	}
}

func TestInboxReclaimLegacyContextOnlySource(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	inboxCall[InboxReceipt](t, h, ReceiveInbox{MemberIdentity: h.id, Item: ownLarkItem("own", "om_own", true)})
	inboxRunning(t, h, 1, 1)
	page, err := referenceTestService(h).readReferenceInbox(h.ctx, h.id, 1, 25, func(_ context.Context, _ MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		return lark.ResolveNotification(source, time.Now().UTC()), platform.AccessContext{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	confirmInboxPage(t, h, InboxBatchPage{Batch: page.Batch})
	settleReclaimTurn(t, h)
	// Simulate the old event schema which did not persist this validation fact.
	link, err := h.store.readFollowupSource(h.id, 1, page.Sources[0].SourceRef)
	if err != nil {
		t.Fatal(err)
	}
	link.ContextOnly = false
	if err := h.store.save(h.store.eventPath(h.id, 1), link, followupBytes); err != nil {
		t.Fatal(err)
	}
	inboxCall[InboxStatus](t, h, ReclaimInbox{h.id, 1})
	h.reload(t, true)
	if source, err := h.store.readFollowupSource(h.id, 1, link.SourceRef); err != nil || !source.ContextOnly || source.TargetRef != link.TargetRef {
		t.Fatal("context-only source required a nonexistent business unit", source, err)
	}
}

func TestInboxReclaimSerializesReadsAndStaleTurn(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	inboxCall[InboxReceipt](t, h, h.receive("one"))
	real := h.store.ops.remove
	entered, release := make(chan struct{}), make(chan struct{})
	defer onceRelease(release)()
	h.store.ops.remove = func(path string) error { close(entered); <-release; return real(path) }
	done := make(chan error, 1)
	go func() { _, err := h.addr.Call[InboxStatus](h.ctx, ReclaimInbox{h.id, 1}); done <- err }()
	<-entered
	inboxError[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id}, ErrBusy)
	inboxError[InboxReceipt](t, h, h.receive("two"), ErrBusy)
	stale := make(chan error, 1)
	go func() {
		stale <- h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: h.id, TurnNumber: 1, LastOfferedInboxSeq: 1,
			Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}})
	}()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-stale; !errors.Is(err, ErrInboxExpired) {
		t.Fatal("stale turn accepted after cleanup", err)
	}
}

func TestInboxExpiredAPIError(t *testing.T) {
	w := httptest.NewRecorder()
	writeAPIError(w, ErrInboxExpired)
	var failure apiError
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || w.Code != http.StatusConflict || failure.Code != "inbox_expired" || !strings.Contains(failure.Message, "expired") {
		t.Fatal(w.Code, failure, err)
	}
}
