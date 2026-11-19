package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func historyResult[T any](t *testing.T, s *inputSession, call, tool string, args any) (T, []byte) {
	t.Helper()
	data, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.historyCall(context.Background(), call, tool, data)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(result, &v); err != nil {
		t.Fatal(err, string(result))
	}
	return v, result
}
func historyRead(t *testing.T, s *inputSession, call, ref string, limit int) (historyBody, []byte) {
	t.Helper()
	return historyResult[historyBody](t, s, call, "history_read", historyReadRequest{Stream: ref, Limit: limit})
}
func historyNext(t *testing.T, s *inputSession, call, cursor string) (historyBody, []byte) {
	t.Helper()
	return historyResult[historyBody](t, s, call, "history_read", historyReadRequest{Cursor: cursor})
}
func historyTree(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[strings.TrimPrefix(path, root)] = sha256.Sum256(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func historySeqs(page historyBody) []uint64 {
	out := []uint64{}
	for _, entry := range page.Entries {
		out = append(out, uint64(entry.(map[string]any)["seq"].(float64)))
	}
	return out
}
func receiveHistoryItem(t *testing.T, s *inputSession, item notification.Item) string {
	t.Helper()
	r, err := s.storage.Receive(item, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.storage.repository.StreamAt(inputstream.Location{Partition: r.Partition, Seq: r.Seq})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func nextHistoryTurn(t *testing.T, h *roomHarness, s *inputSession) *inputSession {
	t.Helper()
	a, err := h.store.LoadAgent(s.storage.id)
	if err != nil {
		t.Fatal(err)
	}
	a.TurnNumber++
	a.Turn = &TurnRecord{Number: a.TurnNumber, Status: "running", SnapshotSeq: h.list(t).LatestSeq}
	if err := h.store.SaveAgent(a); err != nil {
		t.Fatal(err)
	}
	next, err := newInputSession(h.ctx, s.service, s.storage, s.execution.Generation, a.TurnNumber, false, s.validate)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestHistoryRoomVisibilityFrozenPagesAndNoWrites(t *testing.T) {
	h, s := unifiedReaderFixture(t, 5)
	roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: agentCallerIdentity(s.storage.id), ClientMessageID: "own", Text: "own saved decision"})
	private := appendInputEvent(t, h, "mine", s.storage.id.MembershipID)
	appendInputEvent(t, h, "foreign", "generation-B")
	public := appendInputEvent(t, h, "public", "")
	before := historyTree(t, h.store.dir)
	ref := roomStreamRef(s.storage.id)
	first, raw := historyRead(t, s, "recent", ref, 3)
	if !reflect.DeepEqual(historySeqs(first), []uint64{6, private.Seq, public.Seq}) || first.Next == "" {
		t.Fatal(first)
	}
	if len(first.Warnings) != 0 || stringContainsJSONKey(raw, "remaining_entries") || stringContainsJSONKey(raw, "followups") || !bytes.Contains(raw, []byte("platforms/selection/public")) {
		t.Fatal(string(raw))
	}
	if after := historyTree(t, h.store.dir); !reflect.DeepEqual(before, after) {
		t.Fatal("history modified authoritative storage")
	}
	_, replay := historyRead(t, s, "recent", ref, 3)
	if !bytes.Equal(raw, replay) {
		t.Fatal("call replay changed")
	}
	roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: "new", Text: "new saved message"})
	older, oldRaw := historyNext(t, s, "older", first.Next)
	if !reflect.DeepEqual(historySeqs(older), []uint64{3, 4, 5}) {
		t.Fatal(older)
	}
	_, oldRetry := historyNext(t, s, "older-retry", first.Next)
	if !bytes.Equal(oldRaw, oldRetry) {
		t.Fatal("cursor replay changed")
	}
	last, _ := historyNext(t, s, "last", older.Next)
	if !reflect.DeepEqual(historySeqs(last), []uint64{1, 2}) || last.Next != "" {
		t.Fatal(last)
	}
	fresh, _ := historyRead(t, s, "fresh", ref, 3)
	if !reflect.DeepEqual(historySeqs(fresh), []uint64{private.Seq, public.Seq, 10}) {
		t.Fatal(fresh)
	}
	state, err := s.inputState(h.ctx)
	if err != nil || len(state.Attempts) != 0 || state.Used != 0 || h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal(state, err)
	}
	readInputStream(t, s, "formal-room", ref, 100, true)
	if h.list(t).Members[0].ReadThrough != 10 {
		t.Fatal("formal read did not consume native timeline")
	}
	before = historyTree(t, h.store.dir)
	consumed, _ := historyRead(t, s, "consumed-room", ref, 3)
	if !reflect.DeepEqual(historySeqs(fresh), historySeqs(consumed)) {
		t.Fatal("consumed Room background disappeared", consumed)
	}
	directory, _ := historyResult[historyDirectory](t, s, "consumed-room-directory", "history_list", map[string]any{})
	if !slicesHistoryContains(directory.Streams, ref) || !reflect.DeepEqual(before, historyTree(t, h.store.dir)) {
		t.Fatal("history changed consumed Room state", directory)
	}
}

