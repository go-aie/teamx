//go:build darwin || linux

package room

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// Timeout must also stop git/HTTPS children, preserving a partial workspace for
// explicit inspection instead of leaving a preparer writing after activation.
func configurePreparation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 3 * time.Second
}

// Wait alone does not prove the HTTPS/askpass children exited. Kill a surviving
// group even after a successful parent exit, then require its disappearance.
func settlePreparation(cmd *exec.Cmd) error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		err := syscall.Kill(-cmd.Process.Pid, 0)
		if err == syscall.ESRCH {
			return nil
		}
		if err != nil {
			return errors.New("workspace preparation cleanup is unverified")
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if time.Now().After(deadline) {
			return errors.New("workspace preparation processes have not settled")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Observation only. An explicit retry never kills a potentially reused PID.
func preparationGroupSettled(pid int) bool {
	return pid > 0 && syscall.Kill(-pid, 0) == syscall.ESRCH
}
