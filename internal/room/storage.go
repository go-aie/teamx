package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
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

// Store owns the directory lock. Domain owners serialize changes to their
// state; the snapshot writer permits concurrent I/O on different paths. Values
// passed to Save must be immutable until it returns. Actor handlers call
// these blocking methods from controlled tasks, never from their state loop.
// Agent and Session files have separate owners; they are not a transaction.
type Store struct {
	sharedInput          *sharedInputStorage // guarded by inputOwners.mu
	sharedHandle         atomic.Pointer[sharedInputStorage]
	inputHandles         sync.Map
	inputOwners          inputOwners
	roomInputCache       *roomLogCache
	platformInputs       map[string]*platformInputStorage // Member-owned engines; all normal mutations use their input Actor.
	observe              func(string, MemberIdentity)
	observeExpiry        func(string, MemberIdentity, time.Time)
	mcpOperations        mcpTurnOperations
	notificationAdapters func(string) notification.Capabilities
	mu                   sync.Mutex
	activeDSH            sync.Map                    // MemberIdentity -> *dshSession; observations only, never persisted.
	activeInboxes        sync.Map                    // MembershipID -> *AgentInbox; reserved until all owned tasks settle.
	executingReferences  map[referenceActionKey]bool // protected by mu; never replayed
	dir                  string
	roomID               string
	instance             string
	lock                 *os.File
	closeMu              sync.Mutex // serializes shutdown, never acquired by a writer
	snapshotWriter                  // healthMu also protects this Store's directory lock
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
	remove     func(string) error
}

func defaultSnapshotOps() snapshotOps {
	return snapshotOps{
		makeDir:    makeDurableDir,
		createTemp: func(dir, pattern string) (snapshotFile, error) { return os.CreateTemp(dir, pattern) },
		rename:     os.Rename,
		syncDir:    syncDirectory,
		remove:     os.Remove,
	}
}

// OpenStore acquires a nonblocking OS lock, released even on process death.
// Existing room data is checked before the store is returned. Only a missing
// room.json represents an uninitialized directory; bad data is never reset.
func OpenStore(dir, roomID string) (*Store, error) {
	return openStore(dir, roomID, nil)
}

func lockStore(dir, roomID string) (*Store, error) {
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
	return &Store{dir: abs, roomID: roomID, lock: lock, snapshotWriter: snapshotWriter{ops: defaultSnapshotOps()}}, nil
}