func TestHistoryConsumedSkippedPendingAndNoMatters(t *testing.T) {
	h, s := unifiedReaderFixture(t, 0)
	id := s.storage.id
	ref := receiveHistoryItem(t, s, storedGitLab(id, "first", "events", "7", "42", "consumed"))
	r, err := s.storage.Receive(storedGitLab(id, "skip", "events", "7", "42", "skipped"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.storage.run(h.ctx, "exclude", func(context.Context) error {
		return s.storage.repository.ExcludeLocal(inputstream.Location{Partition: r.Partition, Seq: r.Seq}, "test verified exclusion")
	}); err != nil {
		t.Fatal(err)
	}
	receiveHistoryItem(t, s, storedGitLab(id, "pending", "events", "7", "42", "pending"))
	lark := storedLark(id, "lark", "oc_saved", "已保存群聊", "omt", "saved without a matter")
	lark.Content.ContextOnly, lark.Content.Truncated, lark.Content.Deleted = true, true, true
	lark.Content.Attachments = []notification.Attachment{{Type: "image", Reference: "saved_image", Status: "not_supplied"}}
	larkRef := receiveHistoryItem(t, s, lark)
	s = nextHistoryTurn(t, h, s)
	// Formal delivery and a lawful local exclusion leave both bodies retained.
	_, confirm, err := s.readPage(h.ctx, inputstream.ReadRequest{Turn: s.execution.Turn, Stream: ref, ID: "formal", Limit: 1}, s.renderInputPage)
	if err != nil {
		t.Fatal(err)
	}
	if err := confirm(h.ctx); err != nil {
		t.Fatal(err)
	}
	before := historyTree(t, h.store.dir)
	page, raw := historyRead(t, s, "all-statuses", ref, 20)
	if len(page.Entries) != 3 || !bytes.Contains(raw, []byte("consumed")) || !bytes.Contains(raw, []byte("skipped")) || !bytes.Contains(raw, []byte("pending")) {
		t.Fatal(string(raw))
	}
	larkPage, larkRaw := historyRead(t, s, "no-matter", larkRef, 20)
	if len(larkPage.Entries) != 1 || !bytes.Contains(larkRaw, []byte(`"context_only":true`)) || !bytes.Contains(larkRaw, []byte(`"truncated":true`)) || !bytes.Contains(larkRaw, []byte("not_supplied")) || !bytes.Contains(larkRaw, []byte("source_ref")) {
		t.Fatal(string(larkRaw))
	}
	// Consume the last pending record: the old ref remains discoverable/readable.
	if after := historyTree(t, h.store.dir); !reflect.DeepEqual(before, after) {
		t.Fatal("history wrote matters, source links or delivery state")
	}
	_, confirm, err = s.readPage(h.ctx, inputstream.ReadRequest{Turn: s.execution.Turn, Stream: ref, ID: "finish"}, s.renderInputPage)
	if err != nil {
		t.Fatal(err)
	}
	if err := confirm(h.ctx); err != nil {
		t.Fatal(err)
	}
	d, _ := historyResult[historyDirectory](t, s, "all-retained", "history_list", map[string]any{})
	if !slicesHistoryContains(d.Streams, ref) {
		t.Fatal("consumed stream disappeared", d)
	}
	_, raw = historyRead(t, s, "consumed-again", ref, 20)
	if !bytes.Contains(raw, []byte("pending")) {
		t.Fatal(string(raw))
	}
}
func slicesHistoryContains(streams []historyStreamSummary, ref string) bool {
	for _, stream := range streams {
		if stream.Ref == ref {
			return true
		}
	}
	return false
}

func TestHistoryDirectorySnapshotIdentityAndPagination(t *testing.T) {
	h, s := unifiedReaderFixture(t, 0)
	id := s.storage.id
	refs := map[string]bool{}
	for i := range 7 {
		item := storedGitLab(id, fmt.Sprintf("row-%d", i), "events", "7", fmt.Sprint(40+i), "no preview")
		refs[receiveHistoryItem(t, s, item)] = true
	}
	same := storedGitLab(id, "other-account", "events", "7", "40", "no preview")
	same.Source.Reference.RecipientID.Value = "10"
	other := receiveHistoryItem(t, s, same)
	refs[other] = true
	first, raw := historyResult[historyDirectory](t, s, "directory", "history_list", map[string]any{"limit": 2})
	if len(first.Streams) != 2 || first.Next == "" || stringContainsJSONKey(raw, "pending_entries") || bytes.Contains(raw, []byte("no preview")) {
		t.Fatal(string(raw))
	}
	freshRef := receiveHistoryItem(t, s, storedGitLab(id, "new-stream", "events", "8", "99", "later"))
	seen := map[string]bool{}
	for page := first; ; {
		for _, stream := range page.Streams {
			if seen[stream.Ref] || !refs[stream.Ref] {
				t.Fatal("duplicate or newly appended stream", page)
			}
			seen[stream.Ref] = true
		}
		if page.Next == "" {
			break
		}
		cursor := page.Next
		next, a := historyResult[historyDirectory](t, s, "dir-"+cursor, "history_list", map[string]any{"cursor": cursor})
		_, b := historyResult[historyDirectory](t, s, "retry-"+cursor, "history_list", map[string]any{"cursor": cursor})
		if !bytes.Equal(a, b) {
			t.Fatal("directory cursor is not stable")
		}
		page = next
	}
	if len(seen) != len(refs) {
		t.Fatal(seen, refs)
	}
	fresh, _ := historyResult[historyDirectory](t, s, "new-directory", "history_list", map[string]any{"limit": 20})
	if fresh.Streams[0].Ref != freshRef || !slicesHistoryContains(fresh.Streams, other) {
		t.Fatal(fresh)
	}
	// Another member cannot use a private stream or a directory cursor.
	bID := h.memberID("B", "generation-B")
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: bID, InputStateRef: "input-state.json", TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: h.list(t).LatestSeq}}); err != nil {
		t.Fatal(err)
	}
	p, err := h.store.openPlatformInputStorage(bID, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	b, err := newInputSession(h.ctx, s.service, p, "session-B", 1, false, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(historyReadRequest{Stream: other})
	if _, err := b.historyCall(h.ctx, "foreign", "history_read", args); !errors.Is(err, errHistoryStream) {
		t.Fatal(err)
	}
	args, _ = json.Marshal(map[string]string{"cursor": first.Next})
	if _, err := b.historyCall(h.ctx, "foreign-cursor", "history_list", args); !errors.Is(err, errHistoryCursor) {
		t.Fatal(err)
	}
}

func TestHistorySharedGrantRevocationRejoinAndFrozenRecipients(t *testing.T) {
	h, a, b, p := sharedReaders(t)
	receiveSharedFixture(t, a, "one", "42")
	receiveSharedFixture(t, a, "two", "42")
	ar, br := sharedRef(t, a), sharedRef(t, b)
	private := receiveHistoryItem(t, a, storedGitLab(a.storage.id, "private", "events", "7", "42", "private same MR"))
	if ar == br || ar == private {
		t.Fatal("shared identity collapsed")
	}
	before := historyTree(t, h.store.dir)
	first, raw := historyRead(t, a, "shared", ar, 1)
	if first.Next == "" {
		t.Fatal(first)
	}
	if !reflect.DeepEqual(before, historyTree(t, h.store.dir)) || len(p.view.Load().root.Pages) != 0 {
		t.Fatal("history created shared protection or consumption state")
	}
	args, _ := json.Marshal(historyReadRequest{Stream: ar, Limit: 1})
	if _, err := b.historyCall(h.ctx, "foreign", "history_read", args); !errors.Is(err, errHistoryStream) {
		t.Fatal(err)
	}
	if err := a.service.closeSharedMember(h.ctx, a.storage.id); err != nil {
		t.Fatal(err)
	}
	if _, err := a.historyCall(h.ctx, "shared", "history_read", args); !errors.Is(err, errHistoryAccess) {
		t.Fatal("cached result escaped revoke", err, string(raw))
	}
	args, _ = json.Marshal(historyReadRequest{Cursor: first.Next})
	if _, err := a.historyCall(h.ctx, "revoked-page", "history_read", args); !errors.Is(err, errHistoryAccess) {
		t.Fatal(err)
	}
	v := p.view.Load()
	if _, err := a.service.configureSharedMembers(h.ctx, v.root.ConfigVersion, []MemberIdentity{a.storage.id, b.storage.id}); err != nil {
		t.Fatal(err)
	}
	d, _ := historyResult[historyDirectory](t, a, "rejoined", "history_list", map[string]any{})
	if slicesHistoryContains(d.Streams, ar) {
		t.Fatal("old grant rediscovered")
	}
	for _, v := range d.Streams {
		if v.Kind == "shared" {
			t.Fatal("rejoined grant inherited old bodies", d)
		}
	}
	receiveSharedFixture(t, a, "after-rejoin", "42")
	d, _ = historyResult[historyDirectory](t, a, "new-shared-directory", "history_list", map[string]any{})
	newRef := ""
	for _, stream := range d.Streams {
		if stream.Kind == "shared" {
			newRef = stream.Ref
		}
	}
	page, body := historyRead(t, a, "new-grant", newRef, 20)
	if newRef == ar || len(page.Entries) != 1 || !bytes.Contains(body, []byte("after-rejoin")) {
		t.Fatal(page, string(body))
	}
}

func TestHistoryBudgetSuffixAndActualContinuationBoundary(t *testing.T) {
	h, s := unifiedReaderFixture(t, 0)
	ref := ""
	for i := range 28 {
		item := storedGitLab(s.storage.id, fmt.Sprintf("large-%d", i), "events", "7", "42", fmt.Sprintf("record-%d:", i)+strings.Repeat("x", 15000))
		ref = receiveHistoryItem(t, s, item)
	}
	before := historyTree(t, h.store.dir)
	first, raw := historyRead(t, s, "large", ref, 10000)
	if len(raw) > historyReadBytes || len(first.Entries) != 17 || first.Next == "" || !strings.HasPrefix(first.Entries[0].(map[string]any)["text"].(string), "record-11:") {
		t.Fatal(len(raw), len(first.Entries))
	}
	second, _ := historyNext(t, s, "earlier-large", first.Next)
	if len(second.Entries) != 11 || second.Next != "" || !strings.HasPrefix(second.Entries[0].(map[string]any)["text"].(string), "record-0:") {
		t.Fatal(second)
	}
	_, replay := historyRead(t, s, "large", ref, 10000)
	if !bytes.Equal(raw, replay) {
		t.Fatal("replay changed after budget use")
	}
	third, _ := historyRead(t, s, "remaining-budget", ref, 10000)
	if len(third.Entries) < 1 || len(third.Entries) >= 17 || third.Next == "" {
		t.Fatal(third)
	}
	stop, stopRaw := historyNext(t, s, "budget-stop", third.Next)
	if stop.Stop != "history_budget" || stop.Next != "" || len(stop.Entries) != 0 {
		t.Fatal(string(stopRaw))
	}
	if !reflect.DeepEqual(before, historyTree(t, h.store.dir)) {
		t.Fatal("history changed storage")
	}
	state, err := s.inputState(h.ctx)
	if err != nil || state.Used != 0 || len(state.Attempts) != 0 {
		t.Fatal(state, err)
	}
}

func TestHistoryExpiryClearRestartAndStableStreamReference(t *testing.T) {
	h, s := unifiedReaderFixture(t, 5)
	ref := roomStreamRef(s.storage.id)
	page, _ := historyRead(t, s, "before-clear", ref, 2)
	roomCall[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 1})
	args, _ := json.Marshal(historyReadRequest{Cursor: page.Next})
	if _, err := s.historyCall(h.ctx, "expired-clear", "history_read", args); !errors.Is(err, errHistorySnapshot) {
		t.Fatal(err)
	}
	fresh, _ := historyRead(t, s, "after-clear", ref, 2)
	if len(fresh.Warnings) != 1 {
		t.Fatal(fresh)
	}
	// Reopening preserves stream identity but discards ephemeral query cursors.
	h.reload(t, true)
	s.service.room, s.service.store = h.addr, h.store
	s.service.info.ServiceEpoch = "after-restart"
	p, err := h.store.openPlatformInputStorage(s.storage.id, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := newInputSession(h.ctx, s.service, p, s.execution.Generation, 1, false, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	after, _ := historyRead(t, reopened, "restored", ref, 2)
	if !reflect.DeepEqual(historySeqs(fresh), historySeqs(after)) {
		t.Fatal(after)
	}
	args, _ = json.Marshal(historyReadRequest{Cursor: fresh.Next})
	if _, err := reopened.historyCall(h.ctx, "old-cursor", "history_read", args); !errors.Is(err, errHistoryCursor) {
		t.Fatal(err)
	}
	p.historyMu.Lock()
	now := time.Now().Add(historyIdleTTL + time.Second)
	p.history.now = func() time.Time { return now }
	p.historyMu.Unlock()
	args, _ = json.Marshal(historyReadRequest{Stream: ref, Limit: 2})
	if _, err := reopened.historyCall(h.ctx, "restored", "history_read", args); !errors.Is(err, errHistoryCursor) {
		t.Fatal("expired call silently restarted", err)
	}
}

func TestHistoryArgumentsLifecycleAndSharedExecutionBudget(t *testing.T) {
	h, s := unifiedReaderFixture(t, 3)
	invalid := []string{`{}`, `{"stream_ref":""}`, `{"stream_ref":"x","limit":0}`, `{"stream_ref":"x","limit":10001}`, `{"cursor":""}`, `{"cursor":"x","limit":1}`, `{"cursor":"x","stream_ref":"y"}`, `{"stream_ref":"x","query":"q"}`, `{"stream_ref":"x","consume":false}`, `{"stream_ref":null}`, `{"stream_ref":"x","stream_ref":"y"}`}
	for _, args := range invalid {
		if _, err := s.historyCall(h.ctx, "invalid", "history_read", []byte(args)); err == nil {
			t.Fatal(args)
		}
	}
	page, _ := historyRead(t, s, "first", roomStreamRef(s.storage.id), 1)
	args, _ := json.Marshal(map[string]string{"cursor": page.Next})
	if _, err := s.historyCall(h.ctx, "wrong-tool", "history_list", args); !errors.Is(err, errHistoryCursor) {
		t.Fatal(err)
	}
	other, err := newInputSession(h.ctx, s.service, s.storage, s.execution.Generation, 1, false, s.validate)
	if err != nil {
		t.Fatal(err)
	}
	before := s.storage.history.bytes
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	args, _ = json.Marshal(historyReadRequest{Stream: roomStreamRef(s.storage.id), Limit: 1})
	for range 8 {
		wg.Go(func() { _, err := other.historyCall(h.ctx, "first", "history_read", args); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if s.storage.history.bytes != before {
		t.Fatal("same call recharged across sessions")
	}
	args, _ = json.Marshal(historyReadRequest{Stream: roomStreamRef(s.storage.id), Limit: 2})
	if _, err := s.historyCall(h.ctx, "first", "history_read", args); !errors.Is(err, inputstream.ErrConflict) {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(h.ctx)
	cancel()
	if _, err := s.historyCall(cancelled, "cancelled", "history_list", []byte(`{}`)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	s.service.closing.Store(true)
	if _, err := s.historyCall(h.ctx, "closed", "history_read", args); !errors.Is(err, ErrClosing) {
		t.Fatal(err)
	}
	if len(h.store.roomHistoryPins) != 0 {
		t.Fatal("native pin leaked")
	}
}

func TestHistoryCorruptPublishedBodyIsAnError(t *testing.T) {
	h, s := unifiedReaderFixture(t, 0)
	ref := receiveHistoryItem(t, s, storedGitLab(s.storage.id, "corrupt", "events", "7", "42", "saved"))
	files, err := filepath.Glob(filepath.Join(h.store.dir, "inboxes", s.storage.id.MembershipID, "gitlab", "records", "*"))
	if err != nil || len(files) != 1 {
		t.Fatal(files, err)
	}
	if err := os.WriteFile(files[0], []byte("bad\n"), 0600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(historyReadRequest{Stream: ref})
	if _, err := s.historyCall(h.ctx, "corrupt", "history_read", args); !errors.Is(err, inputstream.ErrCorrupt) {
		t.Fatal("corrupt body became an empty history", err)
	}
}

func TestHistoryReclamationInvalidatesReadAndDirectorySnapshots(t *testing.T) {
	h, s := unifiedReaderFixture(t, 0)
	ref := ""
	locations := []inputstream.Location{}
	for i := range 3 {
		r, err := s.storage.Receive(storedGitLab(s.storage.id, fmt.Sprint(i), "events", "7", "42", fmt.Sprintf("saved-%d", i)), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		l := inputstream.Location{Partition: r.Partition, Seq: r.Seq}
		locations = append(locations, l)
		ref, err = s.storage.repository.StreamAt(l)
		if err != nil {
			t.Fatal(err)
		}
	}
	receiveHistoryItem(t, s, storedLark(s.storage.id, "lark", "oc_saved", "群聊", "omt", "saved lark"))
	page, _ := historyRead(t, s, "before-reclaim", ref, 1)
	directory, _ := historyResult[historyDirectory](t, s, "before-directory", "history_list", map[string]any{"limit": 1})
	if directory.Next == "" {
		t.Fatal(directory)
	}
	if _, err := s.storage.repository.ExcludeCandidates("gitlab", locations, "verified exclusion"); err != nil {
		t.Fatal(err)
	}
	facts := inputstream.ReleaseSet{Revision: "released", Records: map[uint64]inputstream.ReleaseFacts{}}
	for _, l := range locations[:2] {
		facts.Records[l.Seq] = inputstream.ReleaseFacts{Revision: "verified", SourceConfirmed: true, BusinessReleased: true, ReceiptReleased: true}
	}
	facts, err := s.storage.repository.ConsumptionRelease("gitlab", facts)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := s.storage.repository.PrepareReclaim("gitlab", facts, time.Now())
	if err != nil || plan.Through != 2 {
		t.Fatal(plan, err)
	}
	if _, err := s.storage.repository.CommitReclaim("gitlab", plan, facts); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct{ call, tool, cursor string }{{"old-read", "history_read", page.Next}, {"old-directory", "history_list", directory.Next}} {
		args, _ := json.Marshal(map[string]string{"cursor": q.cursor})
		if _, err := s.historyCall(h.ctx, q.call, q.tool, args); !errors.Is(err, errHistorySnapshot) {
			t.Fatal(q, err)
		}
	}
	fresh, _ := historyRead(t, s, "after-reclaim", ref, 20)
	if len(fresh.Entries) != 1 || len(fresh.Warnings) != 1 {
		t.Fatal(fresh)
	}
	// Retention pins are held only for an actual read, not by a cached query.
	if len(h.store.roomHistoryPins) != 0 {
		t.Fatal("room pin leaked")
	}
}

func TestHistorySnapshotBoundAndNativePinLifetime(t *testing.T) {
	h, s := unifiedReaderFixture(t, 4)
	ref := roomStreamRef(s.storage.id)
	first, _ := historyRead(t, s, "old", ref, 1)
	for i := range historySnapshotLimit {
		historyRead(t, s, fmt.Sprintf("fresh-%d", i), ref, 1)
	}
	args, _ := json.Marshal(historyReadRequest{Cursor: first.Next})
	if _, err := s.historyCall(h.ctx, "evicted", "history_read", args); !errors.Is(err, errHistoryCursor) {
		t.Fatal(err)
	}
	args, _ = json.Marshal(historyReadRequest{Stream: ref, Limit: 1})
	if _, err := s.historyCall(h.ctx, "old", "history_read", args); !errors.Is(err, errHistoryCursor) {
		t.Fatal("evicted call silently refreshed", err)
	}
	_, _, release, err := s.pinHistoryRoom(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	roomError[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 1}, ErrBusy)
	release()
	release()
	roomCall[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 1})
	if len(h.store.roomHistoryPins) != 0 {
		t.Fatal("native read pin survived release")
	}
	// A later generation can reuse the Room ref, with new query state and budget.
	a, err := h.store.LoadAgent(s.storage.id)
	if err != nil {
		t.Fatal(err)
	}
	a.TurnNumber++
	a.Turn = &TurnRecord{Number: a.TurnNumber, Status: "running", SnapshotSeq: 4}
	if err := h.store.SaveAgent(a); err != nil {
		t.Fatal(err)
	}
	rotated, err := newInputSession(h.ctx, s.service, s.storage, "next-session-generation", a.TurnNumber, false, s.validate)
	if err != nil {
		t.Fatal(err)
	}
	restored, _ := historyRead(t, rotated, "new-generation", ref, 20)
	if !reflect.DeepEqual(historySeqs(restored), []uint64{2, 3, 4}) {
		t.Fatal(restored)
	}
	h.store.finishMCPTurn(rotated.storage.id, rotated.execution.Turn)
	if s.storage.history != nil {
		t.Fatal("finished turn retained query bytes")
	}
	if _, err := rotated.historyCall(h.ctx, "after-finish", "history_list", []byte(`{}`)); !errors.Is(err, ErrClosing) {
		t.Fatal(err)
	}
}

func TestHistorySchemasAndProductionReadOnlyDefinitions(t *testing.T) {
	definitions := historyToolDefinitions()
	if len(definitions) != 2 {
		t.Fatal(definitions)
	}
	production := (&serverAccess{}).inputMCPFor(false)
	for _, tool := range definitions {
		if tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint {
			t.Fatal("missing read-only declaration", tool.Name)
		}
		if production.server.ListTools()[tool.Name] == nil {
			t.Fatal("history missing from production transport")
		}
		raw := historyListSchema()
		if tool.Name == "history_read" {
			raw = historyReadSchema()
		}
		var schemaDoc any
		if err := json.Unmarshal(raw, &schemaDoc); err != nil {
			t.Fatal(err)
		}
		compiler := jsonschema.NewCompiler()
		url := "https://teamx.test/" + tool.Name
		if err := compiler.AddResource(url, schemaDoc); err != nil {
			t.Fatal(err)
		}
		schema, err := compiler.Compile(url)
		if err != nil {
			t.Fatal(err)
		}
		valid := []string{`{}`, `{"limit":20}`, `{"cursor":"opaque"}`}
		invalid := []string{`{"limit":0}`, `{"limit":21}`, `{"cursor":""}`, `{"cursor":"x","limit":1}`, `{"query":"x"}`, `{"scope":"x"}`}
		if tool.Name == "history_read" {
			valid = []string{`{"stream_ref":"stream_saved"}`, `{"stream_ref":"stream_saved","limit":10000}`, `{"cursor":"opaque"}`}
			invalid = append(invalid, `{}`, `{"stream_ref":"x","limit":0}`, `{"stream_ref":"x","limit":10001}`, `{"stream_ref":"x","cursor":"c"}`, `{"stream_ref":"x","query":"q"}`, `{"stream_ref":"x","consume":false}`)
		}
		for _, args := range valid {
			var value any
			_ = json.Unmarshal([]byte(args), &value)
			if err := schema.Validate(value); err != nil {
				t.Fatal(tool.Name, args, err)
			}
		}
		for _, args := range invalid {
			var value any
			_ = json.Unmarshal([]byte(args), &value)
			if err := schema.Validate(value); err == nil {
				t.Fatal("schema accepted", tool.Name, args)
			}
		}
	}
}

func TestHistoryEmptyDirectoryAndFallbackScope(t *testing.T) {
	h, s := unifiedReaderFixture(t, 0)
	empty, _ := historyResult[historyDirectory](t, s, "empty", "history_list", map[string]any{})
	if len(empty.Streams) != 0 || empty.Next != "" || empty.Stop != "" {
		t.Fatal(empty)
	}
	item := storedGitLab(s.storage.id, "fallback", "events", "7", "42", "saved source-wide record")
	item.Source.Reference.Object = platform.ObjectReference{}
	ref := receiveHistoryItem(t, s, item)
	directory, _ := historyResult[historyDirectory](t, s, "fallback-directory", "history_list", map[string]any{})
	if len(directory.Streams) != 1 || directory.Streams[0].Ref != ref || directory.Streams[0].Hint == "" {
		t.Fatal("fallback scope hidden", directory)
	}
	before := historyTree(t, h.store.dir)
	page, body := historyRead(t, s, "fallback-read", ref, 20)
	if len(page.Entries) != 1 || !bytes.Contains(body, []byte("source-wide")) || !reflect.DeepEqual(before, historyTree(t, h.store.dir)) {
		t.Fatal(page)
	}
}

func TestHistoryNativeDirectoryUsesPublishedMetadata(t *testing.T) {
	h, s := unifiedReaderFixture(t, 1)
	state, err := h.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.store.roomLogDir(), state.InputLog.Segments[0].Name+".jsonl")
	if err := os.WriteFile(path, []byte("bad\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Selection itself uses only published visibility metadata. The unchanged
	// execution gate may separately reject a corrupt native log.
	streams, err := s.captureHistory(h.ctx, "")
	if err != nil || len(streams) != 1 || streams[0].summary.Ref != roomStreamRef(s.storage.id) {
		t.Fatal(streams, err)
	}
	args, _ := json.Marshal(historyReadRequest{Stream: roomStreamRef(s.storage.id)})
	if _, err := s.historyCall(h.ctx, "native-body", "history_read", args); !errors.Is(err, inputstream.ErrCorrupt) {
		t.Fatal("native corruption became empty history", err)
	}
	if len(h.store.roomHistoryPins) != 0 {
		t.Fatal("corruption leaked a native pin")
	}
}
