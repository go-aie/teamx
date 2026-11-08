//go:build !darwin && !linux

package room

import (
	"fmt"
	"os"
)

func lockDirectory(string) (*os.File, error) {
	return nil, fmt.Errorf("Room snapshot directory locking requires macOS or Linux")
}

func lockBundleHome(dir string) (*os.File, error) { return lockDirectory(dir) }

func unlockDirectory(f *os.File) error { return f.Close() }
