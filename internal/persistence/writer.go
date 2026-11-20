// Package persistence provides bounded JSON snapshots and a shared commit-health fence.
package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

var (
	ErrInvalid       = errors.New("invalid snapshot")
	ErrCommitUnknown = errors.New("snapshot replacement may have committed; reopen and reload before writing")
	ErrClosed        = errors.New("state store closed")
)

type directoryIdentity struct{ directory, parent os.FileInfo }

// File is the temporary snapshot file needed at the commit boundary.
type File interface {
	io.Writer
	Sync() error
	Close() error
	Name() string
}

// Operations supplies filesystem I/O and permits fault injection at exact
// commit boundaries. Body-log commit algorithms remain with their domain owner.
type Operations struct {
	MakeDir    func(string) error
	CreateTemp func(string, string) (File, error)
	Rename     func(string, string) error
	SyncDir    func(string) error
	Remove     func(string) error
}

func DefaultOperations() Operations {
	return Operations{
		MakeDir:    MakeDurableDir,
		CreateTemp: func(dir, pattern string) (File, error) { return os.CreateTemp(dir, pattern) },
		Rename:     os.Rename,
		SyncDir:    SyncDirectory,
		Remove:     os.Remove,
	}
}

// Writer owns one snapshot health domain. Domain owners retain their directory
// locks and read/modify/write protocols. Construct it with NewWriter; do not copy it.
type Writer struct {
	healthMu           sync.Mutex
	uncertain          bool
	writerClosing      bool
	writes             int
	writesDone         chan struct{}
	ops                Operations
	pathMu             sync.Mutex
	writePaths         map[string]*snapshotWritePath
	directoryMu        sync.Mutex
	durableDirectories map[string]*snapshotDirectory
}

type snapshotDirectory struct {
	mu       sync.Mutex
	identity directoryIdentity
}

type snapshotWritePath struct {
	mu   sync.Mutex
	refs int // protected by Writer.pathMu; includes waiters
}

// Path serialization is a file-level backstop, not a substitute for the domain
// owner's read/modify/write protocol. Waiters are admitted only after obtaining
// the path, so a freeze also rejects requests queued behind another save.
func (s *Writer) admitSnapshotWrite(path string) (func(), error) {
	path = filepath.Clean(path)
	s.pathMu.Lock()
	if s.writePaths == nil {
		s.writePaths = map[string]*snapshotWritePath{}
	}
	p := s.writePaths[path]
	if p == nil {
		p = &snapshotWritePath{}
		s.writePaths[path] = p
	}
	p.refs++
	s.pathMu.Unlock()
	p.mu.Lock()
	releasePath := func() {
		p.mu.Unlock()
		s.pathMu.Lock()
		p.refs--
		if p.refs == 0 {
			delete(s.writePaths, path)
		}
		s.pathMu.Unlock()
	}
	s.healthMu.Lock()
	var err error
	switch {
	case s.writerClosing:
		err = ErrClosed
	case s.uncertain:
		err = ErrCommitUnknown
	default:
		if s.writes == 0 {
			s.writesDone = make(chan struct{})
		}
		s.writes++
	}
	s.healthMu.Unlock()
	if err != nil {
		releasePath()
		return nil, err
	}
	return func() {
		// No file I/O follows this point, including temporary-file cleanup.
		releasePath()
		s.healthMu.Lock()
		s.writes--
		if s.writes == 0 {
			close(s.writesDone)
		}
		s.healthMu.Unlock()
	}, nil
}

// NewWriter creates one independent health domain. Operations must be complete
// and remain safe for concurrent calls; DefaultOperations supplies filesystem I/O.
func NewWriter(ops Operations) *Writer { return &Writer{ops: ops} }

// Check preserves read access during an uncertain commit and while closing.
// The directory owner decides whether reads remain admitted after releasing its lock.
func (s *Writer) Check(writing bool) error {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	if writing && s.writerClosing {
		return ErrClosed
	}
	if writing && s.uncertain {
		return ErrCommitUnknown
	}
	return nil
}

// MarkUncertain reports an ambiguous non-snapshot commit in this same domain.
// Already admitted writes retain their individual outcomes.
func (s *Writer) MarkUncertain() {
	s.healthMu.Lock()
	s.uncertain = true
	s.healthMu.Unlock()
}

// Close seals write admission and joins all admitted I/O, including cleanup.
// Owners must settle higher-level operations before calling it. Cancellation
// stops waiting, but never reopens admission or establishes that I/O settled.
// Call Close again to join after a canceled wait; release ownership only on nil.
func (s *Writer) Close(ctx context.Context) error {
	s.healthMu.Lock()
	s.writerClosing = true
	done := s.writesDone
	s.healthMu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Save atomically publishes one bounded JSON snapshot. The value must remain
// immutable until Save returns. Directory ownership and domain read/modify/write
// serialization remain the caller's responsibility.
func (s *Writer) Save(path string, v any, limit int64) error {
	finish, err := s.admitSnapshotWrite(path)
	if err != nil {
		return err
	}
	defer finish()
	dir := filepath.Dir(path)
	if err := s.EnsureDirectory(dir); err != nil {
		return err
	}
	f, err := s.ops.CreateTemp(dir, ".snapshot-*")
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
	if err := s.ops.Rename(f.Name(), path); err != nil {
		// An error from replacement is treated conservatively as uncertain;
		// a filesystem can report an error after performing the operation.
		s.MarkUncertain()
		return fmt.Errorf("%w: replace snapshot: %w", ErrCommitUnknown, err)
	}
	if err := s.ops.SyncDir(dir); err != nil {
		s.MarkUncertain()
		return fmt.Errorf("%w: sync directory: %w", ErrCommitUnknown, err)
	}
	return nil
}

// Directory creation is a durability boundary, but an already initialized
// directory need not be synced again before every write. Keep the file Sync
// and the post-rename directory Sync on every commit. Recheck both inodes so
// replacing a directory or its parent requires initialization again.
// EnsureDirectory also serves body-log preparation under its domain owner.
// That owner must provide operation admission and settlement before Writer.Close.
func (s *Writer) EnsureDirectory(dir string) error {
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
	if err := s.ops.MakeDir(dir); err != nil {
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

// Remove deletes a snapshot under the same path, health and settlement fence
// as Save. An attempted deletion can have an ambiguous outcome.
func (s *Writer) Remove(path string) error {
	finish, err := s.admitSnapshotWrite(path)
	if err != nil {
		return err
	}
	defer finish()
	if err := s.ops.Remove(path); err != nil {
		s.MarkUncertain()
		return fmt.Errorf("%w: remove snapshot: %w", ErrCommitUnknown, err)
	}
	if err := s.ops.SyncDir(filepath.Dir(path)); err != nil {
		s.MarkUncertain()
		return fmt.Errorf("%w: sync removal: %w", ErrCommitUnknown, err)
	}
	return nil
}

// ReadJSON reads one bounded regular file and rejects unknown fields and trailing data.
func ReadJSON(path string, v any, limit int64) error {
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

// SyncDirectory persists directory entries and closes its descriptor.
func SyncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// MakeDurableDir syncs each newly created directory and its parent so the first
// snapshot's durable rename cannot depend on an uncommitted parent directory.
func MakeDurableDir(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%s is not a directory", path)
		}
		if err := SyncDirectory(path); err != nil {
			return err
		}
		return SyncDirectory(filepath.Dir(path))
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(path)
	if err := MakeDurableDir(parent); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := SyncDirectory(path); err != nil {
		return err
	}
	return SyncDirectory(parent)
}
