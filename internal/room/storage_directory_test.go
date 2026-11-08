package room

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type syncCountingFile struct {
	snapshotFile
	count *int
}

func (f *syncCountingFile) Sync() error {
	*f.count++
	return f.snapshotFile.Sync()
}

func TestSnapshotDirectoryInitializationTracksIdentity(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	ops := store.ops
	initializations, fileSyncs, commitSyncs := 0, 0, 0
	store.ops.makeDir = func(dir string) error { initializations++; return ops.makeDir(dir) }
	store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		file, err := ops.createTemp(dir, pattern)
		return &syncCountingFile{file, &fileSyncs}, err
	}
	store.ops.syncDir = func(dir string) error { commitSyncs++; return ops.syncDir(dir) }
	parent := filepath.Join(store.dir, "group")
	dir := filepath.Join(parent, "nested")
	path := filepath.Join(dir, "snapshot.json")
	save := func() {
		t.Helper()
		if err := store.save(path, map[string]int{"value": fileSyncs}, 1024); err != nil {
			t.Fatal(err)
		}
	}
	save()
	save()
	if initializations != 1 || fileSyncs != 2 || commitSyncs != 2 {
		t.Fatal("repeated writes must reuse initialization and retain both commit syncs", initializations, fileSyncs, commitSyncs)
	}
	if err := os.Rename(dir, dir+"-old"); err != nil {
		t.Fatal(err)
	}
	save()
	if initializations != 2 {
		t.Fatal("replacement directory reused old durability evidence")
	}
	// Move the same child inode under a new parent inode.
	if err := os.Rename(parent, parent+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(parent+"-old", "nested"), dir); err != nil {
		t.Fatal(err)
	}
	save()
	if initializations != 3 || fileSyncs != 4 || commitSyncs != 4 {
		t.Fatal("replacement parent reused old durability evidence", initializations, fileSyncs, commitSyncs)
	}
}

func TestSnapshotDirectoryInitializationFailureIsRetried(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	initialize := store.ops.makeDir
	injected := errors.New("directory initialization failed")
	calls := 0
	store.ops.makeDir = func(dir string) error {
		calls++
		if calls == 1 {
			return injected
		}
		return initialize(dir)
	}
	path := filepath.Join(store.dir, "new", "snapshot.json")
	if err := store.save(path, "first", 1024); !errors.Is(err, injected) || store.uncertain {
		t.Fatal("pre-commit initialization failure changed commit state", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed initialization wrote a snapshot", err)
	}
	for range 2 {
		if err := store.save(path, "retry", 1024); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("failed initialization was cached or successful initialization repeated", calls)
	}
}
