package room

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type snapshotSyncBarrier struct {
	snapshotFile
	enter   func()
	failure error
}

func (f *snapshotSyncBarrier) Sync() error {
	f.enter()
	if f.failure != nil {
		return f.failure
	}
	return f.snapshotFile.Sync()
}

func snapshotBarrier(t *testing.T) (<-chan struct{}, func(), func()) {
	t.Helper()
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	return entered, func() { close(entered); <-release }, unblock
}

func waitSnapshotBoundary(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot boundary not reached")
	}
}

func waitSnapshotResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot operation did not complete")
		return nil
	}
}

func startSnapshotSave(s *Store, path string, value any) <-chan error {
	done := make(chan error, 1)
	go func() { done <- s.save(path, value, 1024) }()
	return done
}

func TestSnapshotWriterIndependentCommitOutcomes(t *testing.T) {
	for _, boundary := range []string{"completed", "directory preparation", "file sync", "before replace", "after replace", "directory sync", "precommit failure"} {
		t.Run(boundary, func(t *testing.T) {
			s := openTestStore(t, t.TempDir())
			aDir, bDir := filepath.Join(s.dir, "rooms", "a"), filepath.Join(s.dir, "rooms", "b")
			aPath, bPath := filepath.Join(aDir, "source.json"), filepath.Join(bDir, "source.json")
			aEntered, aBlock, aRelease := snapshotBarrier(t)
			bEntered, bBlock, bRelease := snapshotBarrier(t)
			fault := errors.New("injected sync failure")
			ops := s.ops
			s.ops.makeDir = func(dir string) error {
				if dir == bDir && boundary == "directory preparation" {
					bBlock()
				}
				return ops.makeDir(dir)
			}
			s.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
				f, err := ops.createTemp(dir, pattern)
				if err == nil && dir == bDir && (boundary == "file sync" || boundary == "precommit failure") {
					var failure error
					if boundary == "precommit failure" {
						failure = fault
					}
					return &snapshotSyncBarrier{f, bBlock, failure}, nil
				}
				return f, err
			}
			s.ops.rename = func(from, to string) error {
				if to == bPath && boundary == "before replace" {
					bBlock()
				}
				if err := ops.rename(from, to); err != nil {
					return err
				}
				if to == bPath && boundary == "after replace" {
					bBlock()
				}
				return nil
			}
			s.ops.syncDir = func(dir string) error {
				if dir == aDir {
					aBlock()
					return fault
				}
				if dir == bDir && boundary == "directory sync" {
					bBlock()
				}
				return ops.syncDir(dir)
			}
			a := startSnapshotSave(s, aPath, "a")
			waitSnapshotBoundary(t, aEntered)
			b := startSnapshotSave(s, bPath, "b")
			if boundary == "completed" {
				if err := waitSnapshotResult(t, b); err != nil {
					t.Fatal("independent Room blocked or failed", err)
				}
			} else {
				waitSnapshotBoundary(t, bEntered)
			}
			aRelease()
			if err := waitSnapshotResult(t, a); !errors.Is(err, ErrCommitUnknown) || !errors.Is(err, fault) {
				t.Fatal("A lost its ambiguous result", err)
			}
			// Already admitted B must decide its own result at every boundary.
			if boundary != "completed" {
				bRelease()
				err := waitSnapshotResult(t, b)
				if boundary == "precommit failure" {
					if !errors.Is(err, fault) || errors.Is(err, ErrCommitUnknown) {
						t.Fatal("B's definite failure was changed by A", err)
					}
				} else if err != nil {
					t.Fatal("B's successful commit was changed by A", err)
				}
			}
			if err := s.check(true); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal("shared Store did not freeze", err)
			}
			// A source confirmation is a new write, even after B returned success.
			ack := filepath.Join(bDir, "confirmation.json")
			if err := s.save(ack, "ack", 1024); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal("new confirmation bypassed the health boundary", err)
			}
			if _, err := os.Stat(ack); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected confirmation touched disk", err)
			}
			if err := s.SaveRoom(sampleRoom()); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal("management write bypassed freeze", err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := openTestStore(t, s.dir)
			for _, path := range []string{aPath, bPath} {
				var value string
				err := readSnapshot(path, &value, 1024)
				if path == bPath && boundary == "precommit failure" {
					if !errors.Is(err, os.ErrNotExist) {
						t.Fatal("definitely uncommitted B appeared", err)
					}
				} else if err != nil || value != filepath.Base(filepath.Dir(path)) {
					t.Fatal("reopen did not recover the complete snapshot", path, value, err)
				}
			}
			if err := reopened.save(ack, "verified", 1024); err != nil {
				t.Fatal("reopened writer stayed frozen", err)
			}
		})
	}
}

