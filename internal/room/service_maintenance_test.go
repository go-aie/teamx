//go:build darwin || linux

package room

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func TestServiceMaintenanceSerializesBackgroundCleanupAndUpgrade(t *testing.T) {
	root := t.TempDir()
	held, err := lockDirectoryFile(root, ".service.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	type result struct {
		file *os.File
		err  error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var diagnostics bytes.Buffer
	go func() {
		file, err := acquireServiceMaintenance(ctx, root, true, &diagnostics)
		done <- result{file, err}
	}()
	select {
	case got := <-done:
		if got.file != nil {
			unlockDirectory(got.file)
		}
		t.Fatal("maintenance entered a held lock", got.err)
	case <-time.After(150 * time.Millisecond):
	}
	if err := unlockDirectory(held); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	defer unlockDirectory(got.file)
	if diagnostics.Len() == 0 {
		t.Fatal("waiting was not explained")
	}
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := acquireServiceMaintenance(canceled, root, true, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := acquireServiceMaintenance(context.Background(), root, false, io.Discard); !errors.Is(err, ErrLocked) {
		t.Fatal("inspection unexpectedly waited", err)
	}
}
