package room

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-aie/teamx/internal/platform/notification"
)

var (
	ErrLocked        = errors.New("state directory already in use")
	ErrVersion       = errors.New("unsupported snapshot version")
	ErrInvalid       = errors.New("invalid snapshot")
	ErrIdentity      = errors.New("snapshot identity mismatch")
	ErrCommitUnknown = errors.New("snapshot replacement may have committed; reopen and reload before writing")
	ErrStoreClosed   = errors.New("state store closed")
)

// JSON escaping may expand a 64 MiB history. Private snapshots have a much
// smaller bound. Limits apply both while encoding and before decoding.
const (
	maxRoomFileBytes    int64 = 512 << 20
	maxPrivateFileBytes int64 = 1 << 20
)

// Store owns the directory lock and serializes file access. Snapshot values
// passed to Save must be immutable until it returns. Actor handlers call
// these blocking methods from controlled tasks, never from their state loop.
// Agent and Session files have separate owners; they are not a transaction.
type Store struct {
	notificationAdapters func(string) notification.Capabilities
	mu                   sync.Mutex
	activeDSH            sync.Map                    // MemberIdentity -> *dshSession; observations only, never persisted.
	activeInboxes        sync.Map                    // MembershipID -> *AgentInbox; reserved until all owned tasks settle.
	executingReferences  map[referenceActionKey]bool // protected by mu; never replayed
	dir                  string
	roomID               string
	instance             string
	lock                 *os.File
	uncertain            bool
	ops                  snapshotOps
	directoryMu          sync.Mutex
	durableDirectories   map[string]directoryIdentity
}

type directoryIdentity struct{ directory, parent os.FileInfo }

type snapshotFile interface {
	io.Writer
	Sync() error
	Close() error
	Name() string
}

// The narrow I/O seam lets tests stop at exact commit boundaries.
type snapshotOps struct {
	makeDir    func(string) error
	createTemp func(string, string) (snapshotFile, error)
	rename     func(string, string) error
	syncDir    func(string) error
}

func defaultSnapshotOps() snapshotOps {
	return snapshotOps{
		makeDir:    makeDurableDir,
		createTemp: func(dir, pattern string) (snapshotFile, error) { return os.CreateTemp(dir, pattern) },
		rename:     os.Rename,
		syncDir:    syncDirectory,
	}
}

// OpenStore acquires a nonblocking OS lock, released even on process death.
// Existing room data is checked before the store is returned. Only a missing
// room.json represents an uninitialized directory; bad data is never reset.
func OpenStore(dir, roomID string) (*Store, error) {
	if !validText(roomID) {
		return nil, fmt.Errorf("%w: empty room ID", ErrIdentity)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := makeDurableDir(abs); err != nil {
		return nil, err
	}
	lock, err := lockDirectory(abs)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: abs, roomID: roomID, lock: lock, ops: defaultSnapshotOps()}
	room, err := s.loadRoom()
	if err == nil {
		s.instance = room.RoomInstance
	} else if errors.Is(err, os.ErrNotExist) {
		// Orphaned private state is evidence of an existing Room, not a new
		// directory that may silently be initialized with an empty history.
		err = nil
		for _, name := range []string{"agents", "inboxes"} {
			if _, statErr := os.Stat(filepath.Join(abs, name)); !errors.Is(statErr, os.ErrNotExist) {
				err = fmt.Errorf("%w: missing room.json with existing or inaccessible %s directory", ErrInvalid, name)
				break
			}
		}
	}
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil
	}
	err := unlockDirectory(s.lock)
	s.lock = nil
	// Never remove the lock file: unlinking a locked inode permits a second
	// service to lock a different inode under the same pathname.
	return err
}

func (s *Store) LoadRoom() (RoomSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(false); err != nil {
		return RoomSnapshot{}, err
	}
	return s.loadRoom()
}

func (s *Store) loadRoom() (RoomSnapshot, error) {
	var v RoomSnapshot
	if err := readSnapshot(filepath.Join(s.dir, "room.json"), &v, maxRoomFileBytes); err != nil {
		return v, err
	}
	if err := v.validate(); err != nil {
		return RoomSnapshot{}, fmt.Errorf("%w: room.json: %w", ErrInvalid, err)
	}
	if v.RoomID != s.roomID || (s.instance != "" && v.RoomInstance != s.instance) {
		return RoomSnapshot{}, fmt.Errorf("%w: room.json", ErrIdentity)
	}
	return v, nil
}

func (s *Store) SaveRoom(v RoomSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(true); err != nil {
		return err
	}
	if err := v.validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if v.RoomID != s.roomID || (s.instance != "" && v.RoomInstance != s.instance) {
		return ErrIdentity
	}
	if err := s.save(filepath.Join(s.dir, "room.json"), v, maxRoomFileBytes); err != nil {
		return err
	}
	s.instance = v.RoomInstance
	return nil
}

