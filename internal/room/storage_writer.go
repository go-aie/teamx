package room

import (
	"context"
	"sync"

	"github.com/go-aie/teamx/internal/persistence"
)

// snapshotWriter is a temporary adapter for existing Store/access/model callers
// and the room-specific I/O fault seam shared with body logs and reclamation.
// All health, path serialization and I/O settlement belong to persistence.Writer.
// Remove this adapter when those domain seams and callers move to their modules.
// Like Writer, this value must not be copied after first use.
type snapshotWriter struct {
	ops    snapshotOps
	init   sync.Once
	writer *persistence.Writer
}

func (s *snapshotWriter) persistence() *persistence.Writer {
	s.init.Do(func() {
		s.writer = persistence.NewWriter(persistence.Operations{
			MakeDir:    func(path string) error { return s.ops.makeDir(path) },
			CreateTemp: func(dir, pattern string) (persistence.File, error) { return s.ops.createTemp(dir, pattern) },
			Rename:     func(from, to string) error { return s.ops.rename(from, to) },
			SyncDir:    func(path string) error { return s.ops.syncDir(path) },
			Remove:     func(path string) error { return s.ops.remove(path) },
		})
	})
	return s.writer
}
func (s *snapshotWriter) save(path string, v any, limit int64) error {
	return s.persistence().Save(path, v, limit)
}
func (s *snapshotWriter) ensureSnapshotDirectory(dir string) error {
	return s.persistence().EnsureDirectory(dir)
}
func (s *snapshotWriter) markUncertain()                        { s.persistence().MarkUncertain() }
func (s *snapshotWriter) checkWriter(writing bool) error        { return s.persistence().Check(writing) }
func (s *snapshotWriter) closeWriter(ctx context.Context) error { return s.persistence().Close(ctx) }

func (s *Store) removeSnapshot(path string) error {
	if err := s.check(true); err != nil {
		return err
	}
	return s.persistence().Remove(path)
}

func (s *Store) directoryReleased() bool {
	s.lockMu.Lock()
	defer s.lockMu.Unlock()
	return s.lock == nil
}
