package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
)

func TestRoomInputConfirmationRejectsReverseScan(t *testing.T) {
	h, s := unifiedReaderFixture(t, 2)
	id := s.storage.id
	for _, a := range []inputstream.ReadAttempt{
		{From: 0, Through: 3}, {From: 2, Through: 1},
		{From: 0, Through: 2, Entries: []inputstream.Location{{Partition: roomPartitionRef(h.id), Seq: 2}, {Partition: roomPartitionRef(h.id), Seq: 1}}},
	} {
		a.Kind = "room"
		a.Stream = roomStreamRef(id)
		a.Snapshot = inputstream.Snapshot{Partition: roomPartitionRef(h.id), Through: 2}
		a.Phase = "awaiting_confirmation"
		roomError[ReadConfirmation](t, h, ConfirmInputRead{MemberIdentity: id, Attempt: a}, ErrConflict)
	}
	if h.list(t).Members[0].ReadThrough != 0 {
		t.Fatal("invalid page advanced cursor")
	}
}

func TestRoomInputClearProtectsDurablePageAcrossRestart(t *testing.T) {
	h, s := unifiedReaderFixture(t, 3)
	id := s.storage.id
	args := platformArgs(1, roomStreamRef(id), "clear-race")
	_, _, release, err := s.read(h.ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	release()
	clear := ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 2}
	roomError[ClearReceipt](t, h, clear, ErrBusy)
	h.reload(t, true)
	roomError[ClearReceipt](t, h, clear, ErrBusy) // unloaded member evidence protects too
	svc := s.service
	svc.store = h.store
	svc.room = h.addr
	p, err := h.store.openPlatformInputStorage(id, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	s, err = newInputSession(h.ctx, svc, p, "fixture-session", 1, false, func(ctx context.Context) error { return ctx.Err() })
	if err != nil {
		t.Fatal(err)
	}
	deliverRoom(t, s, inputstream.ReadRequest{Turn: 1, Stream: roomStreamRef(id), ID: "clear-race"})
	if err := s.coordinate(h.ctx, func(r inputstream.ExecutionStorage) error { return r.Seal(s.execution) }); err != nil {
		t.Fatal(err)
	}
	roomCall[ClearReceipt](t, h, clear)
	h.reload(t, true)
	state, err := h.store.LoadRoom()
	if err != nil || state.ClearedThrough != 2 || state.LatestSeq != 3 || len(state.Messages) != 1 || state.Messages[0].Seq != 3 || state.Members[0].ReadThrough != 3 {
		t.Fatal(state, err)
	}
	if state.ReadProofs[id.MembershipID].Confirmation.ReadThrough != 3 {
		t.Fatal("native proof lost")
	}
}

func TestRoomLogClearRecoversAfterProcessExit(t *testing.T) {
	state := historyRoom(4, "retained message")
	state.InputLog = &roomLogRoot{Version: 1}
	h := newRoomHarness(t, state, nil)
	a := agentCaller("A", "generation-A")
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread, Limit: 1})
	confirmed := roomCall[ReadConfirmation](t, h, h.confirm(a, page.ReadReceipt))
	event := appendInputEvent(t, h, "persistent-event", a.MembershipID)
	reply := h.post(humanCaller, "persistent-reply", "retained reply")
	reply.ReplyTo = 3
	receipt := roomCall[PostReceipt](t, h, reply)
	before, err := h.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	for _, boundary := range []string{"segment_synced", "before_root", "after_root", "cleanup"} {
		t.Run(boundary, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.CopyFS(dir, os.DirFS(h.store.dir)); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRoomLogProcessExitHelper$")
			cmd.Env = append(os.Environ(), "TEAMX_ROOM_LOG_EXIT="+boundary, "TEAMX_ROOM_LOG_DIR="+dir, "TEAMX_ROOM_LOG_ID="+h.id.RoomID)
			out, err := cmd.CombinedOutput()
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 73 {
				t.Fatal(string(out), err)
			}
			store, err := OpenStore(dir, h.id.RoomID)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			after, err := store.LoadRoom()
			if err != nil {
				t.Fatal(err)
			}
			floor := uint64(0)
			if boundary == "after_root" || boundary == "cleanup" {
				floor = 2
			}
			if after.ClearedThrough != floor || after.LatestSeq != before.LatestSeq || !reflect.DeepEqual(after.Messages, before.Messages[floor:]) || !reflect.DeepEqual(after.EventReceipts, before.EventReceipts) || !reflect.DeepEqual(after.ReadProofs, before.ReadProofs) || after.ReadProofs[a.MembershipID].Confirmation != confirmed {
				t.Fatal("native facts changed", after)
			}
			if after.Messages[len(after.Messages)-1].ReplyTo != 3 || event.Seq != 5 || receipt.Seq != 6 {
				t.Fatal("reply/event identity lost")
			}
			files, err := os.ReadDir(store.roomLogDir())
			if err != nil || len(files) != len(after.InputLog.Segments) {
				t.Fatal("orphan segment remained", files, err)
			}
		})
	}
}

type exitSyncedRoomSegment struct{ snapshotFile }

