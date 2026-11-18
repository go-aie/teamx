package room

import (
	"fmt"
	"path/filepath"
	"sync"
)

// snapshotWriter owns file health and I/O, not a Room directory. Store adds its
// OS-lock check; access/configuration domains provide their existing ownership.
type snapshotWriter struct {
	healthMu           sync.Mutex
	uncertain          bool
	writerClosing      bool
	writes             int
	writesDone         chan struct{}
	ops                snapshotOps
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
	refs int // protected by snapshotWriter.pathMu; includes waiters
}

// Path serialization is a file-level backstop, not a substitute for the domain
// owner's read/modify/write protocol. Waiters are admitted only after obtaining
// the path, so a freeze also rejects requests queued behind another save.
func (s *snapshotWriter) admitSnapshotWrite(path string) (func(), error) {
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
		err = ErrStoreClosed
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

// Deleting a snapshot participates in the same health and close boundary as
// replacement. An attempted deletion can be ambiguous just like rename.
func (s *Store) removeSnapshot(path string) error {
	if err := s.check(true); err != nil {
		return err
	}
	return s.snapshotWriter.removeSnapshot(path)
}

func (s *snapshotWriter) removeSnapshot(path string) error {
	finish, err := s.admitSnapshotWrite(path)
	if err != nil {
		return err
	}
	defer finish()
	if err := s.ops.remove(path); err != nil {
		s.markUncertain()
		return fmt.Errorf("%w: remove snapshot: %w", ErrCommitUnknown, err)
	}
	if err := s.ops.syncDir(filepath.Dir(path)); err != nil {
		s.markUncertain()
		return fmt.Errorf("%w: sync removal: %w", ErrCommitUnknown, err)
	}
	return nil
}

func (s *Store) directoryReleased() bool {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	return s.lock == nil
}