func TestSnapshotWriterIndependentPreparationAndFileSync(t *testing.T) {
	for _, boundary := range []string{"directory preparation", "file sync"} {
		t.Run(boundary, func(t *testing.T) {
			s := openTestStore(t, t.TempDir())
			aDir, bDir := filepath.Join(s.dir, "a"), filepath.Join(s.dir, "b")
			entered, block, release := snapshotBarrier(t)
			ops := s.ops
			s.ops.makeDir = func(dir string) error {
				if dir == aDir && boundary == "directory preparation" {
					block()
				}
				return ops.makeDir(dir)
			}
			s.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
				f, err := ops.createTemp(dir, pattern)
				if err == nil && dir == aDir && boundary == "file sync" {
					return &snapshotSyncBarrier{snapshotFile: f, enter: block}, nil
				}
				return f, err
			}
			a := startSnapshotSave(s, filepath.Join(aDir, "value.json"), "a")
			waitSnapshotBoundary(t, entered)
			if err := waitSnapshotResult(t, startSnapshotSave(s, filepath.Join(bDir, "value.json"), "b")); err != nil {
				t.Fatal("another Room's save blocked", err)
			}
			release()
			if err := waitSnapshotResult(t, a); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSnapshotRemovalUsesWriterHealth(t *testing.T) {
	for _, boundary := range []string{"remove", "directory sync"} {
		t.Run(boundary, func(t *testing.T) {
			s := openTestStore(t, t.TempDir())
			path := filepath.Join(s.dir, "value.json")
			if err := s.save(path, "value", 1024); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("ambiguous removal")
			if boundary == "remove" {
				s.ops.remove = func(string) error { return fault }
			} else {
				s.ops.syncDir = func(string) error { return fault }
			}
			if err := s.removeSnapshot(path); !errors.Is(err, fault) || !errors.Is(err, ErrCommitUnknown) {
				t.Fatal(err)
			}
			if err := s.save(path, "late", 1024); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal("removal failed to freeze replacement", err)
			}
			// A later removal must fail at admission, before invoking its I/O.
			if err := s.removeSnapshot(path); !errors.Is(err, ErrCommitUnknown) || errors.Is(err, fault) {
				t.Fatal("removal bypassed the shared health boundary", err)
			}
		})
	}
}

func TestSnapshotWriterExternalOwnershipRetainsHealthBoundary(t *testing.T) {
	dir := t.TempDir()
	w := &snapshotWriter{ops: defaultSnapshotOps()}
	path := filepath.Join(dir, "external.json")
	if err := w.save(path, "first", 1024); err != nil {
		t.Fatal("externally owned writer required a Room lock", err)
	}
	fault := errors.New("external snapshot sync result lost")
	w.ops.syncDir = func(string) error { return fault }
	if err := w.save(path, "second", 1024); !errors.Is(err, ErrCommitUnknown) || !errors.Is(err, fault) {
		t.Fatal(err)
	}
	if err := w.save(path, "third", 1024); !errors.Is(err, ErrCommitUnknown) || errors.Is(err, fault) {
		t.Fatal("external writer bypassed its own freeze", err)
	}
	var value string
	if err := readSnapshot(path, &value, 1024); err != nil || value != "second" {
		t.Fatal("rejected external save overwrote evidence", value, err)
	}
	// A Room Store still requires its OS directory ownership. Extracting the
	// reusable writer must not make a zero or released Store writable.
	unowned := &Store{snapshotWriter: snapshotWriter{ops: defaultSnapshotOps()}}
	if err := unowned.save(filepath.Join(dir, "unowned.json"), "value", 1024); !errors.Is(err, ErrStoreClosed) {
		t.Fatal("Room Store bypassed directory ownership", err)
	}
}

func TestSnapshotWriterCloseDrainsAllAdmittedWrites(t *testing.T) {
	s := openTestStore(t, t.TempDir())
	aDir, bDir := filepath.Join(s.dir, "a"), filepath.Join(s.dir, "b")
	aEntered, aBlock, aRelease := snapshotBarrier(t)
	bEntered, bBlock, bRelease := snapshotBarrier(t)
	fault := errors.New("unknown A")
	create := s.ops.createTemp
	s.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		f, err := create(dir, pattern)
		if err == nil && dir == bDir {
			return &snapshotSyncBarrier{snapshotFile: f, enter: bBlock}, nil
		}
		return f, err
	}
	s.ops.syncDir = func(dir string) error {
		if dir == aDir {
			aBlock()
			return fault
		}
		return syncDirectory(dir)
	}
	a := startSnapshotSave(s, filepath.Join(aDir, "value.json"), "a")
	b := startSnapshotSave(s, filepath.Join(bDir, "value.json"), "b")
	waitSnapshotBoundary(t, aEntered)
	waitSnapshotBoundary(t, bEntered)
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		closing := errors.Is(s.check(true), ErrStoreClosed)
		if closing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("close did not freeze new admission")
		}
		time.Sleep(time.Millisecond)
	}
	if err := s.save(filepath.Join(s.dir, "late.json"), "late", 1024); !errors.Is(err, ErrStoreClosed) {
		t.Fatal("close accepted another write", err)
	}
	aRelease()
	if err := waitSnapshotResult(t, a); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	select {
	case err := <-closed:
		t.Fatal("close released ownership with B still in flight", err)
	default:
	}
	if lock, err := lockDirectory(s.dir); !errors.Is(err, ErrLocked) {
		if lock != nil {
			_ = unlockDirectory(lock)
		}
		t.Fatal("directory lock released early", err)
	}
	bRelease()
	if err := waitSnapshotResult(t, b); err != nil {
		t.Fatal("admitted B failed during shutdown", err)
	}
	if err := waitSnapshotResult(t, closed); err != nil {
		t.Fatal(err)
	}
	if !s.directoryReleased() {
		t.Fatal("settled writer retained directory lock")
	}
	if err := s.save(filepath.Join(s.dir, "late.json"), "late", 1024); !errors.Is(err, ErrStoreClosed) {
		t.Fatal("closed direct writer remained writable", err)
	}
	_ = openTestStore(t, s.dir)
}
