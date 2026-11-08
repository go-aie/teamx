package room

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func sampleRoom() RoomSnapshot {
	return RoomSnapshot{
		Version:      formatVersion,
		RoomIdentity: RoomIdentity{RoomID: "design-review", RoomInstance: "room-1"},
		LatestSeq:    2, Revision: 5,
		Messages: []RoomMessage{
			{Seq: 1, ClientMessageID: "post-1", Author: Author{"human", "user"}, Text: "请 review", CreatedAt: time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)},
			{Seq: 2, ClientMessageID: "post-2", Author: Author{"agent", "reviewer"}, Text: "已查看", ReplyTo: 1, CreatedAt: time.Date(2026, 10, 31, 1, 1, 0, 0, time.UTC)},
		},
		Members: []MemberState{{AgentID: "reviewer", MembershipID: "membership-1", ReadThrough: 1, Runner: RunnerConfig{Kind: "dsh", Profile: "fixture-" + "review-once"}}},
	}
}

func samplePrivate(room RoomSnapshot) (AgentSnapshot, virtualSnapshot) {
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: "reviewer", MembershipID: "membership-1"}
	return AgentSnapshot{
			Version: formatVersion, MemberIdentity: id, LastOfferedUnreadSeq: 1, TurnNumber: 2,
			Turn: &TurnRecord{Number: 2, Status: "running", StartReadThrough: 0, SnapshotSeq: 1, OfferedUnreadSeq: 1},
		}, virtualSnapshot{
			Version: formatVersion, MemberIdentity: id, SessionID: "session-1", Turns: 1, ScriptPosition: 1, ProcessedThrough: 1,
		}
}

func openTestStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := OpenStore(dir, "design-review")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func saveTestRoom(t *testing.T, s *Store) RoomSnapshot {
	t.Helper()
	v := sampleRoom()
	if err := s.SaveRoom(v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSnapshotRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "room")
	s := openTestStore(t, dir)
	if _, err := s.LoadRoom(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new store: %v", err)
	}
	wantRoom := saveTestRoom(t, s)
	wantAgent, wantSession := samplePrivate(wantRoom)
	if err := s.SaveAgent(wantAgent); err != nil {
		t.Fatal(err)
	}
	if err := s.saveVirtualSession(wantSession); err != nil {
		t.Fatal(err)
	}
	epoch, err := newOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestStore(t, dir)
	gotRoom, err := s.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	gotAgent, err := s.LoadAgent(wantAgent.MemberIdentity)
	if err != nil {
		t.Fatal(err)
	}
	gotSession, err := s.loadVirtualSession(wantSession.MemberIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotRoom, wantRoom) || !reflect.DeepEqual(gotAgent, wantAgent) || !reflect.DeepEqual(gotSession, wantSession) {
		t.Fatalf("round trip changed business data: room=%+v agent=%+v session=%+v", gotRoom, gotAgent, gotSession)
	}
	// Loading a running record does not execute or silently mark it completed.
	if gotAgent.Turn.Status != "running" {
		t.Fatal("running record was rewritten")
	}
	nextEpoch, err := newOpaqueID()
	if err != nil || len(nextEpoch) != 32 || nextEpoch == epoch {
		t.Fatalf("service epoch: %q, %v", nextEpoch, err)
	}
	for _, path := range []string{"room.json", "agents/membership-1/agent.json", "agents/membership-1/session.json"} {
		b, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"service_epoch", "target", "read_receipt", "unread_count"} {
			if bytes.Contains(b, []byte(`"`+forbidden+`"`)) {
				t.Fatalf("runtime field %s in %s", forbidden, path)
			}
		}
		info, err := os.Stat(filepath.Join(dir, path))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("snapshot permissions: %v, %v", info, err)
		}
	}
	// A returned slice is not an alias into any cached Store state.
	gotRoom.Messages[0].Text = "caller mutation"
	reloaded, err := s.LoadRoom()
	if err != nil || reloaded.Messages[0].Text != wantRoom.Messages[0].Text {
		t.Fatal("load returned mutable shared state", err)
	}
}

