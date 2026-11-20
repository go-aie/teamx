package room

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

func humanReadFixture(t *testing.T, n int) (*Store, RoomSnapshot, *humanReadStore) {
	t.Helper()
	s := openTestStore(t, t.TempDir())
	room := historyRoom(n, "@owner 请看 @owner")
	for i := range room.Messages {
		room.Messages[i].Author = Author{Kind: "agent", ID: "A"}
	}
	if err := s.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	return s, room, newHumanReadStore(s.dir, room, "owner")
}

func humanReadConfirm(t *testing.T, h *humanReadStore, s *Store, latest uint64, seen ...uint64) humanReadState {
	t.Helper()
	v, err := h.update(s, humanReadDelta{SnapshotSeq: &latest, MentionSeenSeqs: seen}, "店长")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func humanReadOpen(t *testing.T, h *humanReadStore, s *Store, latest uint64) humanReadState {
	t.Helper()
	v, err := h.update(s, humanReadDelta{SnapshotSeq: &latest, OpenedThrough: &latest}, "店长")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestHumanReadUnionGapsCountsAndNoop(t *testing.T) {
	s, room, h := humanReadFixture(t, 100)
	room.Messages[0].Author = Author{Kind: "human", ID: "owner"}
	room.Messages[1].Text = "普通消息"
	room.Messages[2].Author = Author{Kind: "human", ID: "someone-else"}
	room.Messages[3].Text = "@店长"
	if err := s.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(s.dir, "room.json"))
	v, err := h.update(s, humanReadDelta{}, "店长")
	if err != nil || v.Revision != 0 || v.UnreadCount != 99 || v.MentionCount != 98 || len(v.MentionSeenSeqs) != 0 {
		t.Fatal(v, err)
	}
	if _, err := os.Stat(h.path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("query created file", err)
	}
	var wg sync.WaitGroup
	for _, seq := range []uint64{100, 50, 100, 3} {
		wg.Go(func() {
			latest := uint64(100)
			if _, err := h.update(s, humanReadDelta{SnapshotSeq: &latest, MentionSeenSeqs: []uint64{seq}}, "店长"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	v = humanReadConfirm(t, h, s, 100, 100, 50, 3, 1)
	if !reflect.DeepEqual(v.MentionSeenSeqs, []uint64{3, 50, 100}) || v.Revision != 3 || v.UnreadCount != 99 || v.MentionCount != 95 {
		t.Fatal(v)
	}
	// A duplicate must not write, including when the incoming boundary grows.
	h.ops.createTemp = func(string, string) (snapshotFile, error) { t.Error("noop wrote"); return nil, io.ErrClosedPipe }
	v = humanReadConfirm(t, h, s, 100, 1, 3, 3, 100)
	if v.Revision != 3 || slices.Contains(v.MentionSeenSeqs, 21) {
		t.Fatal(v)
	}
	after, _ := os.ReadFile(filepath.Join(s.dir, "room.json"))
	if string(before) != string(after) {
		t.Fatal("reading changed business state or Agent cursors")
	}
}

func TestHumanReadOpenedAndMentionsMergeIndependently(t *testing.T) {
	s, room, h := humanReadFixture(t, 100)
	for i := range room.Messages {
		room.Messages[i].Text = "普通消息"
	}
	room.Messages[0].Author = Author{Kind: "human", ID: "owner"}
	for _, seq := range []uint64{10, 70, 95} {
		room.Messages[seq-1].Text = "@owner @店长"
	}
	if err := s.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	initial, err := h.update(s, humanReadDelta{IncludeNewMessages: true}, "店长")
	if err != nil || initial.Version != 2 || initial.UnreadCount != 99 || initial.MentionCount != 3 ||
		len(*initial.NewMessageSeqs) != 99 || !reflect.DeepEqual(initial.UnseenMentionSeqs, []uint64{10, 70, 95}) {
		t.Fatal(initial, err)
	}
	var wg sync.WaitGroup
	for _, opened := range []uint64{50, 80, 60} {
		wg.Go(func() {
			snapshot := uint64(100)
			if _, err := h.update(s, humanReadDelta{SnapshotSeq: &snapshot, OpenedThrough: &opened, MentionSeenSeqs: []uint64{1, 2, 10, 70}}, "店长"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	v, err := h.update(s, humanReadDelta{IncludeNewMessages: true}, "店长")
	if err != nil || v.OpenedThrough != 80 || v.UnreadCount != 20 || v.MentionCount != 1 ||
		!reflect.DeepEqual(v.MentionSeenSeqs, []uint64{10, 70}) || len(*v.NewMessageSeqs) != 20 {
		t.Fatal(v, err)
	}
	_, err, catalog := readHumanReadCatalog(s.dir, room.RoomIdentity, "owner", "店长")
	if err != nil || catalog.UnreadCount != v.UnreadCount || catalog.MentionCount != v.MentionCount || catalog.Revision != v.Revision {
		t.Fatal(catalog, v, err)
	}
	v = humanReadOpen(t, h, s, 100)
	if v.UnreadCount != 1 || v.MentionCount != 1 || !reflect.DeepEqual(v.UnseenMentionSeqs, []uint64{95}) {
		t.Fatal("opening cleared a mention", v)
	}
	v = humanReadConfirm(t, h, s, 100, 95)
	if v.UnreadCount != 0 || v.MentionCount != 0 {
		t.Fatal(v)
	}
	revision := v.Revision
	h.ops.createTemp = func(string, string) (snapshotFile, error) { t.Error("duplicate wrote"); return nil, io.ErrClosedPipe }
	v = humanReadOpen(t, h, s, 80)
	v = humanReadConfirm(t, h, s, 100, 1, 2, 95, 95)
	if v.OpenedThrough != 100 || v.Revision != revision {
		t.Fatal(v)
	}
}

func TestHumanReadOpeningUsesSubmittedSnapshot(t *testing.T) {
	s, room, h := humanReadFixture(t, 101)
	for i := range room.Messages {
		room.Messages[i].Text = "普通消息"
	}
	room.Messages[9].Text = "@owner"
	if err := s.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	v := humanReadOpen(t, h, s, 100)
	if v.OpenedThrough != 100 || v.SnapshotSeq != 101 || v.UnreadCount != 2 || v.MentionCount != 1 || h.file.ObservedLatestSeq != 100 {
		t.Fatal("K confirmation covered K+1 or cleared mentions", v, h.file)
	}
	v = humanReadConfirm(t, h, s, 100, 10)
	if v.UnreadCount != 1 || v.MentionCount != 0 || v.OpenedThrough != 100 {
		t.Fatal(v)
	}
	snapshot, opened := uint64(100), uint64(101)
	if _, err := h.update(s, humanReadDelta{SnapshotSeq: &snapshot, OpenedThrough: &opened}, ""); !errors.Is(err, ErrRequest) {
		t.Fatal("opened position exceeded its snapshot", err)
	}
}

func TestHumanReadLegacyProjectionAndFirstSave(t *testing.T) {
	s, room, h := humanReadFixture(t, 6)
	room.Messages[1].Text = "普通消息"
	room.Messages[2].Author = Author{Kind: "human", ID: "owner"}
	room.Messages[3].Text = "@店长"
	room.ClearedThrough, room.Messages = 1, room.Messages[1:]
	room.Members[0].ReadThrough = 1
	if err := s.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	legacy := humanReadLegacyFile{Version: 1, RoomIdentity: room.RoomIdentity, OperatorID: "owner", Revision: 7,
		ObservedLatestSeq: 6, SeenSeqs: []uint64{1, 2, 3, 4, 6}}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(h.path(), data, 0600); err != nil {
		t.Fatal(err)
	}
	h = newHumanReadStore(s.dir, room, "owner")
	v, err := h.update(s, humanReadDelta{IncludeNewMessages: true}, "店长")
	if err != nil || v.Version != 2 || v.OpenedThrough != 1 || v.Revision != 7 ||
		v.UnreadCount != 4 || v.MentionCount != 1 || !reflect.DeepEqual(v.MentionSeenSeqs, []uint64{4, 6}) ||
		!reflect.DeepEqual(*v.NewMessageSeqs, []uint64{2, 4, 5, 6}) {
		t.Fatal(v, err)
	}
	_, err, catalog := readHumanReadCatalog(s.dir, room.RoomIdentity, "owner", "店长")
	if err != nil || catalog.UnreadCount != v.UnreadCount || catalog.MentionCount != v.MentionCount {
		t.Fatal(catalog, v, err)
	}
	after, _ := os.ReadFile(h.path())
	if string(after) != string(data) {
		t.Fatal("query or catalog wrote legacy projection")
	}
	// Even an otherwise redundant valid confirmation performs the one-time save.
	v = humanReadConfirm(t, h, s, 6, 6)
	var saved humanReadFile
	if err := readSnapshot(h.path(), &saved, maxPrivateFileBytes); err != nil || saved.Version != 2 || saved.SeenSeqs != nil ||
		saved.OpenedThrough != 1 || v.Revision != 8 || !reflect.DeepEqual(saved.MentionSeenSeqs, []uint64{4, 6}) {
		t.Fatal(saved, v, err)
	}
	h = newHumanReadStore(s.dir, room, "owner")
	v = humanReadOpen(t, h, s, 6)
	if v.UnreadCount != 1 || !reflect.DeepEqual(v.UnseenMentionSeqs, []uint64{5}) {
		t.Fatal(v)
	}
}

func TestHumanReadInvalidLegacyFileIsNeverConverted(t *testing.T) {
	for _, kind := range []string{"duplicate", "null", "future", "observed", "identity", "opened-field", "mention-field"} {
		t.Run(kind, func(t *testing.T) {
			s, room, h := humanReadFixture(t, 3)
			old := humanReadLegacyFile{Version: 1, RoomIdentity: room.RoomIdentity, OperatorID: "owner",
				Revision: 1, ObservedLatestSeq: 3, SeenSeqs: []uint64{1, 2}}
			switch kind {
			case "duplicate":
				old.SeenSeqs = []uint64{1, 1}
			case "null":
				old.SeenSeqs = nil
			case "future":
				old.SeenSeqs = []uint64{4}
			case "observed":
				old.ObservedLatestSeq = 4
			case "identity":
				old.RoomInstance = "old-instance"
			}
			data, _ := json.Marshal(old)
			if kind == "opened-field" {
				data = append(data[:len(data)-1], []byte(`,"opened_through":0}`)...)
			}
			if kind == "mention-field" {
				data = append(data[:len(data)-1], []byte(`,"mention_seen_seqs":null}`)...)
			}
			if err := os.WriteFile(h.path(), data, 0600); err != nil {
				t.Fatal(err)
			}
			h = newHumanReadStore(s.dir, room, "owner")
			latest := room.LatestSeq
			if _, err := h.update(s, humanReadDelta{SnapshotSeq: &latest, OpenedThrough: &latest}, "店长"); err != errHumanReadUnavailable {
				t.Fatal(err)
			}
			_, err, summary := readHumanReadCatalog(s.dir, room.RoomIdentity, "owner", "店长")
			after, _ := os.ReadFile(h.path())
			if err != nil || summary.Status != "unavailable" || string(data) != string(after) {
				t.Fatal("invalid legacy file was converted/reset", summary, err)
			}
		})
	}
}

func TestHumanReadClearRestartAndObservationBounds(t *testing.T) {
	s, room, h := humanReadFixture(t, 6)
	humanReadConfirm(t, h, s, 6, 1, 5)
	for _, tc := range []struct {
		snapshot uint64
		seqs     []uint64
	}{{7, []uint64{2}}, {4, []uint64{5}}, {6, []uint64{0}}, {6, make([]uint64, 101)}} {
		if _, err := h.update(s, humanReadDelta{SnapshotSeq: &tc.snapshot, MentionSeenSeqs: tc.seqs}, ""); !errors.Is(err, ErrRequest) {
			t.Fatal(tc, err)
		}
	}
	h = newHumanReadStore(s.dir, room, "owner")
	v := humanReadConfirm(t, h, s, 6, 5)
	if v.Revision != 1 || !reflect.DeepEqual(v.MentionSeenSeqs, []uint64{1, 5}) {
		t.Fatal(v)
	}
	room.ClearedThrough, room.Messages = 4, room.Messages[4:]
	room.Members[0].ReadThrough = 4
	room.Revision++
	if err := s.SaveRoom(room); err != nil {
		t.Fatal(err)
	}
	v, err := h.update(s, humanReadDelta{}, "")
	if err != nil || !reflect.DeepEqual(v.MentionSeenSeqs, []uint64{5}) || v.UnreadCount != 2 {
		t.Fatal(v, err)
	}
	// Old cleared increments are ignored, not treated as a new read watermark.
	v = humanReadConfirm(t, h, s, 4, 1, 2)
	if v.Revision != 1 {
		t.Fatal(v)
	}
	v = humanReadConfirm(t, h, s, 6, 6)
	var saved humanReadFile
	if err := readSnapshot(h.path(), &saved, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.MentionSeenSeqs, []uint64{5, 6}) || saved.ObservedLatestSeq != 6 || v.UnreadCount != 2 {
		t.Fatal(saved, v)
	}
}

func TestHumanReadBoundedHistory(t *testing.T) {
	s, room, h := humanReadFixture(t, maxMessages)
	humanReadOpen(t, h, s, room.LatestSeq)
	for start := uint64(1); start <= maxMessages; start += 100 {
		seqs := make([]uint64, 100)
		for i := range seqs {
			seqs[i] = start + uint64(i)
		}
		humanReadConfirm(t, h, s, room.LatestSeq, seqs...)
	}
	v, err := h.update(s, humanReadDelta{IncludeNewMessages: true}, "")
	info, statErr := os.Stat(h.path())
	if err != nil || statErr != nil || len(v.MentionSeenSeqs) != maxMessages || len(*v.NewMessageSeqs) != 0 ||
		len(v.UnseenMentionSeqs) != 0 || v.UnreadCount != 0 || info.Size() > maxPrivateFileBytes {
		t.Fatal(v.UnreadCount, err, statErr)
	}
}

type humanReadFailFile struct {
	snapshotFile
	at string
}

func (f humanReadFailFile) Write(p []byte) (int, error) {
	if f.at == "write" {
		return 0, io.ErrClosedPipe
	}
	return f.snapshotFile.Write(p)
}
func (f humanReadFailFile) Sync() error {
	if f.at == "sync" {
		return io.ErrClosedPipe
	}
	return f.snapshotFile.Sync()
}
func (f humanReadFailFile) Close() error {
	err := f.snapshotFile.Close()
	if f.at == "close" {
		return io.ErrClosedPipe
	}
	return err
}

func TestHumanReadCommitFailuresAndBoundedRecovery(t *testing.T) {
	for _, at := range []string{"create", "write", "sync", "close", "rename-before", "rename-after", "directory"} {
		t.Run(at, func(t *testing.T) {
			s, room, h := humanReadFixture(t, 5)
			agent := AgentSnapshot{Version: formatVersion, MemberIdentity: MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: "A", MembershipID: "generation-A"}}
			if err := s.SaveAgent(agent); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(s.dir, "agents", "generation-A", "agent.json"))
			if err != nil {
				t.Fatal(err)
			}
			humanReadConfirm(t, h, s, 5, 1)
			ops := h.ops
			switch at {
			case "create":
				h.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, io.ErrClosedPipe }
			case "write", "sync", "close":
				h.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
					f, err := ops.createTemp(dir, pattern)
					return humanReadFailFile{f, at}, err
				}
			case "rename-before":
				h.ops.rename = func(string, string) error { return io.ErrClosedPipe }
			case "rename-after":
				h.ops.rename = func(from, to string) error {
					if err := ops.rename(from, to); err != nil {
						return err
					}
					return io.ErrClosedPipe
				}
			case "directory":
				h.ops.syncDir = func(string) error { return io.ErrClosedPipe }
			}
			latest, opened := room.LatestSeq, uint64(4)
			if _, err := h.update(s, humanReadDelta{SnapshotSeq: &latest, OpenedThrough: &opened, MentionSeenSeqs: []uint64{2}}, ""); err != errHumanReadUnavailable {
				t.Fatal(err)
			}
			if h.file.Revision != 1 || h.file.OpenedThrough != 0 || !reflect.DeepEqual(h.file.MentionSeenSeqs, []uint64{1}) {
				t.Fatal("installed unconfirmed candidate", h.file)
			}
			if errors.Is(s.check(true), ErrCommitUnknown) {
				t.Fatal("read failure froze business store")
			}
			room.Revision++
			if err := s.SaveRoom(room); err != nil {
				t.Fatal("business writes failed", err)
			}
			after, err := os.ReadFile(filepath.Join(s.dir, "agents", "generation-A", "agent.json"))
			if err != nil || string(before) != string(after) {
				t.Fatal("reading changed Agent state", err)
			}
			if err := s.SaveAgent(agent); err != nil {
				t.Fatal("read failure blocked Agent checkpoint", err)
			}
			h.ops = ops
			v := humanReadConfirm(t, h, s, 5, 3)
			want := []uint64{1, 3}
			if at == "rename-after" || at == "directory" {
				want = []uint64{1, 2, 3}
			}
			wantOpened := uint64(0)
			if at == "rename-after" || at == "directory" {
				wantOpened = 4
			}
			if !reflect.DeepEqual(v.MentionSeenSeqs, want) || v.OpenedThrough != wantOpened {
				t.Fatal(v, want, wantOpened)
			}
		})
	}
	// An unsuccessful recovery latches unavailable, with no repeated I/O.
	s, _, h := humanReadFixture(t, 3)
	calls := 0
	h.ops.syncDir = func(string) error { calls++; return io.ErrClosedPipe }
	latest := uint64(3)
	if _, err := h.update(s, humanReadDelta{SnapshotSeq: &latest, MentionSeenSeqs: []uint64{2}}, ""); err == nil {
		t.Fatal("unexpected success")
	}
	for range 4 {
		if _, err := h.update(s, humanReadDelta{}, ""); err != errHumanReadUnavailable {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("recovery was not bounded", calls)
	}
	// Reload can recover after the storage issue is removed.
	room, _ := s.LoadRoom()
	reloaded := newHumanReadStore(s.dir, room, "owner")
	v := humanReadConfirm(t, reloaded, s, 3, 3)
	if !reflect.DeepEqual(v.MentionSeenSeqs, []uint64{2, 3}) {
		t.Fatal(v)
	}
}

func TestHumanReadInvalidFilesAndOrphan(t *testing.T) {
	for _, which := range []string{"json", "version", "room", "instance", "operator", "zero", "duplicate", "order", "future", "observed", "revision", "null", "oversized", "too-many", "opened", "legacy-field"} {
		t.Run(which, func(t *testing.T) {
			s, room, h := humanReadFixture(t, 3)
			v := humanReadFile{Version: humanReadVersion, RoomIdentity: room.RoomIdentity, OperatorID: "owner", Revision: 1, ObservedLatestSeq: 3, MentionSeenSeqs: []uint64{1, 2}}
			switch which {
			case "version":
				v.Version++
			case "room":
				v.RoomID = "wrong"
			case "instance":
				v.RoomInstance = "new"
			case "operator":
				v.OperatorID = "other"
			case "zero":
				v.MentionSeenSeqs[0] = 0
			case "duplicate":
				v.MentionSeenSeqs[1] = 1
			case "order":
				v.MentionSeenSeqs = []uint64{2, 1}
			case "future":
				v.MentionSeenSeqs[1] = 4
			case "observed":
				v.ObservedLatestSeq = 4
			case "opened":
				v.OpenedThrough = 4
			case "revision":
				v.Revision = 0
			case "null":
				v.MentionSeenSeqs = nil
			case "too-many":
				v.MentionSeenSeqs = make([]uint64, maxMessages+1)
			}
			data, _ := json.Marshal(v)
			if which == "legacy-field" {
				data = append(data[:len(data)-1], []byte(`,"seen_seqs":null}`)...)
			}
			if which == "json" {
				data = []byte("{")
			}
			if which == "oversized" {
				data = []byte(strings.Repeat(" ", int(maxPrivateFileBytes)+1))
			}
			if err := os.WriteFile(h.path(), data, 0600); err != nil {
				t.Fatal(err)
			}
			h = newHumanReadStore(s.dir, room, "owner")
			if _, err := h.update(s, humanReadDelta{}, ""); err != errHumanReadUnavailable {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(h.path())
			if string(data) != string(after) {
				t.Fatal("bad file overwritten")
			}
			room.Revision++
			if err := s.SaveRoom(room); err != nil {
				t.Fatal(err)
			}
		})
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "human-read.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenStore(dir, "design-review"); !errors.Is(err, ErrInvalid) {
		if s != nil {
			_ = s.Close()
		}
		t.Fatal("orphan was reinitialized", err)
	}
}

func TestHumanReadMentionParity(t *testing.T) {
	data, err := os.ReadFile("testdata/human_mentions.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Text    string
		Names   []string
		Matches bool
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		if got := newHumanMentionMatcher(tc.Names...).matches(tc.Text); got != tc.Matches {
			t.Errorf("%q %v: got %v", tc.Text, tc.Names, got)
		}
	}
}

func TestHumanReadFileCannotBecomeWorkspace(t *testing.T) {
	for _, root := range []string{"human-read.json", "Human-Read.json/sub", "local/../human-read.json/sub"} {
		if _, err := normalizeWorkspace(workspaceConfig{Root: root}, false); !errors.Is(err, ErrRequest) {
			t.Fatalf("reading file accepted as workspace: %q, %v", root, err)
		}
	}
}

func TestHumanReadAgentProgressRemainsIndependent(t *testing.T) {
	room := historyRoom(3, "ordinary reply")
	harness := newRoomHarness(t, room, nil)
	h := newHumanReadStore(harness.store.dir, room, "owner")
	id := harness.memberID("A", "generation-A")
	page := roomCall[ReadPage](t, harness, ReadMessages{RoomIdentity: room.RoomIdentity, Caller: Caller{Author: Author{Kind: "agent", ID: "A"}, MembershipID: "generation-A"}, Mode: ReadUnread})
	roomCall[ReadConfirmation](t, harness, ConfirmRead{MemberIdentity: id, ReadReceipt: page.ReadReceipt})
	v, err := h.update(harness.store, humanReadDelta{}, "")
	if err != nil || v.UnreadCount != 3 || v.Revision != 0 {
		t.Fatal("Agent consumption advanced human progress", v, err)
	}
	humanReadOpen(t, h, harness.store, 3)
	members := roomCall[MemberList](t, harness, ListMembers{RoomIdentity: room.RoomIdentity})
	if members.Members[0].ReadThrough != 3 {
		t.Fatal("human reading changed Agent cursor", members)
	}
}

func TestHumanReadFirstReplacementAndCorruptRecovery(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing-first-file", true: "corrupt-replacement"}[corrupt], func(t *testing.T) {
			s, room, h := humanReadFixture(t, 3)
			ops := h.ops
			h.ops.rename = func(from, to string) error {
				if corrupt {
					if err := os.WriteFile(to, []byte("broken"), 0600); err != nil {
						return err
					}
				}
				return io.ErrClosedPipe
			}
			latest := uint64(3)
			if _, err := h.update(s, humanReadDelta{SnapshotSeq: &latest, MentionSeenSeqs: []uint64{2}}, ""); err != errHumanReadUnavailable {
				t.Fatal(err)
			}
			calls := 0
			h.ops = ops
			h.ops.syncDir = func(dir string) error { calls++; return ops.syncDir(dir) }
			v, err := h.update(s, humanReadDelta{}, "")
			if corrupt {
				if err != errHumanReadUnavailable || h.unavailable == nil {
					t.Fatal(v, err)
				}
				for range 3 {
					_, _ = h.update(s, humanReadDelta{}, "")
				}
				if calls != 0 {
					t.Fatal("corrupt recovery was synced")
				}
			} else {
				if err != nil || v.Revision != 0 || len(v.MentionSeenSeqs) != 0 || calls != 1 {
					t.Fatal(v, err, calls)
				}
				v = humanReadConfirm(t, h, s, 2, 2)
				if h.file.ObservedLatestSeq != 2 || v.SnapshotSeq != room.LatestSeq {
					t.Fatal("observation boundary was replaced by latest", v, h.file)
				}
				info, err := os.Stat(h.path())
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal(info, err)
				}
			}
		})
	}
}
