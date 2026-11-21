//go:build darwin || linux

package room

import (
	"os"
	"syscall"
)

// Nonblocking + nofollow closes the lstat/open FIFO and final-symlink races.
// Directories are pinned separately by the component walker.
func openWorkspaceFile(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