func TestDirectoryOwnership(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	if other, err := OpenStore(dir, "design-review"); !errors.Is(err, ErrLocked) {
		if other != nil {
			_ = other.Close()
		}
		t.Fatalf("second owner: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRoom(sampleRoom()); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("closed write: %v", err)
	}
	if _, err := s.LoadRoom(); !errors.Is(err, ErrStoreClosed) {
		t.Fatalf("closed read: %v", err)
	}
	_ = openTestStore(t, dir)
}

func TestDirectoryLockProcess(t *testing.T) {
	if os.Getenv("TEAMX_ROOM_LOCK_HELPER") == "1" {
		s, err := OpenStore(os.Getenv("TEAMX_ROOM_LOCK_DIR"), "design-review")
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		fmt.Println("locked")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDirectoryLockProcess$")
	cmd.Env = append(os.Environ(), "TEAMX_ROOM_LOCK_HELPER=1", "TEAMX_ROOM_LOCK_DIR="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("lock handshake: %q, %v", line, err)
	}
	if s, err := OpenStore(dir, "design-review"); !errors.Is(err, ErrLocked) {
		if s != nil {
			_ = s.Close()
		}
		t.Fatalf("process did not own lock: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait() // Kill does not run the child's Close; the kernel releases it.
	_ = openTestStore(t, dir)
}

func TestInvalidSnapshots(t *testing.T) {
	valid := sampleRoom()
	cases := []struct {
		name   string
		change func(*RoomSnapshot)
		raw    string
		want   error
	}{
		{name: "truncated", raw: `{"version":1,`, want: ErrInvalid},
		{name: "unsupported version", change: func(s *RoomSnapshot) { s.Version++ }, want: ErrVersion},
		{name: "missing version", change: func(s *RoomSnapshot) { s.Version = 0 }, want: ErrVersion},
		{name: "wrong room", change: func(s *RoomSnapshot) { s.RoomID = "other" }, want: ErrIdentity},
		{name: "missing identity", change: func(s *RoomSnapshot) { s.RoomInstance = "" }, want: ErrInvalid},
		{name: "sequence gap", change: func(s *RoomSnapshot) { s.Messages[1].Seq = 3 }, want: ErrInvalid},
		{name: "wrong latest", change: func(s *RoomSnapshot) { s.LatestSeq++ }, want: ErrInvalid},
		{name: "wrong revision", change: func(s *RoomSnapshot) { s.Revision = 0 }, want: ErrInvalid},
		{name: "cursor beyond history", change: func(s *RoomSnapshot) { s.Members[0].ReadThrough = 3 }, want: ErrInvalid},
		{name: "duplicate key", change: func(s *RoomSnapshot) {
			s.Messages[1].Author = s.Messages[0].Author
			s.Messages[1].ClientMessageID = "post-1"
		}, want: ErrInvalid},
		{name: "duplicate member", change: func(s *RoomSnapshot) { s.Members = append(s.Members, s.Members[0]) }, want: ErrInvalid},
		{name: "unsafe membership", change: func(s *RoomSnapshot) { s.Members[0].MembershipID = "../../outside" }, want: ErrInvalid},
		{name: "missing array", change: func(s *RoomSnapshot) { s.Members = nil }, want: ErrInvalid},
		{name: "bad runner", change: func(s *RoomSnapshot) { s.Members[0].Runner.Kind = "unknown" }, want: ErrInvalid},
		{name: "oversized text", change: func(s *RoomSnapshot) { s.Messages[0].Text = strings.Repeat("a", maxTextBytes+1) }, want: ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			v := sampleRoom()
			if tc.change != nil {
				tc.change(&v)
			}
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if tc.raw != "" {
				b = []byte(tc.raw)
			}
			path := filepath.Join(dir, "room.json")
			if err := os.WriteFile(path, b, 0600); err != nil {
				t.Fatal(err)
			}
			if s, err := OpenStore(dir, valid.RoomID); !errors.Is(err, tc.want) {
				if s != nil {
					_ = s.Close()
				}
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, b) {
				t.Fatal("bad snapshot was overwritten", err)
			}
		})
	}
	for _, suffix := range []string{` {}`, ` trailing`, `,"service_epoch":"old"}`} {
		t.Run("unexpected JSON "+suffix, func(t *testing.T) {
			dir := t.TempDir()
			b, _ := json.Marshal(valid)
			if strings.HasPrefix(suffix, ",") {
				b = b[:len(b)-1]
			}
			b = append(b, suffix...)
			if err := os.WriteFile(filepath.Join(dir, "room.json"), b, 0600); err != nil {
				t.Fatal(err)
			}
			if s, err := OpenStore(dir, valid.RoomID); !errors.Is(err, ErrInvalid) {
				if s != nil {
					_ = s.Close()
				}
				t.Fatalf("unexpected JSON accepted: %v", err)
			}
		})
	}
}

func TestPrivateSnapshotIdentityAndValidation(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	r := saveTestRoom(t, s)
	a, session := samplePrivate(r)
	if err := s.SaveAgent(a); err != nil {
		t.Fatal(err)
	}
	if err := s.saveVirtualSession(session); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*MemberIdentity){
		func(id *MemberIdentity) { id.RoomID = "wrong" },
		func(id *MemberIdentity) { id.RoomInstance = "wrong" },
		func(id *MemberIdentity) { id.AgentID = "wrong" },
		func(id *MemberIdentity) { id.MembershipID = "new-generation" },
		func(id *MemberIdentity) { id.MembershipID = "../outside" },
	} {
		id := a.MemberIdentity
		change(&id)
		if _, err := s.LoadAgent(id); !errors.Is(err, ErrIdentity) {
			t.Fatalf("agent identity: %v", err)
		}
		if _, err := s.loadVirtualSession(id); !errors.Is(err, ErrIdentity) {
			t.Fatalf("session identity: %v", err)
		}
	}
	for _, name := range []string{"agent.json", "session.json"} {
		path := filepath.Join(s.dir, "agents", a.MembershipID, name)
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			from, to string
			want     error
		}{
			{`"version":1`, `"version":99`, ErrVersion},
			{`"room_instance":"room-1"`, `"room_instance":"old-room"`, ErrIdentity},
			{`"membership_id":"membership-1"`, `"membership_id":"old-member"`, ErrIdentity},
		} {
			if err := os.WriteFile(path, bytes.Replace(b, []byte(tc.from), []byte(tc.to), 1), 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			if name == "agent.json" {
				_, err = s.LoadAgent(a.MemberIdentity)
			} else {
				_, err = s.loadVirtualSession(a.MemberIdentity)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("%s: want %v got %v", name, tc.want, err)
			}
		}
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	a.Turn.StartReadThrough = 2
	if err := s.SaveAgent(a); !errors.Is(err, ErrInvalid) {
		t.Fatalf("turn boundary: %v", err)
	}
	a.Turn.StartReadThrough = 0
	a.Turn.SnapshotSeq = r.LatestSeq + 1
	if err := s.SaveAgent(a); !errors.Is(err, ErrInvalid) {
		t.Fatalf("agent progress beyond history: %v", err)
	}
	session.ProcessedThrough = r.LatestSeq + 1
	if err := s.saveVirtualSession(session); !errors.Is(err, ErrInvalid) {
		t.Fatalf("session progress beyond history: %v", err)
	}
	session.ProcessedThrough = 1
	session.SessionID = ""
	if err := s.saveVirtualSession(session); !errors.Is(err, ErrInvalid) {
		t.Fatalf("session ID: %v", err)
	}
	r.Members = []MemberState{}
	r.Revision++
	if err := s.SaveRoom(r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadAgent(a.MemberIdentity); !errors.Is(err, ErrIdentity) {
		t.Fatalf("orphan resurrected: %v", err)
	}
}

type failingFile struct {
	snapshotFile
	stage string
	err   error
}

func (f *failingFile) Write(p []byte) (int, error) {
	if f.stage == "write" {
		n, _ := f.snapshotFile.Write(p[:len(p)/2])
		return n, f.err
	}
	return f.snapshotFile.Write(p)
}
func (f *failingFile) Sync() error {
	if f.stage == "file sync" {
		return f.err
	}
	return f.snapshotFile.Sync()
}
func (f *failingFile) Close() error {
	err := f.snapshotFile.Close()
	if f.stage == "close" {
		return f.err
	}
	return err
}

func TestCommitFailureBoundaries(t *testing.T) {
	for _, stage := range []string{"create", "write", "file sync", "close", "replace", "replace then error", "directory sync"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			s := openTestStore(t, dir)
			old := saveTestRoom(t, s)
			path := filepath.Join(dir, "room.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("controlled storage failure")
			real := defaultSnapshotOps()
			s.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
				if stage == "create" {
					return nil, injected
				}
				f, err := real.createTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				return &failingFile{f, stage, injected}, nil
			}
			s.ops.rename = func(from, to string) error {
				if stage == "replace" {
					return injected
				}
				if err := real.rename(from, to); err != nil {
					return err
				}
				if stage == "replace then error" {
					return injected
				}
				return nil
			}
			s.ops.syncDir = func(dir string) error {
				if stage == "directory sync" {
					return injected
				}
				return real.syncDir(dir)
			}
			next := old
			next.Revision++
			err = s.SaveRoom(next)
			if !errors.Is(err, injected) {
				t.Fatalf("fault not observed: %v", err)
			}
			uncertain := stage == "replace" || stage == "replace then error" || stage == "directory sync"
			if errors.Is(err, ErrCommitUnknown) != uncertain {
				t.Fatalf("incorrect commit outcome: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := stage == "replace then error" || stage == "directory sync"
			if bytes.Equal(before, after) == changed {
				t.Fatalf("unexpected file contents at %s", stage)
			}
			temps, err := filepath.Glob(filepath.Join(dir, ".snapshot-*"))
			if err != nil || len(temps) != 0 {
				t.Fatalf("temporary files leaked: %v, %v", temps, err)
			}
			s.ops = real
			if uncertain {
				a, session := samplePrivate(old)
				for _, save := range []func() error{
					func() error { return s.SaveRoom(old) },
					func() error { return s.SaveAgent(a) },
					func() error { return s.saveVirtualSession(session) },
				} {
					if err := save(); !errors.Is(err, ErrCommitUnknown) {
						t.Fatalf("write after uncertain commit: %v", err)
					}
				}
				still, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(after, still) {
					t.Fatal("frozen store overwrote file", err)
				}
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				s = openTestStore(t, dir)
				got, err := s.LoadRoom()
				want := old.Revision
				if changed {
					want = next.Revision
				}
				if err != nil || got.Revision != want {
					t.Fatalf("reload: %+v, %v", got, err)
				}
			}
			if err := s.SaveRoom(next); err != nil {
				t.Fatalf("safe retry/reopen: %v", err)
			}
		})
	}
}

func TestSerializedCommits(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	r := saveTestRoom(t, s)
	a, session := samplePrivate(r)
	entered, release := make(chan struct{}), make(chan struct{})
	real := s.ops.rename
	var once sync.Once
	s.ops.rename = func(from, to string) error {
		once.Do(func() { close(entered); <-release })
		return real(from, to)
	}
	done := make(chan error, 2)
	go func() { done <- s.SaveAgent(a) }()
	<-entered // First writer is stopped immediately before replacement.
	go func() { done <- s.saveVirtualSession(session) }()
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.LoadAgent(a.MemberIdentity); err != nil {
		t.Fatal(err)
	}
	if _, err := s.loadVirtualSession(session.MemberIdentity); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotFileBounds(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, dir)
	r := saveTestRoom(t, s)
	a, _ := samplePrivate(r)
	if err := s.SaveAgent(a); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "agents", a.MembershipID, "agent.json")
	f, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxPrivateFileBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadAgent(a.MemberIdentity); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized snapshot: %v", err)
	}
	a.Turn.Error = strings.Repeat("x", int(maxPrivateFileBytes))
	if err := s.SaveAgent(a); err == nil || errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("encoding limit: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "room.json")); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenStore(dir, r.RoomID); !errors.Is(err, ErrInvalid) {
		if s != nil {
			_ = s.Close()
		}
		t.Fatalf("missing room with private state accepted: %v", err)
	}
}
