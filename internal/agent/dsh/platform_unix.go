//go:build darwin || linux

package dsh

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func lockDirectory(dir string) (*os.File, error) {
	// Do not follow a caller-supplied lock symlink.
	fd, err := syscall.Open(filepath.Join(dir, ".lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	f := os.NewFile(uintptr(fd), ".lock")
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return f, nil
}

func ownProcess(cmd *exec.Cmd)                       { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func signalPID(pid int, signal syscall.Signal) error { return syscall.Kill(pid, signal) }
