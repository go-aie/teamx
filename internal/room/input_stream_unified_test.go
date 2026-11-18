package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	frozen "github.com/go-aie/teamx/internal/inputstreamtest/frozen"
)

func unifiedReaderFixture(t *testing.T, n int) (*roomHarness, *inputSession) {
	return unifiedReaderFixtureWithTimeout(t, n, 10*time.Second)
}
func unifiedReaderFixtureWithTimeout(t *testing.T, n int, timeout time.Duration) (*roomHarness, *inputSession) {
	t.Helper()
	state := historyRoom(n, "只读检查，不要评论或合并")
	state.InputLog = &roomLogRoot{Version: 1}
	state.Members = append(state.Members, MemberState{AgentID: "B", MembershipID: "generation-B", Runner: state.Members[0].Runner})
	h := newRoomHarnessWithTimeout(t, state, nil, timeout)
	id := h.memberID("A", "generation-A")
	a := AgentSnapshot{Version: formatVersion, MemberIdentity: id, InputStateRef: "input-state.json", TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: uint64(n)}}
	if err := h.store.SaveAgent(a); err != nil {
		t.Fatal(err)
	}
	p, err := h.store.openPlatformInputStorage(id, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	svc := &service{unifiedInputs: true, inputAdapters: storageAdapters(), store: h.store, info: ServiceInfo{RoomIdentity: h.id, ServiceEpoch: "fixture"}, room: h.addr}
	s, err := newInputSession(h.ctx, svc, p, "fixture-session", 1, false, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	return h, s
}
func appendInputEvent(t *testing.T, h *roomHarness, name, target string) PostReceipt {
	t.Helper()
	targets := []string{}
	if target != "" {
		targets = append(targets, target)
	}
	return roomCall[PostReceipt](t, h, AppendRoomEvent{RoomIdentity: h.id, Event: RoomEvent{ID: name, Kind: "user_selection", SourceSeq: 1, OperationID: name, ResultVersion: 1, ResultRef: "platforms/selection/" + name, Actor: Author{Kind: "human", ID: "actual-clicker"}, Targets: targets}, Summary: "选择结果已更新", CreatedAt: time.Date(2026, 11, 17, 3, 0, 0, 0, time.UTC)})
}
func deliverRoom(t *testing.T, s *inputSession, q inputstream.ReadRequest) platformInputView {
	t.Helper()
	b, _ := json.Marshal(q)
	var out bytes.Buffer
	if err := s.writeCLI(context.Background(), b, &out, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	var v platformInputView
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
func TestUnifiedRoomTimelineNativeLogAndPagination(t *testing.T) {
	h, s := unifiedReaderFixture(t, 230)
	id := s.storage.id
	// Same shared log, independent cursors. Formal pages use the one coordinator.
	roomRef := roomStreamRef(id)
	v := deliverRoom(t, s, inputstream.ReadRequest{Turn: 1, Stream: roomRef, ID: "large", Limit: 230})
	if len(v.RoomEntries) != 100 || !v.HasMore || v.Next == "" {
		t.Fatal(v)
	}
	roomCall[PostReceipt](t, h, PostMessage{RoomIdentity: h.id, Caller: humanCaller, ClientMessageID: "later", Text: "后来消息留给新读取"})
	v = deliverRoom(t, s, inputstream.ReadRequest{Turn: 1, Cursor: v.Next})
	if len(v.RoomEntries) != 100 || v.Next == "" {
		t.Fatal(v)
	}
	v = deliverRoom(t, s, inputstream.ReadRequest{Turn: 1, Cursor: v.Next})
	if len(v.RoomEntries) != 30 || v.HasMore {
		t.Fatal(v)
	}
	state, err := s.storage.repository.State(s.execution)
	if err != nil || state.Used != 0 {
		t.Fatal(state.Used, err)
	}
	list := h.list(t)
	if list.Members[0].ReadThrough != 230 || list.Members[1].ReadThrough != 0 {
		t.Fatal(list)
	}
	v = deliverRoom(t, s, inputstream.ReadRequest{Turn: 1, Stream: roomRef, ID: "later"})
	if len(v.RoomEntries) != 1 || v.RoomEntries[0].Seq != 231 {
		t.Fatal(v)
	}
	disk, err := os.ReadFile(filepath.Join(h.store.dir, "room.json"))
	if err != nil || bytes.Contains(disk, []byte("只读检查")) {
		t.Fatal("body in mutable state", err)
	}
	saved, err := h.store.LoadRoom()
	if err != nil || len(saved.Messages) != 231 || saved.InputLog.Records != 231 {
		t.Fatal(err)
	}
	before := *saved.InputLog
	// A state-only native confirmation never rewrites historical segments.
	files := map[string]time.Time{}
	for _, seg := range before.Segments {
		p := filepath.Join(h.store.roomLogDir(), seg.Name+".jsonl")
		st, _ := os.Stat(p)
		files[p] = st.ModTime()
	}
	saved.Revision++
	if err := h.store.SaveRoom(saved); err != nil {
		t.Fatal(err)
	}
	for p, mtime := range files {
		st, _ := os.Stat(p)
		if st.ModTime() != mtime {
			t.Fatal("history rewritten by cursor/state save")
		}
	}
	h.reload(t, true)
	reloaded, err := h.store.LoadRoom()
	if err != nil || !reflect.DeepEqual(reloaded.ReadProofs, saved.ReadProofs) {
		t.Fatal("native proof lost", err)
	}
}
func TestUnifiedEventsAudienceHintsDedupAndNativeProofRecovery(t *testing.T) {
	h, s := unifiedReaderFixture(t, 1)
	id := s.storage.id
	a := appendInputEvent(t, h, "a", id.MembershipID)
	b := appendInputEvent(t, h, "b", "generation-B")
	appendInputEvent(t, h, "page-only", "")
	if again := appendInputEvent(t, h, "a", id.MembershipID); again != a {
		t.Fatal("duplicate callback appended")
	}
	c, err := s.legacyCatalog(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	directory, _, err := c.Directory()
	if err != nil || len(directory.Streams) != 1 || directory.Streams[0].Pending != 2 || !strings.Contains(directory.Streams[0].Hint, "人类") || !strings.Contains(directory.Streams[0].Hint, "交互事件") {
		t.Fatal(directory, err)
	}
	list := h.list(t)
	if list.Members[1].UnreadCount != 2 {
		t.Fatal(list)
	}
	// No consumption until full write + flush; output failure blocks other streams.
	args := platformArgs(1, roomStreamRef(id), "event-page")
	if err := s.writeCLI(h.ctx, args, io.Discard, func() error { return errors.New("flush failed") }); err == nil {
		t.Fatal("flush failure ignored")
	}
	roomError[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: a.Seq}, ErrBusy)
	state, _ := s.storage.repository.State(s.execution)
	aPage := state.Attempts[0]
	if aPage.Phase != "awaiting_confirmation" || h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal(aPage)
	}
	// Native commit succeeds; coordinator settlement is deliberately lost.
	native := roomCall[ReadConfirmation](t, h, ConfirmInputRead{MemberIdentity: id, Attempt: aPage})
	if native.ReadThrough != 4 {
		t.Fatal(native)
	}
	h.reload(t, true)
	svc := s.service
	svc.room = h.addr
	svc.store = h.store
	p, err := h.store.openPlatformInputStorage(id, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	s, err = newInputSession(h.ctx, svc, p, "fixture-session", 1, false, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	// Same on-disk coordinator and exact native proof settle without a second cursor.
	v := deliverRoom(t, s, inputstream.ReadRequest{Turn: 1, Stream: roomStreamRef(id), ID: "event-page"})
	if len(v.RoomEntries) != 2 || v.RoomEntries[1].Event == nil || v.RoomEntries[1].Author.ID != "actual-clicker" {
		t.Fatal(v)
	}
	after := h.list(t)
	if after.Members[0].ReadThrough != 4 || after.Members[1].ReadThrough != 0 {
		t.Fatal(after)
	}
	history := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: agentCaller("A", id.MembershipID), Mode: ReadAll})
	for _, m := range history.Messages {
		if m.Seq == b.Seq {
			t.Fatal("foreign private event in history")
		}
	}
	// Removing and rejoining with the same name receives no old private audience.
	roomCall[MemberInfo](t, h, RemoveMember{MemberIdentity: id})
	newID := id
	newID.MembershipID = "generation-A-new"
	roomCall[MemberInfo](t, h, AddMember{MemberIdentity: newID, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-ignore"}, Target: h.target})
	if h.list(t).Members[1].ReadThrough != 4 {
		t.Fatal("new membership inherited unread")
	}
}
func TestUnifiedRoomLogFaultsAndClearReopen(t *testing.T) {
	for _, boundary := range []string{"segment_sync", "before_root", "after_root"} {
		t.Run(boundary, func(t *testing.T) {
			state := historyRoom(1, "retained evidence")
			state.InputLog = &roomLogRoot{Version: 1}
			h := newRoomHarness(t, state, nil)
			old, err := h.store.LoadRoom()
			if err != nil {
				t.Fatal(err)
			}
			next := old
			next.Messages = append(append([]RoomMessage{}, old.Messages...), RoomMessage{Seq: 2, ClientMessageID: "new", Author: humanCaller.Author, Text: "new body", CreatedAt: time.Now()})
			next.LatestSeq = 2
			next.Revision++
			real := h.store.ops
			switch boundary {
			case "segment_sync":
				h.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
					f, e := real.createTemp(dir, pattern)
					if e == nil && strings.HasPrefix(pattern, ".room-log") {
						return &failingFile{snapshotFile: f, stage: "file sync", err: errors.New("sync")}, nil
					}
					return f, e
				}
			case "before_root":
				h.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
					if pattern == ".snapshot-*" {
						return nil, errors.New("before root")
					}
					return real.createTemp(dir, pattern)
				}
			case "after_root":
				h.store.ops.syncDir = func(dir string) error {
					if dir == h.store.dir {
						return errors.New("after root")
					}
					return real.syncDir(dir)
				}
			}
			err = h.store.SaveRoom(next)
			if err == nil {
				t.Fatal("fault not injected")
			}
			h.store.ops = real
			h.reload(t, true)
			got, e := h.store.LoadRoom()
			if e != nil {
				t.Fatal(e)
			}
			want := 1
			if boundary == "after_root" {
				want = 2
			}
			if len(got.Messages) != want {
				t.Fatal(got.LatestSeq, err)
			}
		})
	}
	state := historyRoom(220, strings.Repeat("字", 4000))
	state.InputLog = &roomLogRoot{Version: 1}
	h := newRoomHarness(t, state, nil)
	roomCall[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 150})
	h.reload(t, true)
	saved, err := h.store.LoadRoom()
	if err != nil || len(saved.Messages) != 70 || saved.Messages[0].Seq != 151 {
		t.Fatal(err)
	}
	// A committed bad line is corruption, never an empty/new Room.
	seg := saved.InputLog.Segments[0]
	path := filepath.Join(h.store.roomLogDir(), seg.Name+".jsonl")
	if err := os.WriteFile(path, []byte("bad\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.LoadRoom(); err == nil {
		t.Fatal("corruption accepted")
	}
}

func TestUnifiedRoomReopenSettlesUnpublishedBytesBeforeWrites(t *testing.T) {
	state := historyRoom(2, "saved evidence")
	state.InputLog = &roomLogRoot{Version: 1}
	h := newRoomHarness(t, state, nil)
	saved, err := h.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	seg := saved.InputLog.Segments[0]
	path := filepath.Join(h.store.roomLogDir(), seg.Name+".jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("unpublished tail\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(h.store.roomLogDir(), "unpublished.jsonl")
	if err := os.WriteFile(orphan, []byte("orphan\n"), 0600); err != nil {
		t.Fatal(err)
	}
	h.reload(t, true)
	info, err := os.Stat(path)
	if err != nil || info.Size() != int64(seg.Bytes) {
		t.Fatal(info, err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	got, err := h.store.LoadRoom()
	if err != nil || !reflect.DeepEqual(saved.Messages, got.Messages) {
		t.Fatal(got, err)
	}
}
func TestUnifiedNativeGuidanceAndDiscoveryReadOrder(t *testing.T) {
	h, s := unifiedReaderFixture(t, 1)
	appendInputEvent(t, h, "target", s.storage.id.MembershipID)
	q := fmt.Sprintf(`{"turn_number":1,"within":%q}`, roomStreamRef(s.storage.id))
	v, err := s.legacyDiscover(h.ctx, "inbox_stats", []byte(q))
	if err != nil {
		t.Fatal(err)
	}
	stats := v.(frozen.Stats)
	if stats.Guidance == nil || len(stats.Guidance.Text) > 1024 || stats.Entries != 2 {
		t.Fatal(stats)
	}
	v, err = s.legacyDiscover(h.ctx, "inbox_list", []byte(fmt.Sprintf(`{"turn_number":1,"within":%q,"filter":{"entry_kind":"event","operation_id":"target"}}`, roomStreamRef(s.storage.id))))
	if err != nil {
		t.Fatal(err)
	}
	if v.(frozen.List).Streams[0].Pending != 2 {
		t.Fatal(v)
	}
	view := deliverRoom(t, s, inputstream.ReadRequest{Turn: 1, Stream: roomStreamRef(s.storage.id), ID: "native"})
	if len(view.RoomEntries) != 2 || view.RoomEntries[0].Seq != 1 {
		t.Fatal("filter skipped earlier human restriction")
	}
	c, err := s.legacyCatalog(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	d, _, _ := c.Directory()
	if d.Pending != 0 || len(d.Streams) != 0 {
		t.Fatal("empty Room counted pending", d)
	}
}
