package persistence

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type snapshotSyncBarrier struct {
	File
	enter   func()
	failure error
}

func (f *snapshotSyncBarrier) Sync() error {
	f.enter()
	if f.failure != nil {
		return f.failure
	}
	return f.File.Sync()
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

func startSnapshotSave(s *Writer, path string, value any) <-chan error {
	done := make(chan error, 1)
	go func() { done <- s.Save(path, value, 1024) }()
	return done
}

func TestSnapshotWriterQueuedPathRechecksHealth(t *testing.T) {
	s := NewWriter(DefaultOperations())
	dir := t.TempDir()
	path := filepath.Join(dir, "value.json")
	entered, block, release := snapshotBarrier(t)
	fault := errors.New("ambiguous replacement")
	rename := s.ops.Rename
	s.ops.Rename = func(from, to string) error {
		block()
		if err := rename(from, to); err != nil {
			return err
		}
		return fault
	}
	first := startSnapshotSave(s, path, "first")
	waitSnapshotBoundary(t, entered)
	second := startSnapshotSave(s, filepath.Join(dir, ".", "value.json"), "second")
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.pathMu.Lock()
		queued := s.writePaths[path].refs == 2
		s.pathMu.Unlock()
		if queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("same-path save did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	s.healthMu.Lock()
	admitted := s.writes
	s.healthMu.Unlock()
	if admitted != 1 {
		t.Fatal("same path had more than one admitted writer", admitted)
	}
	release()
	if err := waitSnapshotResult(t, first); !errors.Is(err, fault) || !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if err := waitSnapshotResult(t, second); !errors.Is(err, ErrCommitUnknown) || errors.Is(err, fault) {
		t.Fatal("queued request was not rejected at health admission", err)
	}
	var value string
	if err := ReadJSON(path, &value, 1024); err != nil || value != "first" {
		t.Fatal("queued write overwrote the ambiguous snapshot", value, err)
	}
	s.pathMu.Lock()
	retained := len(s.writePaths)
	s.pathMu.Unlock()
	if retained != 0 {
		t.Fatal("completed paths retained writer slots", retained)
	}
}

func TestSnapshotWriterConcurrentDirectoryInitialization(t *testing.T) {
	s := NewWriter(DefaultOperations())
	dir := t.TempDir()
	var mu sync.Mutex
	initializations := 0
	makeDir := s.ops.MakeDir
	s.ops.MakeDir = func(dir string) error {
		mu.Lock()
		initializations++
		mu.Unlock()
		return makeDir(dir)
	}
	dir = filepath.Join(dir, "same-directory")
	results := make([]<-chan error, 32)
	for i := range results {
		results[i] = startSnapshotSave(s, filepath.Join(dir, strconv.Itoa(i)+".json"), i)
	}
	for _, result := range results {
		if err := waitSnapshotResult(t, result); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if initializations != 1 {
		t.Fatal("concurrent saves did not share directory durability evidence", initializations)
	}
}

type failingFile struct {
	File
	stage   string
	failure error
}

func (f *failingFile) Write(p []byte) (int, error) {
	if f.stage == "write" {
		n, _ := f.File.Write(p[:len(p)/2])
		return n, f.failure
	}
	return f.File.Write(p)
}
func (f *failingFile) Sync() error {
	if f.stage == "file sync" {
		return f.failure
	}
	return f.File.Sync()
}
func (f *failingFile) Close() error {
	err := f.File.Close()
	if f.stage == "close" {
		return f.failure
	}
	return err
}

func TestWriterCommitFailureBoundaries(t *testing.T) {
	for _, stage := range []string{"create", "write", "file sync", "close", "replace", "replace then error", "directory sync", "encode limit"} {
		t.Run(stage, func(t *testing.T) {
			ops := DefaultOperations()
			path := filepath.Join(t.TempDir(), "snapshot.json")
			writer := NewWriter(ops)
			if err := writer.Save(path, "old", 1024); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			fault := errors.New("injected commit failure")
			writer.ops.CreateTemp = func(dir, pattern string) (File, error) {
				if stage == "create" {
					return nil, fault
				}
				f, err := ops.CreateTemp(dir, pattern)
				if err != nil {
					return nil, err
				}
				return &failingFile{f, stage, fault}, nil
			}
			writer.ops.Rename = func(from, to string) error {
				if stage == "replace" {
					return fault
				}
				if err := ops.Rename(from, to); err != nil {
					return err
				}
				if stage == "replace then error" {
					return fault
				}
				return nil
			}
			writer.ops.SyncDir = func(dir string) error {
				if stage == "directory sync" {
					return fault
				}
				return ops.SyncDir(dir)
			}
			limit := int64(1024)
			if stage == "encode limit" {
				limit = 1
			}
			err = writer.Save(path, "new", limit)
			if err == nil || (stage != "encode limit" && !errors.Is(err, fault)) {
				t.Fatal(err)
			}
			uncertain := stage == "replace" || stage == "replace then error" || stage == "directory sync"
			if errors.Is(err, ErrCommitUnknown) != uncertain {
				t.Fatal("incorrect outcome", err)
			}
			if errors.Is(writer.Check(true), ErrCommitUnknown) != uncertain {
				t.Fatal("incorrect health")
			}
			if err := writer.Check(false); err != nil {
				t.Fatal("read rejected", err)
			}
			var value string
			if err := ReadJSON(path, &value, 1024); err != nil {
				t.Fatal(err)
			}
			changed := stage == "replace then error" || stage == "directory sync"
			if (value == "new") != changed {
				t.Fatal(value)
			}
			after, err := os.ReadFile(path)
			if err != nil || bytes.Equal(before, after) == changed {
				t.Fatal("wrong file outcome", err)
			}
			temps, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".snapshot-*"))
			if err != nil || len(temps) != 0 {
				t.Fatal("temporary files leaked", temps, err)
			}
			writer.ops = ops
			if uncertain {
				if err := writer.Save(path, "late", 1024); !errors.Is(err, ErrCommitUnknown) {
					t.Fatal(err)
				}
			} else if err := writer.Save(path, "retry", 1024); err != nil {
				t.Fatal("definite failure froze writer", err)
			}
		})
	}
}

func TestWriterCloseCancellationRetainsFenceAndDrains(t *testing.T) {
	ops := DefaultOperations()
	entered, block, release := snapshotBarrier(t)
	var once sync.Once
	ops.CreateTemp = func(dir, pattern string) (File, error) {
		f, err := os.CreateTemp(dir, pattern)
		if err == nil {
			return &snapshotSyncBarrier{File: f, enter: func() { once.Do(block) }}, nil
		}
		return nil, err
	}
	writer := NewWriter(ops)
	path := filepath.Join(t.TempDir(), "snapshot.json")
	first := startSnapshotSave(writer, path, "first")
	waitSnapshotBoundary(t, entered)
	queued := startSnapshotSave(writer, path, "queued")
	// Establish that this request actually reached the path queue before Close.
	deadline := time.Now().Add(5 * time.Second)
	for {
		writer.pathMu.Lock()
		refs := writer.writePaths[path].refs
		writer.pathMu.Unlock()
		if refs == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("save did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writer.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled close pretended to settle", err)
	}
	if err := writer.Save(filepath.Join(filepath.Dir(path), "late.json"), "late", 1024); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := writer.Check(false); err != nil {
		t.Fatal("close changed read admission", err)
	}
	settled := make(chan error, 1)
	go func() { settled <- writer.Close(context.Background()) }()
	select {
	case err := <-settled:
		t.Fatal("unsettled writer closed", err)
	default:
	}
	release()
	if err := waitSnapshotResult(t, first); err != nil {
		t.Fatal("admitted commit failed", err)
	}
	if err := waitSnapshotResult(t, queued); !errors.Is(err, ErrClosed) {
		t.Fatal("queued write bypassed closing fence", err)
	}
	if err := waitSnapshotResult(t, settled); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := ReadJSON(path, &value, 1024); err != nil || value != "first" {
		t.Fatal(value, err)
	}
}

func TestWriterExternalUncertaintyAndIndependentDomains(t *testing.T) {
	a, b := NewWriter(DefaultOperations()), NewWriter(DefaultOperations())
	a.MarkUncertain()
	if err := a.Check(false); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(filepath.Join(t.TempDir(), "a.json"), "a", 1024); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if err := a.Remove(filepath.Join(t.TempDir(), "a.json")); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if err := b.Save(filepath.Join(t.TempDir(), "b.json"), "b", 1024); err != nil {
		t.Fatal("independent domain froze", err)
	}
}

func TestWriterRemovalFailureBoundaries(t *testing.T) {
	for _, stage := range []string{"remove", "remove then error", "directory sync"} {
		t.Run(stage, func(t *testing.T) {
			ops := DefaultOperations()
			writer := NewWriter(ops)
			path := filepath.Join(t.TempDir(), "snapshot.json")
			if err := writer.Save(path, "old", 1024); err != nil {
				t.Fatal(err)
			}
			fault := errors.New("ambiguous removal")
			writer.ops.Remove = func(path string) error {
				if stage == "remove" {
					return fault
				}
				if err := ops.Remove(path); err != nil {
					return err
				}
				if stage == "remove then error" {
					return fault
				}
				return nil
			}
			writer.ops.SyncDir = func(string) error { return fault }
			if err := writer.Remove(path); !errors.Is(err, ErrCommitUnknown) || !errors.Is(err, fault) {
				t.Fatal(err)
			}
			_, err := os.Stat(path)
			if stage == "remove" && err != nil || stage != "remove" && !errors.Is(err, os.ErrNotExist) {
				t.Fatal("wrong deletion outcome", err)
			}
			if err := writer.Remove(path); !errors.Is(err, ErrCommitUnknown) || errors.Is(err, fault) {
				t.Fatal("removal bypassed health", err)
			}
			if err := writer.Save(path, "late", 1024); !errors.Is(err, ErrCommitUnknown) {
				t.Fatal(err)
			}
			if err := writer.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := NewWriter(ops).Save(path, "reopened", 1024); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReadJSONBoundsAndStrictDecoding(t *testing.T) {
	for _, data := range []string{`{"value":1,"unknown":2}`, `{"value":1} {}`, `{"value":`, strings.Repeat(" ", 1025)} {
		path := filepath.Join(t.TempDir(), "snapshot.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		var value struct {
			Value int `json:"value"`
		}
		if err := ReadJSON(path, &value, 1024); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid snapshot accepted", err)
		}
	}
	var value any
	if err := ReadJSON(t.TempDir(), &value, 1024); !errors.Is(err, ErrInvalid) {
		t.Fatal("directory accepted", err)
	}
	if err := ReadJSON(filepath.Join(t.TempDir(), "absent"), &value, 1024); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
