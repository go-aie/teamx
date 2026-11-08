//go:build darwin || linux

package room

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func lockDirectory(dir string) (*os.File, error) {
	return lockDirectoryFile(dir, ".lock")
}

func lockBundleHome(dir string) (*os.File, error) {
	return lockDirectoryFile(dir, ".bundle.lock")
}

func lockDirectoryFile(dir, name string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrLocked, dir)
		}
		return nil, err
	}
	return f, nil
}

func unlockDirectory(f *os.File) error {
	// fork may temporarily retain a duplicate until exec closes CLOEXEC files.
	// Closing only our descriptor would leave that duplicate holding the lock.
	// Release ownership explicitly before closing the owning descriptor.
	return errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close())
}