func openStore(dir, roomID string, upgrade *inputUpgradeOptions) (*Store, error) {
	s, err := lockStore(dir, roomID)
	if err != nil {
		return nil, err
	}
	if upgrade != nil {
		if err := s.EnsureInputFormat(*upgrade); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	room, err := s.loadRoom()
	if err == nil {
		s.instance = room.RoomInstance
		if room.InputLog != nil {
			err = s.cleanRoomLog(*room.InputLog)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		// Orphaned private state is evidence of an existing Room, not a new
		// directory that may silently be initialized with an empty history.
		err = nil
		for _, name := range []string{"agents", "inboxes", "human-read.json", "room-input", inputUpgradeDirectory, inputUpgradeJournal} {
			if _, statErr := os.Stat(filepath.Join(s.dir, name)); !errors.Is(statErr, os.ErrNotExist) {
				err = fmt.Errorf("%w: missing room.json with existing or inaccessible %s", ErrInvalid, name)
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
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	// Settle the higher-level accepted operations first: they can contain
	// multiple snapshot writes. Service shutdown has already done this join.
	if err := s.closeInputOwners(context.Background()); err != nil {
		return err
	}
	s.healthMu.Lock()
	s.writerClosing = true
	done := s.writesDone
	s.healthMu.Unlock()
	// Do not hold Store.mu or the health lock while admitted writes finish.
	if done != nil {
		<-done
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
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
	if err := s.materializeRoomInput(&v); err != nil {
		return RoomSnapshot{}, err
	}
	if err := v.validate(); err != nil {
		return RoomSnapshot{}, fmt.Errorf("%w: room.json: %w", ErrInvalid, err)
	}
	if v.RoomID != s.roomID || (s.instance != "" && v.RoomInstance != s.instance) {
		return RoomSnapshot{}, fmt.Errorf("%w: room.json", ErrIdentity)
	}
	return v, nil
}

func (s *Store) materializeRoomInput(v *RoomSnapshot) error {
	if v.InputLog == nil {
		return nil
	}
	if len(v.Messages) != 0 {
		return ErrInvalid
	}
	return s.loadRoomLog(v)
}

// Catalog readers may observe a previous Room header immediately before the
// owning Store reclaims its segments. Retry that read once with the new header.
func readPublishedRoom(dir string, id RoomIdentity) (RoomSnapshot, error) {
	s := &Store{dir: dir, roomID: id.RoomID, instance: id.RoomInstance}
	v, err := s.loadRoom()
	if errors.Is(err, os.ErrNotExist) {
		return s.loadRoom()
	}
	return v, err
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
	disk := v
	if v.InputLog != nil {
		if err := s.roomInputProtected(v.ClearedThrough); err != nil {
			return err
		}
		root, err := s.prepareRoomLog(v)
		if err != nil {
			return err
		}
		disk.InputLog, disk.Messages = root, []RoomMessage{}
	}
	if err := s.save(filepath.Join(s.dir, "room.json"), disk, maxRoomFileBytes); err != nil {
		return err
	}
	s.instance = v.RoomInstance
	if disk.InputLog != nil {
		_ = s.cleanRoomLog(*disk.InputLog)
	}
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
		if _, err := s.inboxEntryLocked(id, v.LastOfferedInboxSeq); err != nil && !errors.Is(err, ErrInboxExpired) {
			return AgentSnapshot{}, err
		}
	}
	if err := s.checkAgentInboxTurnLocked(v); err != nil {
		return AgentSnapshot{}, err
	}
	return v, nil
}

func (s *Store) SaveAgent(v AgentSnapshot) error {
	s.mu.Lock()
	p := s.platformInputs[v.MembershipID]
	s.mu.Unlock()
	if p != nil && !p.owner.IsZero() {
		return p.run(context.Background(), "save_turn", func(context.Context) error { return s.saveAgentSnapshot(v) })
	}
	return s.saveAgentSnapshot(v)
}

func (s *Store) saveAgentSnapshot(v AgentSnapshot) error {
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
		if _, err := s.inboxEntryLocked(v.MemberIdentity, v.LastOfferedInboxSeq); err != nil && !errors.Is(err, ErrInboxExpired) {
			return err
		}
	}
	if err := s.checkAgentInboxTurnLocked(v); err != nil {
		return err
	}

	if p := s.platformInputs[v.MembershipID]; p != nil && p.generation != "" {
		v.InputStateRef = "input-state.json"
		if v.Turn != nil && v.Turn.Status == "completed" {
			if err := p.repository.Seal(inputstream.Execution{Generation: p.generation, Turn: v.Turn.Number}); err != nil {
				return p.result(err)
			}
		}
	}
	return s.save(path, v, maxPrivateFileBytes)
}

func (s *Store) checkAgentInboxTurnLocked(v AgentSnapshot) error {
	if v.Turn != nil && v.Turn.Status == "running" && v.Turn.InboxSnapshotSeq != 0 {
		h, err := s.inboxHeaderLocked(v.MemberIdentity)
		if err != nil {
			return err
		}
		if v.Turn.InboxStart < h.ExpiredThrough {
			return ErrInboxExpired // Reject a stale offer racing with reclamation.
		}
	}
	return nil
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
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	if s.lock == nil {
		return ErrStoreClosed
	}
	if writing && s.writerClosing {
		return ErrStoreClosed
	}
	if writing && s.uncertain {
		return ErrCommitUnknown
	}
	return nil
}

func (s *snapshotWriter) markUncertain() {
	s.healthMu.Lock()
	s.uncertain = true
	s.healthMu.Unlock()
}

// save has a single commit boundary. Before replacement, failure leaves the
// old file intact. Once replacement is attempted, any ambiguous outcome freezes
// all writes in this Store, including writes to other snapshot files.
func (s *Store) save(path string, v any, limit int64) error {
	if err := s.check(true); err != nil {
		return err
	}
	return s.snapshotWriter.save(path, v, limit)
}

// The file writer is also used by domains with their own lock and lifecycle.
// Its health admission is independent of Room directory ownership.
func (s *snapshotWriter) save(path string, v any, limit int64) error {
	finish, err := s.admitSnapshotWrite(path)
	if err != nil {
		return err
	}
	defer finish()
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
		s.markUncertain()
		return fmt.Errorf("%w: replace snapshot: %w", ErrCommitUnknown, err)
	}
	if err := s.ops.syncDir(dir); err != nil {
		s.markUncertain()
		return fmt.Errorf("%w: sync directory: %w", ErrCommitUnknown, err)
	}
	return nil
}

// Directory creation is a durability boundary, but an already initialized
// directory need not be synced again before every write. Keep the file Sync
// and the post-rename directory Sync on every commit. Recheck both inodes so
// replacing a directory or its parent requires initialization again.
func (s *snapshotWriter) ensureSnapshotDirectory(dir string) error {
	s.directoryMu.Lock()
	if s.durableDirectories == nil {
		s.durableDirectories = map[string]*snapshotDirectory{}
	}
	directory := s.durableDirectories[dir]
	if directory == nil {
		directory = &snapshotDirectory{}
		s.durableDirectories[dir] = directory
	}
	s.directoryMu.Unlock()
	directory.mu.Lock()
	defer directory.mu.Unlock()
	info, statErr := os.Stat(dir)
	parent, parentErr := os.Stat(filepath.Dir(dir))
	previous := directory.identity
	if previous.directory != nil && statErr == nil && parentErr == nil &&
		os.SameFile(previous.directory, info) && os.SameFile(previous.parent, parent) {
		return nil
	}
	directory.identity = directoryIdentity{}
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
	directory.identity = directoryIdentity{info, parent}
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