func (f exitSyncedRoomSegment) Sync() error {
	if err := f.snapshotFile.Sync(); err != nil {
		return err
	}
	os.Exit(73)
	return nil
}
func TestRoomLogProcessExitHelper(t *testing.T) {
	boundary := os.Getenv("TEAMX_ROOM_LOG_EXIT")
	if boundary == "" {
		t.Skip("subprocess only")
	}
	store, err := OpenStore(os.Getenv("TEAMX_ROOM_LOG_DIR"), os.Getenv("TEAMX_ROOM_LOG_ID"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	real := store.ops
	store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		f, err := real.createTemp(dir, pattern)
		if err == nil && boundary == "segment_synced" && strings.HasPrefix(pattern, ".room-log") {
			return exitSyncedRoomSegment{f}, nil
		}
		return f, err
	}
	store.ops.rename = func(from, to string) error {
		if boundary == "before_root" && to == filepath.Join(store.dir, "room.json") {
			os.Exit(73)
		}
		return real.rename(from, to)
	}
	store.ops.syncDir = func(dir string) error {
		if boundary == "after_root" && dir == store.dir {
			os.Exit(73)
		}
		return real.syncDir(dir)
	}
	store.ops.remove = func(path string) error {
		if boundary == "cleanup" && filepath.Dir(path) == store.roomLogDir() {
			var disk RoomSnapshot
			if readSnapshot(filepath.Join(store.dir, "room.json"), &disk, maxRoomFileBytes) == nil && disk.ClearedThrough == 2 {
				os.Exit(73)
			}
		}
		return real.remove(path)
	}
	state.Messages = cloneRoomLogMessages(state.Messages[2:])
	state.ClearedThrough = 2
	state.Revision++
	for i := range state.Members {
		state.Members[i].ReadThrough = max(state.Members[i].ReadThrough, 2)
	}
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	t.Fatal("exit boundary not reached")
}

func TestRoomLogOperationCosts(t *testing.T) {
	state := historyRoom(1000, strings.Repeat("存储", 32))
	state.InputLog = &roomLogRoot{Version: 1}
	h := newRoomHarness(t, state, nil)
	stats := map[string]any{"records": 1000, "message_text_bytes": len(state.Messages[0].Text)}
	writes := 0
	real := h.store.ops.createTemp
	h.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		if strings.HasPrefix(pattern, ".room-log") {
			writes++
		}
		return real(dir, pattern)
	}
	start := time.Now()
	roomCall[PostReceipt](t, h, h.post(humanCaller, "measured-append", "new message"))
	stats["append_ms"] = float64(time.Since(start)) / float64(time.Millisecond)
	stats["append_segments_written"] = writes
	writes = 0
	a := agentCaller("A", "generation-A")
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread, Limit: 20})
	start = time.Now()
	roomCall[ReadConfirmation](t, h, h.confirm(a, page.ReadReceipt))
	stats["confirm_ms"] = float64(time.Since(start)) / float64(time.Millisecond)
	stats["confirm_segments_written"] = writes
	if writes != 0 {
		t.Fatal("pure confirmation wrote body")
	}
	writes = 0
	start = time.Now()
	roomCall[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 500})
	stats["clear_ms"] = float64(time.Since(start)) / float64(time.Millisecond)
	stats["clear_segments_written"] = writes
	start = time.Now()
	h.reload(t, true)
	stats["reopen_ms"] = float64(time.Since(start)) / float64(time.Millisecond)
	saved, err := h.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	stats["retained_records"] = len(saved.Messages)
	stats["body_bytes"] = saved.InputLog.Bytes
	b, _ := json.Marshal(stats)
	t.Log(string(b))
	if path := os.Getenv("TEAMX_ROOM_LOG_MEASURE"); path != "" {
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRoomLogCurrentFormatRetainsNativeStateWithoutUpgrade(t *testing.T) {
	state := historyRoom(3, "current format")
	state.InputLog = &roomLogRoot{Version: 1}
	h := newRoomHarness(t, state, nil)
	a := agentCaller("A", "generation-A")
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: a, Mode: ReadUnread, Limit: 1})
	roomCall[ReadConfirmation](t, h, h.confirm(a, page.ReadReceipt))
	appendInputEvent(t, h, "current-event", a.MembershipID)
	before, err := h.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(h.store.dir)); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(dir, h.id.RoomID, &inputUpgradeOptions{expected: h.id, stop: func(point string) error { t.Errorf("current format reran upgrade at %s", point); return ErrVersion }})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	after, err := store.LoadRoom()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("current native state changed", err)
	}
	if _, err := os.Stat(filepath.Join(dir, inputUpgradeJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexpected format migration", err)
	}
}

func TestRoomInputClearBetweenScanAndReservation(t *testing.T) {
	h, s := unifiedReaderFixture(t, 3)
	page := roomCall[ReadPage](t, h, ReadMessages{RoomIdentity: h.id, Caller: agentCallerIdentity(s.storage.id), Mode: ReadUnread, Peek: true, Limit: 2})
	roomCall[ClearReceipt](t, h, ClearMessages{RoomIdentity: h.id, Caller: humanCaller, ThroughSeq: 1})
	q := inputstream.ReadRequest{Turn: 1, Stream: roomStreamRef(s.storage.id), ID: "stale-scan"}
	if _, err := s.reserveRoomPage(h.ctx, q, page); !errors.Is(err, ErrReadExpired) {
		t.Fatal("stale scan reserved", err)
	}
	progress, err := s.inputState(h.ctx)
	if err != nil || len(progress.Attempts) != 0 {
		t.Fatal("stale reservation stranded coordinator", progress, err)
	}
	// A new request can immediately read the surviving suffix.
	v := deliverRoom(t, s, inputstream.ReadRequest{Turn: 1, Stream: roomStreamRef(s.storage.id), ID: "after-clear"})
	if len(v.RoomEntries) != 2 || h.list(t).Members[0].ReadThrough != 3 {
		t.Fatal(v)
	}
}
