//go:build darwin || linux

package room

import (
	"errors"
	"syscall"
	"testing"
)

func TestDirectoryCloseReleasesInheritedLockDescriptor(t *testing.T) {
	dir := t.TempDir()
	store := openTestStore(t, dir)
	// fork temporarily inherits this same open-file description until exec
	// closes its CLOEXEC descriptor. Dup reproduces that window deterministically.
	inherited, err := syscall.Dup(int(store.lock.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(inherited)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir, "design-review")
	if err != nil {
		t.Fatal("closed owner retained a lock through an inherited descriptor:", err)
	}
	defer reopened.Close()
	if err := syscall.Flock(inherited, syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatal("inherited descriptor interfered with the new owner:", err)
	}
}