func (s *Store) LoadAgent(id MemberIdentity) (AgentSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var v AgentSnapshot
	path, latest, err := s.memberPath(id, "agent.json", false)
	if err != nil {
		return v, err
	}
	if err := readSnapshot(path, &v, maxPrivateFileBytes); err != nil {
		return AgentSnapshot{}, err
	}
	if err := v.validate(); err != nil {
		return AgentSnapshot{}, fmt.Errorf("%w: agent.json: %w", ErrInvalid, err)
	}
	if v.MemberIdentity != id {
		return AgentSnapshot{}, ErrIdentity
	}
	if v.LastOfferedUnreadSeq > latest || (v.Turn != nil && v.Turn.SnapshotSeq > latest) {
		return AgentSnapshot{}, fmt.Errorf("%w: agent progress exceeds room history", ErrInvalid)
	}
	if v.LastOfferedInboxSeq != 0 {
		if _, err := s.inboxEntryLocked(id, v.LastOfferedInboxSeq); err != nil {
			return AgentSnapshot{}, err
		}
	}
	return v, nil
}

func (s *Store) SaveAgent(v AgentSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := v.validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	path, latest, err := s.memberPath(v.MemberIdentity, "agent.json", true)
	if err != nil {
		return err
	}
	if v.LastOfferedUnreadSeq > latest || (v.Turn != nil && v.Turn.SnapshotSeq > latest) {
		return fmt.Errorf("%w: agent progress exceeds room history", ErrInvalid)
	}
	if v.LastOfferedInboxSeq != 0 {
		if _, err := s.inboxEntryLocked(v.MemberIdentity, v.LastOfferedInboxSeq); err != nil {
			return err
		}
	}
	return s.save(path, v, maxPrivateFileBytes)
}

func (s *Store) memberPath(id MemberIdentity, name string, writing bool) (string, uint64, error) {
	if err := s.check(writing); err != nil {
		return "", 0, err
	}
	if err := validateMemberHeader(formatVersion, id); err != nil {
		return "", 0, fmt.Errorf("%w: %w", ErrIdentity, err)
	}
	room, err := s.loadRoom()
	if err != nil {
		return "", 0, err
	}
	if id.RoomIdentity != room.RoomIdentity {
		return "", 0, ErrIdentity
	}
	for _, m := range room.Members {
		if m.AgentID == id.AgentID && m.MembershipID == id.MembershipID {
			return filepath.Join(s.dir, "agents", id.MembershipID, name), room.LatestSeq, nil
		}
	}
	return "", 0, ErrIdentity // Orphaned files never restore a removed membership.
}

func (s *Store) check(writing bool) error {
	if s.lock == nil {
		return ErrStoreClosed
	}
	if writing && s.uncertain {
		return ErrCommitUnknown
	}
	return nil
}

// save has a single commit boundary. Before replacement, failure leaves the
// old file intact. Once replacement is attempted, any ambiguous outcome freezes
// all writes in this Store, including writes to other snapshot files.
func (s *Store) save(path string, v any, limit int64) error {
	dir := filepath.Dir(path)
	if err := s.ensureSnapshotDirectory(dir); err != nil {
		return err
	}
	f, err := s.ops.createTemp(dir, ".snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()
	enc := json.NewEncoder(&boundedWriter{w: f, remaining: limit})
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode snapshot: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync snapshot: %w", err)
	}
	closed = true
	if err := f.Close(); err != nil {
		return fmt.Errorf("close snapshot: %w", err)
	}
	if err := s.ops.rename(f.Name(), path); err != nil {
		// An error from replacement is treated conservatively as uncertain;
		// a filesystem can report an error after performing the operation.
		s.uncertain = true
		return fmt.Errorf("%w: replace snapshot: %w", ErrCommitUnknown, err)
	}
	if err := s.ops.syncDir(dir); err != nil {
		s.uncertain = true
		return fmt.Errorf("%w: sync directory: %w", ErrCommitUnknown, err)
	}
	return nil
}

// Directory creation is a durability boundary, but an already initialized
// directory need not be synced again before every write. Keep the file Sync
// and the post-rename directory Sync on every commit. Recheck both inodes so
// replacing a directory or its parent requires initialization again.
func (s *Store) ensureSnapshotDirectory(dir string) error {
	s.directoryMu.Lock()
	defer s.directoryMu.Unlock()
	info, statErr := os.Stat(dir)
	parent, parentErr := os.Stat(filepath.Dir(dir))
	previous, found := s.durableDirectories[dir]
	if found && statErr == nil && parentErr == nil &&
		os.SameFile(previous.directory, info) && os.SameFile(previous.parent, parent) {
		return nil
	}
	delete(s.durableDirectories, dir)
	if err := s.ops.makeDir(dir); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	parent, err = os.Stat(filepath.Dir(dir))
	if err != nil {
		return err
	}
	if s.durableDirectories == nil {
		s.durableDirectories = map[string]directoryIdentity{}
	}
	s.durableDirectories[dir] = directoryIdentity{info, parent}
	return nil
}

func readSnapshot(path string, v any, limit int64) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return fmt.Errorf("%w: %s: not a bounded regular file", ErrInvalid, path)
	}
	dec := json.NewDecoder(io.LimitReader(f, limit+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalid, path, err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: %s: trailing JSON data", ErrInvalid, path)
	}
	return nil
}

type boundedWriter struct {
	w         io.Writer
	remaining int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, fmt.Errorf("snapshot byte capacity exceeded")
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// Sync each newly created directory and its parent so the first private
// snapshot's durable rename cannot depend on an uncommitted parent directory.
func makeDurableDir(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", path)
		}
		if err := syncDirectory(path); err != nil {
			return err
		}
		return syncDirectory(filepath.Dir(path))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if err := makeDurableDir(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := syncDirectory(path); err != nil {
		return err
	}
	return syncDirectory(parent)
}
